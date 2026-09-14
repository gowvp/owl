package api

import (
	"bytes"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/ixugo/goddd/pkg/reason"
	"github.com/ixugo/goddd/pkg/web"
)

// AuthMiddleware 鉴权中间件
// 为什么: 优先通过常数时间比对静态 APISecret（替代 JWT 永久有效）；未命中时尝试 JWT 验签；皆未通过且配置了第三方 authURL 时转发鉴权
func AuthMiddleware(jwtSecret string, apiSecret string, authURL string, defaultAdmin string, handler ...web.HandlerOption) gin.HandlerFunc {
	client := http.Client{Timeout: 10 * time.Second}
	return func(c *gin.Context) {
		for _, h := range handler {
			if h(c) {
				c.Next()
				return
			}
		}

		auth := c.Request.Header.Get("Authorization")
		// header 中没有时，尝试从 query 参数中取
		if auth == "" {
			auth = c.Query("token")
		}

		// 剥离可能存在的 Bearer 前缀
		const prefix = "Bearer "
		tokenStr := auth
		hasBearerPrefix := len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix)
		if hasBearerPrefix {
			tokenStr = auth[len(prefix):]
		}

		// 1. 若配置了 APISecret，且请求凭证匹配，则按管理员级别鉴权通过
		if apiSecret != "" && tokenStr != "" && subtle.ConstantTimeCompare([]byte(tokenStr), []byte(apiSecret)) == 1 {
			c.Set(web.KeyTokenString, auth)
			admin := defaultAdmin
			if admin == "" {
				admin = "admin"
			}
			c.Set("username", admin)
			c.Set("role", "admin")
			c.Set("is_admin", true)
			c.Set("auth_type", "api_secret")
			c.Next()
			return
		}

		// 2. 若带有 Bearer 前缀，尝试走原有 JWT 验签
		if hasBearerPrefix {
			claims, err := web.ParseToken(tokenStr, jwtSecret)
			if err != nil && errors.Is(err, jwt.ErrTokenExpired) {
				web.AbortWithStatusJSON(c, reason.ErrUnauthorized.WithMsg("请重新登录"))
				return
			}
			if err == nil {
				// JWT 鉴权通过
				c.Set(web.KeyTokenString, auth)
				for k, v := range claims.Data {
					c.Set(k, v)
				}
				c.Next()
				return
			}
			// JWT 解析失败，继续尝试 authURL
		}

		// 无有效 token，检查是否配置了 authURL
		if authURL == "" {
			web.AbortWithStatusJSON(c, reason.ErrUnauthorized.WithMsg("身份验证失败"))
			return
		}

		// 尝试第三方鉴权，失败时响应已直接写给客户端
		code := forwardToAuthURL(&client, c, authURL)
		if code == http.StatusOK {
			c.Set(web.KeyTokenString, auth)
			c.Next()
			return
		}
		c.Abort()
	}
}

// forwardToAuthURL 将原始请求透传给第三方鉴权服务
// 为什么: 需要将客户端的全部请求信息（header、body）原样转发，让第三方服务自行判断鉴权
// 非 200 时直接流式写回客户端，避免缓冲响应体；返回状态码供调用方判断
func forwardToAuthURL(client *http.Client, c *gin.Context, authURL string) int {
	// 读取原始请求 body，用于透传
	var bodyBytes []byte
	if c.Request.Body != nil {
		bodyBytes, _ = io.ReadAll(c.Request.Body)
		// 恢复 body，让后续 handler 仍能读取
		c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	}

	req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodPost, authURL, bytes.NewReader(bodyBytes))
	if err != nil {
		slog.ErrorContext(c.Request.Context(), "构建 authURL 请求失败", "err", err, "authURL", authURL)
		web.Fail(c, reason.ErrServer.WithHTTPStatus(http.StatusInternalServerError).WithMsg("鉴权服务请求失败"))
		return http.StatusInternalServerError
	}

	// 透传原始请求的所有 header
	for key, values := range c.Request.Header {
		for _, v := range values {
			req.Header.Add(key, v)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		slog.ErrorContext(c.Request.Context(), "请求 authURL 失败", "err", err, "authURL", authURL)
		web.Fail(c, reason.ErrServiceUnavailable.WithHTTPStatus(http.StatusBadGateway).WithMsg("鉴权服务不可达: "+err.Error()))
		return http.StatusBadGateway
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return http.StatusOK
	}

	// 非 200，流式写回客户端，不缓冲响应体
	c.Status(resp.StatusCode)
	c.Header("Content-Type", resp.Header.Get("Content-Type"))
	io.Copy(c.Writer, resp.Body)
	return resp.StatusCode
}
