package api

import (
	"encoding/json/v2"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"testing/synctest"
	"time"
)

func assertLoginLimitMessage(t *testing.T, w *httptest.ResponseRecorder, msg string) {
	t.Helper()
	var response struct {
		Reason string `json:"reason"`
		Msg    string `json:"msg"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if w.Code != http.StatusTooManyRequests || response.Reason != "ErrTooManyRequests" || response.Msg != msg {
		t.Fatalf("unexpected limit response: status=%d reason=%q msg=%q", w.Code, response.Reason, response.Msg)
	}
	t.Logf("status=%d reason=%s msg=%s Retry-After=%q", w.Code, response.Reason, response.Msg, w.Header().Get("Retry-After"))
}

// Independent sources consume the same key quota; the bucket is the only public-key rate limit.
func TestLoginLimitResponseEvidence(t *testing.T) {
	t.Run("key_quota_shared_by_distinct_sources", func(t *testing.T) {
		synctest.Test(t, func(t *testing.T) {
			_, r := newLoginTestAPI(t)
			for i := range 21 {
				w := httptest.NewRecorder()
				req := httptest.NewRequest(http.MethodGet, "/login/key", nil)
				req.RemoteAddr = fmt.Sprintf("192.0.2.%d:1234", i+1)
				r.ServeHTTP(w, req)
				if i < 20 {
					if w.Code != http.StatusOK {
						t.Fatalf("key request %d status=%d", i+1, w.Code)
					}
				} else {
					t.Log("GET /login/key request 21, different source IP on every request")
					assertLoginLimitMessage(t, w, "服务器繁忙")
				}
			}
		})
	})
	t.Run("locked_account_post_response", func(t *testing.T) {
		api, r := newLoginTestAPI(t)
		now := time.Now()
		api.guard.now = func() time.Time { return now }
		for i := range 5 {
			if w := loginRequest(r, `{}`); w.Code != http.StatusBadRequest {
				t.Fatalf("failure %d status=%d", i+1, w.Code)
			}
		}
		w := loginRequest(r, `{}`)
		t.Log("POST /login request 6 after 5 binding failures, frozen test clock")
		assertLoginLimitMessage(t, w, "登录请求受限，请在 60 秒后重试")
		if w.Header().Get("Retry-After") != "60" {
			t.Fatal("missing retry delay")
		}
	})
	t.Run("normal_key_then_login_sequence", func(t *testing.T) {
		api, r := newLoginTestAPI(t)
		api.conf.Server.Username = "isolated-account"
		api.conf.Server.Password = "isolated-expected-input"
		body := encryptedLoginBody(t, api, `{"username":"isolated-account","password":"isolated-wrong-input"}`)
		for i := range 5 {
			key := httptest.NewRecorder()
			r.ServeHTTP(key, httptest.NewRequest(http.MethodGet, "/login/key", nil))
			if key.Code != http.StatusOK {
				t.Fatalf("attempt %d key status=%d", i+1, key.Code)
			}
			w := loginRequest(r, body)
			var response struct {
				Msg string `json:"msg"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if w.Code != http.StatusBadRequest || response.Msg != "用户名或密码错误" {
				t.Fatalf("attempt %d status=%d msg=%q", i+1, w.Code, response.Msg)
			}
		}
		key := httptest.NewRecorder()
		r.ServeHTTP(key, httptest.NewRequest(http.MethodGet, "/login/key", nil))
		t.Log("5 ordinary failed attempts; attempt 6 key succeeds under the 20-token bucket, POST is account-locked")
		if key.Code != http.StatusOK {
			t.Fatalf("attempt 6 key status=%d", key.Code)
		}
		w := loginRequest(r, body)
		var response struct {
			Msg string `json:"msg"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if w.Code != http.StatusTooManyRequests || !strings.HasPrefix(response.Msg, "登录请求受限，请在 ") {
			t.Fatalf("attempt 6 post status=%d msg=%q", w.Code, response.Msg)
		}
		if w.Header().Get("Retry-After") == "" {
			t.Fatal("account lock must retain retry delay")
		}
	})
}
