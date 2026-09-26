package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/gin-contrib/cors"
	"github.com/gin-contrib/gzip"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/gowvp/owl/internal/core/metadata/metadataapi"
	"github.com/gowvp/owl/internal/core/sms"
	"github.com/gowvp/owl/internal/web/onvifserver"
	"github.com/gowvp/owl/pkg/ota"
	"github.com/gowvp/owl/plugin/stat"
	"github.com/gowvp/owl/plugin/stat/statapi"
	"github.com/ixugo/goddd/domain/version/versionapi"
	"github.com/ixugo/goddd/pkg/system"
	"github.com/ixugo/goddd/pkg/web"
)

var startRuntime = time.Now()

func setupRouter(r *gin.Engine, uc *Usecase) {
	uc.GB28181API.uc = uc
	uc.SMSAPI.uc = uc
	uc.WebHookAPI.uc = uc
	// uc.MediaAPI.uc = uc // 已移除 push 模块
	const staticPrefix = "/web"

	go stat.LoadTop(system.Getwd())
	r.Use(
		web.Recover(),
		web.Metrics(),
		web.Logger(
			web.IgnorePrefix(staticPrefix),
			web.IgnoreMethod(http.MethodOptions),
			web.IgnorePrefix("/events/image"),
			web.IgnorePrefix("/recordings/channels"), // m3u8 播放列表
			web.IgnorePrefix("/static/recordings"),   // 录像文件
		),
		web.LoggerWithBody(
			web.DefaultBodyLimit,
			web.IgnoreBool(uc.Conf.Debug),
			web.IgnoreMethod(http.MethodOptions),
			web.IgnorePrefix("/events/image"),
			web.IgnorePrefix("/recordings/channels"),
			web.IgnorePrefix("/static/recordings"),
		),
	)

	r.Use(cors.New(cors.Config{
		AllowMethods: []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS"},
		AllowHeaders: []string{
			"Accept", "Content-Length", "Content-Type", "Range", "Accept-Language",
			"Origin", "Authorization", "Referer", "User-Agent",
			"Accept-Encoding",
			"Cache-Control", "Pragma", "X-Requested-With",
			"Sec-Fetch-Mode", "Sec-Fetch-Site", "Sec-Fetch-Dest",
			"Sec-Ch-Ua", "Sec-Ch-Ua-Mobile", "Sec-Ch-Ua-Platform",
			"Dnt", "X-Forwarded-For", "X-Forwarded-Proto", "X-Forwarded-Host",
			"X-Real-IP", "X-Request-ID", "X-Request-Start", "X-Request-Time",
		},
		AllowCredentials: true,
		MaxAge:           12 * time.Hour,
		AllowOriginFunc: func(_ string) bool {
			return true
		},
	}))

	const staticDir = "www"
	admin := r.Group(staticPrefix, gzip.Gzip(gzip.DefaultCompression))
	admin.Static("/", filepath.Join(system.Getwd(), staticDir))
	r.NoRoute(func(c *gin.Context) {
		// react-router 路由指向前端资源
		if strings.HasPrefix(c.Request.URL.Path, staticPrefix) {
			c.File(filepath.Join(system.Getwd(), staticDir, "index.html"))
			return
		}
		c.JSON(404, gin.H{"msg": "来到了无人的荒漠"})
	})
	// 访问根路径时重定向到前端资源
	r.GET("/", func(ctx *gin.Context) {
		ctx.Redirect(http.StatusPermanentRedirect, staticPrefix+"/"+"index.html")
	})
	// gin Static 对 /web（无尾斜杠）不会自动重定向，显式 301 到 /web/，
	// 与 ZLM 的 public base URL 行为一致，避免相对路径资源解析错乱
	r.GET(staticPrefix, func(ctx *gin.Context) {
		ctx.Redirect(http.StatusPermanentRedirect, staticPrefix+"/")
	})

	auth := AuthMiddleware(uc.Conf.Server.HTTP.JwtSecret, uc.Conf.Server.HTTP.APISecret, uc.Conf.Server.HTTP.AuthURL, uc.Conf.Server.Username)
	onvifserver.Register(r, uc.GB28181API.ipc, uc.SMSAPI.smsCore, uc.Conf)
	r.Any("/health", web.WrapH(uc.getHealth))
	r.GET("/app/metrics/api", web.WrapH(uc.getMetricsAPI))
	r.GET("/app/version/check", web.WrapH(uc.checkVersion))
	r.POST("/app/upgrade", auth, uc.upgradeApp)

	versionapi.Register(r, uc.Version, auth)
	statapi.Register(r)
	registerZLMWebhookAPI(r, uc.WebHookAPI)
	registerGB28181(r, uc.GB28181API, auth)
	uc.ConfigAPI.uc = uc
	registerConfig(r, uc.ConfigAPI, auth)
	registerSms(r, uc.SMSAPI, auth)
	RegisterUser(r, uc.UserAPI, auth)

	registerWS(r, uc.Conf.Server.HTTP.JwtSecret)

	// 反向代理流媒体数据
	r.Any("/proxy/sms/*path", uc.proxySMS)

	// 注册 AI 分析服务回调接口，/ai/events 是 /webhook/events 的别名
	registerAIWebhookAPI(r, uc.AIWebhookAPI, uc.WebHookAPI)
	// 启动 AI 任务同步协程，每 5 分钟检测一次数据库与内存状态差异
	uc.AIWebhookAPI.StartAISyncLoop(context.Background(), uc.SMSAPI.smsCore)
	RegisterEvent(r, uc.EventAPI, auth)
	RegisterRecording(r, uc.RecordingAPI, auth)
	metadataapi.RegisterMetadata(r, uc.MetadataAPI, auth)
}

type playOutput struct {
	App    string               `json:"app"`
	Stream string               `json:"stream"`
	Items  []sms.StreamLiveAddr `json:"items"`
}

const repoName = "gowvp/owl"

type checkVersionOutput struct {
	HasNewVersion  bool   `json:"has_new_version"`
	CurrentVersion string `json:"current_version"`
	NewVersion     string `json:"new_version"`
	Description    string `json:"description"`
}

// checkVersion 检查是否有新版本
// 通过 GitHub API 获取最新 release 信息，与当前版本比较
func (uc *Usecase) checkVersion(_ *gin.Context, _ *struct{}) (checkVersionOutput, error) {
	currentVersion := uc.Conf.BuildVersion
	newVersion, body, err := ota.GetLastVersion(repoName)
	if err != nil {
		return checkVersionOutput{}, err
	}

	hasNew := compareVersion(currentVersion, newVersion) < 0

	return checkVersionOutput{
		HasNewVersion:  hasNew,
		CurrentVersion: currentVersion,
		NewVersion:     newVersion,
		Description:    body,
	}, nil
}

// compareVersion 比较两个版本号
// 返回值: -1 表示 v1 < v2, 0 表示相等, 1 表示 v1 > v2
func compareVersion(v1, v2 string) int {
	v1 = strings.TrimPrefix(v1, "v")
	v2 = strings.TrimPrefix(v2, "v")

	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	maxLen := max(len(parts2), len(parts1))

	for i := 0; i < maxLen; i++ {
		var n1, n2 int
		if i < len(parts1) {
			fmt.Sscanf(parts1[i], "%d", &n1)
		}
		if i < len(parts2) {
			fmt.Sscanf(parts2[i], "%d", &n2)
		}
		if n1 < n2 {
			return -1
		}
		if n1 > n2 {
			return 1
		}
	}
	return 0
}

// upgradeApp 执行应用升级
// 通过 SSE 返回下载进度，下载完成后由回调决定如何升级
func (uc *Usecase) upgradeApp(c *gin.Context) {
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("Access-Control-Allow-Origin", "*")

	flusher, ok := c.Writer.(http.Flusher)
	if !ok {
		c.JSON(http.StatusInternalServerError, gin.H{"msg": "不支持 SSE"})
		return
	}

	sendEvent := func(event, data string) {
		fmt.Fprintf(c.Writer, "event: %s\ndata: %s\n\n", event, data)
		flusher.Flush()
	}

	sendEvent("start", `{"msg":"开始下载升级包"}`)

	filename := "linux_amd64"
	if runtime.GOARCH == "arm64" {
		filename = "linux_arm64"
	}

	o := ota.NewOTA(repoName, filename)
	o.SetProgressCallback(func(current, total int64) {
		percent := 0
		if total > 0 {
			percent = int(current * 100 / total)
		}
		sendEvent("progress", fmt.Sprintf(`{"current":%d,"total":%d,"percent":%d}`, current, total, percent))
	})

	if err := o.Download().Error(); err != nil {
		sendEvent("error", fmt.Sprintf(`{"msg":"%s"}`, err.Error()))
		return
	}

	sendEvent("complete", `{"msg":"下载完成，请手动重启服务"}`)
}

func (uc *Usecase) proxySMS(c *gin.Context) {
	defer func() {
		_ = recover()
	}()

	path := c.Param("path")

	// P0 白名单：禁止通过本反代访问 ZLM 管理 API（/index/api/*），仅放行 WebRTC 拉流
	// 为什么: ZLM 的 HTTP 端口同时承载拉流路径和管理 API，合法观众持 playToken 可构造
	// /proxy/sms/index/api/close_streams?app=X&stream=Y&token=T 越权调用管理接口
	if err := checkProxyPathAllowed(c, path); err != nil {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": 403, "msg": err.Error()})
		return
	}

	// 播放流鉴权：校验 token 中的 app+stream 与请求路径的包含关系
	if err := uc.verifyPlayToken(c, path); err != nil {
		c.AbortWithStatusJSON(http.StatusForbidden, gin.H{"code": 403, "msg": err.Error()})
		return
	}

	rc := http.NewResponseController(c.Writer)
	exp := time.Now().AddDate(99, 0, 0)
	_ = rc.SetReadDeadline(exp)
	_ = rc.SetWriteDeadline(exp)

	addr, err := url.JoinPath(fmt.Sprintf("http://%s:%d", uc.Conf.Media.IP, uc.Conf.Media.HTTPPort), path)
	if err != nil {
		web.Fail(c, err)
		return
	}
	fullAddr, _ := url.Parse(addr)
	c.Request.URL.Path = ""
	proxy := httputil.NewSingleHostReverseProxy(fullAddr)

	proxy.Director = func(req *http.Request) {
		req.URL.Scheme = "http"
		req.URL.Host = fmt.Sprintf("%s:%d", uc.Conf.Media.IP, uc.Conf.Media.HTTPPort)
		req.URL.Path = path
	}
	proxy.ModifyResponse = func(r *http.Response) error {
		r.Header.Del("Access-Control-Allow-Credentials")
		r.Header.Del("Access-Control-Allow-Origin")
		if r.StatusCode >= 300 && r.StatusCode < 400 {
			if l := r.Header.Get("Location"); l != "" {
				if !strings.HasPrefix(l, "http") {
					r.Header.Set("Location", "/proxy/sms/"+strings.TrimPrefix(l, "/"))
				}
			}
		}
		// P2：m3u8 响应重写，给切片 URL 拼上 token，使切片请求也受 playToken 约束
		// 为什么: 原实现豁免 .mp4/.m4s/.ts 后缀，导致 HLS 切片无鉴权永久可拉，token 过期机制失效
		if isM3U8Response(r) {
			return rewriteM3U8WithToken(r, c.Query("token"))
		}
		return nil
	}
	proxy.ServeHTTP(c.Writer, c.Request)
}

// verifyPlayToken 校验播放 token：解析 JWT，确认 token 中的 app+stream 被请求路径包含
// HLS 切片 URL 已在 m3u8 重写阶段拼上 token，故所有请求均需携带 token
func (uc *Usecase) verifyPlayToken(c *gin.Context, path string) error {
	tokenStr := c.Query("token")
	if tokenStr == "" {
		return fmt.Errorf("缺少播放鉴权 token")
	}

	secret := uc.Conf.Server.HTTP.JwtSecret + "_play"
	claims, err := web.ParseToken(tokenStr, secret)
	if err != nil {
		// 区分过期与其他无效，便于客户端提示用户重新获取播放地址
		if errors.Is(err, jwt.ErrTokenExpired) {
			return fmt.Errorf("播放 token 已过期")
		}
		return fmt.Errorf("无效的播放 token")
	}

	app, _ := claims.Data["app"].(string)
	stream, _ := claims.Data["stream"].(string)
	if stream == "" {
		return fmt.Errorf("播放 token 缺少 stream 信息")
	}

	// webrtc 的 path 是 /index/api/webrtc，stream 在 query 中，精确比对
	if path == "/index/api/webrtc" {
		if c.Query("stream") == stream && c.Query("app") == app {
			return nil
		}
		return fmt.Errorf("播放 token 与请求流不匹配")
	}

	// 精确匹配：按 path 段拆分，stream 独立成段或作为文件名前缀（FLV），app 若存在须精确等于首段
	// 为什么: strings.Contains 匹配过宽，stream=live 时 /other/live2 也通过，可跨 app 越权
	if matchStreamPath(path, app, stream) {
		return nil
	}

	return fmt.Errorf("播放 token 与请求流不匹配")
}

// matchStreamPath 精确校验 path 中的 app/stream 与 token claims 一致
// 兼容两种 driver 路径结构：
//
//	ZLM:    /{app}/{stream}.live.flv、/{app}/{stream}/hls.fmp4.m3u8、/{app}/{stream}/{date}/{seg}.mp4
//	Lalmax: /{stream}.flv、/{stream}/hls.fmp4.m3u8、/{stream}/{seg}.m4s
func matchStreamPath(path, app, stream string) bool {
	segments := strings.Split(strings.TrimPrefix(path, "/"), "/")
	if len(segments) == 0 {
		return false
	}

	// 若 token 指定 app，首段必须精确等于 app（ZLM 结构）；否则首段即 stream 相关（Lalmax 结构）
	streamSegIdx := 0
	if app != "" {
		if segments[0] != app {
			return false
		}
		streamSegIdx = 1
	}
	if streamSegIdx >= len(segments) {
		return false
	}

	streamSeg := segments[streamSegIdx]
	// stream 独立成段（HLS m3u8 及切片路径），或作为文件名前缀带后缀（FLV：{stream}.live.flv、{stream}.flv）
	return streamSeg == stream || strings.HasPrefix(streamSeg, stream+".")
}

// checkProxyPathAllowed 白名单：只放行明确的播放路径，其余一律拒绝
// 覆盖 ZLM 与 Lalmax 两种 driver 的播放 URL 规则：
//   - FLV: /{app}/{stream}.live.flv 或 /{stream}.flv
//   - HLS: /{app}/{stream}/hls.fmp4.m3u8 或 /{stream}/hls.fmp4.m3u8，及切片 *.mp4/*.m4s/*.ts
//   - WebRTC: /index/api/webrtc?type=play
//
// 为什么: 原黑名单只禁 /index/api/*，ZLM 自带录像下载 /record/* 及未来新增路径均被放行
func checkProxyPathAllowed(c *gin.Context, path string) error {
	// ZLM 自带录像下载路径，虽以 .mp4 结尾但非播放路径，明确拒绝
	if strings.HasPrefix(path, "/record/") {
		return fmt.Errorf("禁止访问录像下载路径")
	}
	// WebRTC 拉流信令特例
	if path == "/index/api/webrtc" && c.Query("type") == "play" {
		return nil
	}
	// 播放及切片路径按后缀放行
	for _, suffix := range []string{".flv", ".ts", ".m3u8", ".mp4", ".m4s"} {
		if strings.HasSuffix(path, suffix) {
			return nil
		}
	}
	return fmt.Errorf("路径不在播放白名单内")
}

// isM3U8Response 判断响应是否为 m3u8 播放列表
func isM3U8Response(r *http.Response) bool {
	ct := r.Header.Get("Content-Type")
	return strings.Contains(ct, "application/vnd.apple.mpegurl") ||
		strings.Contains(ct, "application/x-mpegurl") ||
		strings.HasSuffix(r.Request.URL.Path, ".m3u8")
}

// rewriteM3U8WithToken 改写 m3u8 body，给每个切片 URL 拼上 token
// m3u8 中以 # 开头的是标签行，其余为切片相对路径；播放器基于 m3u8 URL 解析相对路径时会丢弃原 query，
// 故必须把 token 写进每行切片 URL，否则切片请求无法通过 verifyPlayToken
func rewriteM3U8WithToken(r *http.Response, token string) error {
	if token == "" {
		return nil
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return err
	}
	r.Body.Close()

	lines := strings.Split(string(body), "\n")
	for i, line := range lines {
		trimmed := strings.TrimRight(line, "\r")
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		// 切片行：拼 token（已含 query 时用 & 连接）
		sep := "?"
		if strings.Contains(trimmed, "?") {
			sep = "&"
		}
		lines[i] = trimmed + sep + "token=" + token
	}
	newBody := strings.Join(lines, "\n")

	r.Body = io.NopCloser(strings.NewReader(newBody))
	r.ContentLength = int64(len(newBody))
	r.Header.Set("Content-Length", strconv.Itoa(len(newBody)))
	return nil
}
