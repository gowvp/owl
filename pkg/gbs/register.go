package gbs

import (
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"
	"unicode"

	"github.com/gowvp/owl/internal/conf"
	"github.com/gowvp/owl/internal/core/ipc"
	"github.com/gowvp/owl/internal/core/sms"
	wsnotify "github.com/gowvp/owl/internal/notify"
	"github.com/gowvp/owl/pkg/gbs/sip"
	"github.com/ixugo/goddd/pkg/conc"
	"github.com/ixugo/goddd/pkg/orm"
)

const ignorePassword = "#"

type GB28181API struct {
	cfg  *conf.Bootstrap
	core ipc.Adapter

	catalog *sip.Collector[Channels]

	// TODO: 待替换成 redis
	streams *conc.Map[string, *Streams]

	svr *Server

	sms *sms.NodeManager
}

func NewGB28181API(cfg *conf.Bootstrap, store ipc.Adapter, sms *sms.NodeManager) *GB28181API {
	g := GB28181API{
		cfg:  cfg,
		core: store,
		sms:  sms,
		catalog: sip.NewCollector(func(c1, c2 *Channels) bool {
			return c1.ChannelID == c2.ChannelID
		}),
		streams: &conc.Map[string, *Streams]{},
	}
	go g.catalog.Start(func(s string, channel []*Channels) {
		// 零值不做变更，没有通道又何必注册上来
		if len(channel) == 0 {
			return
		}

		// ipc, ok := g.svr.devices.Load(s)
		// if ok {
		// 	ipc.channels.Clear()
		// 	for _, ch := range c {

		// 	}
		// }

		d, ok := g.svr.memoryStorer.Load(s)
		if ok {
			// 使用设备注册时的 SIP 域名构造通道 URI，而非服务器 ID 域名
			domain := d.region
			if domain == "" {
				cfg := g.sipConfig()
				domain = cfg.GetDomain()
			}
			for _, ch := range channel {
				ch := Channel{
					ChannelID: ch.ChannelID,
					device:    d,
				}
				ch.init(domain)
				d.Channels.Store(ch.ChannelID, &ch)
			}
		}

		out := make([]*ipc.Channel, len(channel))
		for i, ch := range channel {
			// 使用 PTZType 字段判断是否有云台能力
			// PTZType > 0 表示有云台，0 表示无云台或未知
			ptz := ch.PTZType

			// 调试日志：打印解析到的云台能力值
			slog.Info("Catalog PTZ 解析",
				"device_id", s,
				"channel_id", ch.ChannelID,
				"name", ch.Name,
				"ptz_type", ch.PTZType,
				"camera_type", ch.CameraType,
				"result", ptz)

			out[i] = &ipc.Channel{
				DeviceID:  s,
				ChannelID: ch.ChannelID,
				Name:      ch.Name,
				IsOnline:  ch.Status == "OK" || ch.Status == "ON",
				PTZ:       ptz,
				Ext: ipc.DeviceExt{
					Manufacturer: ch.Manufacturer,
					Model:        ch.Model,
				},
				Type: ipc.TypeGB28181,
			}
		}
		if err := g.core.SaveChannels(out); err != nil {
			slog.Error("SaveChannels", "err", err)
		}
	})
	return &g
}

// sipConfig returns one committed configuration for an entire SIP operation.
func (g *GB28181API) sipConfig() conf.SIP {
	return g.cfg.Snapshot().Sip
}

// filterUnknowDevices 国标 ID 校验，正常是长度为 20 的纯数字字符串
func filterUnknowDevices(deviceID string) error {
	if len(deviceID) < 18 {
		return fmt.Errorf("device id too short")
	}
	if len(deviceID) > 20 {
		return fmt.Errorf("device id too long")
	}
	// 验证必须全是数字
	for _, ch := range deviceID {
		if !unicode.IsNumber(ch) {
			return fmt.Errorf("device id must be all numbers")
		}
	}
	return nil
}

func (g *GB28181API) handlerRegister(ctx *sip.Context) {
	cfg := g.sipConfig()
	// 为什么: 设备端 Request-URI 的 user 部分应为目标平台 ID。若不一致说明设备侧 SIP 服务器 ID 配置错误,
	// 直接放行会导致后续 MESSAGE/INVITE 因 Request-URI 不匹配被设备静默丢弃(海康等厂商严格校验),
	// 提前拒绝并给出明确日志便于用户自查。
	if recipient := ctx.Request.Recipient(); recipient != nil {
		var reqID string
		if u := recipient.User(); u != nil {
			reqID = u.String()
		}
		if reqID != cfg.ID {
			slog.Error("设备 SIP 服务器 ID 与平台不匹配，拒绝注册",
				"device_id", ctx.DeviceID,
				"device_target_id", reqID,
				"platform_id", cfg.ID,
				"source", ctx.Source.String(),
			)
			wsnotify.IPCWarn("注册失败: SIP 服务器 ID 不匹配", ctx.DeviceID, "")
			ctx.String(http.StatusForbidden, fmt.Sprintf("server id mismatch, expect %s got %s", cfg.ID, reqID))
			return
		}
	}

	dev, err := g.core.GetDeviceByDeviceID(ctx.DeviceID)
	if err != nil {
		ctx.Log.Error("GetDeviceByDeviceID", "err", err)
		ctx.String(http.StatusInternalServerError, "server db error")
		return
	}
	g.svr.memoryStorer.LoadOrStore(ctx.DeviceID, &Device{
		conn:   ctx.Request.GetConnection(),
		source: ctx.Source,
		to:     ctx.To,
		region: ctx.To.URI.Host(),
	})

	password := dev.Password
	if password == "" {
		password = cfg.Password
	}
	// 免鉴权
	if dev.Password == ignorePassword {
		password = ""
	}

	if password != "" {
		hdrs := ctx.Request.GetHeaders("Authorization")
		if len(hdrs) == 0 {
			resp := sip.NewResponseFromRequest("", ctx.Request, http.StatusUnauthorized, http.StatusText(http.StatusUnauthorized), nil)
			resp.AppendHeader(&sip.GenericHeader{HeaderName: "WWW-Authenticate", Contents: fmt.Sprintf(`Digest realm="%s",qop="auth",nonce="%s"`, cfg.GetDomain(), sip.RandString(32))})
			_ = ctx.Tx.Respond(resp)
			return
		}
		authenticateHeader := hdrs[0].(*sip.GenericHeader)
		auth := sip.AuthFromValue(authenticateHeader.Contents)
		auth.SetPassword(password)
		auth.SetUsername(dev.GetGB28181DeviceID())
		auth.SetMethod(ctx.Request.Method())
		auth.SetURI(auth.Get("uri"))
		if auth.CalcResponse() != auth.Get("response") {
			ctx.Log.Info("设备注册鉴权失败")
			wsnotify.IPCWarn("注册鉴权失败: 密码错误", ctx.DeviceID, dev.GetName())
			ctx.String(http.StatusUnauthorized, "wrong password")
			return
		}
	}

	respFn := func() {
		resp := sip.NewResponseFromRequest("", ctx.Request, http.StatusOK, "OK", nil)
		resp.AppendHeader(&sip.GenericHeader{
			HeaderName: "Date",
			Contents:   time.Now().Format("2006-01-02T15:04:05.000"),
		})
		_ = ctx.Tx.Respond(resp)
	}

	expire := ctx.GetHeader("Expires")
	if expire == "0" {
		ctx.Log.Info("设备注销")
		wsnotify.IPCWarn("设备已注销离线", ctx.DeviceID, dev.GetName())
		g.logout(ctx.DeviceID, func(b *ipc.Device) error {
			b.IsOnline = false
			b.Address = ctx.Source.String()
			return nil
		})
		respFn()
		return
	}

	g.login(ctx, func(b *ipc.Device) error {
		b.IsOnline = true
		b.RegisteredAt = orm.Now()
		b.KeepaliveAt = orm.Now()
		b.Expires, _ = strconv.Atoi(expire)
		b.Address = ctx.Source.String()
		b.Transport = ctx.Source.Network()
		b.Ext.GBVersion = ctx.XGBVer
		return nil
	})

	// conn := ctx.Request.GetConnection()
	// fmt.Printf(">>> %p\n", conn

	ctx.Log.Info("设备注册成功")
	wsnotify.IPCInfo("设备注册上线", ctx.DeviceID, dev.GetName())

	respFn()

	g.QueryDeviceInfo(ctx)
	_ = g.QueryCatalog(dev.GetGB28181DeviceID())
	_ = g.QueryConfigDownloadBasic(dev.GetGB28181DeviceID())
}

func (g GB28181API) login(ctx *sip.Context, fn func(d *ipc.Device) error) {
	slog.Info("status change 设备上线", "device_id", ctx.DeviceID)
	g.svr.memoryStorer.Change(ctx.DeviceID, fn, func(d *Device) {
		d.conn = ctx.Request.GetConnection()
		d.source = ctx.Source
		d.to = ctx.To
		d.region = ctx.To.URI.Host()
	})
}

func (g GB28181API) logout(deviceID string, changeFn func(*ipc.Device) error) error {
	slog.Info("status change 设备离线", "device_id", deviceID)

	// 设备离线前，遍历其所有通道，对有活跃流的通道发 BYE 释放 SIP 会话，
	if dev, ok := g.svr.memoryStorer.Load(deviceID); ok {
		dev.Channels.Range(func(channelID string, ch *Channel) bool {
			key := "play:" + deviceID + ":" + channelID
			if stream, loaded := g.streams.LoadAndDelete(key); loaded && stream.Resp != nil {
				req := sip.NewRequestFromResponse(sip.MethodBYE, stream.Resp)
				req.SetDestination(ch.Source())
				req.SetConnection(ch.Conn())
				if _, err := g.svr.Request(req); err != nil {
					slog.Warn("logout: 发送 BYE 失败", "device_id", deviceID, "channel_id", channelID, "err", err)
				} else {
					slog.Info("logout: 已发送 BYE", "device_id", deviceID, "channel_id", channelID)
				}
			}
			return true
		})
	}

	return g.svr.memoryStorer.Change(deviceID, changeFn, func(d *Device) {
		d.Expires = 0
		d.IsOnline = false
	})
}
