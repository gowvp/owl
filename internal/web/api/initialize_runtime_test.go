package api

import (
	"encoding/json/v2"
	"fmt"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gowvp/owl/internal/conf"
	"github.com/gowvp/owl/internal/core/sms"
)

// TestInitializeConcurrentRuntimeOptions 验证只有成功请求能发布配置，并准确提示 ZLM 地址变更需要重启。
func TestInitializeConcurrentRuntimeOptions(t *testing.T) {
	api, router := newLoginTestAPI(t)
	api.conf.Server.Username = ""
	api.conf.ConfigPath = filepath.Join(t.TempDir(), "config.toml")
	api.conf.Sip.Port = 5060
	api.conf.Media.Type = sms.ProtocolZLMediaKit
	api.conf.Media.SDPIP = "192.0.2.100"
	body := encryptedLoginBody(t, api, `{"username":"operator","password":"chosen-password"}`)
	var envelope map[string]any
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatal(err)
	}
	encrypted := envelope["data"]
	var wg sync.WaitGroup
	winners := make(chan string, 12)
	for i := range 12 {
		wg.Go(func() {
			host := fmt.Sprintf("192.0.2.%d", i+1)
			data, err := json.Marshal(map[string]any{"data": encrypted, "sip": map[string]any{"host": host, "id": "44123456782000000002", "password": host, "port": 5060}, "onvif_password": host, "media": map[string]any{"sdp_ip": host}})
			if err != nil {
				t.Error(err)
				return
			}
			request := httptest.NewRequest("POST", "/init", strings.NewReader(string(data)))
			request.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, request)
			if response.Code == 200 {
				winners <- host
				if !strings.Contains(response.Body.String(), `"restart_required":true`) {
					t.Error("ZLM address change did not request a restart")
				}
			} else if response.Code != 400 {
				t.Errorf("unexpected response: %d", response.Code)
			}
		})
	}
	wg.Go(func() {
		for range 200 {
			snapshot := api.conf.Snapshot()
			if snapshot.Server.Username == "operator" && (snapshot.Sip.Host != snapshot.Sip.Password || snapshot.Sip.Host != snapshot.ONVIF.Password || snapshot.Sip.Host != snapshot.Media.SDPIP) {
				t.Error("partially published runtime configuration")
			}
		}
	})
	wg.Wait()
	close(winners)
	var successful []string
	for host := range winners {
		successful = append(successful, host)
	}
	if len(successful) != 1 {
		t.Fatalf("successful submissions: %d", len(successful))
	}
	snapshot := api.conf.Snapshot()
	if snapshot.Sip.Host != successful[0] || snapshot.Sip.Port != 5060 {
		t.Fatal("losing request or port change reached runtime")
	}
	var persisted conf.Bootstrap
	if err := conf.SetupConfig(&persisted, api.conf.ConfigPath); err != nil {
		t.Fatal(err)
	}
	if persisted.Sip != snapshot.Sip || persisted.ONVIF != snapshot.ONVIF || persisted.Media != snapshot.Media {
		t.Fatal("durable and runtime configuration diverged")
	}
}

// TestInitializeMediaRestartRequired 验证重启提示只针对 ZLM 收流地址的实际变化，避免误报其他即时配置。
func TestInitializeMediaRestartRequired(t *testing.T) {
	// 用例数量固定，每例独立初始化，整体复杂度为 O(n)。
	for _, tc := range []struct {
		name      string
		mediaType string
		options   map[string]any
		want      bool
	}{
		{"invalid address", sms.ProtocolZLMediaKit, map[string]any{"media": map[string]any{"sdp_ip": "http://invalid/path"}}, false},
		{"changed ZLM address", sms.ProtocolZLMediaKit, map[string]any{"media": map[string]any{"sdp_ip": "198.51.100.40"}}, true},
		{"same ZLM address", sms.ProtocolZLMediaKit, map[string]any{"media": map[string]any{"sdp_ip": " 192.0.2.40 "}}, false},
		{"omitted address", sms.ProtocolZLMediaKit, nil, false},
		{"immediate settings", sms.ProtocolZLMediaKit, map[string]any{"sip": map[string]any{"host": "192.0.2.50"}, "onvif_password": "new-onvif"}, false},
		{"changed lalmax address", sms.ProtocolLalmax, map[string]any{"media": map[string]any{"sdp_ip": "198.51.100.40"}}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, router := newLoginTestAPI(t)
			api.conf.Server.Username = ""
			api.conf.ConfigPath = filepath.Join(t.TempDir(), "config.toml")
			api.conf.Media.Type, api.conf.Media.SDPIP = tc.mediaType, "192.0.2.40"
			response := initRequest(t, api, router, tc.options)
			if tc.name == "invalid address" {
				if response.Code != 400 || !api.conf.NeedsInit() || api.conf.Snapshot().Media.SDPIP != "192.0.2.40" {
					t.Fatalf("invalid input changed configuration: %d %s", response.Code, response.Body.String())
				}
				return
			}
			var result struct {
				RestartRequired *bool `json:"restart_required"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if response.Code != 200 || result.RestartRequired == nil || *result.RestartRequired != tc.want {
				t.Fatalf("want restart_required=%v: %d %s", tc.want, response.Code, response.Body.String())
			}
		})
	}
}
