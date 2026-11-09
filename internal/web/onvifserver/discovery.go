package onvifserver

import (
	"context"
	"errors"
	"fmt"
	"html"
	"log/slog"
	"net"
	"regexp"
	"strconv"
	"strings"
	"uuid"

	"github.com/gowvp/owl/internal/conf"
)

var probeMessageID = regexp.MustCompile(`(?i)<(?:\w+:)?MessageID[^>]*>(?:uuid:)?([^<]+)</(?:\w+:)?MessageID>`)

// startDiscovery owns exactly one listener for its lifetime. The dependency's
// Discovery caches Host with no update API, so replies are built here from the
// current snapshot instead of restarting discovery after configuration changes.
func startDiscovery(ctx context.Context, cfg *conf.Bootstrap, serial string) {
	if cfg.Snapshot().Server.HTTP.Port <= 0 {
		slog.Warn("onvif ws-discovery init failed", "err", "invalid http port")
		return
	}
	conn, err := net.ListenMulticastUDP("udp4", nil, &net.UDPAddr{IP: net.IPv4(239, 255, 255, 250), Port: 3702})
	if err != nil {
		slog.Warn("onvif ws-discovery start failed", "err", err)
		return
	}
	if err := conn.SetReadBuffer(8192); err != nil {
		_ = conn.Close()
		slog.Warn("onvif ws-discovery init failed", "err", err)
		return
	}
	go serveDiscovery(ctx, conn, cfg, "uuid:"+serial)
}

// serveDiscovery takes ownership of conn. Tests use an ephemeral loopback socket;
// configuration publication never opens a new socket or starts another worker.
func serveDiscovery(ctx context.Context, conn *net.UDPConn, cfg *conf.Bootstrap, endpointID string) {
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	defer stop()
	defer conn.Close()
	buf := make([]byte, 8192)
	for {
		n, peer, err := conn.ReadFromUDP(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			slog.Warn("onvif discovery read failed", "err", err)
			return
		}
		response := discoveryResponse(buf[:n], peer, cfg, endpointID)
		if response == nil {
			continue
		}
		if _, err := conn.WriteToUDP(response, peer); err != nil {
			slog.Debug("onvif discovery reply", "err", err, "peer", peer)
		}
	}
}

func discoveryResponse(request []byte, peer *net.UDPAddr, cfg *conf.Bootstrap, endpointID string) []byte {
	body := string(request)
	// Match the dependency's Probe support, ignoring ProbeMatches to avoid loops.
	if strings.Contains(body, "ProbeMatch") || !(strings.Contains(body, "discovery/Probe") || (strings.Contains(body, "Probe") && strings.Contains(body, "discovery"))) {
		return nil
	}
	snapshot := cfg.Snapshot()
	host := advertiseHost(&snapshot)
	if host == "" {
		host = discoveryLocalHost(peer)
	}
	xaddr := "http://" + net.JoinHostPort(host, strconv.Itoa(snapshot.Server.HTTP.Port)) + "/onvif/device_service"
	relatesTo := ""
	if match := probeMessageID.FindSubmatch(request); len(match) == 2 {
		id := strings.TrimSpace(string(match[1]))
		if !strings.HasPrefix(id, "uuid:") {
			id = "uuid:" + id
		}
		relatesTo = "<wsa:RelatesTo>" + html.EscapeString(id) + "</wsa:RelatesTo>"
	}
	return []byte(fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<SOAP-ENV:Envelope xmlns:SOAP-ENV="http://www.w3.org/2003/05/soap-envelope" xmlns:wsa="http://schemas.xmlsoap.org/ws/2004/08/addressing" xmlns:d="http://schemas.xmlsoap.org/ws/2005/04/discovery" xmlns:dn="http://www.onvif.org/ver10/network/wsdl">
<SOAP-ENV:Header><wsa:MessageID>uuid:%s</wsa:MessageID>%s<wsa:To>http://schemas.xmlsoap.org/ws/2004/08/addressing/role/anonymous</wsa:To><wsa:Action>http://schemas.xmlsoap.org/ws/2005/04/discovery/ProbeMatches</wsa:Action></SOAP-ENV:Header>
<SOAP-ENV:Body><d:ProbeMatches><d:ProbeMatch><wsa:EndpointReference><wsa:Address>%s</wsa:Address></wsa:EndpointReference><d:Types>dn:NetworkVideoTransmitter</d:Types><d:Scopes>onvif://www.onvif.org/name/owl onvif://www.onvif.org/Profile/Streaming onvif://www.onvif.org/type/Network_Video_Transmitter</d:Scopes><d:XAddrs>%s</d:XAddrs><d:MetadataVersion>1</d:MetadataVersion></d:ProbeMatch></d:ProbeMatches></SOAP-ENV:Body></SOAP-ENV:Envelope>`, uuid.NewV4(), relatesTo, html.EscapeString(endpointID), html.EscapeString(xaddr)))
}

// A UDP connect only resolves the local route; it does not send a datagram.
func discoveryLocalHost(peer *net.UDPAddr) string {
	conn, err := net.DialUDP("udp4", nil, peer)
	if err != nil {
		return "127.0.0.1"
	}
	defer conn.Close()
	if addr, ok := conn.LocalAddr().(*net.UDPAddr); ok {
		return addr.IP.String()
	}
	return "127.0.0.1"
}
