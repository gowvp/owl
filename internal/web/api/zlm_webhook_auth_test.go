package api

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/gowvp/owl/internal/core/sms"
)

// TestZLMWebhookMissingSecret 验证所有媒体回调在缺失参数时都先于业务处理拒绝请求。
func TestZLMWebhookMissingSecret(t *testing.T) {
	r := gin.New()
	registerZLMWebhookAPI(r, WebHookAPI{}, func(c *gin.Context) {
		c.Status(http.StatusNoContent)
		c.Abort()
	})
	for _, path := range []string{"on_server_started", "on_server_keepalive", "on_stream_changed", "on_publish", "on_play", "on_stream_none_reader", "on_rtp_server_timeout", "on_stream_not_found", "on_record_mp4"} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/webhook/"+path, nil))
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("回调 %s 缺失参数时期望 401，实际 %d", path, w.Code)
		}
	}
}

// TestZLMWebhookSecret 验证认证先于业务处理，并拒绝错误、重复、旧参数名及仅请求体或头部携带的密钥。
func TestZLMWebhookSecret(t *testing.T) {
	n := sms.NewNodeManager(nil)
	t.Cleanup(n.Close)
	secret := n.WebhookSecret()
	for _, tt := range []struct {
		name  string
		query string
		want  int
	}{
		{"缺失", "", http.StatusUnauthorized},
		{"空值", "?owl_secret=", http.StatusUnauthorized},
		{"错误值", "?owl_secret=!!!!!!", http.StatusUnauthorized},
		{"超长", "?owl_secret=" + secret + "x", http.StatusUnauthorized},
		{"旧参数名", "?secret=" + secret, http.StatusUnauthorized},
		{"重复参数", "?owl_secret=" + secret + "&owl_secret=" + secret, http.StatusUnauthorized},
		{"正确参数", "?owl_secret=" + secret, http.StatusNoContent},
	} {
		t.Run(tt.name, func(t *testing.T) {
			r := gin.New()
			var handled bool
			var loggedQuery string
			r.Use(func(c *gin.Context) {
				c.Next()
				loggedQuery = c.Request.URL.RawQuery
			})
			registerZLMWebhookAPI(r, WebHookAPI{smsCore: sms.Core{NodeManager: n}}, func(c *gin.Context) {
				handled = true
				c.Status(http.StatusNoContent)
				c.Abort()
			})
			w := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/webhook/on_play"+tt.query, strings.NewReader(`{"owl_secret":"`+secret+`"}`))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("owl_secret", secret)
			r.ServeHTTP(w, req)
			if w.Code != tt.want || handled != (tt.want == http.StatusNoContent) {
				t.Fatalf("期望状态 %d，实际 %d，业务处理 %v", tt.want, w.Code, handled)
			}
			if strings.Contains(loggedQuery, "owl_secret") || strings.Contains(req.RequestURI, "owl_secret") {
				t.Fatal("owl_secret 不得出现在处理结束后的请求日志字段中")
			}
		})
	}
}

// TestWebhookEventsKeepsOwnAuth 验证事件接收路由不被媒体回调的参数认证中间件覆盖。
func TestWebhookEventsKeepsOwnAuth(t *testing.T) {
	r := gin.New()
	registerZLMWebhookAPI(r, WebHookAPI{}, func(c *gin.Context) {
		c.Status(http.StatusNoContent)
		c.Abort()
	})
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/webhook/events", nil))
	if w.Code != http.StatusNoContent {
		t.Fatalf("事件入口不应被 owl_secret 中间件拦截，实际 %d", w.Code)
	}
}
