package api

import (
	"encoding/json/v2"
	"net"
	"net/http"
	"strings"
	"unicode"

	"github.com/gin-gonic/gin"
	"github.com/gowvp/owl/internal/conf"
	"github.com/gowvp/owl/internal/core/sms"
	"github.com/ixugo/goddd/pkg/reason"
	"github.com/ixugo/goddd/pkg/web"
)

// The encrypted data contains only username and password, matching POST /login.
// Password confirmation is checked in the browser before sending any request.
type initializeInput struct {
	Data          string           `json:"data" binding:"required,max=512"`
	Media         *initializeMedia `json:"media"`
	SIP           *initializeSIP   `json:"sip"`
	ONVIFPassword *string          `json:"onvif_password" binding:"omitempty,max=128"`
}

type initializeMedia struct {
	SDPIP *string `json:"sdp_ip"`
}

type initializeSIP struct {
	Host     *string `json:"host"`
	ID       *string `json:"id"`
	Password *string `json:"password"`
	Port     *int    `json:"port"`
}

func (api UserAPI) requireInitialized(auth gin.HandlerFunc) gin.HandlerFunc {
	return func(c *gin.Context) {
		username, password := api.conf.Credentials()
		if username == "" || password == "" {
			web.AbortWithStatusJSON(c, reason.ErrConflict.WithHTTPStatus(http.StatusConflict).WithMsg("请先初始化账号"))
			return
		}
		c.Set("configured_username", username)
		auth(c)
	}
}

// initialize 原子保存首次初始化配置，并在 ZLM 的 ICE 地址需要重新下发时提示重启。
func (api UserAPI) initialize(_ *gin.Context, in *initializeInput) (gin.H, error) {
	// Early rejection avoids decrypting requests after setup. The decisive check
	// is repeated under the configuration write lock below.
	if !api.conf.NeedsInit() {
		return nil, alreadyInitialized()
	}
	body, err := api.secret.Decrypt(in.Data)
	if err != nil {
		return nil, reason.ErrBadRequest.WithMsg("凭据解密失败，请重试")
	}
	var credentials struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.Unmarshal(body, &credentials); err != nil {
		return nil, reason.ErrBadRequest.WithMsg("账号或密码格式错误")
	}
	credentials.Username = strings.TrimSpace(credentials.Username)
	if credentials.Username == "" || strings.TrimSpace(credentials.Password) == "" || len(credentials.Username) > 64 || len(credentials.Password) > 128 {
		return nil, reason.ErrBadRequest.WithMsg("请输入有效的账号和密码")
	}
	// Reserve space for the normal login's auto_login field as well, so every
	// accepted account can subsequently log in with the same RSA key size.
	loginBody, err := json.Marshal(struct {
		Username  string `json:"username"`
		Password  string `json:"password"`
		AutoLogin bool   `json:"auto_login"`
	}{Username: credentials.Username, Password: credentials.Password})
	if err != nil || len(loginBody) > 190 {
		return nil, reason.ErrBadRequest.WithMsg("账号和密码内容过长，请缩短后重试")
	}
	restartRequired := false
	err = api.conf.Update(func(next *conf.Bootstrap) error {
		if next.Server.Username != "" && next.Server.Password != "" {
			return alreadyInitialized()
		}
		if in.SIP != nil {
			sip := in.SIP
			if sip.Port != nil && *sip.Port != next.Sip.Port {
				return reason.ErrBadRequest.WithMsg("SIP 端口请在部署配置中设置")
			}
			if sip.Host != nil {
				host := strings.TrimSpace(*sip.Host)
				if !validSIPHost(host) {
					return reason.ErrBadRequest.WithMsg("SIP 地址应为 IP 或主机名")
				}
				next.Sip.Host = host
			}
			if sip.ID != nil {
				if len(*sip.ID) != 20 || strings.IndexFunc(*sip.ID, func(r rune) bool { return r < '0' || r > '9' }) != -1 {
					return reason.ErrBadRequest.WithMsg("国标 ID 必须为 20 位数字")
				}
				next.Sip.ID = *sip.ID
			}
			if sip.Password != nil {
				if len(*sip.Password) > 128 {
					return reason.ErrBadRequest.WithMsg("SIP 密码过长")
				}
				next.Sip.Password = *sip.Password
			}
		}
		if in.Media != nil && in.Media.SDPIP != nil {
			host := strings.TrimSpace(*in.Media.SDPIP)
			ip := net.ParseIP(host)
			if !validSIPHost(host) || (ip != nil && ip.To4() == nil) {
				return reason.ErrBadRequest.WithMsg("收流地址应为设备可达的 IPv4 或主机名")
			}
			// 返回模型的地址会即时更新，但 ZLM 的 rtc.externIP 仍需重启平台后重新下发。
			restartRequired = next.Media.Type == sms.ProtocolZLMediaKit && next.Media.SDPIP != host
			next.Media.SDPIP = host
		}
		if in.ONVIFPassword != nil && *in.ONVIFPassword != "" {
			next.ONVIF.Password = *in.ONVIFPassword
		}
		next.Server.Username, next.Server.Password = credentials.Username, credentials.Password
		return nil
	})
	if err != nil {
		return nil, err
	}
	return gin.H{"msg": "初始化成功，请使用新账号登录", "restart_required": restartRequired}, nil
}

func alreadyInitialized() error {
	return reason.ErrBadRequest.WithMsg("账号已初始化，请登录")
}

func validSIPHost(host string) bool {
	if host == "" || net.ParseIP(host) != nil {
		return true
	}
	if len(host) > 253 {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, r := range label {
			if r != '-' && !unicode.IsLetter(r) && !unicode.IsDigit(r) {
				return false
			}
		}
	}
	return true
}
