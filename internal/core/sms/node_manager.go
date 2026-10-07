package sms

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/gowvp/owl/internal/conf"
	"github.com/gowvp/owl/pkg/zlm"
	"github.com/ixugo/goddd/pkg/conc"
	"github.com/ixugo/goddd/pkg/orm"
	"github.com/ixugo/goddd/pkg/reason"
	"github.com/ixugo/goddd/pkg/web"
)

const KeepaliveInterval = 2 * 15 * time.Second

// WebhookSecretLength 是启动时生成的媒体回调密钥长度。
const WebhookSecretLength = 6

// WebhookSecretParam 是媒体回调地址携带随机密钥的查询参数名。
const WebhookSecretParam = "owl_secret"

type WarpMediaServer struct {
	IsOnline      bool
	LastUpdatedAt time.Time
	Config        *MediaServer
}

type NodeManager struct {
	storer Storer

	drivers       map[string]Driver
	cacheServers  conc.Map[string, *WarpMediaServer]
	quit          chan struct{}
	webhookSecret string
}

// NewNodeManager 启动节点管理并生成本次运行共用的回调密钥，重连时保持不变。
func NewNodeManager(storer Storer) *NodeManager {
	n := NodeManager{
		storer:        storer,
		drivers:       make(map[string]Driver),
		quit:          make(chan struct{}, 1),
		webhookSecret: orm.GenerateRandomString(WebhookSecretLength),
	}
	n.RegisterDriver(ProtocolZLMediaKit, NewZLMDriver())
	n.RegisterDriver(ProtocolLalmax, NewLalmaxDriver())
	go n.tickCheck()
	return &n
}

// WebhookSecret 返回本次运行的回调密钥，未初始化时返回空值以便认证拒绝请求。
func (n *NodeManager) WebhookSecret() string {
	if n == nil {
		return ""
	}
	return n.webhookSecret
}

// webhookURL 构造带启动密钥的回调基址，所有媒体节点配置共用同一个值。
func (n *NodeManager) webhookURL(server *MediaServer, serverPort int) string {
	return fmt.Sprintf("http://%s:%d/webhook?%s=%s", server.HookIP, serverPort, WebhookSecretParam, n.webhookSecret)
}

func (n *NodeManager) RegisterDriver(name string, driver Driver) {
	n.drivers[name] = driver
}

func (n *NodeManager) getDriver(name string) (Driver, error) {
	if name == "" {
		name = "zlm"
	}
	d, ok := n.drivers[name]
	if !ok {
		return nil, fmt.Errorf("driver [%s] not found", name)
	}
	return d, nil
}

func (n *NodeManager) Close() {
	close(n.quit)
}

// tickCheck 定时检查服务是否离线
func (n *NodeManager) tickCheck() {
	ticker := time.NewTicker(3 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-n.quit:
			return
		case <-ticker.C:
			n.cacheServers.Range(func(_ string, ms *WarpMediaServer) bool {
				if time.Since(ms.LastUpdatedAt) < KeepaliveInterval {
					ms.IsOnline = true
					return true
				}

				// 尝试主动探测
				if ms.Config != nil {
					driver, err := n.getDriver(ms.Config.Type)
					if err == nil {
						ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
						if err := driver.Ping(ctx, ms.Config); err == nil {
							ms.LastUpdatedAt = time.Now()
							ms.IsOnline = true
							cancel()
							return true
						}
						cancel()
					}
				}

				ms.IsOnline = false
				return true
			})
		}
	}
}

// 读取 config.ini 文件，通过正则表达式，获取 secret 的值
func getSecret(configDir string) (string, error) {
	for _, file := range []string{"zlm.ini", "config.ini"} {
		content, err := os.ReadFile(filepath.Join(configDir, file))
		if err != nil {
			continue
		}
		re := regexp.MustCompile(`secret=(\w+)`)
		matches := re.FindStringSubmatch(string(content))
		if len(matches) < 2 {
			continue
		}
		return matches[1], nil
	}
	return "", fmt.Errorf("unknow")
}

// TODO: 发现配置会导致程序延迟 1~2s 才能启动
func setupSecret(bc *conf.Bootstrap) {
	// 六六大顺
	for range 6 {
		secret, err := getSecret(bc.ConfigDir)
		if err == nil {
			slog.Info("发现 zlm 配置，已赋值，未回写配置文件", "secret", secret)
			bc.Media.Secret = secret
			return
		}
		time.Sleep(200 * time.Millisecond)
		continue
	}
	slog.Warn("未发现 zlm 配置，请手动配置 zlm secret")
}

func (n *NodeManager) Run(bc *conf.Bootstrap, serverPort int) error {
	ctx := context.Background()
	setupSecret(bc)
	cfg := bc.Media
	setValueFn := func(ms *MediaServer) {
		ms.ID = DefaultMediaServerID
		ms.IP = cfg.IP
		ms.Ports.HTTP = cfg.HTTPPort
		ms.Secret = cfg.Secret
		ms.Type = cfg.Type
		// TODO: 应该读取环境变量
		if ms.Type == "" {
			ms.Type = ProtocolZLMediaKit
		}
		ms.Status = false
		ms.RTPPortRange = cfg.RTPPortRange
		ms.HookIP = cfg.WebHookIP
		ms.SDPIP = cfg.SDPIP
	}

	ms := MediaServer{ID: DefaultMediaServerID}
	if err := n.storer.MediaServer().Update(ctx, &ms, func(b *MediaServer) error {
		setValueFn(b)
		return nil
	}); err != nil {
		if !orm.IsErrRecordNotFound(err) {
			return err
		}
		ms = MediaServer{}
		setValueFn(&ms)
		if err := n.storer.MediaServer().Create(ctx, &ms); err != nil {
			return err
		}
	}

	mediaServers, _, err := n.listMediaServers(ctx, &FindMediaServerInput{
		PagerFilter: web.NewPagerFilterMaxSize(),
	})
	if err != nil {
		return err
	}

	for _, ms := range mediaServers {
		go func(ms *MediaServer) {
			if err := n.connection(ms, serverPort); err != nil {
				slog.Error("Connect media server failed", "id", ms.ID, "err", err)
			}
		}(ms)
	}

	return nil
}

// connection 连接媒体节点并下发包含本次运行密钥的回调配置。
func (n *NodeManager) connection(server *MediaServer, serverPort int) error {
	n.cacheServers.Store(server.ID, &WarpMediaServer{
		LastUpdatedAt: time.Now(),
		Config:        server,
	})

	driver, err := n.getDriver(server.Type)
	if err != nil {
		slog.Error("获取驱动失败", "type", server.Type, "err", err)
		return err
	}

	log := slog.With("id", server.ID, "type", server.Type)
	log.Info("MediaServer 连接中...")

	ctx := context.Background()
	if err := n.connectWithRetry(ctx, driver, server, log); err != nil {
		return err
	}
	log.Info("MediaServer 连接成功")

	ms2 := MediaServer{ID: server.ID}
	if err := n.storer.MediaServer().Update(ctx, &ms2, func(b *MediaServer) error {
		b.Ports = server.Ports
		b.HookAliveInterval = server.HookAliveInterval
		b.Status = server.Status
		return nil
	}); err != nil {
		panic(fmt.Errorf("保存 MediaServer 失败 %w", err))
	}

	log.Info("MediaServer 配置设置...")
	hookPrefix := n.webhookURL(server, serverPort)
	if err := driver.Setup(ctx, server, hookPrefix); err != nil {
		log.Error("MediaServer 配置设置失败", "err", err)
		return err
	}

	return nil
}

const (
	// connectMaxRetries 启动时连接流媒体服务器的最大重试次数，
	// 同容器部署时 ZLM 可能晚于 gowvp 就绪，需等待。
	connectMaxRetries = 30
	// connectRetryInterval 重试间隔，30 次 × 1s = 最多等 30s。
	connectRetryInterval = time.Second
)

// connectWithRetry 带重试的 Connect，应对 ZLM 晚于 gowvp 就绪的启动时序。
func (n *NodeManager) connectWithRetry(ctx context.Context, driver Driver, server *MediaServer, log *slog.Logger) error {
	var lastErr error
	for i := range connectMaxRetries {
		select {
		case <-n.quit:
			return fmt.Errorf("node manager closed")
		case <-ctx.Done():
			return ctx.Err()
		default:
		}

		if lastErr = driver.Connect(ctx, server); lastErr == nil {
			return nil
		}

		if i == 0 {
			log.Warn("MediaServer 尚未就绪，等待重试", "err", lastErr)
		}
		time.Sleep(connectRetryInterval)
	}
	log.Error("MediaServer 连接失败，已耗尽重试", "attempts", connectMaxRetries, "err", lastErr)
	return lastErr
}

func (n *NodeManager) Keepalive(serverID string) {
	value, ok := n.cacheServers.Load(serverID)
	if !ok {
		return
	}
	value.LastUpdatedAt = time.Now()
}

func (n *NodeManager) IsOnline(serverID string) bool {
	value, ok := n.cacheServers.Load(serverID)
	if !ok {
		return false
	}
	return value.IsOnline
}

// listMediaServers Paginated search
func (n *NodeManager) listMediaServers(ctx context.Context, in *FindMediaServerInput) ([]*MediaServer, int64, error) {
	items, total, err := n.storer.MediaServer().List(ctx, in)
	if err != nil {
		return nil, 0, reason.ErrDB.Withf(`List err[%s]`, err.Error())
	}
	return items, total, nil
}

// OpenRTPServer 开启RTP服务器
func (n *NodeManager) OpenRTPServer(server *MediaServer, in zlm.OpenRTPServerRequest) (*zlm.OpenRTPServerResponse, error) {
	driver, err := n.getDriver(server.Type)
	if err != nil {
		return nil, err
	}
	return driver.OpenRTPServer(context.Background(), server, &in)
}

// CloseRTPServer 关闭RTP服务器
func (n *NodeManager) CloseRTPServer(server *MediaServer, in zlm.CloseRTPServerRequest) (*zlm.CloseRTPServerResponse, error) {
	driver, err := n.getDriver(server.Type)
	if err != nil {
		return nil, err
	}
	return driver.CloseRTPServer(context.Background(), server, &in)
}

// CloseStreams 关闭指定流
func (n *NodeManager) CloseStreams(server *MediaServer, in zlm.CloseStreamsRequest) (*zlm.CloseStreamsResponse, error) {
	driver, err := n.getDriver(server.Type)
	if err != nil {
		return nil, err
	}
	return driver.CloseStreams(context.Background(), server, &in)
}

// CreateStreamProxy 添加流代理
func (n *NodeManager) CreateStreamProxy(server *MediaServer, in AddStreamProxyRequest) (*zlm.AddStreamProxyResponse, error) {
	driver, err := n.getDriver(server.Type)
	if err != nil {
		return nil, err
	}
	return driver.AddStreamProxy(context.Background(), server, &in)
}

func (n *NodeManager) GetSnapshot(server *MediaServer, in GetSnapRequest) ([]byte, error) {
	driver, err := n.getDriver(server.Type)
	if err != nil {
		return nil, err
	}
	return driver.GetSnapshot(context.Background(), server, &in)
}

func (n *NodeManager) GetStreamLiveAddr(server *MediaServer, httpPrefix, host, app, stream, token string) StreamLiveAddr {
	driver, err := n.getDriver(server.Type)
	if err != nil {
		return StreamLiveAddr{Label: err.Error()}
	}
	return driver.GetStreamLiveAddr(context.Background(), server, httpPrefix, host, app, stream, token)
}

// GetMediaInfo 获取指定流的详细音视频轨道信息
func (n *NodeManager) GetMediaInfo(server *MediaServer, app, stream string) ([]zlm.MediaItem, error) {
	driver, err := n.getDriver(server.Type)
	if err != nil {
		return nil, err
	}
	return driver.GetMediaInfo(context.Background(), server, app, stream)
}

// StartRecord 开始录制指定流
func (n *NodeManager) StartRecord(server *MediaServer, in zlm.StartRecordRequest) (*zlm.StartRecordResponse, error) {
	driver, err := n.getDriver(server.Type)
	if err != nil {
		return nil, err
	}
	return driver.StartRecord(context.Background(), server, &in)
}

// StopRecord 停止录制指定流
func (n *NodeManager) StopRecord(server *MediaServer, in zlm.StopRecordRequest) (*zlm.StopRecordResponse, error) {
	driver, err := n.getDriver(server.Type)
	if err != nil {
		return nil, err
	}
	return driver.StopRecord(context.Background(), server, &in)
}

// GetMediaList 批量获取所有在线流列表（含录制状态）
func (n *NodeManager) GetMediaList(server *MediaServer) (*zlm.GetMediaListResponse, error) {
	driver, err := n.getDriver(server.Type)
	if err != nil {
		return nil, err
	}
	return driver.GetMediaList(context.Background(), server)
}
