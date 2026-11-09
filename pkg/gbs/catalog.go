package gbs

import (
	"encoding/xml"
	"log/slog"
	"net"

	"github.com/gowvp/owl/pkg/gbs/sip"
)

// MessageDeviceListResponse 设备明细列表返回结构
type MessageDeviceListResponse struct {
	XMLName  xml.Name   `xml:"Response"`
	CmdType  string     `xml:"CmdType"`
	SN       int        `xml:"SN"`
	DeviceID string     `xml:"DeviceID"`
	SumNum   int        `xml:"SumNum"`
	Item     []Channels `xml:"DeviceList>Item"`
}

// sipMessageCatalog 设备目录信息查询应答
// GB/T28181 90 页 A.2.6.4
func (g GB28181API) sipMessageCatalog(ctx *sip.Context) {
	// 调试：打印原始 XML 内容，确认球机返回的云台字段名
	slog.Info("Catalog 原始 XML", "body", string(ctx.Request.Body()))

	var msg MessageDeviceListResponse
	if err := sip.XMLDecode(ctx.Request.Body(), &msg); err != nil {
		slog.Error("Message Unmarshal xml", "err", err)
		ctx.String(400, "xml err")
		return
	}
	if msg.SumNum < 0 {
		ctx.String(200, "OK")
		return
	}

	for _, d := range msg.Item {
		d.DeviceID = msg.DeviceID
		g.catalog.Write(&sip.CollectorMsg[Channels]{
			Key:   d.DeviceID,
			Data:  &d,
			Total: msg.SumNum,
		})

		// channel := Channels{ChannelID: d.ChannelID, DeviceID: message.DeviceID}
		// if err := db.Get(db.DBClient, &channel); err == nil {
		// 	channel.Active = time.Now().Unix()
		// 	channel.URIStr = fmt.Sprintf("sip:%s@%s", d.ChannelID, _sysinfo.Region)
		// 	channel.Status = transDeviceStatus(d.Status)
		// 	channel.Name = d.Name
		// 	channel.Manufacturer = d.Manufacturer
		// 	channel.Model = d.Model
		// 	channel.Owner = d.Owner
		// 	channel.CivilCode = d.CivilCode
		// 	// Address ip地址
		// 	channel.Address = d.Address
		// 	channel.Parental = d.Parental
		// 	channel.SafetyWay = d.SafetyWay
		// 	channel.RegisterWay = d.RegisterWay
		// 	channel.Secrecy = d.Secrecy
		// 	db.Save(db.DBClient, &channel)
		// 	go notify(notifyChannelsActive(channel))
		// } else {
		// 	// logrus.Infoln("deviceid not found,deviceid:", d.DeviceID, "pdid:", message.DeviceID, "err", err)
		// }
	}

	ctx.String(200, "OK")
}

// QueryCatalog 设备目录查询或订阅请求
// GB/T28181 81 页 A.2.4.3
func (g *GB28181API) QueryCatalog(deviceID string) error {
	slog.Debug("QueryCatalog", "deviceID", deviceID)
	ipc, ok := g.svr.memoryStorer.Load(deviceID)
	if !ok || !ipc.IsOnline {
		return ErrDeviceOffline
	}

	_, err := g.svr.wrapRequest(ipc, sip.MethodMessage, &sip.ContentTypeXML, sip.GetCatalogXML(deviceID))
	if err != nil {
		return err
	}

	g.catalog.Run(deviceID)
	g.catalog.Wait(deviceID)
	return nil
}

type Targeter interface {
	To() *sip.Address
	Conn() sip.Connection
	Source() net.Addr
}

type RequestOption func(*sip.Request)

// wrapRequest 构造并发送一个通用 SIP 请求。
func (s *Server) wrapRequest(t Targeter, method string, contentType *sip.ContentType, body []byte, opts ...RequestOption) (*sip.Transaction, error) {
	to := t.To()
	conn := t.Conn()
	source := t.Source()

	cfg := s.gb.sipConfig()
	address := s.addressFor(cfg)
	from := address.Clone()
	from.Params = sip.NewParams().Add("tag", sip.String{Str: sip.RandString(10)})

	contact := address.Clone()
	contact.Params = sip.NewParams()

	transport := "UDP"
	if source != nil && source.Network() == "tcp" {
		transport = "TCP"
	}

	// Via Host 优先级: 配置 sip.host → conn.LocalAddr（设备连接本端地址） → fromAddress LAN IP
	viaHost := resolveHost(cfg.Host)
	if viaHost == "" && conn != nil {
		if host, _, err := net.SplitHostPort(conn.LocalAddr().String()); err == nil {
			viaHost = host
		}
	}
	if viaHost == "" {
		viaHost = address.URI.FHost
	}

	hb := sip.NewHeaderBuilder().
		SetTo(to).
		SetFrom(from).
		SetContentType(contentType).
		SetMethod(method).
		SetContact(contact).
		AddVia(&sip.ViaHop{
			ProtocolName:    "SIP",
			ProtocolVersion: "2.0",
			Transport:       transport,
			Host:            viaHost,
			Params:          sip.NewParams().Add("branch", sip.String{Str: sip.GenerateBranch()}),
		})

	req := sip.NewRequest("", method, to.URI, sip.DefaultSipVersion, hb.Build(), body)
	req.SetConnection(conn)
	req.SetSource(source)
	req.SetDestination(source)

	for _, opt := range opts {
		opt(req)
	}

	return s.Request(req)
}
