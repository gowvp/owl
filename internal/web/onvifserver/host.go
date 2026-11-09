package onvifserver

import "github.com/gowvp/owl/internal/conf"

// advertiseHost 返回供 ONVIF/RTSP 对外宣告的局域网 IP，避免使用 127.0.0.1。
//
// 优先级与 GB28181 Sip.Host 注释一致：SDPIP > Sip.Host > WebHookIP > 非回环 Media.IP。
func advertiseHost(cfg *conf.Bootstrap) string {
	if cfg == nil {
		return ""
	}
	snapshot := cfg.Snapshot()
	for _, h := range []string{
		snapshot.Media.SDPIP,
		snapshot.Sip.Host,
		snapshot.Media.WebHookIP,
		snapshot.Media.IP,
	} {
		if isLANHost(h) {
			return h
		}
	}
	return ""
}

func isLANHost(host string) bool {
	return host != "" && host != "127.0.0.1" && host != "::1" && host != "localhost"
}
