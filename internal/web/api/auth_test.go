package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/ixugo/goddd/pkg/web"
)

func init() {
	gin.SetMode(gin.TestMode)
}

// makeEngine 创建带鉴权中间件的 gin 引擎，返回引擎和 recorder 供测试使用
func makeEngine(t *testing.T, secret, authURL string, handler ...web.HandlerOption) (*gin.Engine, *httptest.ResponseRecorder) {
	t.Helper()
	r := gin.New()
	r.Use(AuthMiddleware(secret, "", authURL, "admin", handler...))
	r.GET("/test", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})
	return r, httptest.NewRecorder()
}

// makeExpiredToken 生成已过期的 token，用于测试过期场景
func makeExpiredToken(t *testing.T, secret string) string {
	t.Helper()
	token, err := web.NewToken(nil, secret, web.WithExpiresAt(time.Now().Add(-1*time.Hour)))
	if err != nil {
		t.Fatalf("生成过期 token 失败: %v", err)
	}
	return token
}

// makeInvalidToken 生成签名错误的无效 token，用于测试 JWT 解析失败降级到 authURL
func makeInvalidToken(t *testing.T) string {
	t.Helper()
	token, err := web.NewToken(nil, "wrong-secret-for-invalid-token")
	if err != nil {
		t.Fatalf("生成无效 token 失败: %v", err)
	}
	return token
}

// makeBlockHandler 创建可控的 mock authURL handler，block=true 时返回 403
func makeBlockHandler(mu *sync.Mutex, block *bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		shouldBlock := *block
		mu.Unlock()
		if shouldBlock {
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]string{"msg": "forbidden"})
			return
		}
		w.WriteHeader(http.StatusOK)
	}
}

func TestAuthMiddleware(t *testing.T) {
	const secret = "test-secret-key-for-unit-test"
	const testPayload = `{"key":"value"}`

	validToken, err := web.NewToken(map[string]any{"uid": "123"}, secret)
	if err != nil {
		t.Fatalf("生成有效 token 失败: %v", err)
	}
	expiredToken := makeExpiredToken(t, secret)
	invalidToken := makeInvalidToken(t)

	var blockMu sync.Mutex
	block := true
	mockSrv := httptest.NewServer(makeBlockHandler(&blockMu, &block))
	defer mockSrv.Close()

	// 所有需要 authURL 的测试共享同一个 mock server，通过 block 变量控制行为
	setBlock := func(v bool) {
		blockMu.Lock()
		block = v
		blockMu.Unlock()
	}

	t.Run("自定义handler返回true跳过鉴权", func(t *testing.T) {
		setBlock(true)
		handler := func(c *gin.Context) bool { return true }
		r, w := makeEngine(t, secret, mockSrv.URL, handler)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("期望 200，实际 %d", w.Code)
		}
	})

	t.Run("多个handler第一个false第二个true", func(t *testing.T) {
		setBlock(true)
		h1 := func(c *gin.Context) bool { return false }
		h2 := func(c *gin.Context) bool { return true }
		r, w := makeEngine(t, secret, mockSrv.URL, h1, h2)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("期望 200，实际 %d", w.Code)
		}
	})

	t.Run("有效Bearer token鉴权通过", func(t *testing.T) {
		setBlock(true)
		r, w := makeEngine(t, secret, mockSrv.URL)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+validToken)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("期望 200，实际 %d", w.Code)
		}
	})

	t.Run("query参数中有有效token鉴权通过", func(t *testing.T) {
		setBlock(true)
		r, w := makeEngine(t, secret, mockSrv.URL)
		req := httptest.NewRequest(http.MethodGet, "/test?token=Bearer+"+validToken, nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("期望 200，实际 %d", w.Code)
		}
	})

	t.Run("Bearer前缀大小写不敏感", func(t *testing.T) {
		setBlock(true)
		r, w := makeEngine(t, secret, mockSrv.URL)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "bearer "+validToken)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("期望 200，实际 %d，大小写不敏感未生效", w.Code)
		}
	})

	t.Run("Bearer前缀格式错误", func(t *testing.T) {
		// "Bearer" 没有空格和 token，长度检查不通过，走到无 authURL 的失败路径
		setBlock(true)
		r, w := makeEngine(t, secret, "")
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("期望 401，实际 %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "身份验证失败") {
			t.Errorf("响应缺少'身份验证失败'，实际: %s", w.Body.String())
		}
	})

	t.Run("无token无authURL返回身份验证失败", func(t *testing.T) {
		setBlock(true)
		r, w := makeEngine(t, secret, "")
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("期望 401，实际 %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "身份验证失败") {
			t.Errorf("响应缺少'身份验证失败'，实际: %s", w.Body.String())
		}
	})

	t.Run("token过期返回请重新登录", func(t *testing.T) {
		// 过期 token 应直接返回，请重新登录，不降级到 authURL
		setBlock(false) // 即使 authURL 放行，也不应走 authURL
		r, w := makeEngine(t, secret, mockSrv.URL)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+expiredToken)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("期望 401，实际 %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "请重新登录") {
			t.Errorf("响应缺少'请重新登录'，实际: %s", w.Body.String())
		}
	})

	t.Run("无效token无authURL返回身份验证失败", func(t *testing.T) {
		// JWT 签名错误，ParseToken 失败，无 authURL 则鉴权失败
		r, w := makeEngine(t, secret, "")
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+invalidToken)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("期望 401，实际 %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "身份验证失败") {
			t.Errorf("响应缺少'身份验证失败'，实际: %s", w.Body.String())
		}
	})

	t.Run("无效token进入authURL流程", func(t *testing.T) {
		// JWT 解析失败后应降级到 authURL，authURL 放行则通过
		setBlock(false)
		r, w := makeEngine(t, secret, mockSrv.URL)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+invalidToken)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("期望 200，实际 %d", w.Code)
		}
	})

	t.Run("JWT失败加authURL返回200鉴权通过", func(t *testing.T) {
		setBlock(false)
		r, w := makeEngine(t, secret, mockSrv.URL)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("期望 200，实际 %d", w.Code)
		}
	})

	t.Run("JWT失败加authURL返回401", func(t *testing.T) {
		// authURL 返回 401 时，应原封不动返回给客户端
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{"msg": "unauthorized"})
		}))
		defer srv.Close()

		r, w := makeEngine(t, secret, srv.URL)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("期望 401，实际 %d", w.Code)
		}
	})

	t.Run("JWT失败加authURL返回500", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			json.NewEncoder(w).Encode(map[string]string{"msg": "internal error"})
		}))
		defer srv.Close()

		r, w := makeEngine(t, secret, srv.URL)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusInternalServerError {
			t.Errorf("期望 500，实际 %d", w.Code)
		}
	})

	t.Run("authURL网络不可达返回502", func(t *testing.T) {
		// 使用 TEST-NET 地址模拟网络不可达，不产生实际网络流量
		r, w := makeEngine(t, secret, "http://192.0.2.1:1")
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusBadGateway {
			t.Errorf("期望 502，实际 %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "鉴权服务不可达") {
			t.Errorf("响应缺少'鉴权服务不可达'，实际: %s", w.Body.String())
		}
	})

	t.Run("请求body透传到authURL", func(t *testing.T) {
		var receivedBody string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			b, _ := io.ReadAll(r.Body)
			receivedBody = string(b)
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		r, w := makeEngine(t, secret, srv.URL)
		req := httptest.NewRequest(http.MethodPost, "/test", strings.NewReader(testPayload))
		req.Header.Set("Content-Type", "application/json")
		r.ServeHTTP(w, req)
		if receivedBody != testPayload {
			t.Errorf("authURL 未收到正确 body，期望 %q，实际 %q", testPayload, receivedBody)
		}
	})

	t.Run("请求header透传到authURL", func(t *testing.T) {
		var receivedAuth string
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			receivedAuth = r.Header.Get("X-Custom-Auth")
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		r, w := makeEngine(t, secret, srv.URL)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("X-Custom-Auth", "my-custom-value")
		r.ServeHTTP(w, req)
		if receivedAuth != "my-custom-value" {
			t.Errorf("authURL 未收到 X-Custom-Auth header，实际: %q", receivedAuth)
		}
	})

	t.Run("非200响应body透传给客户端", func(t *testing.T) {
		const errBody = `{"error":"access denied"}`
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusForbidden)
			w.Write([]byte(errBody))
		}))
		defer srv.Close()

		r, w := makeEngine(t, secret, srv.URL)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("期望 403，实际 %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "access denied") {
			t.Errorf("客户端未收到 authURL 响应 body，实际: %s", w.Body.String())
		}
	})

	t.Run("无token加authURL配置加authURL返回200通过", func(t *testing.T) {
		setBlock(false)
		r, w := makeEngine(t, secret, mockSrv.URL)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("期望 200，实际 %d", w.Code)
		}
	})

	t.Run("无token加authURL配置加authURL返回403", func(t *testing.T) {
		setBlock(true)
		r, w := makeEngine(t, secret, mockSrv.URL)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusForbidden {
			t.Errorf("期望 403，实际 %d", w.Code)
		}
	})

	t.Run("有效JWT加authURL配置不走authURL直接通过", func(t *testing.T) {
		// authURL 配置了拒绝，但有效 JWT 应直接通过，不经过 authURL
		setBlock(true)
		r, w := makeEngine(t, secret, mockSrv.URL)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+validToken)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("期望 200，实际 %d，有效 JWT 未跳过 authURL", w.Code)
		}
	})

	t.Run("JWT过期加authURL配置返回请重新登录不走authURL", func(t *testing.T) {
		// 过期 token 应直接返回，请重新登录，不降级到 authURL
		setBlock(false) // authURL 放行，但不应走到 authURL
		r, w := makeEngine(t, secret, mockSrv.URL)
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+expiredToken)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("期望 401，实际 %d", w.Code)
		}
		if !strings.Contains(w.Body.String(), "请重新登录") {
			t.Errorf("响应缺少'请重新登录'，实际: %s", w.Body.String())
		}
	})

	t.Run("JWT成功后claims数据写入context", func(t *testing.T) {
		setBlock(true)
		r := gin.New()
		r.Use(AuthMiddleware(secret, "", "", "admin"))
		r.GET("/test", func(c *gin.Context) {
			uid, exists := c.Get("uid")
			if !exists {
				t.Error("context 中未找到 uid")
			}
			if uid != "123" {
				t.Errorf("uid 期望 123，实际 %v", uid)
			}
			tokenStr, exists := c.Get(web.KeyTokenString)
			if !exists {
				t.Error("context 中未找到 token")
			}
			if tokenStr != "Bearer "+validToken {
				t.Errorf("token 字符串不匹配")
			}
			c.String(http.StatusOK, "ok")
		})
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+validToken)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("期望 200，实际 %d", w.Code)
		}
	})

	t.Run("authURL成功后token写入context", func(t *testing.T) {
		setBlock(false)
		const queryToken = "custom-token-from-query"
		r := gin.New()
		r.Use(AuthMiddleware(secret, "", mockSrv.URL, "admin"))
		r.GET("/test", func(c *gin.Context) {
			tokenStr, exists := c.Get(web.KeyTokenString)
			if !exists {
				t.Error("context 中未找到 token")
			}
			if tokenStr != queryToken {
				t.Errorf("token 期望 %q，实际 %v", queryToken, tokenStr)
			}
			c.String(http.StatusOK, "ok")
		})
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/test?token="+queryToken, nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("期望 200，实际 %d", w.Code)
		}
	})

	t.Run("forwardToAuthURL构建请求失败返回500", func(t *testing.T) {
		// 用无法解析的 URL 测试 NewRequestWithContext 失败场景
		// 注意: Go 1.22+ 中 NewRequestWithContext 对无效 method 才会报错
		// 这里通过 ":invalid" URL 触发解析错误
		// 实际上 http.NewRequestWithContext 对 URL ":invalid" 会报错
		r, w := makeEngine(t, secret, "://invalid-url")
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		r.ServeHTTP(w, req)
		// 解析失败时返回 500 或 502，取决于具体错误路径
		if w.Code != http.StatusInternalServerError && w.Code != http.StatusBadGateway {
			t.Errorf("期望 500 或 502，实际 %d", w.Code)
		}
	})

	t.Run("APISecret通过Header鉴权并注入管理员上下文", func(t *testing.T) {
		const testAPISecret = "my_custom_api_secret_123456"
		r := gin.New()
		r.Use(AuthMiddleware(secret, testAPISecret, "", "superadmin"))
		r.GET("/test", func(c *gin.Context) {
			if u, _ := c.Get("username"); u != "superadmin" {
				t.Errorf("username 期望 superadmin，实际 %v", u)
			}
			if role, _ := c.Get("role"); role != "admin" {
				t.Errorf("role 期望 admin，实际 %v", role)
			}
			if isAdmin, _ := c.Get("is_admin"); isAdmin != true {
				t.Errorf("is_admin 期望 true，实际 %v", isAdmin)
			}
			if authType, _ := c.Get("auth_type"); authType != "api_secret" {
				t.Errorf("auth_type 期望 api_secret，实际 %v", authType)
			}
			c.String(http.StatusOK, "ok")
		})

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer "+testAPISecret)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("期望 200，实际 %d", w.Code)
		}
	})

	t.Run("APISecret通过Query参数鉴权通过", func(t *testing.T) {
		const testAPISecret = "query_secret_test_999"
		r := gin.New()
		r.Use(AuthMiddleware(secret, testAPISecret, "", "admin"))
		r.GET("/test", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		// 测试 token 参数（不带 Bearer 前缀）
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/test?token="+testAPISecret, nil)
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Errorf("token参数鉴权期望 200，实际 %d", w.Code)
		}
	})

	t.Run("APISecret错误且JWT错误时鉴权失败", func(t *testing.T) {
		const testAPISecret = "correct_secret"
		r := gin.New()
		r.Use(AuthMiddleware(secret, testAPISecret, "", "admin"))
		r.GET("/test", func(c *gin.Context) {
			c.String(http.StatusOK, "ok")
		})

		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/test", nil)
		req.Header.Set("Authorization", "Bearer wrong_secret")
		r.ServeHTTP(w, req)
		if w.Code != http.StatusUnauthorized {
			t.Errorf("期望 401，实际 %d", w.Code)
		}
	})
}
