package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gowvp/owl/internal/conf"
	"github.com/gowvp/owl/internal/core/ipc"
	"github.com/gowvp/owl/internal/core/sms"
	"github.com/gowvp/owl/pkg/zlm"
	"github.com/ixugo/goddd/domain/uniqueid"
)

func TestOfferedVideoCodec(t *testing.T) {
	for _, tt := range []struct {
		name, sdp string
		want      bool
	}{
		{"receiving", "m=video 9 UDP/TLS/RTP/SAVPF 96\r\na=rtpmap:96 H265/90000\r\na=recvonly", true},
		{"payload excluded", "m=video 9 UDP/TLS/RTP/SAVPF 97\na=rtpmap:96 H265/90000", false},
		{"rejected video", "m=video 0 UDP/TLS/RTP/SAVPF 96\na=rtpmap:96 H265/90000", false},
		{"audio", "m=audio 9 UDP/TLS/RTP/SAVPF 96\na=rtpmap:96 H265/90000", false},
		{"fmtp only", "m=video 9 UDP/TLS/RTP/SAVPF 96\na=fmtp:96 H265", false},
		{"inactive", "m=video 9 UDP/TLS/RTP/SAVPF 96\na=rtpmap:96 H265/90000\na=inactive", false},
		{"send only", "m=video 9 UDP/TLS/RTP/SAVPF 96\na=sendonly\na=rtpmap:96 H265/90000", false},
		{"session inactive", "a=inactive\nm=video 9 UDP/TLS/RTP/SAVPF 96\na=rtpmap:96 H265/90000", false},
		{"override session", "a=inactive\nm=video 9 UDP/TLS/RTP/SAVPF 96\na=rtpmap:96 H265/90000\na=recvonly", true},
		{"multiple media", "m=video 0 RTP/AVP 96\na=rtpmap:96 H265/90000\nm=video 9 RTP/AVP 97\na=rtpmap:97 h265/90000\nm=audio 9 RTP/AVP 8", true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := offeredVideoCodec(tt.sdp, "H265"); got != tt.want {
				t.Fatalf("got %v, want %v", got, tt.want)
			}
		})
	}
}

// A real subprocess exercises cancellation/Wait without requiring a media binary.
func TestPreviewProcess(t *testing.T) {
	if os.Getenv("OWL_TEST_PREVIEW_PROCESS") != "1" {
		return
	}
	time.Sleep(time.Minute)
	os.Exit(0)
}

func previewTestServer(t *testing.T, handler http.HandlerFunc) *sms.MediaServer {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	u, _ := url.Parse(srv.URL)
	host, port, _ := net.SplitHostPort(u.Host)
	p, _ := strconv.Atoi(port)
	return &sms.MediaServer{ID: sms.DefaultMediaServerID, IP: host, Ports: sms.MediaServerPorts{HTTP: p, RTSP: 554}, Type: sms.ProtocolZLMediaKit}
}

func previewInfo(w http.ResponseWriter, codec string, readers int) {
	w.Header().Set("Content-Type", "application/json")
	items := []zlm.MediaItem{}
	if codec != "" {
		items = append(items, zlm.MediaItem{TotalReaderCount: readers, Tracks: []zlm.MediaTrack{{CodecIDName: codec, CodecType: 0, Ready: true}}})
	}
	_ = json.NewEncoder(w).Encode(zlm.GetMediaInfoResponse{Data: items})
}

func previewTestManager(t *testing.T, config conf.Media) (*previewManager, *atomic.Int32, *atomic.Pointer[exec.Cmd]) {
	t.Helper()
	m := newPreviewManager(config)
	t.Cleanup(m.close)
	starts := new(atomic.Int32)
	process := new(atomic.Pointer[exec.Cmd])
	m.command = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		starts.Add(1)
		cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPreviewProcess$")
		cmd.Env = append(os.Environ(), "OWL_TEST_PREVIEW_PROCESS=1")
		process.Store(cmd)
		return cmd
	}
	return m, starts, process
}

func awaitPreview(t *testing.T, c <-chan struct{}) {
	t.Helper()
	select {
	case <-c:
	case <-time.After(5 * time.Second):
		t.Fatal("preview did not finish")
	}
}

func TestPreviewSharedAndIdle(t *testing.T) {
	readers := new(atomic.Int32)
	ms := previewTestServer(t, func(w http.ResponseWriter, r *http.Request) { previewInfo(w, "CodecH264", int(readers.Load())) })
	m, starts, process := previewTestManager(t, conf.Media{})
	jobs := make(chan *previewJob, 12)
	errs := make(chan error, 12)
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			j, err := m.ensure(t.Context(), ms, "rtp", "camera", "")
			jobs <- j
			errs <- err
		})
	}
	wg.Wait()
	close(jobs)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	var job *previewJob
	for j := range jobs {
		if job != nil && job != j {
			t.Fatal("duplicate job")
		}
		job = j
	}
	if starts.Load() != 1 {
		t.Fatalf("started %d processes", starts.Load())
	}
	readers.Store(2)
	m.mu.Lock()
	job.lastUsed = time.Now().Add(-time.Minute)
	m.mu.Unlock()
	m.sweep()
	select {
	case <-job.done:
		t.Fatal("viewers lost their transcoder")
	default:
	}
	readers.Store(0)
	m.mu.Lock()
	job.lastUsed = time.Now().Add(-time.Minute)
	m.mu.Unlock()
	m.sweep()
	awaitPreview(t, job.done)
	if process.Load().ProcessState == nil {
		t.Fatal("process was not reaped")
	}
	m.mu.Lock()
	n := len(m.jobs)
	m.mu.Unlock()
	if n != 0 {
		t.Fatal("idle job still occupies a slot")
	}
	next, err := m.ensure(t.Context(), ms, "rtp", "camera", "")
	if err != nil || next == job || starts.Load() != 2 {
		t.Fatalf("restart failed: %v", err)
	}
	m.close()
	awaitPreview(t, next.done)
	if process.Load().ProcessState == nil {
		t.Fatal("shutdown did not reap process")
	}
}

func TestPreviewCanceledWaiterAndLimit(t *testing.T) {
	ready := make(chan struct{})
	inspected := make(chan struct{})
	var once sync.Once
	ms := previewTestServer(t, func(w http.ResponseWriter, r *http.Request) {
		once.Do(func() { close(inspected) })
		select {
		case <-ready:
		case <-r.Context().Done():
			return
		}
		previewInfo(w, "H264", 0)
	})
	m, starts, _ := previewTestManager(t, conf.Media{PreviewMaxConcurrent: 1})
	ctx, cancel := context.WithCancel(t.Context())
	result := make(chan error, 1)
	go func() { _, err := m.ensure(ctx, ms, "rtp", "camera", ""); result <- err }()
	awaitPreview(t, inspected)
	cancel()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("expected cancellation: %v", err)
	}
	if _, err := m.ensure(t.Context(), ms, "rtp", "other", ""); err == nil {
		t.Fatal("channel limit not enforced")
	}
	close(ready)
	job, err := m.ensure(t.Context(), ms, "rtp", "camera", "")
	if err != nil || starts.Load() != 1 {
		t.Fatalf("canceled waiter killed shared job: %v", err)
	}
	// No browser ever connects: the watchdog must still collect this process.
	m.mu.Lock()
	job.lastUsed = time.Now().Add(-time.Minute)
	m.mu.Unlock()
	m.sweep()
	awaitPreview(t, job.done)
}

func TestPreviewStartupFailure(t *testing.T) {
	ms := previewTestServer(t, func(w http.ResponseWriter, r *http.Request) { previewInfo(w, "", 0) })
	m, _, _ := previewTestManager(t, conf.Media{PreviewMaxConcurrent: 1})
	m.command = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "/nonexistent/owl-preview-ffmpeg")
	}
	if _, err := m.ensure(t.Context(), ms, "rtp", "camera", ""); err == nil {
		t.Fatal("missing FFmpeg accepted")
	}
	m.mu.Lock()
	n := len(m.jobs)
	m.mu.Unlock()
	if n != 0 {
		t.Fatal("failed startup retained slot")
	}
	m.command = func(ctx context.Context, _ string, _ ...string) *exec.Cmd {
		return exec.CommandContext(ctx, os.Args[0], "-test.run=^TestPreviewProcess$")
	}
	if _, err := m.ensure(t.Context(), ms, "rtp", "camera", ""); err == nil {
		t.Fatal("early process exit accepted")
	}
	m.mu.Lock()
	n = len(m.jobs)
	m.mu.Unlock()
	if n != 0 {
		t.Fatal("exited process retained slot")
	}
}

func TestPreviewStartupTimeout(t *testing.T) {
	ms := previewTestServer(t, func(w http.ResponseWriter, r *http.Request) { previewInfo(w, "", 0) })
	m, _, process := previewTestManager(t, conf.Media{PreviewMaxConcurrent: 1})
	if _, err := m.ensure(t.Context(), ms, "rtp", "camera", ""); err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("expected bounded startup timeout: %v", err)
	}
	m.mu.Lock()
	n := len(m.jobs)
	m.mu.Unlock()
	if n != 0 || process.Load().ProcessState == nil {
		t.Fatal("timeout did not release slot and reap process")
	}
}

func TestPreviewStoppingJob(t *testing.T) {
	ms := previewTestServer(t, func(w http.ResponseWriter, r *http.Request) { previewInfo(w, "H264", 0) })
	m, starts, _ := previewTestManager(t, conf.Media{})
	// A stopping process remains tracked until Wait completes.
	old := &previewJob{stream: "stopping", stopping: true, done: make(chan struct{}), cancel: func() {}}
	// Reuse the exact manager key produced for this source.
	first, err := m.ensure(t.Context(), ms, "rtp", "camera", "")
	if err != nil {
		t.Fatal(err)
	}
	first.cancel()
	awaitPreview(t, first.done)
	old.stream = first.stream
	m.mu.Lock()
	m.jobs[old.stream] = old
	m.mu.Unlock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := m.ensure(ctx, ms, "rtp", "camera", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("expected waiting cancellation: %v", err)
	}
	if starts.Load() != 1 {
		t.Fatal("started a duplicate during process cleanup")
	}
	m.mu.Lock()
	delete(m.jobs, old.stream)
	close(old.done)
	m.mu.Unlock()
	if _, err := m.ensure(t.Context(), ms, "rtp", "camera", ""); err != nil {
		t.Fatal(err)
	}
	if starts.Load() != 2 {
		t.Fatal("did not restart after cleanup")
	}
}

func TestPreviewHooks(t *testing.T) {
	ms := previewTestServer(t, func(w http.ResponseWriter, r *http.Request) { previewInfo(w, "H264", 0) })
	m, _, _ := previewTestManager(t, conf.Media{})
	job, err := m.ensure(t.Context(), ms, "rtp", "camera", "")
	if err != nil {
		t.Fatal(err)
	}
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	// Nil stores catch accidental original-channel/recording calls.
	hook := WebHookAPI{uc: &Usecase{preview: m}, log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	for _, tt := range []struct {
		name, server, params string
		want                 int
	}{
		{"missing", ms.ID, "", 1},
		{"wrong", ms.ID, previewKeyParam + "=wrong", 1},
		{"duplicate", ms.ID, previewKeyParam + "=" + job.key + "&" + previewKeyParam + "=" + job.key, 1},
		{"foreign server", "other", previewKeyParam + "=" + job.key, 1},
		{"valid", ms.ID, previewKeyParam + "=" + job.key, 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			in := &onPublishInput{App: previewApp, Stream: job.stream, MediaServerID: tt.server, Params: tt.params}
			published, err := hook.onPublish(c, in)
			if err != nil || published.Code != tt.want {
				t.Fatalf("publish: %+v %v", published, err)
			}
			played, err := hook.onPlay(c, in)
			if err != nil || played.Code != tt.want {
				t.Fatalf("play: %+v %v", played, err)
			}
			if tt.want == 0 {
				for _, flag := range []*bool{published.EnableMp4, published.EnableHls, published.EnableHlsFmp4, published.EnableRtmp, published.EnableTs, published.EnableFmp4, published.Mp4AsPlayer, published.AutoClose} {
					if flag == nil || *flag {
						t.Fatal("preview recording/muxing/auto-close enabled")
					}
				}
			}
		})
	}
	if out, err := hook.onStreamChanged(c, &onStreamChangedInput{App: previewApp, Regist: true}); err != nil || out.Code != 0 {
		t.Fatal("stream registration")
	}
	if out, err := hook.onStreamChanged(c, &onStreamChangedInput{App: previewApp}); err != nil || out.Code != 0 {
		t.Fatal("stream removal")
	}
	if out, err := hook.onStreamNoneReader(c, &onStreamNoneReaderInput{App: previewApp, Stream: job.stream, MediaServerID: ms.ID}); err != nil || out.Close {
		t.Fatal("stream closed before grace period")
	}
	if out, err := hook.onStreamNotFound(c, &onStreamNotFoundInput{App: previewApp, Schema: "rtsp"}); err != nil || out.Code != 0 {
		t.Fatal("derived stream routed into source protocol")
	}
	if out, err := hook.onRecordMP4(c, &onRecordMP4Input{App: previewApp}); err != nil || out.Code != 0 {
		t.Fatal("derived recording touched stores")
	}
	if out, err := hook.onStreamNoneReader(c, &onStreamNoneReaderInput{App: previewApp, Stream: "unknown", MediaServerID: ms.ID}); err != nil || !out.Close {
		t.Fatal("unmanaged preview stream must close")
	}
	m.mu.Lock()
	job.stopping = true
	m.mu.Unlock()
	if m.authorized(ms.ID, job.stream, previewKeyParam+"="+job.key) {
		t.Fatal("stopping stream still authorized")
	}
}

// Only the reads used by preview resolution are implemented; other calls panic.
type previewChannelStore struct {
	ipc.ChannelStorer
	channel *ipc.Channel
}

func (s previewChannelStore) GetByAppStream(context.Context, string, string) (*ipc.Channel, error) {
	return s.channel, nil
}

type previewIPCStore struct {
	ipc.Storer
	channel *ipc.Channel
}

func (s previewIPCStore) Channel() ipc.ChannelStorer { return previewChannelStore{channel: s.channel} }

type previewMediaStore struct {
	sms.MediaServerStorer
	server *sms.MediaServer
}

func (s previewMediaStore) GetByID(context.Context, string) (*sms.MediaServer, error) {
	return s.server, nil
}

type previewSMSStore struct {
	sms.Storer
	server *sms.MediaServer
}

func (s previewSMSStore) MediaServer() sms.MediaServerStorer {
	return previewMediaStore{server: s.server}
}

type previewProtocol struct {
	ipc.Protocoler
	start func() error
}

func (p previewProtocol) OnStreamNotFound(context.Context, string, string) error { return p.start() }

type previewRecorder struct{ *httptest.ResponseRecorder }

func (previewRecorder) CloseNotify() <-chan bool { return make(chan bool) }

func TestPreviewProxy(t *testing.T) {
	for _, tt := range []struct {
		name, source, offer               string
		disabled, wantPreview, cold, rtsp bool
	}{
		{name: "H265 to H264", source: "H265", offer: "H264", wantPreview: true},
		{name: "RTSP H265 source", source: "H265", offer: "H264", wantPreview: true, rtsp: true},
		{name: "cold H265 source", source: "H265", offer: "H264", wantPreview: true, cold: true},
		{name: "ZLM codec prefix", source: "CodecH265", offer: "H264", wantPreview: true},
		{name: "H264 source", source: "H264", offer: "H264"},
		{name: "H265 browser", source: "H265", offer: "H265"},
		{name: "no H264 decoder", source: "H265", offer: "VP8"},
		{name: "disabled", source: "H265", offer: "H264", disabled: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sourceReady := new(atomic.Bool)
			sourceReady.Store(!tt.cold)
			sourceStarts := new(atomic.Int32)
			upstream := make(chan url.Values, 1)
			bodyRead := make(chan string, 1)
			ms := previewTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/index/api/webrtc" {
					upstream <- r.URL.Query()
					b, _ := io.ReadAll(r.Body)
					bodyRead <- string(b)
					_, _ = io.WriteString(w, `{"code":0,"sdp":"answer"}`)
					return
				}
				var in map[string]any
				_ = json.NewDecoder(r.Body).Decode(&in)
				codec := tt.source
				if !sourceReady.Load() {
					codec = ""
				}
				if in["app"] == previewApp {
					codec = "H264"
				}
				previewInfo(w, codec, 0)
			})
			m, starts, _ := previewTestManager(t, conf.Media{})
			uc := newTestUsecase()
			uc.preview = m
			uc.Conf.Media = conf.Media{IP: ms.IP, HTTPPort: ms.Ports.HTTP, PreviewDisabled: tt.disabled}
			ch := &ipc.Channel{ID: "camera", Type: ipc.TypeGB28181}
			if tt.rtsp {
				ch.Type, ch.App, ch.Stream = ipc.TypeRTSP, "pull", "camera"
			}
			uc.GB28181API.ipc = ipc.NewCore(previewIPCStore{channel: ch}, uniqueid.Core{}, nil)
			uc.WebHookAPI.protocols = map[string]ipc.Protocoler{ipc.TypeGB28181: previewProtocol{start: func() error { sourceStarts.Add(1); sourceReady.Store(true); return nil }}}
			uc.SMSAPI.smsCore = sms.NewCore(previewSMSStore{server: ms})
			t.Cleanup(uc.SMSAPI.smsCore.NodeManager.Close)
			r := gin.New()
			r.Any("/proxy/sms/*path", uc.proxySMS)
			sdp := "v=0\r\nm=video 9 UDP/TLS/RTP/SAVPF 96\r\na=rtpmap:96 " + tt.offer + "/90000\r\na=recvonly\r\n"
			rawQuery := "app=" + ch.GetApp() + "&stream=camera&type=play&token=" + makePlayToken(t, ch.GetApp(), "camera", time.Now().Add(time.Hour))
			rec := previewRecorder{httptest.NewRecorder()}
			req := httptest.NewRequest(http.MethodPost, "/proxy/sms/index/api/webrtc?"+rawQuery, strings.NewReader(sdp))
			r.ServeHTTP(rec, req)
			if rec.Code != 200 || !strings.Contains(rec.Body.String(), "answer") {
				t.Fatalf("proxy failed: %d %s", rec.Code, rec.Body.String())
			}
			if tt.cold && sourceStarts.Load() != 1 {
				t.Fatal("cold preview did not start original source")
			}
			query := <-upstream
			if (query.Get("app") == previewApp) != tt.wantPreview {
				t.Fatalf("wrong upstream: %v", query)
			}
			if tt.wantPreview {
				if starts.Load() != 1 || query.Get("stream") == "camera" || query.Get(previewKeyParam) == "" {
					t.Fatal("missing transcoder or internal routing")
				}
				if strings.Contains(rec.Body.String(), query.Get(previewKeyParam)) {
					t.Fatal("internal key leaked in response")
				}
			} else if starts.Load() != 0 {
				t.Fatal("unnecessary transcoder")
			}
			if req.URL.RawQuery != rawQuery {
				t.Fatal("internal key leaked into original request/log URL")
			}
			if got := <-bodyRead; got != sdp {
				t.Fatal("SDP body changed")
			}
			// An unauthorized play must never resolve a source or start FFmpeg.
			denied := httptest.NewRecorder()
			r.ServeHTTP(denied, httptest.NewRequest(http.MethodPost, "/proxy/sms/index/api/webrtc?app="+ch.GetApp()+"&stream=camera&type=play", strings.NewReader(sdp)))
			if denied.Code != http.StatusForbidden {
				t.Fatal("unauthorized playback accepted")
			}
			select {
			case <-upstream:
				t.Fatal("unauthorized request forwarded")
			default:
			}
		})
	}
}
