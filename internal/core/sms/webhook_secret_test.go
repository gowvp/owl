package sms

import (
	"encoding/json/v2"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// TestWebhookSecretLifetime 验证初始化时生成六位随机值，读取和下发配置不会重新生成。
func TestWebhookSecretLifetime(t *testing.T) {
	n := NewNodeManager(nil)
	t.Cleanup(n.Close)
	secret := n.WebhookSecret()
	if len(secret) != 6 || strings.ContainsAny(secret, "?&= ") {
		t.Fatal("启动密钥必须为可直接用于 URL 的六位随机值")
	}
	for range 3 {
		u, err := url.Parse(n.webhookURL(&MediaServer{HookIP: "127.0.0.1"}, 15123))
		if err != nil {
			t.Fatal(err)
		}
		if u.Query().Get("owl_secret") != secret || n.WebhookSecret() != secret {
			t.Fatal("同一次运行的配置下发必须使用同一个密钥")
		}
	}
	var uninitialized *NodeManager
	if uninitialized.WebhookSecret() != "" {
		t.Fatal("未初始化的节点管理器不得返回可用密钥")
	}
}

// TestZLMWebhookQuery 验证真实配置请求中的每条启用回调保留查询密钥与正确路径。
func TestZLMWebhookQuery(t *testing.T) {
	captured := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		response := `{"code":0,"data":[{}]}`
		if r.URL.Path == "/index/api/setServerConfig" {
			var request map[string]any
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
				return
			}
			if err := json.Unmarshal(body, &request); err != nil {
				t.Error(err)
				return
			}
			captured <- request
			response = `{"code":0,"changed":1}`
		}
		if _, err := io.WriteString(w, response); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	ms := webhookTestMediaServer(t, srv)
	if err := NewZLMDriver().Setup(t.Context(), ms, "http://127.0.0.1:15123/webhook?owl_secret=Ab1234"); err != nil {
		t.Fatal(err)
	}
	request := <-captured
	for _, path := range []string{"on_play", "on_publish", "on_stream_none_reader", "on_stream_not_found", "on_record_mp4", "on_stream_changed", "on_server_keepalive", "on_server_started"} {
		verifyWebhookURL(t, request["hook."+path], path)
	}
}

// webhookTestMediaServer 让驱动请求隔离的本机配置 API，避免连接真实媒体服务器。
func webhookTestMediaServer(t *testing.T, srv *httptest.Server) *MediaServer {
	t.Helper()
	host, port, err := net.SplitHostPort(strings.TrimPrefix(srv.URL, "http://"))
	if err != nil {
		t.Fatal(err)
	}
	value, err := strconv.Atoi(port)
	if err != nil {
		t.Fatal(err)
	}
	return &MediaServer{IP: host, Ports: MediaServerPorts{HTTP: value}, HookAliveInterval: 10}
}

// verifyWebhookURL 核验最终 URL 的路径和参数，避免密钥被错误地拼到路径之前。
func verifyWebhookURL(t *testing.T, value any, path string) {
	t.Helper()
	raw, ok := value.(string)
	if !ok {
		t.Fatalf("回调 %s 未配置", path)
	}
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatal(err)
	}
	if u.Path != "/webhook/"+path || u.Query().Get("owl_secret") != "Ab1234" {
		t.Fatalf("回调 %s 的路径或 owl_secret 不符合预期", path)
	}
}

// TestLALWebhookQuery 验证共用媒体回调路由的 LAL 配置同步携带启动密钥。
func TestLALWebhookQuery(t *testing.T) {
	captured := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		var request struct {
			Hooks map[string]any `json:"http_notify"`
		}
		if err := json.Unmarshal(body, &request); err != nil {
			t.Error(err)
			return
		}
		captured <- request.Hooks
		if _, err := io.WriteString(w, `{"code":0}`); err != nil {
			t.Error(err)
		}
	}))
	defer srv.Close()
	if err := NewLalmaxDriver().Setup(t.Context(), webhookTestMediaServer(t, srv), "http://127.0.0.1:15123/webhook?owl_secret=Ab1234"); err != nil {
		t.Fatal(err)
	}
	hooks := <-captured
	for field, path := range map[string]string{
		"on_keepalive": "on_server_keepalive", "on_stream_changed": "on_stream_changed", "on_sub_start_without_stream": "on_stream_not_found",
	} {
		verifyWebhookURL(t, hooks[field], path)
	}
}

// TestWebhookInvalidURL 验证非法配置地址在请求发出前失败，错误信息不泄漏随机密钥。
func TestWebhookInvalidURL(t *testing.T) {
	for _, driver := range []Driver{NewZLMDriver(), NewLalmaxDriver()} {
		err := driver.Setup(t.Context(), &MediaServer{}, "http://%/webhook?owl_secret=Ab1234")
		if err == nil || strings.Contains(err.Error(), "Ab1234") {
			t.Fatal("非法回调地址必须失败且错误信息不得包含密钥")
		}
	}
}
