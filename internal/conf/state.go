package conf

import (
	"sync"

	"github.com/ixugo/goddd/pkg/reason"
)

// configStateMu serializes runtime configuration writes and credential reads.
// Keep it outside Bootstrap so configuration snapshots never copy a mutex.
var configStateMu sync.RWMutex

func (b *Bootstrap) Credentials() (string, string) {
	configStateMu.RLock()
	defer configStateMu.RUnlock()
	return b.Server.Username, b.Server.Password
}

func (b *Bootstrap) NeedsInit() bool {
	username, password := b.Credentials()
	return username == "" || password == ""
}

// Update validates and persists a snapshot before publishing it. Callbacks must
// inspect their snapshot rather than calling other locking configuration methods.
func (b *Bootstrap) Update(change func(*Bootstrap) error) error {
	configStateMu.Lock()
	defer configStateMu.Unlock()
	next := *b
	if err := change(&next); err != nil {
		return err
	}
	if err := WriteConfig(&next, next.ConfigPath); err != nil {
		return reason.ErrServer.WithMsg("保存配置失败，请检查配置文件权限").WithCause(err)
	}
	// Publish only editable sections; running services keep pointers into Bootstrap.
	b.Server.Username, b.Server.Password = next.Server.Username, next.Server.Password
	if b.Sip != next.Sip {
		b.Sip = next.Sip
	}
	if b.ONVIF != next.ONVIF {
		b.ONVIF = next.ONVIF
	}
	if b.Media != next.Media {
		b.Media = next.Media
	}
	return nil
}

// Snapshot is a read-consistent copy for internal use. Public endpoints must
// explicitly whitelist fields rather than serializing this value.
func (b *Bootstrap) Snapshot() Bootstrap {
	configStateMu.RLock()
	defer configStateMu.RUnlock()
	return *b
}

// MediaSnapshot 集中读取媒体配置，复用配置锁避免并发赋值，并只复制调用方需要的部分。
func (b *Bootstrap) MediaSnapshot() Media {
	configStateMu.RLock()
	defer configStateMu.RUnlock()
	return b.Media
}
