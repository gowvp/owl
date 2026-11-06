package api

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gowvp/owl/internal/conf"
)

type initializeInfo struct {
	NeedInit bool                     `json:"need_init"`
	SIP      *initializeSIPDefaults   `json:"sip,omitempty"`
	ONVIF    *initializeONVIFDefaults `json:"onvif,omitempty"`
	Media    *initializeMediaDefaults `json:"media,omitempty"`
	Firewall []initializePort         `json:"firewall,omitempty"`
	Notes    []string                 `json:"notes,omitempty"`
}

type initializeSIPDefaults struct {
	Host string `json:"host"`
	Port int    `json:"port"`
	ID   string `json:"id"`
}
type initializeONVIFDefaults struct {
	Username string `json:"username"`
}
type initializeMediaDefaults struct {
	SDPIP string `json:"sdp_ip"`
	Type  string `json:"type"`
}

type initializePort struct {
	ID          string   `json:"id"`
	Label       string   `json:"label"`
	Ports       string   `json:"ports"`
	Protocols   []string `json:"protocols"`
	Direction   string   `json:"direction"`
	Condition   string   `json:"condition"`
	Description string   `json:"description"`
}

// getInitialization only exposes setup defaults while either Web credential is
// missing. It never returns passwords, API tokens, JWT secrets or media secrets.
func (api UserAPI) getInitialization(c *gin.Context, _ *struct{}) (*initializeInfo, error) {
	c.Header("Cache-Control", "no-store")
	cfg := api.conf.Snapshot()
	out := &initializeInfo{NeedInit: cfg.Server.Username == "" || cfg.Server.Password == ""}
	if !out.NeedInit {
		return out, nil
	}
	out.SIP = &initializeSIPDefaults{Host: cfg.Sip.Host, Port: cfg.Sip.Port, ID: cfg.Sip.ID}
	out.ONVIF = &initializeONVIFDefaults{Username: cfg.ONVIF.Username}
	out.Media = &initializeMediaDefaults{SDPIP: cfg.Media.SDPIP, Type: cfg.Media.Type}
	out.Firewall = initializationPorts(cfg)
	if api.mediaPorts != nil {
		rtsp, rtmp, known := api.mediaPorts()
		for i := range out.Firewall {
			row := &out.Firewall[i]
			if row.ID == "rtsp" && known {
				row.Ports = configuredPort(rtsp)
				row.Description = "已连接默认媒体节点上报的 RTSP 端口；空值表示未启用或无效。"
			}
			if row.ID == "rtmp" && known {
				row.Ports = configuredPort(rtmp)
				row.Description = "已连接默认媒体节点上报的 RTMP 端口；空值表示未启用或无效。"
			}
		}
	}
	out.Notes = []string{
		"以下为平台配置推导的端口计划，不代表已经监听、放行或完成容器/NAT 映射；外部端口以实际部署为准。",
		"平台与媒体节点可能不是同一台机器，应在对应主机配置规则，并限制到需要访问的设备或客户端来源。",
		"本步骤仅展示说明，不修改防火墙、云安全组或任何系统权限。",
	}
	return out, nil
}

func initializationPorts(cfg conf.Bootstrap) []initializePort {
	rows := []initializePort{
		{"web", "平台 Web / API / ONVIF SOAP", configuredPort(cfg.Server.HTTP.Port), []string{"TCP"}, "客户端 / 媒体节点 → 平台", "访问网页、API、ONVIF SOAP 或远端媒体回调", "ONVIF SOAP 与网页共用平台 HTTP 端口。经 HTTPS 反代时开放反代映射端口，平台原端口可限内网。"},
		{"sip", "国标 SIP 信令", configuredPort(cfg.Sip.Port), []string{"TCP", "UDP"}, "设备 → 平台", "使用 GB28181 设备注册及信令", "平台监听 TCP 和 UDP；按设备使用的传输方式及可信来源放行。信令端口不是媒体收流端口。"},
		{"rtp", "国标 RTP 收流", "", []string{"UDP", "TCP"}, "设备 → 媒体节点", "UDP 收流或 TCP 被动收流", "按流动态分配接收端口。TCP 主动模式由媒体节点连接设备，不可当作设备入站端口。"},
		{"webrtc", "WebRTC 媒体传输", "", []string{"UDP", "TCP"}, "浏览器 → 媒体节点", "使用 WebRTC 播放", "端口无法从当前平台配置确定，请核对媒体服务实际 RTC 配置与映射，不默认要求开放 8000。"},
		{"media_api", "媒体服务 HTTP API", configuredPort(cfg.Media.HTTPPort), []string{"TCP"}, "平台 → 媒体节点", "平台管理媒体服务", "仅需允许平台访问；同机部署通常走本机网络。不要因此把媒体管理 API 对公网开放。网页代理播放仍经平台端口。"},
		{"onvif_discovery", "ONVIF 局域网发现", "3702", []string{"UDP"}, "局域网组播 ↔ 平台", "需要自动发现本机 ONVIF 服务", "WS-Discovery 使用 239.255.255.250:3702，仅用于同一局域网。云安全组开放此端口不保证跨网发现；跨网可手动添加 SOAP 地址。"},
		{"rtsp", "ONVIF / RTSP 播放", "", []string{"TCP"}, "播放器 / NVR → 媒体节点", "从本机 ONVIF 服务取流或直接 RTSP 播放", "端口由媒体服务 RTSP/RTSPS 配置决定，平台配置未提供，须现场核对。若使用 UDP RTP/RTCP，还需按媒体服务的协商端口及范围另行放行。"},
		{"rtmp", "RTMP 推流 / 播放", "", []string{"TCP"}, "推流设备 / 播放器 → 媒体节点", "仅在使用 RTMP 时", "端口由媒体服务 RTMP/RTMPS 配置决定，未在此硬编码；仅国标接入无需因此开放 RTMP。"},
		{"device_access", "主动连接设备", "", []string{"TCP", "UDP"}, "平台 / 媒体节点 → 设备", "ONVIF / RTSP 拉流或国标 TCP 主动收流", "目标端口由设备配置或会话协商决定，无统一固定范围；有出站限制时按目标设备放行，并允许相应回包。"},
	}
	mediaType := cfg.Media.Type
	if mediaType == "" {
		mediaType = "zlm"
	}
	if mediaType == "zlm" {
		// Verified ZLMediaKit defaults, matching this project's Docker Compose.
		// Connected node ports override these recommendations in GET /init.
		rows[6].Ports = "554"
		rows[6].Description = "未取得在线节点端口时采用 ZLM 默认 RTSP 端口，外部端口以实际 Compose 映射为准。"
		rows[7].Ports = "1935"
		rows[7].Description = "未取得在线节点端口时采用 ZLM 默认 RTMP 端口，外部端口以实际 Compose 映射为准。"
	}
	first, last, ok := initializationPortRange(cfg.Media.RTPPortRange)
	if !ok || (mediaType != "zlm" && mediaType != "lalmax") {
		rows[2].Description += " 当前 RTP 范围无效或媒体类型未知，需核对媒体服务配置。"
		return rows
	}
	if mediaType == "zlm" {
		// ZLMDriver.Setup reserves the first configured RTP port for RTC and sends
		// first+1..last to rtp_proxy.port_range; it also rebinds RTC after a restart.
		rows[3].Ports = strconv.Itoa(first)
		rows[3].Description = "ZLM 按平台下发配置将 RTP 范围首端口用于 WebRTC UDP/TCP；须确认媒体服务已应用配置并完成必要重启。"
		first++
	}
	rows[2].Ports = strconv.Itoa(first) + "-" + strconv.Itoa(last)
	return rows
}

func configuredPort(port int) string {
	if port < 1 || port > 65535 {
		return ""
	}
	return strconv.Itoa(port)
}

func initializationPortRange(value string) (int, int, bool) {
	left, right, ok := strings.Cut(strings.TrimSpace(value), "-")
	if !ok {
		return 0, 0, false
	}
	first, err1 := strconv.Atoi(strings.TrimSpace(left))
	last, err2 := strconv.Atoi(strings.TrimSpace(right))
	return first, last, err1 == nil && err2 == nil && first > 0 && first < last && last <= 65535
}
