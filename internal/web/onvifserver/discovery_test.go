package onvifserver

import (
	"context"
	"encoding/xml"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gowvp/owl/internal/conf"
)

const runtimeProbe = `<s:Envelope xmlns:s="http://www.w3.org/2003/05/soap-envelope" xmlns:a="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery"><s:Header><a:MessageID>uuid:probe-123</a:MessageID><a:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/Probe</a:Action></s:Header><s:Body><d:Probe/></s:Body></s:Envelope>`

func TestDiscoveryRuntimeConfiguration(t *testing.T) {
	cfg := runtimeConfig(t)
	cfg.Server.HTTP.Port = 8080
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); serveDiscovery(ctx, conn, cfg, "uuid:stable-endpoint") }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("discovery did not stop")
		}
	})
	client, err := net.DialUDP("udp4", nil, conn.LocalAddr().(*net.UDPAddr))
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	probe := func(want string) {
		t.Helper()
		if err := client.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := client.Write([]byte(runtimeProbe)); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 8192)
		n, err := client.Read(buf)
		if err != nil {
			t.Fatal(err)
		}
		verifyProbeResponse(t, buf[:n], want)
	}
	probe("192.0.2.10")
	publishRuntime(t, cfg, "new-password", "192.0.2.20")
	probe("192.0.2.20")
	// Removing SDPIP falls back to the newly published SIP host, on the same socket.
	if err := cfg.Update(func(next *conf.Bootstrap) error { next.Media.SDPIP = ""; next.Sip.Host = "192.0.2.30"; return nil }); err != nil {
		t.Fatal(err)
	}
	probe("192.0.2.30")
}

func verifyProbeResponse(t *testing.T, response []byte, host string) {
	t.Helper()
	var envelope struct {
		Header struct{ MessageID, RelatesTo, To, Action string }
		Body   struct {
			ProbeMatches struct {
				ProbeMatch struct {
					EndpointReference     struct{ Address string }
					Types, Scopes, XAddrs string
					MetadataVersion       int
				}
			}
		}
	}
	if err := xml.Unmarshal(response, &envelope); err != nil {
		t.Fatal(err)
	}
	match := envelope.Body.ProbeMatches.ProbeMatch
	if match.XAddrs != "http://"+host+":8080/onvif/device_service" || match.EndpointReference.Address != "uuid:stable-endpoint" || match.Types != "dn:NetworkVideoTransmitter" || match.MetadataVersion != 1 || !strings.Contains(match.Scopes, "onvif://www.onvif.org/name/owl") {
		t.Fatalf("invalid ProbeMatch: %s", response)
	}
	header := envelope.Header
	if header.RelatesTo != "uuid:probe-123" || !strings.HasPrefix(header.MessageID, "uuid:") || header.Action != "http://schemas.xmlsoap.org/ws/2005/04/discovery/ProbeMatches" || header.To != "http://schemas.xmlsoap.org/ws/2004/08/addressing/role/anonymous" {
		t.Fatalf("invalid WS addressing: %s", response)
	}
}

func TestDiscoveryConcurrentPublication(t *testing.T) {
	cfg := runtimeConfig(t)
	cfg.Server.HTTP.Port = 8080
	peer := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 12345}
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			for range 100 {
				response := discoveryResponse([]byte(runtimeProbe), peer, cfg, "uuid:stable-endpoint")
				host := "192.0.2.10"
				if strings.Contains(string(response), "192.0.2.20") {
					host = "192.0.2.20"
				}
				verifyProbeResponse(t, response, host)
			}
		})
	}
	for range 10 {
		publishRuntime(t, cfg, "new-password", "192.0.2.20")
		publishRuntime(t, cfg, "old-password", "192.0.2.10")
	}
	wg.Wait()
	for _, request := range []string{"garbage", `<d:Resolve xmlns:d="discovery"/>`, `<d:ProbeMatches xmlns:d="discovery"/>`} {
		if got := discoveryResponse([]byte(request), peer, cfg, "uuid:stable-endpoint"); got != nil {
			t.Fatalf("unexpected reply to %s", request)
		}
	}
}
