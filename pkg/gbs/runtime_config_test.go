package gbs

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/gowvp/owl/internal/conf"
	"github.com/gowvp/owl/internal/core/ipc"
	"github.com/gowvp/owl/pkg/gbs/sip"
	"github.com/ixugo/goddd/domain/uniqueid"
)

func runtimeConfig(t *testing.T) *conf.Bootstrap {
	t.Helper()
	return &conf.Bootstrap{ConfigPath: filepath.Join(t.TempDir(), "config.toml"), Sip: conf.SIP{Host: "192.0.2.1", Port: 5060, ID: "34020000002000000001", Password: "old-sip"}}
}

func TestSIPRuntimeIdentity(t *testing.T) {
	cfg := runtimeConfig(t)
	api := &GB28181API{cfg: cfg}
	server := sip.NewServer(sipFrom(api.sipConfig()))
	defer server.Close()
	owner := &Server{Server: server, gb: api}
	server.SetFromProvider(func() *sip.Address { return owner.addressFor(api.sipConfig()) })
	before := server.From()
	if err := cfg.Update(func(next *conf.Bootstrap) error {
		next.Sip.Host, next.Sip.ID, next.Sip.Password = "192.0.2.2", "44123456782000000002", "new-sip"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	after := server.From()
	if after.URI.FHost != "192.0.2.2" || after.URI.User().String() != "44123456782000000002" || after.URI.FPort.String() != "5060" {
		t.Fatalf("new SIP identity was not applied: %s", after.URI.String())
	}
	if before.URI.FHost != "192.0.2.1" || before.URI.User().String() != "34020000002000000001" {
		t.Fatal("an in-flight address was mutated")
	}
	if api.sipConfig().Password != "new-sip" {
		t.Fatal("registration still uses previous password")
	}
	if domain := api.sipConfig(); domain.GetDomain() != "4412345678" {
		t.Fatal("domain was not refreshed")
	}
	if ssrc := api.getSSRC(0); !strings.HasPrefix(ssrc, "023456") {
		t.Fatalf("SSRC uses old domain: %s", ssrc)
	}
	// A failed durable save must not change any subsequent request.
	cfg.ConfigPath = filepath.Join(t.TempDir(), "missing", "config.toml")
	if err := cfg.Update(func(next *conf.Bootstrap) error { next.Sip.Host = "192.0.2.3"; return nil }); err == nil {
		t.Fatal("expected persistence failure")
	}
	if server.From().URI.FHost != "192.0.2.2" {
		t.Fatal("failed save changed live identity")
	}
}

func TestSIPRuntimeConcurrentSnapshots(t *testing.T) {
	cfg := runtimeConfig(t)
	api := &GB28181API{cfg: cfg}
	server := sip.NewServer(sipFrom(api.sipConfig()))
	defer server.Close()
	owner := &Server{Server: server, gb: api}
	server.SetFromProvider(func() *sip.Address { return owner.addressFor(api.sipConfig()) })
	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() {
			for range 200 {
				current := api.sipConfig()
				if current.Host != "192.0.2.1" && current.Host != "192.0.2.2" {
					t.Error("partial configuration")
				}
				from := server.From()
				if from.URI.FHost == "192.0.2.2" && from.URI.User().String() != "44123456782000000002" {
					t.Error("mixed SIP identity")
				}
				_ = api.getSSRC(0)
			}
		})
	}
	for i := range 20 {
		if err := cfg.Update(func(next *conf.Bootstrap) error {
			if i%2 == 0 {
				next.Sip.Host, next.Sip.ID = "192.0.2.2", "44123456782000000002"
			} else {
				next.Sip.Host, next.Sip.ID = "192.0.2.1", "34020000002000000001"
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	wg.Wait()
}

type runtimeDeviceStore struct{ ipc.DeviceStorer }

func (runtimeDeviceStore) GetByDeviceID(_ context.Context, id string) (*ipc.Device, error) {
	return &ipc.Device{DeviceID: id}, nil
}

type runtimeStore struct{ ipc.Storer }

func (runtimeStore) Device() ipc.DeviceStorer { return runtimeDeviceStore{} }

type runtimeMemory struct{ MemoryStorer }

func (runtimeMemory) LoadOrStore(string, *Device)                                 {}
func (runtimeMemory) Load(string) (*Device, bool)                                 { return nil, false }
func (runtimeMemory) Change(string, func(*ipc.Device) error, func(*Device)) error { return nil }

type runtimeConn struct {
	net.Conn
	data string
}

func (c *runtimeConn) Network() string { return "tcp" }
func (c *runtimeConn) LocalAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5060}
}
func (c *runtimeConn) RemoteAddr() net.Addr {
	return &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: 5061}
}
func (c *runtimeConn) ReadFrom([]byte) (int, net.Addr, error) { panic("unexpected read") }
func (c *runtimeConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	c.data = string(p)
	return len(p), nil
}

func TestSIPRuntimeRegistrationAndOutgoingHeaders(t *testing.T) {
	cfg := runtimeConfig(t)
	api := &GB28181API{cfg: cfg, core: ipc.NewAdapter(runtimeStore{}, uniqueid.Core{})}
	transport := sip.NewServer(sipFrom(api.sipConfig()))
	defer transport.Close()
	server := &Server{Server: transport, gb: api, memoryStorer: runtimeMemory{}}
	api.svr = server
	device := sipFrom(conf.SIP{ID: "34020000001320000001", Host: "127.0.0.1", Port: 5061})
	register := func(id, password string, want int) {
		t.Helper()
		conn := &runtimeConn{}
		to := sipFrom(conf.SIP{ID: id, Host: "127.0.0.1", Port: 5060})
		headers := sip.NewHeaderBuilder().SetFrom(device).SetTo(to).SetMethod(sip.MethodRegister).AddVia(&sip.ViaHop{ProtocolName: "SIP", ProtocolVersion: "2.0", Transport: "TCP", Host: "127.0.0.1", Port: sip.NewPort(5061), Params: sip.NewParams()}).Build()
		req := sip.NewRequest("", sip.MethodRegister, to.URI, sip.DefaultSipVersion, headers, nil)
		if to, ok := req.To(); ok {
			to.Params = sip.NewParams()
		}
		req.SetSource(conn.RemoteAddr())
		req.SetDestination(conn.LocalAddr())
		req.SetConnection(conn)
		req.AppendHeader(&sip.GenericHeader{HeaderName: "Expires", Contents: "0"})
		auth := sip.AuthFromValue(fmt.Sprintf(`Digest realm="%s",nonce="test",username="%s",uri="%s"`, id[:10], device.URI.User().String(), to.URI.String()))
		auth.SetMethod(sip.MethodRegister).SetPassword(password)
		auth.CalcResponse()
		req.AppendHeader(&sip.GenericHeader{HeaderName: "Authorization", Contents: auth.String()})
		tx := sip.NewTransaction("register-runtime", conn)
		defer tx.Close()
		api.handlerRegister(&sip.Context{Request: req, Tx: tx, DeviceID: device.URI.User().String(), Source: conn.RemoteAddr(), To: device, Log: slog.Default()})
		if !strings.HasPrefix(conn.data, fmt.Sprintf("SIP/2.0 %d ", want)) {
			t.Fatalf("register status: %s", conn.data)
		}
	}
	register("34020000002000000001", "old-sip", 200)
	if err := cfg.Update(func(next *conf.Bootstrap) error {
		next.Sip.Host, next.Sip.ID, next.Sip.Password = "192.0.2.2", "44123456782000000002", "new-sip"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	register("34020000002000000001", "old-sip", 403)
	register("44123456782000000002", "old-sip", 401)
	register("44123456782000000002", "new-sip", 200)
	conn := &runtimeConn{}
	target := &Device{conn: conn, source: conn.RemoteAddr(), to: device}
	tx, err := server.wrapRequest(target, sip.MethodMessage, &sip.ContentTypeXML, []byte("<Query/>"))
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Close()
	for _, value := range []string{"From: ", "Contact: ", "44123456782000000002@192.0.2.2:5060", "Via: SIP/2.0/TCP 192.0.2.2"} {
		if !strings.Contains(conn.data, value) {
			t.Errorf("outgoing headers missing %q: %s", value, conn.data)
		}
	}
}
