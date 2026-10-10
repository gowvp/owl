package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/gowvp/owl/internal/adapter/rtmpadapter"
	"github.com/gowvp/owl/internal/conf"
	"github.com/gowvp/owl/internal/core/ipc"
	"github.com/gowvp/owl/internal/core/ipc/stores/ipcdb"
	"github.com/gowvp/owl/internal/core/recording"
	"github.com/gowvp/owl/internal/core/recording/stores/recordingdb"
	"github.com/gowvp/owl/internal/core/sms"
	"github.com/ixugo/goddd/domain/uniqueid"
	"github.com/ixugo/goddd/pkg/hook"
	"github.com/ixugo/goddd/pkg/reason"
	"gorm.io/gorm"
)

func TestPreviewReaderQueryFailure(t *testing.T) {
	for _, failure := range []string{"http", "api", "invalid json", "transport"} {
		t.Run(failure, func(t *testing.T) {
			var unavailable, empty atomic.Bool
			ms := previewTestServer(t, func(w http.ResponseWriter, r *http.Request) {
				if unavailable.Load() {
					switch failure {
					case "http":
						http.Error(w, "unavailable", http.StatusServiceUnavailable)
					case "api":
						_, _ = io.WriteString(w, `{"code":-1,"msg":"unavailable"}`)
					case "transport":
						conn, _, err := w.(http.Hijacker).Hijack()
						if err == nil {
							_ = conn.Close()
						}
					default:
						_, _ = io.WriteString(w, "invalid json")
					}
					return
				}
				readers := 2
				if empty.Load() {
					readers = 0
				}
				previewInfo(w, "H264", readers)
			})
			m, starts, _ := previewTestManager(t, conf.Media{})
			job, err := m.ensure(t.Context(), ms, "rtp", "camera", "")
			if err != nil {
				t.Fatal(err)
			}
			m.sweep() // Establish that the shared job has viewers before the outage.
			unavailable.Store(true)
			for range 3 {
				m.mu.Lock()
				job.lastUsed = time.Now().Add(-2 * m.idle)
				m.mu.Unlock()
				m.sweep()
				m.mu.Lock()
				stopping := job.stopping
				m.mu.Unlock()
				if stopping {
					t.Fatal("control API failure stopped a transcoder with viewers")
				}
			}
			unavailable.Store(false)
			empty.Store(true)
			m.sweep()
			m.mu.Lock()
			stopping := job.stopping
			m.mu.Unlock()
			if stopping {
				t.Fatal("unknown reader period consumed the idle grace period")
			}
			m.mu.Lock()
			job.lastUsed = time.Now().Add(-2 * m.idle)
			m.mu.Unlock()
			m.sweep()
			awaitPreview(t, job.done)
			if starts.Load() != 1 {
				t.Fatal("shared transcoder restarted during the outage")
			}
		})
	}
}

func previewReviewIPC(t *testing.T) (*gorm.DB, ipc.Core) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	db.Callback().Query().Before("gorm:query").Register("strip_for_update", func(d *gorm.DB) {
		delete(d.Statement.Clauses, "FOR")
	})
	if err := db.AutoMigrate(&ipc.Device{}, &ipc.Channel{}, &recording.Recording{}); err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return db, ipc.NewCore(ipcdb.NewDB(db), uniqueid.Core{}, nil)
}

func TestPreviewReservedChannelChanges(t *testing.T) {
	db, core := previewReviewIPC(t)
	for _, app := range []string{previewApp, strings.ToUpper(previewApp)} {
		for _, typ := range []string{ipc.TypeRTMP, ipc.TypeRTSP} {
			_, err := core.CreateChannel(t.Context(), &ipc.AddChannelInput{
				Type: typ, Name: "reserved", App: app, DeviceID: "missing",
			})
			if !errors.Is(err, reason.ErrBadRequest) || !strings.Contains(err.Error(), "预览保留") {
				t.Fatalf("reserved create app=%s type=%s: %v", app, typ, err)
			}
		}
	}
	for _, app := range []string{"push", previewApp} {
		ch := ipc.Channel{ID: app, Name: "original", Type: ipc.TypeRTMP, App: app, Stream: "camera"}
		if err := db.Create(&ch).Error; err != nil {
			t.Fatal(err)
		}
		updated, err := core.UpdateChannel(t.Context(), &ipc.EditChannelInput{Name: "renamed", App: previewApp}, ch.ID)
		if app == "push" {
			if !errors.Is(err, reason.ErrBadRequest) {
				t.Fatalf("ordinary channel moved into reserved app: %+v %v", updated, err)
			}
			stored, err := core.GetChannel(t.Context(), ch.ID)
			if err != nil || stored.App != "push" || stored.Name != "original" {
				t.Fatal("rejected update changed the channel")
			}
		} else if err != nil || updated.App != previewApp || updated.Name != "renamed" {
			t.Fatalf("existing reserved channel cannot be edited: %+v %v", updated, err)
		}
	}
	if _, err := core.UpdateChannel(t.Context(), &ipc.EditChannelInput{App: "push"}, previewApp); err != nil {
		t.Fatalf("legacy channel cannot leave reserved app: %v", err)
	}
	if _, err := core.UpdateChannel(t.Context(), &ipc.EditChannelInput{App: strings.ToUpper(previewApp)}, "push"); !errors.Is(err, reason.ErrBadRequest) {
		t.Fatalf("reserved update accepted a case variant: %v", err)
	}
}

func TestPreviewStartupNamespace(t *testing.T) {
	for _, tt := range []struct {
		name, app             string
		disabled, unavailable bool
		wantEnabled           bool
	}{
		{name: "empty", wantEnabled: true},
		{name: "ordinary app", app: "push", wantEnabled: true},
		{name: "legacy conflict", app: previewApp},
		{name: "database unavailable", unavailable: true},
		{name: "explicitly disabled", disabled: true, unavailable: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db, core := previewReviewIPC(t)
			if tt.app != "" {
				if err := db.Create(&ipc.Channel{ID: "camera", App: tt.app, Stream: "camera", Type: ipc.TypeRTMP}).Error; err != nil {
					t.Fatal(err)
				}
			}
			if tt.unavailable {
				sqlDB, _ := db.DB()
				_ = sqlDB.Close()
			}
			uc := &Usecase{Conf: &conf.Bootstrap{Media: conf.Media{PreviewDisabled: tt.disabled}}}
			uc.GB28181API.ipc = core
			if tt.disabled {
				// Disabled mode must not query the channel store at all.
				uc.GB28181API.ipc = ipc.Core{}
			}
			uc.initPreview()
			t.Cleanup(uc.preview.close)
			if uc.previewEnabled() != tt.wantEnabled {
				t.Fatalf("preview enabled=%v, want %v", uc.previewEnabled(), tt.wantEnabled)
			}
			if uc.Conf.Media.PreviewDisabled != tt.disabled {
				t.Fatal("conflict check changed the user's persisted configuration")
			}
		})
	}
}

type previewReviewRecording struct {
	recording.SMSProvider
	starts, stops int
}

func (p *previewReviewRecording) StartRecord(string, string, string, int) error {
	p.starts++
	return nil
}

func (p *previewReviewRecording) StopRecord(string, string) error {
	p.stops++
	return nil
}

type previewReviewRTMP struct {
	*rtmpadapter.Adapter
	missing int
}

func (p *previewReviewRTMP) OnStreamNotFound(context.Context, string, string) error {
	p.missing++
	return nil
}

func TestPreviewLegacyChannelCompatibility(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		name := "startup conflict"
		if disabled {
			name = "explicitly disabled"
		}
		t.Run(name, func(t *testing.T) {
			db, core := previewReviewIPC(t)
			ch := ipc.Channel{ID: "rmlegacy", App: previewApp, Stream: "legacy", Type: ipc.TypeRTMP}
			if err := db.Create(&ch).Error; err != nil {
				t.Fatal(err)
			}
			cfg := &conf.Bootstrap{Media: conf.Media{PreviewDisabled: disabled}}
			cfg.Server.RTMPSecret = "test-rtmp-secret"
			cfg.Server.Recording.StorageDir = t.TempDir()
			uc := &Usecase{Conf: cfg}
			uc.GB28181API.ipc = core
			uc.initPreview()
			t.Cleanup(uc.preview.close)
			if uc.previewEnabled() {
				t.Fatal("legacy namespace conflict did not disable transcoding")
			}
			if stream, key, err := uc.preparePreview(nil, ""); err != nil || stream != "" || key != "" {
				t.Fatal("disabled transcoding did not bypass preview preparation")
			}
			provider := &previewReviewRecording{}
			protocol := &previewReviewRTMP{Adapter: rtmpadapter.NewAdapter(core, cfg)}
			h := WebHookAPI{uc: uc, conf: cfg, ipcCore: core,
				log:           slog.New(slog.NewTextHandler(io.Discard, nil)),
				protocols:     map[string]ipc.Protocoler{ipc.TypeRTMP: protocol},
				recordingCore: recording.NewCore(recordingdb.NewDB(db), recording.WithConfig(&cfg.Server.Recording), recording.WithSMSProvider(provider)),
			}
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			c.Request = httptest.NewRequest(http.MethodPost, "/", nil)
			in := &onPublishInput{App: previewApp, Stream: "legacy", MediaServerID: sms.DefaultMediaServerID}
			if out, err := h.onPublish(c, in); err != nil || out.Code == 0 {
				t.Fatal("legacy publisher bypassed its original authentication")
			}
			in.Params = "sign=" + hook.MD5(cfg.Server.RTMPSecret)
			if out, err := h.onPublish(c, in); err != nil || out.Code != 0 || out.EnableMp4 != nil {
				t.Fatalf("legacy publisher treated as internal preview: %+v %v", out, err)
			}
			if out, err := h.onPlay(c, in); err != nil || out.Code != 0 {
				t.Fatalf("legacy playback denied: %+v %v", out, err)
			}
			if _, err := h.onStreamChanged(c, &onStreamChangedInput{App: previewApp, Stream: "legacy", Regist: true}); err != nil || provider.starts != 1 {
				t.Fatal("legacy channel did not start recording")
			}
			if out, err := h.onStreamNoneReader(c, &onStreamNoneReaderInput{App: previewApp, Stream: "legacy"}); err != nil || out.Close {
				t.Fatal("legacy always-record channel closed without viewers")
			}
			if _, err := h.onStreamNotFound(c, &onStreamNotFoundInput{App: previewApp, Stream: "legacy", Schema: "rtmp"}); err != nil || protocol.missing != 1 {
				t.Fatal("legacy stream-not-found callback skipped")
			}
			if _, err := h.onRecordMP4(c, &onRecordMP4Input{App: previewApp, Stream: "legacy", FilePath: cfg.Server.Recording.StorageDir + "/legacy.mp4", FileSize: 100, StartTime: time.Now().Unix(), TimeLen: 10}); err != nil {
				t.Fatal(err)
			}
			var count int64
			if err := db.Model(&recording.Recording{}).Count(&count).Error; err != nil || count != 1 {
				t.Fatal("legacy recording callback did not persist the recording")
			}
			if _, err := h.onStreamChanged(c, &onStreamChangedInput{App: previewApp, Stream: "legacy"}); err != nil || provider.stops != 1 {
				t.Fatal("legacy channel did not stop recording")
			}
			stored, err := core.GetChannel(t.Context(), ch.ID)
			if err != nil || stored.IsOnline || stored.IsPlaying {
				t.Fatal("legacy stream removal did not update the channel")
			}
		})
	}
}
