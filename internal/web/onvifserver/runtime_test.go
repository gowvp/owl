package onvifserver

import (
	"crypto/sha1"
	"encoding/base64"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gowvp/owl/internal/conf"
	"github.com/gowvp/owl/internal/core/sms"
)

func runtimeConfig(t *testing.T) *conf.Bootstrap {
	t.Helper()
	return &conf.Bootstrap{ConfigPath: filepath.Join(t.TempDir(), "config.toml"),
		ONVIF: conf.ONVIF{Username: "nvr", Password: "old-password"},
		Media: conf.Media{SDPIP: "192.0.2.10"}, Sip: conf.SIP{Host: "192.0.2.11"}}
}

func publishRuntime(t *testing.T, cfg *conf.Bootstrap, password, host string) {
	t.Helper()
	if err := cfg.Update(func(next *conf.Bootstrap) error {
		next.ONVIF.Password = password
		next.Media.SDPIP = host
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// The same registered handler must use credentials and addresses published after startup.
func TestSOAPRuntimeConfiguration(t *testing.T) {
	cfg := runtimeConfig(t)
	handler := newSOAPHandler(cfg, authStreamProvider{}, "test")
	checkSOAPAuth(t, handler, "/onvif/device_service", cfg.ONVIF, http.StatusOK)
	publishRuntime(t, cfg, "new-password", "192.0.2.20")
	for _, path := range []string{"/onvif/device_service", "/onvif/media_service"} {
		checkSOAPAuth(t, handler, path, conf.ONVIF{Username: "nvr", Password: "old-password"}, http.StatusUnauthorized)
		checkSOAPAuth(t, handler, path, conf.ONVIF{Username: "nvr", Password: "new-password"}, http.StatusOK)
	}
	for _, operation := range []string{"GetCapabilities", "GetServices", "GetSnapshotUri"} {
		rec := runtimeSOAP(handler, "new-password", operation)
		if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "http://192.0.2.20:8080/") || strings.Contains(rec.Body.String(), "192.0.2.10") {
			t.Fatalf("%s did not use published host: %d %s", operation, rec.Code, rec.Body.String())
		}
	}
}

func runtimeSOAP(handler http.Handler, password, operation string) *httptest.ResponseRecorder {
	body := fmt.Sprintf(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Header><wsse:Security><wsse:UsernameToken><wsse:Username>nvr</wsse:Username><wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordText">%s</wsse:Password></wsse:UsernameToken></wsse:Security></s:Header><s:Body><tds:%s/></s:Body></s:Envelope>`, password, operation)
	req := httptest.NewRequest(http.MethodPost, "http://127.0.0.1:8080/onvif/device_service", strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	return rec
}

func TestConcurrentRuntimeConfiguration(t *testing.T) {
	cfg := runtimeConfig(t)
	handler := newSOAPHandler(cfg, authStreamProvider{}, "test")
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 50 {
				// A request may straddle publication. It must either reject the old credential
				// or return the matching host from the same coherent snapshot.
				rec := runtimeSOAP(handler, "old-password", "GetCapabilities")
				if rec.Code != http.StatusUnauthorized && (rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "192.0.2.10")) {
					t.Errorf("mixed runtime SOAP snapshot: %d %s", rec.Code, rec.Body.String())
				}
				host := rtspHost(&sms.MediaServer{IP: "127.0.0.1"}, cfg)
				if host != "192.0.2.10" && host != "192.0.2.20" {
					t.Errorf("unexpected RTSP host %q", host)
				}
			}
		})
	}
	for range 10 {
		publishRuntime(t, cfg, "new-password", "192.0.2.20")
		publishRuntime(t, cfg, "old-password", "192.0.2.10")
	}
	wg.Wait()
	publishRuntime(t, cfg, "new-password", "192.0.2.20")
	if host := rtspHost(&sms.MediaServer{IP: "127.0.0.1"}, cfg); host != "192.0.2.20" {
		t.Fatalf("RTSP host %q", host)
	}
}

func TestRTSPRuntimeHostPriority(t *testing.T) {
	cfg := runtimeConfig(t)
	local := &sms.MediaServer{IP: "127.0.0.1"}
	publishRuntime(t, cfg, "new-password", "192.0.2.20")
	if got := rtspHost(local, cfg); got != "192.0.2.20" {
		t.Fatalf("got %q", got)
	}
	if err := cfg.Update(func(next *conf.Bootstrap) error { next.Media.SDPIP = ""; next.Sip.Host = "192.0.2.30"; return nil }); err != nil {
		t.Fatal(err)
	}
	if got := rtspHost(local, cfg); got != "192.0.2.30" {
		t.Fatalf("SIP fallback got %q", got)
	}
	remote := &sms.MediaServer{IP: "192.0.2.40", StreamIP: "192.0.2.50"}
	if got := rtspHost(remote, cfg); got != "192.0.2.50" {
		t.Fatalf("remote node got %q", got)
	}
}

// Real WSSE PasswordDigest clients must also switch without recreating routes.
func TestSOAPRuntimeDigestCredentials(t *testing.T) {
	cfg := runtimeConfig(t)
	handler := newSOAPHandler(cfg, authStreamProvider{}, "test")
	check := func(password string, status int) {
		t.Helper()
		nonce := []byte("isolated-test-nonce")
		created := time.Now().UTC().Format(time.RFC3339)
		hash := sha1.New()
		hash.Write(nonce)
		hash.Write([]byte(created))
		hash.Write([]byte(password))
		digest := base64.StdEncoding.EncodeToString(hash.Sum(nil))
		body := fmt.Sprintf(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope"><s:Header><wsse:Security><wsse:UsernameToken><wsse:Username>nvr</wsse:Username><wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordDigest">%s</wsse:Password><wsse:Nonce>%s</wsse:Nonce><wsu:Created>%s</wsu:Created></wsse:UsernameToken></wsse:Security></s:Header><s:Body><tds:GetDeviceInformation/></s:Body></s:Envelope>`, digest, base64.StdEncoding.EncodeToString(nonce), created)
		for _, path := range []string{"/onvif/device_service", "/onvif/media_service"} {
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, path, strings.NewReader(body)))
			if rec.Code != status {
				t.Fatalf("%s digest status=%d want=%d", path, rec.Code, status)
			}
		}
	}
	check("old-password", http.StatusOK)
	publishRuntime(t, cfg, "new-password", "192.0.2.20")
	check("old-password", http.StatusUnauthorized)
	check("new-password", http.StatusOK)
}
