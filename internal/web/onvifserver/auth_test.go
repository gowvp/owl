package onvifserver

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gowvp/onvif/server"
	"github.com/gowvp/owl/internal/conf"
)

// authStreamProvider 隔离媒体和数据库，只用于验证真实 SOAP 认证。
type authStreamProvider struct{}

// ListStreamProfiles 返回空列表，使认证测试不访问数据库。
func (authStreamProvider) ListStreamProfiles() []server.StreamProfile { return nil }

// GetStreamURI 返回空地址，认证测试不连接媒体服务。
func (authStreamProvider) GetStreamURI(string, string) string { return "" }

// GetSnapshotURI 返回空地址，认证测试不请求截图。
func (authStreamProvider) GetSnapshotURI(string, string) string { return "" }

// TestSOAPIndependentCredentials 验证两个 SOAP 端点拒绝 Web 凭据，并在修改 Web 密码后仍接受独立 ONVIF 凭据。
func TestSOAPIndependentCredentials(t *testing.T) {
	cfg := &conf.Bootstrap{
		Server: conf.Server{Username: "web-user", Password: "web-password"},
		ONVIF:  conf.ONVIF{Username: "nvr-user", Password: "nvr-password"},
	}
	handler := newSOAPServer(cfg, authStreamProvider{}, "test", "127.0.0.1").Handler()
	// 模拟网页修改账号密码，ONVIF 已创建对象及后续重建对象都应继续使用独立凭据。
	cfg.Server.Username, cfg.Server.Password = "new-web-user", "new-web-password"
	cases := []struct {
		name, username, password string
		status                   int
	}{
		{"未认证", "", "", http.StatusUnauthorized},
		{"原网页凭据", "web-user", "web-password", http.StatusUnauthorized},
		{"新网页凭据", "new-web-user", "new-web-password", http.StatusUnauthorized},
		{"错误ONVIF密码", "nvr-user", "wrong-password", http.StatusUnauthorized},
		{"独立ONVIF凭据", "nvr-user", "nvr-password", http.StatusOK},
	}
	for _, path := range []string{"/onvif/device_service", "/onvif/media_service"} {
		for _, tc := range cases {
			t.Run(path+"/"+tc.name, func(t *testing.T) {
				checkSOAPAuth(t, handler, path, conf.ONVIF{Username: tc.username, Password: tc.password}, tc.status)
			})
		}
	}
	rebuilt := newSOAPServer(cfg, authStreamProvider{}, "test", "127.0.0.1").Handler()
	checkSOAPAuth(t, rebuilt, "/onvif/device_service", cfg.ONVIF, http.StatusOK)
}

// TestSOAPGeneratedCredentials 验证 Web 凭据为空时，启动补全的 ONVIF 凭据仍强制认证。
func TestSOAPGeneratedCredentials(t *testing.T) {
	cfg := &conf.Bootstrap{ConfigPath: filepath.Join(t.TempDir(), "config.toml")}
	if err := cfg.InitONVIF(); err != nil {
		t.Fatal(err)
	}
	handler := newSOAPServer(cfg, authStreamProvider{}, "test", "127.0.0.1").Handler()
	checkSOAPAuth(t, handler, "/onvif/device_service", conf.ONVIF{}, http.StatusUnauthorized)
	checkSOAPAuth(t, handler, "/onvif/device_service", cfg.ONVIF, http.StatusOK)
}

// checkSOAPAuth 发送真实 SOAP 请求并检查状态和成功响应，验证依赖的 WS-Security 认证行为。
func checkSOAPAuth(t *testing.T, handler http.Handler, path string, credentials conf.ONVIF, status int) {
	t.Helper()
	body := fmt.Sprintf(`<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope">
<s:Header><wsse:Security><wsse:UsernameToken>
<wsse:Username>%s</wsse:Username>
<wsse:Password Type="http://docs.oasis-open.org/wss/2004/01/oasis-200401-wss-username-token-profile-1.0#PasswordText">%s</wsse:Password>
</wsse:UsernameToken></wsse:Security></s:Header>
<s:Body><tds:GetDeviceInformation/></s:Body></s:Envelope>`, credentials.Username, credentials.Password)
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != status {
		t.Fatalf("SOAP HTTP 状态 = %d，期望 %d", rec.Code, status)
	}
	if status == http.StatusOK && !strings.Contains(rec.Body.String(), "GetDeviceInformationResponse") {
		t.Fatal("成功认证后应返回设备信息")
	}
}
