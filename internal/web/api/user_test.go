package api

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gowvp/owl/internal/conf"
)

// TestLoginResetAccount 验证只有默认账号和默认密码同时存在时提示修改凭据。
func TestLoginResetAccount(t *testing.T) {
	for _, tt := range []struct {
		username string
		password string
		reset    bool
	}{{"admin", "admin", true}, {"operator", "admin", false}, {"admin", "changed", false}} {
		t.Run(tt.username+"-"+tt.password, func(t *testing.T) {
			api, r := newLoginTestAPI(t)
			api.conf.Server.Username, api.conf.Server.Password = tt.username, tt.password
			body := encryptedLoginBody(t, api, fmt.Sprintf(`{"username":%q,"password":%q}`, tt.username, tt.password))
			w := loginRequest(r, body)
			if w.Code != http.StatusOK {
				t.Fatalf("登录失败: %d", w.Code)
			}
			var result map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if reset, ok := result["reset_account"].(bool); !ok || reset != tt.reset {
				t.Fatalf("reset_account 期望 %v，实际 %v", tt.reset, result["reset_account"])
			}
		})
	}
}

// newLoginTestAPI 创建独立登录路由，避免测试之间共享失败次数或密钥。
func newLoginTestAPI(t *testing.T) (UserAPI, *gin.Engine) {
	t.Helper()
	cfg := conf.DefaultConfig()
	api := NewUserAPI(&cfg)
	r := gin.New()
	RegisterUser(r, api)
	return api, r
}

// encryptedLoginBody 使用真实 RSA 加密，覆盖客户端到服务端的解密与登录路径。
func encryptedLoginBody(t *testing.T, api UserAPI, credentials string) string {
	t.Helper()
	key, err := api.secret.GetOrCreatePublicKey()
	if err != nil {
		t.Fatal(err)
	}
	data, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, key, []byte(credentials), nil)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf(`{"data":%q}`, base64.StdEncoding.EncodeToString(data))
}

// loginRequest 通过实际 Gin 路由执行请求，保留绑定失败和中间件行为。
func loginRequest(r *gin.Engine, body string) *httptest.ResponseRecorder {
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	return w
}

// TestLoginFailureLock 验证用户名、密码、解密和绑定失败共用五次上限。
func TestLoginFailureLock(t *testing.T) {
	api, r := newLoginTestAPI(t)
	bodies := []string{
		encryptedLoginBody(t, api, `{"username":"unknown","password":"admin"}`),
		encryptedLoginBody(t, api, `{"username":"admin","password":"wrong"}`),
		`{"data":"invalid"}`,
		`{}`,
		encryptedLoginBody(t, api, `{"username":"another","password":"wrong"}`),
	}
	for i, body := range bodies {
		w := loginRequest(r, body)
		if w.Code < 400 {
			t.Fatalf("失败请求 %d 意外成功: %s", i+1, w.Body.String())
		}
	}
	w := loginRequest(r, encryptedLoginBody(t, api, `{"username":"admin","password":"admin"}`))
	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("第五次失败后正确凭据也应被锁定，实际 %d: %s", w.Code, w.Body.String())
	}
}

// TestLoginEmptyConfig 验证任一凭据为空时拒绝登录且不回填默认密码。
func TestLoginEmptyConfig(t *testing.T) {
	for _, credentials := range [][2]string{{"", ""}, {"admin", ""}, {"", "admin"}} {
		t.Run(fmt.Sprintf("%q-%q", credentials[0], credentials[1]), func(t *testing.T) {
			api, r := newLoginTestAPI(t)
			api.conf.Server.Username, api.conf.Server.Password = credentials[0], credentials[1]
			for _, input := range []string{
				`{"username":"admin","password":"admin"}`,
				fmt.Sprintf(`{"username":%q,"password":%q}`, credentials[0], credentials[1]),
			} {
				w := loginRequest(r, encryptedLoginBody(t, api, input))
				if w.Code < 400 {
					t.Fatalf("空配置不得登录，实际 %d: %s", w.Code, w.Body.String())
				}
			}
			if api.conf.Server.Username != credentials[0] || api.conf.Server.Password != credentials[1] {
				t.Fatal("登录不得修改配置中的空凭据")
			}
		})
	}
}

// TestLoginKeyRateLimit 验证不同来源共用密钥接口的每分钟五次配额。
func TestLoginKeyRateLimit(t *testing.T) {
	_, r := newLoginTestAPI(t)
	for i := range 6 {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/login/key", nil)
		req.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", i+1)
		r.ServeHTTP(w, req)
		want := http.StatusOK
		if i == 5 {
			want = http.StatusTooManyRequests
		}
		if w.Code != want {
			t.Fatalf("请求 %d: 期望 %d，实际 %d: %s", i+1, want, w.Code, w.Body.String())
		}
	}
}

// TestLoginLockEscalation 验证锁定边界、逐次升级、八小时封顶和成功登录清零。
func TestLoginLockEscalation(t *testing.T) {
	api, r := newLoginTestAPI(t)
	now := time.Now()
	api.guard.now = func() time.Time { return now }
	valid := encryptedLoginBody(t, api, `{"username":"admin","password":"admin"}`)
	for range 4 {
		loginRequest(r, `{}`)
	}
	for _, duration := range []time.Duration{time.Minute, 5 * time.Minute, time.Hour, 8 * time.Hour, 8 * time.Hour} {
		if w := loginRequest(r, `{}`); w.Code < 400 || w.Code == http.StatusTooManyRequests {
			t.Fatalf("解锁后的失败应执行登录并重新锁定，实际 %d", w.Code)
		}
		until := now.Add(duration)
		now = until.Add(-time.Nanosecond)
		if w := loginRequest(r, valid); w.Code != http.StatusTooManyRequests || w.Header().Get("Retry-After") != "1" {
			t.Fatalf("到期前应锁定，实际 %d: %s", w.Code, w.Body.String())
		}
		now = until
	}
	if w := loginRequest(r, valid); w.Code != http.StatusOK {
		t.Fatalf("到期时正确凭据应成功，实际 %d: %s", w.Code, w.Body.String())
	}
	for range 4 {
		loginRequest(r, `{}`)
	}
	if w := loginRequest(r, valid); w.Code != http.StatusOK {
		t.Fatalf("成功后应重新允许四次失败，实际 %d", w.Code)
	}
}

// TestLoginConcurrentFailures 验证并发请求只有前五次能进入登录校验。
func TestLoginConcurrentFailures(t *testing.T) {
	_, r := newLoginTestAPI(t)
	var wg sync.WaitGroup
	results := make(chan int, 20)
	for range cap(results) {
		wg.Go(func() { results <- loginRequest(r, `{}`).Code })
	}
	wg.Wait()
	close(results)
	var failures, locked int
	for status := range results {
		if status == http.StatusTooManyRequests {
			locked++
		} else if status >= 400 {
			failures++
		} else {
			t.Fatalf("无效请求意外成功: %d", status)
		}
	}
	if failures != 5 || locked != 15 {
		t.Fatalf("期望 5 次失败、15 次拦截，实际 %d、%d", failures, locked)
	}
}

// TestLoginKeyRollingWindow 验证窗口按实际通过时间滚动，不在整分钟边界额外放行。
func TestLoginKeyRollingWindow(t *testing.T) {
	now := time.Now()
	r := gin.New()
	r.GET("/login/key", newLoginKeyWindow(func() time.Time { return now }), func(c *gin.Context) { c.Status(http.StatusOK) })
	request := func() int {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/login/key", nil))
		return w.Code
	}
	for range 5 {
		if status := request(); status != http.StatusOK {
			t.Fatalf("窗口内前五次应放行，实际 %d", status)
		}
	}
	now = now.Add(time.Minute - time.Nanosecond)
	if status := request(); status != http.StatusTooManyRequests {
		t.Fatalf("一分钟内第六次应拦截，实际 %d", status)
	}
	now = now.Add(time.Nanosecond)
	for range 5 {
		if status := request(); status != http.StatusOK {
			t.Fatalf("一分钟到期应恢复配额，实际 %d", status)
		}
	}
	if status := request(); status != http.StatusTooManyRequests {
		t.Fatalf("新窗口第六次应拦截，实际 %d", status)
	}
}
