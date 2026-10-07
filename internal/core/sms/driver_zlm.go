package sms

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gowvp/owl/internal/notify"
	"github.com/gowvp/owl/pkg/zlm"
)

// splitFirstPort 将 "20000-20100" 拆为首端口与剩余段。
//
// 为什么: ZLM WebRTC 默认占 UDP/TCP 8000, 线上部署每加一台机器就要单独放行安全组一个端口;
// 将 rtc.port 合并到 GB28181 RTP 端口段的首位, 运维只需开放一段区间, 其余端口仍给
// rtp_proxy 做 GB28181 随机收流; ZLM 自己从 port_range 里挑, 无需 go 层管端口池。
//
// 返回: firstPort 字符串, 剩余 "start+1-end", 是否解析成功。解析失败由调用方回退。
func splitFirstPort(rng string) (string, string, bool) {
	i := strings.Index(rng, "-")
	if i <= 0 || i == len(rng)-1 {
		return "", "", false
	}
	startStr := strings.TrimSpace(rng[:i])
	endStr := strings.TrimSpace(rng[i+1:])
	start, err1 := strconv.Atoi(startStr)
	end, err2 := strconv.Atoi(endStr)
	if err1 != nil || err2 != nil || start <= 0 || start >= end {
		return "", "", false
	}
	return startStr, fmt.Sprintf("%d-%d", start+1, end), true
}

var _ Driver = (*ZLMDriver)(nil)

type ZLMDriver struct {
	engine zlm.Engine
}

// GetStreamLiveAddr implements Driver.
func (d *ZLMDriver) GetStreamLiveAddr(ctx context.Context, ms *MediaServer, httpPrefix, host, app, stream, token string) StreamLiveAddr {
	var out StreamLiveAddr
	out.Label = "ZLM"
	wsPrefix := strings.Replace(strings.Replace(httpPrefix, "https", "wss", 1), "http", "ws", 1)
	out.WSFLV = fmt.Sprintf("%s/proxy/sms/%s/%s.live.flv?token=%s", wsPrefix, app, stream, token)
	out.FLV = fmt.Sprintf("%s/proxy/sms/%s/%s.live.flv?token=%s", httpPrefix, app, stream, token)
	out.HLS = fmt.Sprintf("%s/proxy/sms/%s/%s/hls.fmp4.m3u8?token=%s", httpPrefix, app, stream, token)
	rtcPrefix := strings.Replace(strings.Replace(httpPrefix, "https", "webrtc", 1), "http", "webrtc", 1)
	out.WebRTC = fmt.Sprintf("%s/proxy/sms/index/api/webrtc?app=%s&stream=%s&type=play&token=%s", rtcPrefix, app, stream, token)
	out.RTMP = fmt.Sprintf("rtmp://%s:%d/%s/%s", host, ms.Ports.RTMP, app, stream)
	out.RTSP = fmt.Sprintf("rtsp://%s:%d/%s/%s", host, ms.Ports.RTSP, app, stream)
	return out
}

func NewZLMDriver() *ZLMDriver {
	return &ZLMDriver{
		engine: zlm.NewEngine(),
	}
}

func (d *ZLMDriver) Protocol() string {
	return ProtocolZLMediaKit
}

func (d *ZLMDriver) withConfig(ms *MediaServer) zlm.Engine {
	url := fmt.Sprintf("http://%s:%d", ms.IP, ms.Ports.HTTP)
	return d.engine.SetConfig(zlm.Config{
		URL:    url,
		Secret: ms.Secret,
	})
}

func (d *ZLMDriver) Connect(ctx context.Context, ms *MediaServer) error {
	engine := d.withConfig(ms)
	resp, err := engine.GetServerConfig()
	if err != nil {
		return err
	}
	if len(resp.Data) == 0 {
		return fmt.Errorf("ZLM 服务节点配置为空")
	}

	// 更新端口信息等
	// 注意：这里我们不直接修改数据库，而是修改传入的 ms 对象，调用者负责持久化或使用
	zlmConfig := resp.Data[0]
	http := ms.Ports.HTTP
	ms.Ports.FLV = http
	ms.Ports.WsFLV = http
	ms.Ports.HTTPS = zlmConfig.HTTPSslport
	ms.Ports.RTMP = zlmConfig.RtmpPort
	ms.Ports.RTMPs = zlmConfig.RtmpSslport
	ms.Ports.RTSP = zlmConfig.RtspPort
	ms.Ports.RTSPs = zlmConfig.RtspSslport
	ms.Ports.RTPPorxy = zlmConfig.RtpProxyPort
	ms.Ports.FLVs = zlmConfig.HTTPSslport
	ms.Ports.WsFLVs = zlmConfig.HTTPSslport
	ms.HookAliveInterval = 10
	ms.Status = true

	return nil
}

// Setup 下发 ZLM 参数，并在拼接回调路径时保留基址中的启动密钥。
func (d *ZLMDriver) Setup(ctx context.Context, ms *MediaServer, webhookURL string) error {
	hookURL, err := url.Parse(webhookURL)
	if err != nil {
		// 解析错误包含完整 URL，返回固定提示，避免将回调密钥写入连接失败日志。
		return fmt.Errorf("解析媒体回调地址失败，请检查回调主机配置")
	}
	engine := d.withConfig(ms)

	// 拼接 IP 但是不要空格
	ips := make([]string, 0, 2)
	for _, ip := range []string{ms.SDPIP, ms.IP} {
		if ip != "" {
			ips = append(ips, ip)
		}
	}
	_ = ips

	// 为什么: 把 WebRTC 的 UDP/TCP 端口合并到 RTP 端口段首位, 云安全组只需开放一段区间。
	// 剩余端口(start+1..end)下发给 rtp_proxy.port_range, GB28181 随机收流自动避开首端口。
	portRange := ms.RTPPortRange
	rtcPort := ""
	if p, rest, ok := splitFirstPort(ms.RTPPortRange); ok {
		rtcPort = p
		portRange = rest
		slog.Info("ZLM 将 rtc 端口合并到 RTP 段首位",
			"rtc.port", rtcPort, "rtp_proxy.port_range", portRange)
	} else {
		slog.Warn("ZLM RTPPortRange 解析失败, 保持 rtc 默认端口", "range", ms.RTPPortRange)
	}

	// 构造配置请求
	req := zlm.SetServerConfigRequest{
		RtcExternIP: new(strings.Join(ips, ",")),

		GeneralMediaServerID: new(ms.ID),
		HookEnable:           new("1"),
		HookOnFlowReport:     new(""),

		ProtocolEnableTs:      new("0"),
		ProtocolEnableFmp4:    new("0"),
		ProtocolEnableHls:     new("0"),
		ProtocolEnableHlsFmp4: new("1"),
		RtmpEnhanced:          new("1"),

		HookOnPlay:                     new(hookURL.JoinPath("on_play").String()),
		HookOnPublish:                  new(hookURL.JoinPath("on_publish").String()),
		HookOnStreamNoneReader:         new(hookURL.JoinPath("on_stream_none_reader").String()),
		GeneralStreamNoneReaderDelayMS: new("30000"),
		HookOnStreamNotFound:           new(hookURL.JoinPath("on_stream_not_found").String()),
		HookOnRecordTs:                 new(""),
		HookOnRecordMp4:                new(hookURL.JoinPath("on_record_mp4").String()),
		HookOnRtspAuth:                 new(""),
		HookOnRtspRealm:                new(""),
		HookOnShellLogin:               new(""),
		HookOnStreamChanged:            new(hookURL.JoinPath("on_stream_changed").String()),
		HookOnServerKeepalive:          new(hookURL.JoinPath("on_server_keepalive").String()),
		HookOnServerStarted:            new(hookURL.JoinPath("on_server_started").String()),
		HookTimeoutSec:                 new("10"),
		HookAliveInterval:              new(fmt.Sprint(ms.HookAliveInterval)),
		ProtocolContinuePushMs:         new("3000"),
		RtpProxyPortRange:              &portRange,
		FfmpegLog:                      new("./fflogs/ffmpeg.log"),

		// 为什么: 低延迟直播优化, 但保留 GOP 缓存保证首画面快速呈现。
		// rtp_proxy.gop_cache 保持默认开启: 新连接进来立刻下发最近一个 I 帧缓冲, 首帧延迟 <1s; 由前端追帧消化多余缓冲。
		// protocol.add_mute_audio=0: 无音频时不插静音帧, 免去等待音频同步的额外延迟。
		// rtp.lowLatency=1/rtsp.lowLatency=1: 开启 ZLM 自身的低延迟标志, 关闭 RTP 累积缓冲策略。
		// general.unready_frame_cache=50: 流未 ready 期间最多缓 50 帧(约 2 秒), 避免冷启动积压过多历史帧。
		// general.mergeWriteMS=100: 写合并控制在 100ms, 平衡 TCP 发包碎片与延迟(默认 300ms 偏高)。
		ProtocolAddMuteAudio:     new("0"),
		RtpLowLatency:            new("1"),
		RtspLowLatency:           new("1"),
		GeneralUnreadyFrameCache: new("50"),
		GeneralMergeWriteMS:      new("100"),
		GeneralListenIP:          new("0.0.0.0"),

		// 录像配置
		// 移除默认的 "record" 目录层级，简化路径结构
		RecordAppName:    new(""),
		RecordFastStart:  new("1"), // moov 写在开头，便于流式播放
		RecordEnableFmp4: new("0"), // 启用 fMP4 格式，HLS.js 可直接播放
	}
	if rtcPort != "" {
		req.RtcPort = &rtcPort
		req.RtcTCPPort = &rtcPort
	}

	// 为什么: rtc.port / rtc.tcpPort 被 SetServerConfig 修改后只更新内存配置, 不会重新 bind UDP/TCP 套接字;
	// 若 ZLM 当前监听端口与目标不一致(常见于首次把默认 8000 改成 RTP 段首位 20000), 必须重启 ZLM 进程后
	// 新端口才会生效, 否则 WebRTC 的 ICE 会因目标 candidate 端口无人监听而长时间 checking 最终 failed。
	// 这里仅在端口真实变更时触发一次重启, 常规热重启不受影响。
	needRestart := false
	var currentRtcPort string
	if cfg, cerr := engine.GetServerConfig(); cerr == nil && len(cfg.Data) > 0 {
		currentRtcPort = cfg.Data[0].RtcPort
	}

	resp, err := engine.SetServerConfig(&req)
	if err != nil {
		return err
	}
	slog.Info("ZLM 服务节点配置设置成功", "changed", resp.Changed)

	if rtcPort != "" && currentRtcPort != "" && currentRtcPort != rtcPort {
		needRestart = true
	}
	if needRestart {
		slog.Info("ZLM rtc.port 变更, 重启 MediaServer 以重绑 UDP/TCP 端口",
			"from", currentRtcPort, "to", rtcPort)
		if rerr := engine.RestartServer(); rerr != nil {
			slog.Warn("ZLM 重启调用失败, WebRTC 可能因端口未重绑而黑屏, 请手动重启容器", "err", rerr)
			notify.Warn("ZLM 重启调用失败, WebRTC 可能因端口未重绑而黑屏, 请手动重启容器")
			return nil
		}
		waitZLMReady(ctx, engine, rtcPort)
	}
	return nil
}

// waitZLMReady 轮询 ZLM 进程恢复并确认 rtc.port 已被新端口接管。
//
// 为什么: restartServer 接口是异步的, 重启后进程有 1~3s 不可达窗口, 直接返回会让调用方误以为配置已生效;
// 循环探测 GetServerConfig 直到拿到预期 rtc.port 或总超时(10s), 给后续依赖 ZLM 的逻辑一个稳定的起点。
func waitZLMReady(ctx context.Context, engine zlm.Engine, expectRtcPort string) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		case <-time.After(500 * time.Millisecond):
		}
		cfg, err := engine.GetServerConfig()
		if err != nil || len(cfg.Data) == 0 {
			continue
		}
		if cfg.Data[0].RtcPort == expectRtcPort {
			slog.Info("ZLM 重启就绪, rtc.port 已生效", "rtc.port", expectRtcPort)
			return
		}
	}
	slog.Warn("等待 ZLM 重启就绪超时, 建议检查容器状态", "expect_rtc_port", expectRtcPort)
	notify.Warn(fmt.Sprintf("等待 ZLM 重启就绪超时 (期望 rtc.port=%s), 建议检查容器状态", expectRtcPort))
}

func (d *ZLMDriver) Ping(ctx context.Context, ms *MediaServer) error {
	// 使用 getApiList 或简单的获取配置来探测是否存活
	engine := d.withConfig(ms)
	// 可以使用更轻量级的接口，这里暂时复用 GetServerConfig
	_, err := engine.GetServerConfig()
	return err
}

func (d *ZLMDriver) OpenRTPServer(ctx context.Context, ms *MediaServer, req *zlm.OpenRTPServerRequest) (*zlm.OpenRTPServerResponse, error) {
	engine := d.withConfig(ms)
	return engine.OpenRTPServer(*req)
}

func (d *ZLMDriver) CloseRTPServer(ctx context.Context, ms *MediaServer, req *zlm.CloseRTPServerRequest) (*zlm.CloseRTPServerResponse, error) {
	engine := d.withConfig(ms)
	return engine.CloseRTPServer(*req)
}

func (d *ZLMDriver) CloseStreams(ctx context.Context, ms *MediaServer, req *zlm.CloseStreamsRequest) (*zlm.CloseStreamsResponse, error) {
	engine := d.withConfig(ms)
	return engine.CloseStreams(*req)
}

func (d *ZLMDriver) AddStreamProxy(ctx context.Context, ms *MediaServer, req *AddStreamProxyRequest) (*zlm.AddStreamProxyResponse, error) {
	engine := d.withConfig(ms)
	return engine.AddStreamProxy(zlm.AddStreamProxyRequest{
		Vhost:         "__defaultVhost__",
		App:           req.App,
		Stream:        req.Stream,
		URL:           req.URL,
		RTPType:       req.RTPType,
		RetryCount:    3,
		TimeoutSec:    PullTimeoutMs / 1000,
		EnableHLSFMP4: new(true),
		EnableAudio:   new(true),
		EnableRTSP:    new(true),
		EnableRTMP:    new(true),
		AddMuteAudio:  new(true),
		AutoClose:     new(true),
	})
}

func (d *ZLMDriver) GetSnapshot(ctx context.Context, ms *MediaServer, req *GetSnapRequest) ([]byte, error) {
	engine := d.withConfig(ms)
	return engine.GetSnap(req.GetSnapRequest)
}

// GetMediaInfo 获取指定流的详细音视频轨道信息
func (d *ZLMDriver) GetMediaInfo(ctx context.Context, ms *MediaServer, app, stream string) ([]zlm.MediaItem, error) {
	engine := d.withConfig(ms)
	resp, err := engine.GetMediaInfo(zlm.GetMediaInfoRequest{
		Schema: "rtsp",
		Vhost:  "__defaultVhost__",
		App:    app,
		Stream: stream,
	})
	if err != nil {
		return nil, err
	}
	return resp.Data, nil
}

// StartRecord 开始录制，通知 ZLM 对指定流进行 MP4 录制
func (d *ZLMDriver) StartRecord(ctx context.Context, ms *MediaServer, req *zlm.StartRecordRequest) (*zlm.StartRecordResponse, error) {
	engine := d.withConfig(ms)
	return engine.StartRecord(*req)
}

// StopRecord 停止录制
func (d *ZLMDriver) StopRecord(ctx context.Context, ms *MediaServer, req *zlm.StopRecordRequest) (*zlm.StopRecordResponse, error) {
	engine := d.withConfig(ms)
	return engine.StopRecord(*req)
}

// GetMediaList 批量获取所有在线流列表（含录制状态）
func (d *ZLMDriver) GetMediaList(ctx context.Context, ms *MediaServer) (*zlm.GetMediaListResponse, error) {
	engine := d.withConfig(ms)
	return engine.GetMediaList()
}
