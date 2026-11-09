package sms

import (
	"context"
	"path/filepath"
	"sync"
	"testing"

	"github.com/gowvp/owl/internal/conf"
	"github.com/ixugo/goddd/pkg/web"
)

type runtimeMediaStore struct {
	MediaServerStorer
	values []*MediaServer
}

func (s runtimeMediaStore) GetByID(_ context.Context, id string) (*MediaServer, error) {
	for _, item := range s.values {
		if item.ID == id {
			return item, nil
		}
	}
	return nil, nil
}
func (s runtimeMediaStore) List(context.Context, *FindMediaServerInput) ([]*MediaServer, int64, error) {
	return append([]*MediaServer(nil), s.values...), int64(len(s.values)), nil
}

type runtimeRootStore struct {
	Storer
	media runtimeMediaStore
}

func (s runtimeRootStore) MediaServer() MediaServerStorer { return s.media }

func TestDefaultMediaRuntimeReceiveAddress(t *testing.T) {
	cfg := &conf.Bootstrap{ConfigPath: filepath.Join(t.TempDir(), "config.toml"), Media: conf.Media{SDPIP: "192.0.2.1"}}
	original := &MediaServer{ID: DefaultMediaServerID, IP: "127.0.0.1", SDPIP: "stale.example", Secret: "unchanged", Ports: MediaServerPorts{RTSP: 18554}}
	remote := &MediaServer{ID: "remote", IP: "192.0.2.50", SDPIP: "remote.example"}
	store := runtimeRootStore{media: runtimeMediaStore{values: []*MediaServer{original, remote}}}
	core := Core{storer: store, NodeManager: &NodeManager{config: cfg}}
	assert := func(want string) {
		t.Helper()
		one, err := core.GetMediaServer(t.Context(), DefaultMediaServerID)
		if err != nil {
			t.Fatal(err)
		}
		def, err := core.GetDefaultMediaServer()
		if err != nil {
			t.Fatal(err)
		}
		list, _, err := core.ListMediaServers(t.Context(), &FindMediaServerInput{PagerFilter: web.NewPagerFilterMaxSize()})
		if err != nil {
			t.Fatal(err)
		}
		for _, got := range []*MediaServer{one, def, list[0]} {
			if got.GetSDPIP() != want || got.Secret != "unchanged" || got.Ports.RTSP != 18554 {
				t.Errorf("runtime media settings not applied: %s", got.GetSDPIP())
			}
			if got == original {
				t.Error("shared database/cache value must not be mutated")
			}
		}
		if list[1].SDPIP != "remote.example" || original.SDPIP != "stale.example" {
			t.Error("other media configuration changed")
		}
	}
	assert("192.0.2.1")
	if err := cfg.Update(func(next *conf.Bootstrap) error { next.Media.SDPIP = "198.51.100.2"; return nil }); err != nil {
		t.Fatal(err)
	}
	assert("198.51.100.2")
	cfg.ConfigPath = filepath.Join(t.TempDir(), "missing", "config.toml")
	if err := cfg.Update(func(next *conf.Bootstrap) error { next.Media.SDPIP = "198.51.100.3"; return nil }); err == nil {
		t.Fatal("expected persistence failure")
	}
	assert("198.51.100.2")
	cfg.ConfigPath = filepath.Join(t.TempDir(), "config.toml")
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 100 {
				value, err := core.GetDefaultMediaServer()
				if err != nil || (value.SDPIP != "198.51.100.2" && value.SDPIP != "198.51.100.4") {
					t.Error("invalid concurrent result")
				}
			}
		})
	}
	if err := cfg.Update(func(next *conf.Bootstrap) error { next.Media.SDPIP = "198.51.100.4"; return nil }); err != nil {
		t.Fatal(err)
	}
	wg.Wait()
	assert("198.51.100.4")
	if err := cfg.Update(func(next *conf.Bootstrap) error { next.Media.SDPIP = ""; return nil }); err != nil {
		t.Fatal(err)
	}
	assert("127.0.0.1")
}
