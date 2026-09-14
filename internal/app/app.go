package app

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/google/uuid"
	"github.com/gowvp/owl/internal/conf"
	"github.com/ixugo/goddd/pkg/logger"
	"github.com/ixugo/goddd/pkg/server"
	"github.com/ixugo/goddd/pkg/system"
)

func Run(bc *conf.Bootstrap) {
	if bc.Server.Recording.DiskUsageThreshold <= 0 {
		bc.Server.Recording.DiskUsageThreshold = 95.0
	}
	if bc.Server.Recording.SegmentSeconds <= 0 {
		bc.Server.Recording.SegmentSeconds = 300
	}
	if bc.Server.Recording.RetainDays <= 0 {
		bc.Server.Recording.RetainDays = 3
	}
	if bc.Server.Recording.StorageDir == "" {
		bc.Server.Recording.StorageDir = "./configs/recordings"
	}

	// 每次启动生成进程内随机 UUID，用于 Python AI 回调鉴权
	bc.AISecret = uuid.New().String()

	// RecvSecret 为空时（旧配置文件升级场景）自动生成并持久化
	if bc.Server.Webhook.RecvSecret == "" {
		bc.Server.Webhook.RecvSecret = uuid.New().String()
		if err := conf.WriteConfig(&bc, bc.ConfigPath); err != nil {
			system.ErrPrintf("WriteConfig RecvSecret err[%s]", err)
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// 以可执行文件所在目录为工作目录，防止以服务方式运行时，工作目录切换到其它位置
	bin, _ := os.Executable()
	if err := os.Chdir(filepath.Dir(bin)); err != nil {
		slog.Error("change work dir fail", "err", err)
	}

	log, clean := SetupLog(bc)
	defer clean()

	// 校验并初始化 APISecret
	initAPISecret(bc)

	go setupZLM(ctx, bc.ConfigDir)
	if !bc.Server.AI.Disabled {
		go setupAIClient(ctx, "http://127.0.0.1:15123/ai", bc.Debug)
	}

	handler, cleanUp, err := wireApp(bc, log)
	if err != nil {
		slog.Error("程序构建失败", "err", err)
		panic(err)
	}
	defer cleanUp()

	svc := server.New(handler,
		server.Port(strconv.Itoa(bc.Server.HTTP.Port)),
		server.ReadTimeout(bc.Server.HTTP.Timeout.Duration()),
		server.WriteTimeout(bc.Server.HTTP.Timeout.Duration()),
	)
	go svc.Start()
	interrupt := make(chan os.Signal, 1)
	signal.Notify(interrupt, syscall.SIGINT, syscall.SIGTERM)
	fmt.Println("服务启动成功 port:", bc.Server.HTTP.Port)

	select {
	case s := <-interrupt:
		slog.Info(`<-interrupt`, "signal", s.String())
	case err := <-svc.Notify():
		system.ErrPrintf("err: %s\n", err.Error())
		slog.Error(`<-server.Notify()`, "err", err)
	}
	cancel()
	if err := svc.Shutdown(); err != nil {
		slog.Error(`server.Shutdown()`, "err", err)
	}
}

// SetupLog 初始化日志
func SetupLog(bc *conf.Bootstrap) (*slog.Logger, func()) {
	logDir := filepath.Join(bc.ConfigDir, bc.Log.Dir)
	_ = os.MkdirAll(logDir, 0o755)
	return logger.SetupSlog(logger.Config{
		Dir:          logDir,
		MaxAge:       bc.Log.MaxDays,
		RotationTime: bc.Log.RotationTime.Duration(),
		MaxSize:      bc.Log.MaxSize,
		Debug:        bc.Debug,     // 服务级别Debug/Release
		Level:        bc.Log.Level, // 日志级别
	})
}

// isContainerEnv 兼容 Docker/containerd/Podman/Kubernetes 等多种容器运行时的检测
func isContainerEnv() bool {
	// Docker 运行时标志文件
	if _, err := os.Stat("/.dockerenv"); err == nil {
		return true
	}
	// Podman 运行时标志文件
	if _, err := os.Stat("/run/.containerenv"); err == nil {
		return true
	}
	// 通过 cgroup 信息检测 containerd / Kubernetes / Docker 等运行时
	if data, err := os.ReadFile("/proc/1/cgroup"); err == nil {
		s := string(data)
		for _, keyword := range []string{"docker", "kubepods", "containerd", "lxc", "podman"} {
			if strings.Contains(s, keyword) {
				return true
			}
		}
	}
	// cgroup v2 场景下 /proc/1/cgroup 可能只有 "0::/"，需要额外检查 mountinfo
	if data, err := os.ReadFile("/proc/self/mountinfo"); err == nil {
		s := string(data)
		for _, keyword := range []string{"/docker/", "/containerd/", "/kubepods/", "/podman/"} {
			if strings.Contains(s, keyword) {
				return true
			}
		}
	}
	return false
}

func setupZLM(ctx context.Context, dir string) {
	// 兼容多种容器运行时以及通过环境变量强制启用
	if !(isContainerEnv() || os.Getenv("NVR_STREAM") == "ZLM") {
		slog.Info("未在容器环境中运行，跳过启动 zlm")
		return
	}

	// 检查 MediaServer 文件是否存在
	mediaServerPath := filepath.Join(system.Getwd(), "MediaServer")
	if _, err := os.Stat(mediaServerPath); os.IsNotExist(err) {
		slog.Info("MediaServer 文件不存在", "path", mediaServerPath)
		return
	}

	workDir := system.Getwd()
	configPath := filepath.Join(dir, "zlm.ini")

	for {
		select {
		case <-ctx.Done():
			slog.Info("收到退出信号，停止重启 zlm")
			return
		default:
			slog.Info("MediaServer 启动中...")
			cmd := exec.CommandContext(ctx, "./MediaServer", "-s", "default.pem", "-c", configPath)
			cmd.Dir = workDir
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Env = os.Environ()

			// 启动命令 - 正常情况下会阻塞在这里
			if err := cmd.Run(); err != nil {
				slog.Error("zlm 运行失败", "err", err)
			} else {
				slog.Info("MediaServer 退出，将重新启动")
			}

			// 等待后重启（不管是正常退出还是异常退出）
			time.Sleep(2 * time.Second)
		}
	}
}

func findPythonPath() string {
	candidates := []string{
		"/opt/homebrew/Caskroom/miniconda/base/bin/python", // macOS Homebrew Miniconda
		"/opt/homebrew/anaconda3/bin/python",               // macOS Homebrew Anaconda
		"/usr/local/anaconda3/bin/python",                  // Linux Anaconda
		"/usr/local/miniconda3/bin/python",                 // Linux Miniconda
		"/root/miniconda3/bin/python",                      // Linux root Miniconda
	}

	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return "python3"
}

func setupAIClient(ctx context.Context, callback string, debug bool) {
	workDir := filepath.Join(system.Getwd(), "analysis")
	if _, err := os.Stat(filepath.Join(workDir, "main.py")); err != nil && os.IsNotExist(err) {
		slog.Info("main.py 文件不存在，跳过启动 ai", "path", filepath.Join(workDir, "main.py"))
		return
	}

	pythonPath := findPythonPath()
	slog.Info("使用 Python 路径", "path", pythonPath)

	args := []string{"main.py"}
	if callback != "" {
		args = append(args, "--callback-url", callback)
	}
	if debug {
		args = append(args, "--log-level", "DEBUG")
	}

	for range 100 {
		select {
		case <-ctx.Done():
			slog.Info("收到退出信号，停止重启 ai")
			return
		default:
			slog.Info("ai 启动中...")
			cmd := exec.CommandContext(ctx, pythonPath, args...)
			cmd.Dir = workDir
			cmd.Stdout = os.Stdout
			cmd.Stderr = os.Stderr
			cmd.Env = os.Environ()

			// 启动命令 - 正常情况下会阻塞在这里
			if err := cmd.Run(); err != nil {
				slog.Error("ai 运行失败", "err", err)
			} else {
				slog.Info("ai 退出，将重新启动")
			}

			// 等待后重启（不管是正常退出还是异常退出）
			time.Sleep(2 * time.Second)
		}
	}
}

// isValidAPISecret 校验 API 秘钥是否符合语法规则
// 为什么: 用户要求 API 秘钥只能由数字、大小写字母和下划线组成，最长 32 位且不能为空
func isValidAPISecret(secret string) bool {
	if len(secret) == 0 || len(secret) > 32 {
		return false
	}
	for i := 0; i < len(secret); i++ {
		c := secret[i]
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || c == '_' {
			continue
		}
		return false
	}
	return true
}

// initAPISecret 启动时检查并初始化 APISecret
// 为什么: 如果配置为空则自动生成 32 位无连字符 UUID 填入并持久化；若不符合语法规范，打印警告日志并重新生成且持久化
func initAPISecret(bc *conf.Bootstrap) {
	raw := bc.Server.HTTP.APISecret
	if raw == "" {
		bc.Server.HTTP.APISecret = strings.ReplaceAll(uuid.New().String(), "-", "")
		if err := conf.WriteConfig(bc, bc.ConfigPath); err != nil {
			system.ErrPrintf("WriteConfig APISecret err[%s]", err)
		}
		return
	}

	if !isValidAPISecret(raw) {
		newSecret := strings.ReplaceAll(uuid.New().String(), "-", "")
		slog.Warn("api_secret 配置不符合语法要求，已重新生成并持久化到配置文件",
			"reason", "必须由数字/大小写字母/下划线组成，最长32位",
			"invalid_secret", raw,
			"new_secret", newSecret,
		)
		bc.Server.HTTP.APISecret = newSecret
		if err := conf.WriteConfig(bc, bc.ConfigPath); err != nil {
			system.ErrPrintf("WriteConfig APISecret err[%s]", err)
		}
	}
}
