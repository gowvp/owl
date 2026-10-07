package onvifserver

import (
	"context"
	"log/slog"
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
	serial := cfg.BuildVersion
	if serial == "" {
		serial = "gowvp-owl"
	}
	advHost := advertiseHost(cfg)
	srv := newSOAPServer(cfg, provider, serial, advHost)
	// 仅注册 SOAP 端点，避免与 /onvif/discover 等 REST 路由冲突。
	h := gin.WrapH(srv.Handler())
	g.Any("/onvif/device_service", h)
	g.Any("/onvif/media_service", h)
	g.GET("/onvif/profiles", func(c *gin.Context) {
		snap := provider.ProfileSnapshot()
		c.JSON(http.StatusOK, gin.H{"count": len(snap), "profiles": snap})
	})

	startDiscovery(cfg, serial, advHost)
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

// startDiscovery 启动 WS-Discovery，供 HA 等客户端在局域网自动发现本机 ONVIF 设备。
func startDiscovery(cfg *conf.Bootstrap, serial, advHost string) {
	disc, err := server.NewDiscovery(server.DiscoveryConfig{
		HTTPPort:   cfg.Server.HTTP.Port,
		Host:       advHost,
		Name:       "owl",
		EndpointID: "uuid:" + serial,
	})
	if err != nil {
		slog.Warn("onvif ws-discovery init failed", "err", err)
		return
	}
	if err := disc.Start(context.Background()); err != nil {
		slog.Warn("onvif ws-discovery start failed", "err", err)
	}
}
