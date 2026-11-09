package gbs

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/gowvp/owl/internal/core/ipc"
	"github.com/gowvp/owl/internal/core/sms"
	wsnotify "github.com/gowvp/owl/internal/notify"
	"github.com/gowvp/owl/pkg/gbs/sip"
	"github.com/gowvp/owl/pkg/zlm"
	sdp "github.com/panjjo/gosdp"
)

type PlayInput struct {
	Channel    *ipc.Channel
	SMS        *sms.MediaServer
	StreamMode int8
}

type StopPlayInput struct {
	Channel *ipc.Channel
}

// stopPlay 不加锁的
func (g *GB28181API) stopPlay(ch *Channel, in *StopPlayInput) error {
	key := "play:" + in.Channel.DeviceID + ":" + in.Channel.ChannelID
	stream, ok := g.streams.LoadAndDelete(key)
	if !ok {
		// 流已被其他调用者移除（重复 stop、logout 并发等），属于正常情况
		slog.Debug("stopPlay: 流不存在，跳过", "device_id", in.Channel.DeviceID, "channel_id", in.Channel.ChannelID)
		return nil
	}

	if stream.Resp == nil {
		// INVITE 尚未收到 200 OK 响应就被 stop，说明 INVITE 超时或设备拒绝后流未正常建立
		slog.Debug("stopPlay: Resp 为空，INVITE 可能未完成，跳过 BYE", "device_id", in.Channel.DeviceID, "channel_id", in.Channel.ChannelID)
		return nil
	}

	req := sip.NewRequestFromResponse(sip.MethodBYE, stream.Resp)
	req.SetDestination(ch.Source())
	req.SetConnection(ch.Conn())

	// 忽略响应，此处必须尽快返回
	_, err := g.svr.Request(req)
	return err
}

// StopPlay 加锁的停止播放
func (g *GB28181API) StopPlay(ctx context.Context, in *StopPlayInput) error {
	ch, ok := g.svr.memoryStorer.GetChannel(in.Channel.DeviceID, in.Channel.ChannelID)
	if !ok {
		return ErrDeviceNotExist
	}

	ch.device.playMutex.Lock()
	defer ch.device.playMutex.Unlock()

	defer func() {
		g.svr.gb.core.UpdatePlaying(ctx, in.Channel.DeviceID, in.Channel.ChannelID, false)
	}()
	return g.stopPlay(ch, in)
}

func (g *GB28181API) Play(in *PlayInput) error {
	log := slog.With("deviceID", in.Channel.DeviceID, "channelID", in.Channel.ChannelID)
	log.Info("开始播放流程")
	ch, ok := g.svr.memoryStorer.GetChannel(in.Channel.DeviceID, in.Channel.ChannelID)
	if !ok {
		log.Error("通道不存在")
		wsnotify.IPCWarn("通道不存在，请等待设备注册或心跳后再试", in.Channel.DeviceID, in.Channel.ChannelID)
		return ErrChannelNotExist
	}

	ch.device.playMutex.Lock()
	defer ch.device.playMutex.Unlock()

	if !ch.device.IsOnline {
		return ErrDeviceOffline
	}

	// 播放中
	key := "play:" + in.Channel.DeviceID + ":" + in.Channel.ChannelID
	if _, ok := g.streams.LoadOrStore(key, &Streams{}); ok {
		log.Debug("PLAY 已存在流，先停止再播放")
		if err := g.stopPlay(ch, &StopPlayInput{
			Channel: in.Channel,
		}); err != nil {
			slog.Error("stop play failed", "err", err)
		}
	}
	stream, _ := g.streams.LoadOrStore(key, &Streams{})

	// 提前分配 SSRC，用于 ZLM 端口绑定防串流
	ssrc := g.getSSRC(0)
	ssrcVal, _ := strconv.ParseUint(ssrc, 10, 64)
	stream.ssrc = ssrc

	log.Debug("1. 开启RTP服务器等待接收视频流", "ssrc", ssrc)
	// 开启RTP服务器等待接收视频流，传入 ssrc 让 ZLM 校验，防止其他设备往该端口推流
	resp, err := g.sms.OpenRTPServer(in.SMS, zlm.OpenRTPServerRequest{
		TCPMode:  in.StreamMode,
		StreamID: in.Channel.ID,
		SSRC:     ssrcVal,
	})
	if err != nil {
		log.Debug("1.1. 开启RTP服务器失败", "err", err)
		// 如果是因为流已存在，先关闭再重新打开
		if strings.Contains(err.Error(), "already exists") || strings.Contains(err.Error(), "-300") {
			log.Info("RTP服务器已存在，尝试关闭后重新打开", "stream_id", in.Channel.ID)
			// 关闭旧的 RTP 服务器
			_, closeErr := g.sms.CloseRTPServer(in.SMS, zlm.CloseRTPServerRequest{
				StreamID: in.Channel.ID,
			})
			if closeErr != nil {
				log.Warn("关闭旧的RTP服务器失败", "err", closeErr)
			} else {
				log.Info("已关闭旧的RTP服务器，等待清理")
				// 等待一小段时间让 ZLM 清理资源
				time.Sleep(500 * time.Millisecond)
			}
			// 重新打开 RTP 服务器
			resp, err = g.sms.OpenRTPServer(in.SMS, zlm.OpenRTPServerRequest{
				TCPMode:  in.StreamMode,
				StreamID: in.Channel.ID,
				SSRC:     ssrcVal,
			})
			if err != nil {
				log.Debug("1.2. 重新开启RTP服务器失败", "err", err)
				wsnotify.IPCWarn("RTP服务器开启失败: "+err.Error(), in.Channel.DeviceID, in.Channel.ChannelID)
				return err
			}
			log.Info("成功重新打开RTP服务器", "port", resp.Port)
		} else {
			wsnotify.IPCWarn("RTP服务器开启失败: "+err.Error(), in.Channel.DeviceID, in.Channel.ChannelID)
			return err
		}
	}

	log.Debug("2. 发送SDP请求", "port", resp.Port)
	if err := g.sipPlayPush2(ch, in, resp.Port, stream); err != nil {
		log.Debug("2.1. 发送SDP请求失败", "err", err)
		wsnotify.IPCWarn("INVITE失败: "+err.Error(), in.Channel.DeviceID, in.Channel.ChannelID)
		g.svr.gb.core.UpdatePlaying(context.TODO(), in.Channel.DeviceID, in.Channel.ChannelID, false)
		return err
	}

	g.svr.gb.core.UpdatePlaying(context.TODO(), in.Channel.DeviceID, in.Channel.ChannelID, true)

	return nil
}

// GetIP 判断输入字符串并返回对应的IP地址
// 输入可能是IPv4地址、域名、空值或其他非法值
func GetIP(input string) (string, error) {
	slog.Info("开始域名解析", "输入", input)
	// 处理空字符串情况
	if input == "" {
		slog.Error("输入为空字符串")
		return input, fmt.Errorf("输入为空")
	}

	// 去除前后空格
	input = strings.TrimSpace(input)

	// 首先尝试直接解析为IPv4地址
	if ip := net.ParseIP(input); ip != nil {
		// 检查是否是IPv4地址
		if ip.To4() != nil {
			return ip.String(), nil
		}
		// 如果是IPv6地址，记录错误
		slog.Error("不支持的IPv6地址", "输入", input)
		return input, fmt.Errorf("IPv6地址暂不支持")
	}

	// 尝试解析为域名
	ips, err := net.LookupIP(input)
	if err != nil {
		slog.Error("域名解析失败", "域名", input, "错误", err)
		return input, fmt.Errorf("无法解析域名: %w", err)
	}

	// 从解析结果中优先选择IPv4地址
	for _, ip := range ips {
		if ip.To4() != nil {
			return ip.To4().String(), nil
		}
	}

	// 如果没有IPv4地址，选择第一个IPv6地址（如果有）
	if len(ips) > 0 {
		slog.Warn("域名只解析到IPv6地址", "域名", input)
		return ips[0].String(), nil
	}

	slog.Error("域名没有解析到任何IP地址", "域名", input)
	return input, fmt.Errorf("域名没有解析到IP地址")
}

// buildPlaySDP 构造实时播放的 SDP 报文体。
// streamMode: 0=UDP, 1=TCP被动, 2=TCP主动
// 为什么 deviceID 和 channelID 分开传: Origin Username 应填设备 ID（GB28181 规范），
// 而 Subject 等其他字段需要通道 ID，两者不可混用。
func buildPlaySDP(deviceID, channelID, ip string, port int, streamMode int8, ssrc string) []byte {
	protocol := "TCP/RTP/AVP"
	if streamMode == 0 {
		protocol = "RTP/AVP"
	}

	video := sdp.Media{
		Description: sdp.MediaDescription{
			Type:     "video",
			Port:     port,
			Formats:  []string{"96", "97", "98", "99"},
			Protocol: protocol,
		},
	}
	video.AddAttribute("recvonly")

	switch streamMode {
	case 1:
		video.AddAttribute("setup", "passive")
		video.AddAttribute("connection", "new")
	case 2:
		video.AddAttribute("setup", "active")
		video.AddAttribute("connection", "new")
	}
	video.AddAttribute("rtpmap", "96", "PS/90000")
	video.AddAttribute("rtpmap", "97", "MPEG4/90000")
	video.AddAttribute("rtpmap", "98", "H264/90000")
	video.AddAttribute("rtpmap", "99", "H265/90000")

	msg := &sdp.Message{
		Version: 0,
		Origin: sdp.Origin{
			Username:    deviceID,
			NetworkType: "IN",
			AddressType: "IP4",
			Address:     ip,
		},
		Name: "Play",
		URI:  fmt.Sprintf("%s:0", channelID),
		Connection: sdp.ConnectionData{
			NetworkType: "IN",
			AddressType: "IP4",
			IP:          net.ParseIP(ip),
		},
		Timing: []sdp.Timing{{}},
		Medias: []sdp.Media{video},
		SSRC:   ssrc,
	}

	body := msg.Append(nil).AppendTo(nil)
	body = append(body, "f=v/////a/1/8/1\r\n"...)
	return body
}

// sipPlayPush2 向摄像机发送 INVITE 请求拉取实时流。
//
// 为何不复用 wrapRequest 而独立构造：
//   - INVITE 的 To/From/Contact/Subject/Via 组合有严格的 GB28181 约束，
//     TP-Link 等厂商对报文字段校验苛刻，独立构造可精确控制每个头部，
//     避免通用函数的默认值干扰设备解析。
//
// GB28181 关键约束：
//  1. SDP `y=`（SSRC）必须与 SIP Subject 头中的发送方 SSRC 一致，
//     否则设备无法将信令与 RTP 流绑定，导致不推流。
//  2. Subject 格式：`<channel_id>:<sender_ssrc>,<sip_server_id>:0`。
func (g *GB28181API) sipPlayPush2(ch *Channel, in *PlayInput, port int, stream *Streams) error {
	cfg := g.sipConfig()
	address := g.svr.addressFor(cfg)
	ipstr := in.SMS.GetSDPIP()
	ip4str, err := GetIP(ipstr)
	if err != nil {
		slog.Error("域名解析失败", "域名", ipstr, "错误", err)
		return err
	}
	slog.Info("域名解析成功", "原始域名", ipstr, "解析IP", ip4str)

	// SSRC 已在 Play 中提前分配并绑定到 ZLM 端口，此处直接使用
	ssrc := stream.ssrc

	body := buildPlaySDP(in.Channel.DeviceID, ch.ChannelID, ip4str, port, in.StreamMode, ssrc)
	slog.Debug("INVITE SDP", "ssrc", ssrc, "channelID", ch.ChannelID, "body", string(body))

	dev := ch.device
	conn := dev.conn
	source := dev.source

	transport := "UDP"
	if source != nil && source.Network() == "tcp" {
		transport = "TCP"
	}

	// Via Host 优先级: 配置 sip.host → conn.LocalAddr → fromAddress LAN IP
	viaHost := resolveHost(cfg.Host)
	if viaHost == "" && conn != nil {
		if host, _, err := net.SplitHostPort(conn.LocalAddr().String()); err == nil {
			viaHost = host
		}
	}
	if viaHost == "" {
		viaHost = address.URI.FHost
	}

	deviceHost := cfg.GetDomain()
	if source != nil {
		deviceHost = source.String()
	}
	toURI, _ := sip.ParseURI(fmt.Sprintf("sip:%s@%s", ch.ChannelID, deviceHost))
	toAddr := &sip.Address{URI: toURI, Params: sip.NewParams()}

	// Contact: 纯地址不带 DisplayName
	contact := &sip.Address{URI: address.URI, Params: sip.NewParams()}

	fromURI, _ := sip.ParseURI(fmt.Sprintf("sip:%s@%s", cfg.ID, cfg.GetDomain()))
	hb := sip.NewHeaderBuilder().
		SetMethod(sip.MethodInvite).
		SetTo(toAddr).
		SetContact(contact).
		AddVia(&sip.ViaHop{
			ProtocolName:    "SIP",
			ProtocolVersion: "2.0",
			Transport:       transport,
			Host:            viaHost,
			Params:          sip.NewParams().Add("branch", sip.String{Str: sip.GenerateBranch()}),
		}).
		SetFrom(&sip.Address{URI: fromURI, Params: sip.NewParams().Add("tag", sip.String{Str: sip.RandString(5)})}).
		SetContentType(&sip.ContentTypeSDP).
		SetSubject(fmt.Sprintf("%s:%s,%s:%s", ch.ChannelID, ssrc, cfg.ID, "0"))

	req := sip.NewRequest("", sip.MethodInvite, toAddr.URI, sip.DefaultSipVersion, hb.Build(), body)
	req.SetDestination(source)
	req.SetConnection(conn)

	slog.Debug("INVITE 完整报文", "request", req.String())

	tx, err := g.svr.Request(req)
	if err != nil {
		slog.Error("INVITE 发送失败", "channelID", ch.ChannelID, "ssrc", ssrc, "err", err)
		return err
	}
	resp, err := sipResponse(tx)
	if err != nil {
		slog.Error("INVITE 等待响应失败", "channelID", ch.ChannelID, "ssrc", ssrc, "err", err)
		return err
	}

	if contact, _ := resp.Contact(); contact == nil {
		resp.AppendHeader(&sip.ContactHeader{
			DisplayName: address.DisplayName,
			Address:     &sip.URI{FUser: sip.String{Str: cfg.ID}, FHost: cfg.GetDomain()},
			Params:      sip.NewParams(),
		})
	}

	stream.Resp = resp

	ackReq := sip.NewRequestFromResponse(sip.MethodACK, resp)
	return tx.Request(ackReq)
}

// sip 请求播放
// func SipPlay(data *Streams) (*Streams, error) {
// 	channel := Channels{ChannelID: data.ChannelID}
// 	// if err := db.Get(db.DBClient, &channel); err != nil {
// 	// 	if db.RecordNotFound(err) {
// 	// 		return nil, errors.New("通道不存在")
// 	// 	}
// 	// 	return nil, err
// 	// }

// 	data.DeviceID = channel.DeviceID
// 	data.StreamType = channel.StreamType
// 	// 使用通道的播放模式进行处理
// 	switch channel.StreamType {
// 	case m.StreamTypePull:
// 		// 拉流

// 	default:
// 		// 推流模式要求设备在线且活跃
// 		if time.Now().Unix()-channel.Active > 30*60 || channel.Status != m.DeviceStatusON {
// 			return nil, errors.New("通道已离线")
// 		}
// 		user, ok := _activeDevices.Get(channel.DeviceID)
// 		if !ok {
// 			return nil, errors.New("设备已离线")
// 		}
// 		// GB28181推流
// 		if data.StreamID == "" {
// 			ssrcLock.Lock()
// 			// data.ssrc =g. getSSRC(data.T)
// 			data.StreamID = ssrc2stream(data.ssrc)

// 			// 成功后保存
// 			// db.Create(db.DBClient, data)
// 			ssrcLock.Unlock()
// 		}

// 		var err error
// 		data, err = sipPlayPush(data, channel, user)
// 		if err != nil {
// 			return nil, fmt.Errorf("获取视频失败:%v", err)
// 		}
// 	}

// 	data.HTTP = fmt.Sprintf("%s/rtp/%s/hls.m3u8", config.Media.HTTP, data.StreamID)
// 	data.RTMP = fmt.Sprintf("%s/rtp/%s", config.Media.RTMP, data.StreamID)
// 	data.RTSP = fmt.Sprintf("%s/rtp/%s", config.Media.RTSP, data.StreamID)
// 	data.WSFLV = fmt.Sprintf("%s/rtp/%s.live.flv", config.Media.WS, data.StreamID)

// 	data.Ext = time.Now().Unix() + 2*60 // 2分钟等待时间
// 	StreamList.Response.Store(data.StreamID, data)
// 	if data.T == 0 {
// 		StreamList.Succ.Store(data.ChannelID, data)
// 	}
// 	// db.Save(db.DBClient, data)
// 	return data, nil
// }

// func sipPlayPush(data *Streams, channel Channels, device Devices) (*Streams, error) {
// 	var (
// 		s sdp.Session
// 		b []byte
// 	)
// 	name := "Play"
// 	protocal := "TCP/RTP/AVP"
// 	if data.T == 1 {
// 		name = "Playback"
// 		protocal = "RTP/RTCP"
// 	}

// 	video := sdp.Media{
// 		Description: sdp.MediaDescription{
// 			Type:     "video",
// 			Port:     _sysinfo.MediaServerRtpPort,
// 			Formats:  []string{"96", "98", "97"},
// 			Protocol: protocal,
// 		},
// 	}
// 	video.AddAttribute("recvonly")
// 	if data.T == 0 {
// 		video.AddAttribute("setup", "passive")
// 		video.AddAttribute("connection", "new")
// 	}
// 	video.AddAttribute("rtpmap", "96", "PS/90000")
// 	video.AddAttribute("rtpmap", "98", "H264/90000")
// 	video.AddAttribute("rtpmap", "97", "MPEG4/90000")

// 	// defining message
// 	msg := &sdp.Message{
// 		Origin: sdp.Origin{
// 			Username: _serverDevices.DeviceID, // 媒体服务器id
// 			Address:  _sysinfo.MediaServerRtpIP.String(),
// 		},
// 		Name: name,
// 		Connection: sdp.ConnectionData{
// 			IP:  _sysinfo.MediaServerRtpIP,
// 			TTL: 0,
// 		},
// 		Timing: []sdp.Timing{
// 			{
// 				Start: data.S,
// 				End:   data.E,
// 			},
// 		},
// 		Medias: []sdp.Media{video},
// 		SSRC:   data.ssrc,
// 	}
// 	if data.T == 1 {
// 		msg.URI = fmt.Sprintf("%s:0", channel.ChannelID)
// 	}

// 	// appending message to session
// 	s = msg.Append(s)
// 	// appending session to byte buffer
// 	b = s.AppendTo(b)
// 	uri, _ := sip.ParseURI(channel.URIStr)
// 	channel.addr = &sip.Address{URI: uri}
// 	_serverDevices.addr.Params.Add("tag", sip.String{Str: sip.RandString(20)})
// 	hb := sip.NewHeaderBuilder().SetTo(channel.addr).SetFrom(_serverDevices.addr).AddVia(&sip.ViaHop{
// 		Params: sip.NewParams().Add("branch", sip.String{Str: sip.GenerateBranch()}),
// 	}).SetContentType(&sip.ContentTypeSDP).SetMethod(sip.MethodInvite).SetContact(_serverDevices.addr)
// 	req := sip.NewRequest("", sip.MethodInvite, channel.addr.URI, sip.DefaultSipVersion, hb.Build(), b)
// 	req.SetDestination(device.source)
// 	req.AppendHeader(&sip.GenericHeader{HeaderName: "Subject", Contents: fmt.Sprintf("%s:%s,%s:%s", channel.ChannelID, data.StreamID, _serverDevices.DeviceID, data.StreamID)})
// 	req.SetRecipient(channel.addr.URI)
// 	tx, err := svr.Request(req)
// 	if err != nil {
// 		// logrus.Warningln("sipPlayPush fail.id:", device.DeviceID, channel.ChannelID, "err:", err)
// 		return data, err
// 	}
// 	// response
// 	response, err := sipResponse(tx)
// 	if err != nil {
// 		// logrus.Warningln("sipPlayPush response fail.id:", device.DeviceID, channel.ChannelID, "err:", err)
// 		return data, err
// 	}
// 	data.Resp = response
// 	// ACK
// 	tx.Request(sip.NewRequestFromResponse(sip.MethodACK, response))

// 	callid, _ := response.CallID()
// 	data.CallID = string(*callid)

// 	cseq, _ := response.CSeq()
// 	if cseq != nil {
// 		data.CseqNo = cseq.SeqNo
// 	}

// 	// from, _ := response.From()
// 	// to, _ := response.To()
// 	// for k, v := range to.Params.Items() {
// 	// 	data.Ttag[k] = v.String()
// 	// }
// 	// for k, v := range from.Params.Items() {
// 	// 	data.Ftag[k] = v.String()
// 	// }
// 	data.Status = 0

// 	return data, err
// }

// // sip 停止播放
// func SipStopPlay(ssrc string) {
// 	zlmCloseStream(ssrc)
// 	data, ok := StreamList.Response.Load(ssrc)
// 	if !ok {
// 		return
// 	}
// 	play := data.(*Streams)
// 	if play.StreamType == m.StreamTypePush {
// 		// 推流，需要发送关闭请求
// 		resp := play.Resp
// 		u, ok := _activeDevices.Load(play.DeviceID)
// 		if !ok {
// 			return
// 		}
// 		user := u.(Devices)
// 		req := sip.NewRequestFromResponse(sip.MethodBYE, resp)
// 		req.SetDestination(user.source)
// 		tx, err := svr.Request(req)
// 		if err != nil {
// 			// logrus.Warningln("sipStopPlay bye fail.id:", play.DeviceID, play.ChannelID, "err:", err)
// 		}
// 		_, err = sipResponse(tx)
// 		if err != nil {
// 			// logrus.Warnln("sipStopPlay response fail", err)
// 			play.Msg = err.Error()
// 		} else {
// 			play.Status = 1
// 			play.Stop = true
// 		}
// 		// db.Save(db.DBClient, play)
// 	}
// 	StreamList.Response.Delete(ssrc)
// 	if play.T == 0 {
// 		StreamList.Succ.Delete(play.ChannelID)
// 	}
// }
