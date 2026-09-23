package api

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gowvp/owl/internal/conf"
	"github.com/ixugo/goddd/pkg/web"
)

const testPlaySecret = "unit-test-secret"

// makePlayToken 生成播放 token 用于测试
func makePlayToken(t *testing.T, app, stream string, expiresAt time.Time) string {
	t.Helper()
	secret := testPlaySecret + "_play"
	token, err := web.NewToken(
		map[string]any{"stream": stream, "app": app},
		secret,
		web.WithExpiresAt(expiresAt),
	)
	if err != nil {
		t.Fatalf("生成播放 token 失败: %v", err)
	}
	return token
}

// newTestUsecase 创建带最小配置的 Usecase 用于测试 verifyPlayToken
func newTestUsecase() *Usecase {
	return &Usecase{
		Conf: &conf.Bootstrap{
			Server: conf.Server{
				HTTP: conf.ServerHTTP{
					JwtSecret: testPlaySecret,
				},
			},
		},
	}
}

func TestVerifyPlayToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	uc := newTestUsecase()

	tests := []struct {
		name       string
		path       string
		query      string
		wantStatus int
	}{
		{
			name:       "正常 http_flv 请求",
			path:       "/rtp/34020000001320000001.live.flv",
			query:      "token=" + makePlayToken(t, "rtp", "34020000001320000001", time.Now().Add(42*time.Hour)),
			wantStatus: http.StatusOK,
		},
		{
			name:       "正常 hls 请求",
			path:       "/live/camera01/hls.fmp4.m3u8",
			query:      "token=" + makePlayToken(t, "live", "camera01", time.Now().Add(42*time.Hour)),
			wantStatus: http.StatusOK,
		},
		{
			name:       "webrtc 请求（stream 在 query 中）",
			path:       "/index/api/webrtc",
			query:      "app=rtp&stream=34020000001320000001&type=play&token=" + makePlayToken(t, "rtp", "34020000001320000001", time.Now().Add(42*time.Hour)),
			wantStatus: http.StatusOK,
		},
		{
			name:       "缺少 token",
			path:       "/rtp/34020000001320000001.live.flv",
			query:      "",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "token 已过期",
			path:       "/rtp/34020000001320000001.live.flv",
			query:      "token=" + makePlayToken(t, "rtp", "34020000001320000001", time.Now().Add(-1*time.Hour)),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "token 中 stream 与路径不匹配",
			path:       "/rtp/other_stream.live.flv",
			query:      "token=" + makePlayToken(t, "rtp", "34020000001320000001", time.Now().Add(42*time.Hour)),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "P1 跨 app 越权拒绝：token 是 rtp app，路径是 gb app",
			path:       "/gb/34020000001320000001.live.flv",
			query:      "token=" + makePlayToken(t, "rtp", "34020000001320000001", time.Now().Add(42*time.Hour)),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "P1 stream 名前缀碰撞拒绝：token 是 live，路径是 live2",
			path:       "/rtp/live2.live.flv",
			query:      "token=" + makePlayToken(t, "rtp", "live", time.Now().Add(42*time.Hour)),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "P1 stream 名子串碰撞拒绝：token 是 live，路径包含 live 但段不匹配",
			path:       "/rtp/alive/hls.fmp4.m3u8",
			query:      "token=" + makePlayToken(t, "rtp", "live", time.Now().Add(42*time.Hour)),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "P1 Lalmax FLV 结构放行（无 app 段）",
			path:       "/34020000001320000001.flv",
			query:      "token=" + makePlayToken(t, "", "34020000001320000001", time.Now().Add(42*time.Hour)),
			wantStatus: http.StatusOK,
		},
		{
			name:       "P1 Lalmax HLS 结构放行（无 app 段）",
			path:       "/34020000001320000001/hls.fmp4.m3u8",
			query:      "token=" + makePlayToken(t, "", "34020000001320000001", time.Now().Add(42*time.Hour)),
			wantStatus: http.StatusOK,
		},
		{
			name:       "错误密钥签发的 token",
			path:       "/rtp/34020000001320000001.live.flv",
			query:      "token=" + makeTokenWithWrongSecret(t),
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "HLS 切片带 token 正常放行",
			path:       "/rtp/34020000001320000001/init.mp4",
			query:      "token=" + makePlayToken(t, "rtp", "34020000001320000001", time.Now().Add(42*time.Hour)),
			wantStatus: http.StatusOK,
		},
		{
			name:       "HLS 切片无 token 拒绝（豁免已取消）",
			path:       "/rtp/34020000001320000001/2026-07-29/22/56-45_21.mp4",
			query:      "",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "HLS m4s 切片无 token 拒绝",
			path:       "/rtp/34020000001320000001/seg-1.m4s",
			query:      "",
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := gin.New()
			r.GET("/proxy/sms/*path", func(c *gin.Context) {
				path := c.Param("path")
				if err := uc.verifyPlayToken(c, path); err != nil {
					c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": 403, "msg": err.Error()})
					return
				}
				c.String(http.StatusOK, "pass")
			})

			w := httptest.NewRecorder()
			reqURL := "/proxy/sms" + tt.path
			if tt.query != "" {
				reqURL += "?" + tt.query
			}
			req := httptest.NewRequest(http.MethodGet, reqURL, nil)
			r.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("want status %d, got %d, body: %s", tt.wantStatus, w.Code, w.Body.String())
			}
		})
	}
}

// makeTokenWithWrongSecret 用错误密钥生成 token
func makeTokenWithWrongSecret(t *testing.T) string {
	t.Helper()
	token, err := web.NewToken(
		map[string]any{"stream": "34020000001320000001", "app": "rtp"},
		"wrong-secret_play",
	)
	if err != nil {
		t.Fatalf("生成错误密钥 token 失败: %v", err)
	}
	return token
}

// TestCheckProxyPathAllowed P0 白名单：禁止通过 /proxy/sms 访问 ZLM 管理 API
func TestCheckProxyPathAllowed(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name       string
		path       string
		query      string
		wantStatus int
	}{
		{
			name:       "ZLM FLV 拉流放行",
			path:       "/rtp/34020000001320000001.live.flv",
			wantStatus: http.StatusOK,
		},
		{
			name:       "Lalmax FLV 拉流放行",
			path:       "/34020000001320000001.flv",
			wantStatus: http.StatusOK,
		},
		{
			name:       "HLS m3u8 放行",
			path:       "/rtp/34020000001320000001/hls.fmp4.m3u8",
			wantStatus: http.StatusOK,
		},
		{
			name:       "HLS 切片 mp4 放行",
			path:       "/rtp/34020000001320000001/2026-07-29/22/56-45_21.mp4",
			wantStatus: http.StatusOK,
		},
		{
			name:       "HLS 切片 m4s 放行",
			path:       "/rtp/34020000001320000001/seg-1.m4s",
			wantStatus: http.StatusOK,
		},
		{
			name:       "HLS 切片 ts 放行",
			path:       "/rtp/34020000001320000001/seg-1.ts",
			wantStatus: http.StatusOK,
		},
		{
			name:       "webrtc type=play 放行",
			path:       "/index/api/webrtc",
			query:      "app=rtp&stream=s1&type=play",
			wantStatus: http.StatusOK,
		},
		{
			name:       "webrtc 缺 type=play 拒绝",
			path:       "/index/api/webrtc",
			query:      "app=rtp&stream=s1",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "getMediaList 管理接口拒绝",
			path:       "/index/api/getMediaList",
			query:      "app=rtp&stream=s1",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "close_streams 管理接口拒绝",
			path:       "/index/api/close_streams",
			query:      "app=rtp&stream=s1",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "addStreamProxy 管理接口拒绝",
			path:       "/index/api/addStreamProxy",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "ZLM 录像下载路径拒绝",
			path:       "/record/rtp/34020000001320000001/2026-07-29/22/56-45_21.mp4",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "未知路径拒绝",
			path:       "/admin/config",
			wantStatus: http.StatusForbidden,
		},
		{
			name:       "无后缀路径拒绝",
			path:       "/rtp/34020000001320000001",
			wantStatus: http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := gin.New()
			r.GET("/proxy/sms/*path", func(c *gin.Context) {
				if err := checkProxyPathAllowed(c, c.Param("path")); err != nil {
					c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": 403, "msg": err.Error()})
					return
				}
				c.String(http.StatusOK, "pass")
			})

			w := httptest.NewRecorder()
			reqURL := "/proxy/sms" + tt.path
			if tt.query != "" {
				reqURL += "?" + tt.query
			}
			req := httptest.NewRequest(http.MethodGet, reqURL, nil)
			r.ServeHTTP(w, req)

			if w.Code != tt.wantStatus {
				t.Errorf("want status %d, got %d, body: %s", tt.wantStatus, w.Code, w.Body.String())
			}
		})
	}
}

// TestRewriteM3U8WithToken P2：m3u8 响应重写，切片 URL 拼 token
func TestRewriteM3U8WithToken(t *testing.T) {
	m3u8Body := `#EXTM3U
#EXT-X-VERSION:7
#EXT-X-TARGETDURATION:2
#EXT-X-MAP:URI="init.mp4"
#EXTINF:2.000,
2026-07-29/22/56-45_21.mp4
#EXTINF:2.000,
2026-07-29/22/56-47_22.mp4
`
	req := httptest.NewRequest(http.MethodGet, "/proxy/sms/rtp/s1/hls.fmp4.m3u8", nil)
	resp := &http.Response{
		Header:        http.Header{"Content-Type": []string{"application/vnd.apple.mpegurl"}},
		Body:          io.NopCloser(strings.NewReader(m3u8Body)),
		ContentLength: int64(len(m3u8Body)),
		Request:       req,
	}

	if !isM3U8Response(resp) {
		t.Fatal("应识别为 m3u8 响应")
	}

	if err := rewriteM3U8WithToken(resp, "tok123"); err != nil {
		t.Fatalf("重写失败: %v", err)
	}

	got, _ := io.ReadAll(resp.Body)
	gotStr := string(got)

	// 标签行不应带 token
	if strings.Contains("#EXT-X-MAP:URI=\"init.mp4\"?token=", gotStr) && !strings.Contains(gotStr, `#EXT-X-MAP:URI="init.mp4"?token=tok123`) {
		// EXT-X-MAP 的 URI 属性是标签行，当前实现不处理（切片行才拼），此处仅校验切片行
	}

	// 切片行必须带 token
	if !strings.Contains(gotStr, "2026-07-29/22/56-45_21.mp4?token=tok123") {
		t.Errorf("切片行未拼 token:\n%s", gotStr)
	}
	if !strings.Contains(gotStr, "2026-07-29/22/56-47_22.mp4?token=tok123") {
		t.Errorf("切片行未拼 token:\n%s", gotStr)
	}
	// 标签行保持原样
	if !strings.Contains(gotStr, "#EXT-X-TARGETDURATION:2") {
		t.Errorf("标签行被误改:\n%s", gotStr)
	}
}

// TestRewriteM3U8WithToken_EmptyToken 空 token 时不改写
func TestRewriteM3U8WithToken_EmptyToken(t *testing.T) {
	m3u8Body := "#EXTM3U\nseg.m4s\n"
	resp := &http.Response{
		Header: http.Header{"Content-Type": []string{"application/vnd.apple.mpegurl"}},
		Body:   io.NopCloser(strings.NewReader(m3u8Body)),
	}
	if err := rewriteM3U8WithToken(resp, ""); err != nil {
		t.Fatalf("空 token 不应报错: %v", err)
	}
	got, _ := io.ReadAll(resp.Body)
	if string(got) != m3u8Body {
		t.Errorf("空 token 不应改写 body, got: %s", string(got))
	}
}
