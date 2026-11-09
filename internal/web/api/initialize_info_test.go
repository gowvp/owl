package api

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gowvp/owl/internal/conf"
)

func TestInitializationUsesConnectedMediaPorts(t *testing.T) {
	for _, tc := range []struct {
		rtsp, rtmp         int
		known              bool
		wantRTSP, wantRTMP string
	}{
		{18554, 11935, true, "18554", "11935"},
		{0, 0, false, "554", "1935"},
		{0, 0, true, "", ""},
		{-1, 65536, true, "", ""},
	} {
		api, _ := newLoginTestAPI(t)
		api.conf.Server.Username = ""
		api.mediaPorts = func() (int, int, bool) { return tc.rtsp, tc.rtmp, tc.known }
		r := gin.New()
		RegisterUser(r, api)
		out := initializationInfo(t, r)
		for _, item := range out["firewall"].([]any) {
			row := item.(map[string]any)
			if row["id"] == "rtsp" && row["ports"] != tc.wantRTSP {
				t.Fatalf("RTSP: %v", row["ports"])
			}
			if row["id"] == "rtmp" && row["ports"] != tc.wantRTMP {
				t.Fatalf("RTMP: %v", row["ports"])
			}
		}
		api.conf.Server.Username, api.conf.Server.Password = "set", "set"
		api.mediaPorts = func() (int, int, bool) { t.Fatal("initialized GET /init queried media details"); return 0, 0, false }
		r = gin.New()
		RegisterUser(r, api)
		if out := initializationInfo(t, r); len(out) != 1 {
			t.Fatal("initialized response exposed media ports")
		}
	}
}

func initializationInfo(t *testing.T, r interface {
	ServeHTTP(http.ResponseWriter, *http.Request)
}) map[string]any {
	t.Helper()
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/init", nil))
	if w.Code != 200 {
		t.Fatalf("GET /init: %d %s", w.Code, w.Body.String())
	}
	if w.Header().Get("Cache-Control") != "no-store" {
		t.Fatal("initialization response may be cached")
	}
	var out map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestInitializationInfoWhitelistsDefaults(t *testing.T) {
	api, r := newLoginTestAPI(t)
	api.conf.Server.Username = ""
	api.conf.Sip = conf.SIP{Host: "192.0.2.2", Port: 17060, ID: "34020000002000000001", Password: "SIP-sensitive"}
	api.conf.ONVIF = conf.ONVIF{Username: "nvr", Password: "ONVIF-sensitive"}
	api.conf.Media.SDPIP = "192.0.2.8"
	api.conf.Media.Secret = "MEDIA-sensitive"
	out := initializationInfo(t, r)
	if out["need_init"] != true {
		t.Fatal("missing account not detected")
	}
	sip := out["sip"].(map[string]any)
	if sip["port"] != float64(17060) || sip["id"] != api.conf.Sip.ID {
		t.Fatalf("wrong SIP defaults: %v", sip)
	}
	media := out["media"].(map[string]any)
	if media["sdp_ip"] != "192.0.2.8" {
		t.Fatal("receive IP missing")
	}
	data, _ := json.Marshal(out)
	for _, value := range []string{"SIP-sensitive", "ONVIF-sensitive", "MEDIA-sensitive", api.conf.Server.HTTP.APISecret, api.conf.Server.HTTP.JwtSecret} {
		if value != "" && strings.Contains(string(data), value) {
			t.Fatal("secret exposed")
		}
	}
	api.conf.Server.Username = "existing"
	api.conf.Server.Password = ""
	if initializationInfo(t, r)["need_init"] != true {
		t.Fatal("missing password not detected")
	}
	api.conf.Server.Password = "existing-password"
	out = initializationInfo(t, r)
	if len(out) != 1 || out["need_init"] != false {
		t.Fatalf("initialized deployment exposed defaults: %v", out)
	}
}

// TestInitializeReceiveIP 验证收流地址校验和持久化，并确保 ZLM 地址变更会提示重启。
func TestInitializeReceiveIP(t *testing.T) {
	for _, tc := range []struct {
		host string
		code int
	}{{"198.51.100.9", 200}, {"media.example.com", 200}, {"https://invalid/path", 400}, {"2001:db8::1", 400}} {
		t.Run(tc.host, func(t *testing.T) {
			api, r := newLoginTestAPI(t)
			api.conf.Server.Username = ""
			api.conf.ConfigPath = filepath.Join(t.TempDir(), "config.toml")
			w := initRequest(t, api, r, map[string]any{"media": map[string]any{"sdp_ip": tc.host}})
			if w.Code != tc.code {
				t.Fatalf("status %d want %d: %s", w.Code, tc.code, w.Body.String())
			}
			if tc.code != 200 {
				if !api.conf.NeedsInit() {
					t.Fatal("invalid receive IP initialized account")
				}
				return
			}
			var saved conf.Bootstrap
			if err := conf.SetupConfig(&saved, api.conf.ConfigPath); err != nil {
				t.Fatal(err)
			}
			if saved.Media.SDPIP != tc.host {
				t.Fatal("receive IP not persisted")
			}
			var out map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
				t.Fatal(err)
			}
			if out["restart_required"] != true {
				t.Fatal("ZLM receive IP change must require a restart")
			}
		})
	}
}

func TestInitializationPortsFollowConfiguration(t *testing.T) {
	api, r := newLoginTestAPI(t)
	api.conf.Server.Username = ""
	api.conf.Server.HTTP.Port = 17123
	api.conf.Sip.Port = 17060
	api.conf.Media.HTTPPort = 18080
	api.conf.Media.RTPPortRange = "31000-31100"
	api.conf.Media.Type = "zlm"
	out := initializationInfo(t, r)
	rules := out["firewall"].([]any)
	byID := map[string]map[string]any{}
	for _, item := range rules {
		row := item.(map[string]any)
		byID[row["id"].(string)] = row
	}
	for id, want := range map[string]string{"web": "17123", "sip": "17060", "media_api": "18080", "webrtc": "31000", "rtp": "31001-31100", "onvif_discovery": "3702"} {
		if byID[id]["ports"] != want {
			t.Errorf("%s ports=%v want %s", id, byID[id]["ports"], want)
		}
	}
	if byID["media_api"]["direction"] != "平台 → 媒体节点" {
		t.Fatal("media API incorrectly advertised as public inbound")
	}
	if byID["rtsp"]["ports"] != "554" || byID["rtmp"]["ports"] != "1935" {
		t.Fatal("ZLM protocol defaults do not match the bundled Compose")
	}
	for _, invalid := range []string{"", "invalid", "65535-65536", "100-10"} {
		api.conf.Media.RTPPortRange = invalid
		data, _ := json.Marshal(initializationInfo(t, r))
		if strings.Contains(string(data), `"ports":"31000"`) || strings.Contains(string(data), `"ports":"8000"`) {
			t.Fatal("invalid range invented WebRTC port")
		}
	}
	api.conf.Media.Type = "lalmax"
	api.conf.Media.RTPPortRange = "32000-32100"
	out = initializationInfo(t, r)
	for _, item := range out["firewall"].([]any) {
		row := item.(map[string]any)
		if row["id"] == "rtp" && row["ports"] != "32000-32100" {
			t.Fatal("Lalmax RTP range incorrectly split")
		}
		if row["id"] == "webrtc" && row["ports"] != "" {
			t.Fatal("Lalmax RTC port guessed from ZLM policy")
		}
		if (row["id"] == "rtsp" || row["id"] == "rtmp") && row["ports"] != "" {
			t.Fatal("ZLM default port incorrectly applied to another media type")
		}
	}
}
