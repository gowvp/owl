package api

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/gowvp/owl/internal/conf"
	"github.com/gowvp/owl/internal/core/ipc"
	"github.com/gowvp/owl/internal/core/sms"
	"github.com/gowvp/owl/pkg/zlm"
	"github.com/ixugo/goddd/pkg/web"
)

const (
	previewApp      = ipc.PreviewApp
	previewKeyParam = "owl_preview_key"
)

type previewHTTPHandler struct {
	http.Handler
	preview *previewManager
}

func (h *previewHTTPHandler) Close() { h.preview.close() }

// key 在任务存续期间固定；生命周期状态和 err 由 previewManager.mu 保护。
type previewJob struct {
	stream, key, serverID string
	engine                zlm.Engine
	ready                 chan struct{} // 发布流已就绪，或启动失败。
	done                  chan struct{} // 子进程已 Wait，任务名额已释放。
	cancel                context.CancelFunc
	err                   error
	starting, stopping    bool
	lastUsed              time.Time
}

// 原流用于录像/AI；这些进程只为不支持 H265 的 WebRTC 观众供流。
type previewManager struct {
	mu      sync.Mutex
	jobs    map[string]*previewJob
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	config  conf.Media
	idle    time.Duration
	limit   int
	command func(context.Context, string, ...string) *exec.Cmd
}

// 不自动改名或接管历史通道；冲突或检查失败时保留原流程，重启后重新检查。
func (uc *Usecase) initPreview() {
	config := uc.Conf.Media
	if !config.PreviewDisabled {
		_, total, err := uc.GB28181API.ipc.ListChannels(context.Background(), &ipc.FindChannelInput{
			PagerFilter: web.PagerFilter{Page: 1, Size: 1}, App: previewApp,
		})
		if err != nil || total > 0 {
			config.PreviewDisabled = true
			slog.Warn("未启用预览转码：保留 app 检查失败或已被通道占用，请检查或改名后重启",
				"app", previewApp, "channels", total, "err", err)
		}
	}
	uc.preview = newPreviewManager(config)
}

func (uc *Usecase) previewEnabled() bool {
	return uc != nil && uc.preview != nil && !uc.Conf.Media.PreviewDisabled && !uc.preview.config.PreviewDisabled
}

func newPreviewManager(config conf.Media) *previewManager {
	ctx, cancel := context.WithCancel(context.Background())
	idle, limit := config.PreviewIdleSeconds, config.PreviewMaxConcurrent
	if idle <= 0 {
		idle = 30
	}
	if limit <= 0 {
		limit = 3
	}
	m := &previewManager{jobs: make(map[string]*previewJob), ctx: ctx, cancel: cancel,
		config: config, idle: time.Duration(idle) * time.Second, limit: limit, command: exec.CommandContext}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(5 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				m.sweep()
			}
		}
	}()
	return m
}

func (m *previewManager) close() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.cancel()
	for _, j := range m.jobs {
		j.cancel()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

// 同一源流共享任务；请求取消只结束该观众的等待，不终止其他观众的转码。
func (m *previewManager) ensure(ctx context.Context, ms *sms.MediaServer, app, stream, session string) (*previewJob, error) {
	id := fmt.Sprintf("%x", sha256.Sum256([]byte(ms.ID+"\x00"+app+"\x00"+stream)))
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		m.mu.Lock()
		if m.ctx.Err() != nil {
			m.mu.Unlock()
			return nil, fmt.Errorf("预览服务已停止")
		}
		j := m.jobs[id]
		// 停止中的进程仍占名额，等 Wait 完成后才能复用同一个发布地址。
		if j != nil && j.stopping {
			m.mu.Unlock()
			select {
			case <-j.done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		if j == nil {
			if len(m.jobs) >= m.limit {
				m.mu.Unlock()
				return nil, fmt.Errorf("预览转码通道已达上限 (%d)", m.limit)
			}
			// 由管理器持有进程，避免第一个请求取消导致共享进程被杀。
			jobCtx, cancel := context.WithCancel(m.ctx)
			j = &previewJob{stream: id, key: uuid.NewString(), serverID: ms.ID,
				engine: zlm.NewEngine().SetConfig(zlm.Config{URL: fmt.Sprintf("http://%s:%d", ms.IP, ms.Ports.HTTP), Secret: ms.Secret}),
				ready:  make(chan struct{}), done: make(chan struct{}), cancel: cancel, starting: true}
			m.jobs[id] = j
			m.wg.Add(1)
			go m.run(jobCtx, j, ms, app, stream, session)
		}
		j.lastUsed = time.Now()
		m.mu.Unlock()
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-j.ready:
			m.mu.Lock()
			err := j.err
			m.mu.Unlock()
			return j, err
		}
	}
}

func (m *previewManager) run(ctx context.Context, j *previewJob, ms *sms.MediaServer, app, stream, session string) {
	defer m.wg.Done()
	defer j.cancel()
	var runErr error
	defer func() {
		m.mu.Lock()
		j.err = runErr
		if j.starting {
			j.starting = false
			close(j.ready)
		}
		delete(m.jobs, j.stream)
		close(j.done)
		m.mu.Unlock()
	}()
	source := url.URL{Scheme: "rtsp", Host: net.JoinHostPort(ms.IP, strconv.Itoa(ms.Ports.RTSP)), Path: "/" + app + "/" + stream}
	if session != "" {
		source.RawQuery = url.Values{"session": {session}}.Encode()
	}
	dest := url.URL{Scheme: "rtsp", Host: net.JoinHostPort(ms.IP, strconv.Itoa(ms.Ports.RTSP)), Path: "/" + previewApp + "/" + j.stream,
		RawQuery: url.Values{previewKeyParam: {j.key}}.Encode()}
	bin := m.config.PreviewFFmpeg
	if bin == "" {
		bin = "ffmpeg"
	}
	cmd := m.command(ctx, bin, "-hide_banner", "-loglevel", "error", "-rtsp_transport", "tcp", "-i", source.String(),
		"-map", "0:v:0", "-map", "0:a:0?", "-c:v", "libx264", "-preset", "ultrafast", "-tune", "zerolatency",
		"-pix_fmt", "yuv420p", "-profile:v", "baseline", "-threads", "2", "-g", "50", "-crf", "23",
		// 高分辨率/噪声画面的无上限 CRF 会产生数 MB 关键帧，WebRTC 丢包后难以组帧。
		"-maxrate", "4M", "-bufsize", "1M",
		// 将 SPS/PPS 附在关键帧，避免 WebRTC 接收端因参数集仅在 RTSP SDP 中而无法解码。
		"-bsf:v", "dump_extra=freq=keyframe",
		"-c:a", "libopus", "-ar", "48000", "-ac", "1", "-f", "rtsp", "-rtsp_transport", "tcp", dest.String())
	// FFmpeg 的错误可能包含带 session/内部密钥的 URL，不直接写到应用日志。
	if err := cmd.Start(); err != nil {
		runErr = fmt.Errorf("无法启动预览转码，请检查 FFmpeg 路径和权限")
		return
	}
	exited := make(chan error, 1)
	go func() { exited <- cmd.Wait() }()
	timer := time.NewTimer(15 * time.Second)
	defer timer.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			runErr = ctx.Err()
			<-exited
			return
		case <-timer.C:
			runErr = fmt.Errorf("预览转码启动超时，请检查源流和 FFmpeg 的 libx264/libopus 支持")
			j.cancel()
			<-exited
			return
		case <-exited:
			runErr = fmt.Errorf("预览转码进程已退出，请检查源流和 FFmpeg 的 libx264/libopus 支持")
			return
		case <-ticker.C:
			info, err := j.engine.GetMediaInfo(zlm.GetMediaInfoRequest{Schema: "rtsp", Vhost: "__defaultVhost__", App: previewApp, Stream: j.stream})
			if err != nil || !hasVideoCodec(info.Data, "H264") {
				continue
			}
			m.mu.Lock()
			j.starting = false
			// 从发布就绪开始留出 ICE 建连宽限期，而非从 FFmpeg 启动开始计时。
			j.lastUsed = time.Now()
			close(j.ready)
			m.mu.Unlock()
			<-exited
			runErr = fmt.Errorf("预览转码进程已结束")
			return
		}
	}
}

// 实际 reader 数负责回收；信令成功但 ICE 失败、页面崩溃也不会留下常驻转码。
func (m *previewManager) sweep() {
	m.mu.Lock()
	jobs := make([]*previewJob, 0, len(m.jobs))
	for _, j := range m.jobs {
		if !j.starting && !j.stopping {
			jobs = append(jobs, j)
		}
	}
	m.mu.Unlock()
	for _, j := range jobs {
		info, err := j.engine.GetMediaInfo(zlm.GetMediaInfoRequest{Schema: "rtsp", Vhost: "__defaultVhost__", App: previewApp, Stream: j.stream})
		readers := 0
		if err == nil {
			for _, item := range info.Data {
				readers += item.TotalReaderCount
			}
		}
		m.mu.Lock()
		if m.jobs[j.stream] == j && !j.stopping {
			// 查询失败表示观看人数未知，不能当成无人观看，也不能计入空闲时间。
			if err != nil || readers > 0 {
				j.lastUsed = time.Now()
			} else if time.Since(j.lastUsed) >= m.idle {
				j.stopping = true
				j.cancel()
			}
		}
		m.mu.Unlock()
	}
}

func (m *previewManager) authorized(server, stream, params string) bool {
	if m == nil {
		return false
	}
	query, err := url.ParseQuery(params)
	if err != nil || len(query[previewKeyParam]) != 1 {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[stream]
	return j != nil && !j.stopping && j.serverID == server &&
		subtle.ConstantTimeCompare([]byte(j.key), []byte(query.Get(previewKeyParam))) == 1
}

func (m *previewManager) managed(server, stream string) bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	j := m.jobs[stream]
	return j != nil && !j.stopping && j.serverID == server
}

func hasVideoCodec(items []zlm.MediaItem, codec string) bool {
	for _, item := range items {
		for _, track := range item.Tracks {
			if track.CodecType == 0 && track.Ready && (codec == "" || strings.EqualFold(track.CodecIDName, codec) || strings.EqualFold(track.CodecIDName, "Codec"+codec)) {
				return true
			}
		}
	}
	return false
}

// 使用 SDP 中实际启用的视频 payload；被拒绝的视频段或 fmtp 文本不代表支持。
func offeredVideoCodec(sdp, codec string) bool {
	video, matched, receiving, sessionReceiving, inMedia := false, false, true, true, false
	payloads := map[string]bool{}
	for _, line := range strings.Split(strings.ReplaceAll(sdp, "\r\n", "\n"), "\n") {
		if strings.HasPrefix(line, "m=") {
			if video && matched && receiving {
				return true
			}
			f := strings.Fields(line)
			video = len(f) >= 4 && f[0] == "m=video" && strings.Split(f[1], "/")[0] != "0"
			inMedia, matched, receiving = true, false, sessionReceiving
			payloads = map[string]bool{}
			if video {
				for _, p := range f[3:] {
					payloads[p] = true
				}
			}
		}
		switch line {
		case "a=inactive", "a=sendonly":
			receiving = false
		case "a=recvonly", "a=sendrecv":
			receiving = true
		}
		if !inMedia {
			sessionReceiving = receiving
		}
		if video && strings.HasPrefix(line, "a=rtpmap:") {
			f := strings.Fields(strings.TrimPrefix(line, "a=rtpmap:"))
			if len(f) == 2 && payloads[f[0]] && strings.EqualFold(strings.Split(f[1], "/")[0], codec) {
				matched = true
			}
		}
	}
	return video && matched && receiving
}

// 在 proxySMS 校验原播放 token 后调用；返回仅供反代使用的流地址与内部密钥。
func (uc *Usecase) preparePreview(c *gin.Context, path string) (string, string, error) {
	if !uc.previewEnabled() || uc.Conf.Media.Type == sms.ProtocolLalmax ||
		path != "/index/api/webrtc" || c.Request.Method != http.MethodPost || c.Query("type") != "play" {
		return "", "", nil
	}
	body, err := io.ReadAll(io.LimitReader(c.Request.Body, 64*1024+1))
	if err != nil || len(body) > 64*1024 {
		return "", "", fmt.Errorf("无效的 WebRTC SDP")
	}
	// ReverseProxy 仍需转发浏览器原始 SDP，探测编码后恢复请求体。
	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	if offeredVideoCodec(string(body), "H265") || !offeredVideoCodec(string(body), "H264") {
		return "", "", nil
	}
	app, stream := c.Query("app"), c.Query("stream")
	ch, err := uc.GB28181API.ipc.GetChannelByAppStreamOrID(c.Request.Context(), app, stream)
	if err != nil {
		return "", "", fmt.Errorf("预览通道不存在")
	}
	// 回退查询可按 ID 找到通道，但必须再次精确验证 app/stream。
	if ch.GetApp() != app || ch.GetStream() != stream {
		return "", "", fmt.Errorf("预览通道与播放地址不匹配")
	}
	serverID := ch.Config.MediaServerID
	if serverID == "" {
		serverID = sms.DefaultMediaServerID
	}
	ms, err := uc.SMSAPI.smsCore.GetMediaServer(c.Request.Context(), serverID)
	if err != nil {
		return "", "", fmt.Errorf("预览媒体服务不可用")
	}
	if ms.Type != "" && ms.Type != sms.ProtocolZLMediaKit {
		return "", "", nil
	}
	// 现有 /proxy/sms 固定转发默认节点，避免将转码流建在另一个节点。
	if ms.ID != sms.DefaultMediaServerID {
		return "", "", nil
	}
	info, err := uc.SMSAPI.smsCore.GetMediaInfo(ms, app, stream)
	if err != nil {
		return "", "", fmt.Errorf("读取预览源流失败")
	}
	if !hasVideoCodec(info, "") {
		if protocol := uc.WebHookAPI.protocols[ch.GetType()]; protocol != nil && len(info) == 0 {
			if err := protocol.OnStreamNotFound(c.Request.Context(), app, stream); err != nil {
				return "", "", fmt.Errorf("启动预览源流失败")
			}
		}
		timer := time.NewTimer(10 * time.Second)
		defer timer.Stop()
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()
		for !hasVideoCodec(info, "") {
			select {
			case <-c.Request.Context().Done():
				return "", "", c.Request.Context().Err()
			case <-timer.C:
				return "", "", fmt.Errorf("等待预览源流超时")
			case <-ticker.C:
				info, _ = uc.SMSAPI.smsCore.GetMediaInfo(ms, app, stream)
			}
		}
	}
	if !hasVideoCodec(info, "H265") {
		return "", "", nil
	}
	j, err := uc.preview.ensure(c.Request.Context(), ms, app, stream, ch.Config.Session)
	if err != nil {
		return "", "", err
	}
	return j.stream, j.key, nil
}
