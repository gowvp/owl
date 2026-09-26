package api

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
)

// 验证 /web 与 Group("/web") 静态挂载共存时路由注册不冲突，
// 且 /web 返回 301 指向 /web/
func TestWebRedirect(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()

	const staticPrefix = "/web"
	admin := r.Group(staticPrefix, gzip.Gzip(gzip.DefaultCompression))
	admin.Static("/", filepath.Join(t.TempDir(), "www"))
	r.GET(staticPrefix, func(ctx *gin.Context) {
		ctx.Redirect(http.StatusPermanentRedirect, staticPrefix+"/")
	})

	req := httptest.NewRequest(http.MethodGet, staticPrefix, nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusPermanentRedirect {
		t.Fatalf("GET /web 状态码 = %d, 期望 %d", w.Code, http.StatusPermanentRedirect)
	}
	if loc := w.Header().Get("Location"); loc != staticPrefix+"/" {
		t.Fatalf("GET /web Location = %q, 期望 %q", loc, staticPrefix+"/")
	}
}
