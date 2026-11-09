package gbs

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/gowvp/owl/internal/conf"
	"github.com/gowvp/owl/internal/core/ipc"
	"github.com/gowvp/owl/internal/core/sms"
	"github.com/gowvp/owl/pkg/gbs/sip"
	"github.com/ixugo/goddd/pkg/conc"
	"github.com/ixugo/netpulse/ip"
)

type MemoryStorer interface {
	LoadOrStore(deviceID string, value *Device)
	LoadDeviceToMemory(conn sip.Connection)               // 加载设备到内存
	RangeDevices(fn func(key string, value *Device) bool) // 遍历设备

	Change(deviceID string, changeFn func(*ipc.Device) error, changeFn2 func(*Device)) error // 登出设备

	Load(deviceID string) (*Device, bool)
	Store(deviceID string, value *Device)
	DeleteDevice(deviceID string)
	GetChannel(deviceID, channelID string) (*Channel, bool)
}

type Server struct {
	*sip.Server
	gb           *GB28181API
	mediaService sms.Core

	memoryStorer  MemoryStorer
	addressMu     sync.Mutex
	addressConfig conf.SIP
	fromAddress   *sip.Address
}

// resolveHost 将 host 统一为 IP：域名做 DNS 解析，IP 直接返回
func resolveHost(host string) string {
	if host == "" {
		return ""
	}
	if net.ParseIP(host) != nil {
		return host
	}
	addrs, err := net.LookupHost(host)
	if err != nil || len(addrs) == 0 {
		slog.Warn("resolveHost failed, fallback to raw host", "host", host, "err", err)
		return host
	}
	slog.Debug("resolveHost", "host", host, "resolved", addrs[0])
	return addrs[0]
}

func NewServer(cfg *conf.Bootstrap, store ipc.Adapter, sc sms.Core) (*Server, func()) {
	api := NewGB28181API(cfg, store, sc.NodeManager)

	from := sipFrom(api.sipConfig())
	svr = sip.NewServer(from)

	svr.Use(func(c *sip.Context) {
		if filterUnknowDevices(c.DeviceID) != nil {
			c.Abort()
			return
		}
		c.Next()
	})
	svr.Register(api.handlerRegister)
	svr.Bye(api.handleBYE)
	msg := svr.Message()
	msg.Handle("Keepalive", api.sipMessageKeepalive)
	msg.Handle("Catalog", api.sipMessageCatalog)
	msg.Handle("DeviceInfo", api.sipMessageDeviceInfo)
	msg.Handle("ConfigDownload", api.sipMessageConfigDownload)
	msg.Handle("DeviceConfig", api.handleDeviceConfig)
	// msg.Handle("RecordInfo", api.handlerMessage)

	c := Server{
		Server:       svr,
		mediaService: sc,
		gb:           api,
		memoryStorer: store.Store().(MemoryStorer),
	}
	api.svr = &c
	// A committed configuration refreshes identity without restarting listeners.
	svr.SetFromProvider(func() *sip.Address { return c.addressFor(api.sipConfig()) })

	go svr.ListenUDPServer(fmt.Sprintf(":%d", cfg.Sip.Port))
	go svr.ListenTCPServer(fmt.Sprintf(":%d", cfg.Sip.Port))
	go c.startTickerCheck()
	// 等待 UDP 连接
	for {
		time.Sleep(50 * time.Millisecond)
		if svr.UDPConn() != nil {
			c.memoryStorer.LoadDeviceToMemory(svr.UDPConn())
			break
		}
	}
	return &c, c.Close
}

// addressFor caches host resolution until identity changes. Every caller gets
// its own clone so outstanding transactions cannot be mutated by a later update.
func (s *Server) addressFor(cfg conf.SIP) *sip.Address {
	cfg.Password = "" // Authentication changes do not require resolving the host.
	s.addressMu.Lock()
	defer s.addressMu.Unlock()
	if s.fromAddress == nil || s.addressConfig != cfg {
		s.fromAddress = sipFrom(cfg)
		s.addressConfig = cfg
	}
	return s.fromAddress.Clone()
}

// sipFrom builds a fresh immutable address for one operation. The SIP port is
// deployment-owned; initialization changes advertised identity, not listeners.
func sipFrom(cfg conf.SIP) *sip.Address {
	sipHost := resolveHost(cfg.Host)
	if sipHost == "" {
		sipHost = ip.InternalIP()
	}
	uri, _ := sip.ParseSipURI(fmt.Sprintf("sip:%s@%s:%d", cfg.ID, sipHost, cfg.Port))
	return &sip.Address{
		DisplayName: sip.String{Str: "gowvp/owl"},
		URI:         &uri,
		Params:      sip.NewParams(),
	}
}

// startTickerCheck 定时检查离线，通过心跳超时判断设备是否离线
func (s *Server) startTickerCheck() {
	conc.Timer(context.Background(), 60*time.Second, time.Second, func() {
		now := time.Now()
		s.memoryStorer.RangeDevices(func(key string, dev *Device) bool {
			if !dev.IsOnline {
				return true
			}
			if len(key) < 18 {
				return true
			}

			// 计算超时时间：心跳间隔 * 超时次数
			// 默认心跳间隔 60s，超时次数 3 次，即 3 分钟无心跳判定离线
			interval := dev.keepaliveInterval
			if interval == 0 {
				interval = 60
			}
			timeoutCount := dev.keepaliveTimeout
			if timeoutCount == 0 {
				timeoutCount = 3
			}
			timeout := time.Duration(interval) * time.Duration(timeoutCount) * time.Second

			// 跳过未收到过心跳的设备（LastKeepaliveAt 为零值），这类设备依赖注册超时处理
			if dev.LastKeepaliveAt.IsZero() {
				// 如果注册时间也超过了超时时间，则判定离线
				if !dev.LastRegisterAt.IsZero() && now.Sub(dev.LastRegisterAt) >= timeout {
					if err := s.gb.logout(key, func(d *ipc.Device) error {
						d.IsOnline = false
						return nil
					}); err != nil {
						slog.Error("logout device failed", "device_id", key, "err", err)
					}
				}
				return true
			}

			// 心跳超时或连接丢失，判定设备离线
			if sub := now.Sub(dev.LastKeepaliveAt); sub >= timeout || dev.conn == nil {
				slog.Info(
					"device offline detected",
					"device_id", key,
					"last_keepalive", dev.LastKeepaliveAt,
					"timeout", timeout,
					"elapsed", sub,
					"conn_nil", dev.conn == nil,
				)
				if err := s.gb.logout(key, func(d *ipc.Device) error {
					d.IsOnline = false
					return nil
				}); err != nil {
					slog.Error("logout device failed", "device_id", key, "err", err)
				}
			}
			return true
		})
	})
}

func sipResponse(tx *sip.Transaction) (*sip.Response, error) {
	response := tx.GetResponse()
	if response == nil {
		return nil, sip.NewError(nil, "response timeout", "tx key:", tx.Key())
	}
	if response.StatusCode() != http.StatusOK {
		return response, sip.NewError(nil, "device: ", response.StatusCode(), " ", response.Reason())
	}
	return response, nil
}

// QueryCatalog 查询 catalog
func (s *Server) QueryCatalog(deviceID string) error {
	return s.gb.QueryCatalog(deviceID)
}

func (s *Server) Play(in *PlayInput) error {
	return s.gb.Play(in)
}

func (s *Server) StopPlay(ctx context.Context, in *StopPlayInput) error {
	return s.gb.StopPlay(ctx, in)
}

// QuerySnapshot 厂商实现抓图的少，sip 层已实现，先搁置
func (s *Server) QuerySnapshot(deviceID, channelID string) error {
	return s.gb.QuerySnapshot(deviceID, channelID)
}

// PTZControl 云台控制
func (s *Server) PTZControl(deviceID, channelID, ptzCmd string) error {
	return s.SendPTZCommand(deviceID, channelID, ptzCmd)
}
