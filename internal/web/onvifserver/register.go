package onvifserver

import (
	"context"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/gowvp/onvif/server"
	"github.com/gowvp/owl/internal/conf"
	"github.com/gowvp/owl/internal/core/ipc"
	"github.com/gowvp/owl/internal/core/sms"
)

// Register 注册 ONVIF SOAP 端点和发现服务，由独立 ONVIF 凭据认证，不经过 Web JWT 鉴权。
func Register(g gin.IRouter, ipcCore ipc.Core, smsCore sms.Core, cfg *conf.Bootstrap) {
	provider := NewStreamProvider(ipcCore, smsCore, cfg)
	snapshot := cfg.Snapshot()
	serial := snapshot.BuildVersion
	if serial == "" {
		serial = "gowvp-owl"
	}
	handler := newSOAPHandler(cfg, provider, serial)
	// 仅注册 SOAP 端点，避免与 /onvif/discover 等 REST 路由冲突。
	h := gin.WrapH(handler)
	g.Any("/onvif/device_service", h)
	g.Any("/onvif/media_service", h)
	g.GET("/onvif/profiles", func(c *gin.Context) {
		snap := provider.ProfileSnapshot()
		c.JSON(http.StatusOK, gin.H{"count": len(snap), "profiles": snap})
	})

	startDiscovery(context.Background(), cfg, serial)
}

// newSOAPServer 构建使用独立 ONVIF 凭据的 SOAP 服务，网页登录密码不参与认证。
func newSOAPServer(cfg *conf.Bootstrap, provider server.StreamProvider, serial, advHost string) *server.Server {
	return server.New(server.Config{
		Manufacturer:  "gowvp",
		Model:         "owl",
		Version:       cfg.BuildVersion,
		SerialNumber:  serial,
		ScopeName:     "owl",
		Username:      cfg.ONVIF.Username,
		Password:      cfg.ONVIF.Password,
		AdvertiseHost: advHost,
	}, provider)
}

// newSOAPHandler reads one coherent configuration per request. server.New only
// creates a value: it owns no socket, background goroutine, or replay cache.
func newSOAPHandler(cfg *conf.Bootstrap, provider server.StreamProvider, serial string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		snapshot := cfg.Snapshot()
		newSOAPServer(&snapshot, provider, serial, advertiseHost(&snapshot)).Handler().ServeHTTP(w, r)
	})
}
