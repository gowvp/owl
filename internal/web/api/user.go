package api

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gowvp/owl/internal/conf"
	"github.com/ixugo/goddd/pkg/reason"
	"github.com/ixugo/goddd/pkg/web"
)

type UserAPI struct {
	conf   *conf.Bootstrap
	secret *Secret
}

type Secret struct {
	privateKey *rsa.PrivateKey
	publicKey  *rsa.PublicKey
	expiredAt  time.Time
	m          sync.RWMutex
}

// TODO: 有概率存在过期导致登录解密识别
func (s *Secret) GetOrCreatePublicKey() (*rsa.PublicKey, error) {
	s.m.RLock()
	if s.publicKey != nil && time.Now().Before(s.expiredAt) {
		s.m.RUnlock()
		return s.publicKey, nil
	}
	s.m.RUnlock()

	s.m.Lock()
	defer s.m.Unlock()

	privateKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return nil, err
	}
	s.privateKey = privateKey
	s.publicKey = &privateKey.PublicKey
	s.expiredAt = time.Now().Add(1 * time.Hour)
	return s.publicKey, nil
}

func (s *Secret) MarshalPKIXPublicKey(key *rsa.PublicKey) []byte {
	publicKeyBytes, _ := x509.MarshalPKIXPublicKey(key)
	return pem.EncodeToMemory(&pem.Block{
		Type:  "PUBLIC KEY",
		Bytes: publicKeyBytes,
	})
}

func (s *Secret) Decrypt(ciphertext string) ([]byte, error) {
	s.m.RLock()
	pri := s.privateKey
	s.m.RUnlock()
	if pri == nil {
		return nil, fmt.Errorf("请刷新页面后重试")
	}
	data, err := base64.StdEncoding.DecodeString(ciphertext)
	if err != nil {
		return nil, err
	}
	plaintext, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, pri, data, nil)
	if err != nil {
		return nil, err
	}
	return plaintext, nil
}

func NewUserAPI(conf *conf.Bootstrap) UserAPI {
	return UserAPI{
		conf:   conf,
		secret: &Secret{},
	}
}

func RegisterUser(r gin.IRouter, api UserAPI, mid ...gin.HandlerFunc) {
	r.POST("/login", web.WrapH(api.login))
	r.GET("/login/key", web.WrapH(api.getPublicKey))

	group := r.Group("/users", mid...)
	group.PUT("", web.WrapHs(api.updateCredentials, mid...)...)
}

// 登录请求结构体
type loginInput struct {
	// Username string `json:"username" binding:"required"`
	// Password string `json:"password" binding:"required"`
	Data string `json:"data" binding:"required"`
}

// 登录响应结构体
type loginOutput struct {
	Token string `json:"token"`
	User  string `json:"user"`
}

// 登录接口
func (api UserAPI) login(_ *gin.Context, in *loginInput) (*loginOutput, error) {
	body, err := api.secret.Decrypt(in.Data)
	if err != nil {
		return nil, reason.ErrServer.WithMsg(err.Error())
	}
	var credentials struct {
		Username  string `json:"username"`
		Password  string `json:"password"`
		AutoLogin bool   `json:"auto_login"`
	}
	if err := json.Unmarshal(body, &credentials); err != nil {
		return nil, reason.ErrServer.WithMsg(err.Error())
	}

	// 验证用户名和密码
	if api.conf.Server.Username == "" && api.conf.Server.Password == "" {
		api.conf.Server.Username = "admin"
		api.conf.Server.Password = "admin"
	}
	if credentials.Username != api.conf.Server.Username || credentials.Password != api.conf.Server.Password {
		return nil, reason.ErrNameOrPasswd
	}

	data := web.NewClaimsData().SetUsername(credentials.Username)

	// 勾选短期自动登录时，token 有效期延长至第 15 天凌晨 5 点（服务端本地时区），
	// 供前端免密自动登录使用；否则维持默认 3 天
	expiresAt := time.Now().Add(3 * 24 * time.Hour)
	if credentials.AutoLogin {
		day15 := time.Now().Add(15 * 24 * time.Hour)
		expiresAt = time.Date(day15.Year(), day15.Month(), day15.Day(), 5, 0, 0, 0, time.Local)
	}

	token, err := web.NewToken(data, api.conf.Server.HTTP.JwtSecret, web.WithExpiresAt(expiresAt))
	if err != nil {
		return nil, reason.ErrServer.WithMsg("生成token失败: " + err.Error())
	}

	return &loginOutput{
		Token: token,
		User:  credentials.Username,
	}, nil
}

// 修改凭据请求结构体（加密传输）
type updateCredentialsInput struct {
	Data string `json:"data" binding:"required"`
}

// 修改凭据接口，需旧密码校验通过才允许修改
func (api UserAPI) updateCredentials(_ *gin.Context, in *updateCredentialsInput) (gin.H, error) {
	body, err := api.secret.Decrypt(in.Data)
	if err != nil {
		return nil, reason.ErrServer.WithMsg(err.Error())
	}
	var credentials struct {
		OldPassword string `json:"old_password"`
		Username    string `json:"username"`
		Password    string `json:"password"`
	}
	if err := json.Unmarshal(body, &credentials); err != nil {
		return nil, reason.ErrServer.WithMsg(err.Error())
	}

	if credentials.Username == "" || credentials.Password == "" {
		return nil, reason.ErrServer.WithMsg("账号和密码不能为空")
	}

	// 校验旧密码
	currentPassword := api.conf.Server.Password
	if currentPassword == "" {
		currentPassword = "admin"
	}
	if credentials.OldPassword != currentPassword {
		return nil, reason.ErrServer.WithMsg("旧密码错误")
	}

	api.conf.Server.Username = credentials.Username
	api.conf.Server.Password = credentials.Password

	if err := conf.WriteConfig(api.conf, api.conf.ConfigPath); err != nil {
		return nil, reason.ErrServer.WithMsg("保存配置失败: " + err.Error())
	}

	return gin.H{"msg": "凭据更新成功"}, nil
}

func (api UserAPI) getPublicKey(_ *gin.Context, _ *struct{}) (gin.H, error) {
	publicKey, err := api.secret.GetOrCreatePublicKey()
	if err != nil {
		return nil, reason.ErrServer.WithMsg(err.Error())
	}
	result := api.secret.MarshalPKIXPublicKey(publicKey)
	return gin.H{"key": base64.StdEncoding.EncodeToString(result)}, nil
}
