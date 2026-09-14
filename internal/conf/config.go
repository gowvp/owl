package conf

import "time"

type Bootstrap struct {
	Debug        bool   `toml:"-" json:"-"`
	BuildVersion string `toml:"-" json:"-"`
	ConfigDir    string `toml:"-" json:"-"`
	ConfigPath   string `toml:"-" json:"-"`
	// AISecret 进程内随机 UUID，用于 Python AI 回调鉴权，重启后失效，不写配置文件
	AISecret string `toml:"-" json:"-"`

	Server Server // 服务器
	Data   Data   // 数据
	Log    Log    // 日志
	Sip    SIP
	Media  Media // 媒体
}

type Server struct {
	Debug      bool
	RTMPSecret string `comment:"rtmp 推流秘钥"`

	Username string `comment:"登录用户名"`
	Password string `comment:"登录密码"`

	AI        ServerAI        `comment:"ai 分析服务"`
	HTTP      ServerHTTP      `comment:"对外提供的服务，建议由 nginx 代理"` // HTTP服务器
	Recording ServerRecording `comment:"录像配置"`
	Webhook   ServerWebhook   `comment:"告警 webhook 推送与接收配置"`
}

// ServerWebhook webhook 推送与接收配置
type ServerWebhook struct {
	// Targets 推送目标 URL 数组，secret 直接内嵌于 URL query 参数
	// 例如: ["http://192.168.1.20:15123/webhook/event?secret=abc123"]
	Targets    []string `comment:"推送目标 URL 数组，secret 直接内嵌于 URL query 参数，如 http://host/webhook/event?secret=xxx"`
	MaxRetry   int      `comment:"推送最大重试次数，0=内置默认3次"`
	BufferSize int      `comment:"每个目标的 channel 缓冲队列大小，0=内置默认64，满队列时新事件被丢弃并记录警告"`
	// RecvSecret 本节点接收 webhook 时校验的密钥，首次启动若为空则自动生成随机值并持久化
	RecvSecret string `comment:"本节点接收 webhook 事件时校验的密钥，发送方在 URL query 参数 secret 中携带此值"`
}

// ServerRecording 录像配置，控制流媒体录制行为和存储策略
// 默认所有录制均开启，通过 Disabled 字段关闭特定类型的录制
type ServerRecording struct {
	Disabled           bool    `comment:"是否禁用录制（全局开关，true=禁用）"`
	StorageDir         string  `comment:"录像存储根目录（相对于工作目录）"`
	RetainDays         int     `comment:"录像保留天数（超过则清理）"`
	DiskUsageThreshold float64 `comment:"磁盘使用率阈值（百分比），超过则触发循环覆盖"`
	SegmentSeconds     int     `comment:"MP4 切片时长（秒）"`
}

type ServerAI struct {
	Disabled   bool `comment:"是否禁用 ai 分析服务"`
	RetainDays int  `comment:"保留天数"`
	// 全局默认分析间隔（秒）。0=内置默认5秒；0.5=每秒2张；1=每秒1张；5=每5秒1张；30=每30秒1张
	AnalysisInterval float32 `comment:"全局默认分析间隔（秒）"`
	// 告警冷却时间（秒）。同一目标在冷却期内不重复告警。0=内置默认30秒；10=10秒；60=1分钟
	AlertCooldownSeconds float32 `comment:"告警冷却时间（秒）"`
}

type ServerHTTP struct {
	Port      int         `comment:"http 端口"`                // 服务器端口号
	Timeout   Duration    `comment:"请求超时时间"`                 // 请求超时时间
	JwtSecret string      `comment:"jwt 秘钥，空串时，每次启动程序将随机赋值"` // JWT密钥
	APISecret string      `comment:"永久 API 秘钥，替代 JWT 鉴权，仅支持数字/大小写字母/下划线且最长32位"`
	PProf     ServerPPROF // Pprof配置
	AuthURL   string      `comment:"第三方认证服务地址，空串则不启用，post 请求返回 200 表示认证通过，填写本服务 /health 表示免鉴权但不安全!"`
}

// ServerPPROF 结构体，包含 Enabled 和 AccessIps 两个字段
type ServerPPROF struct {
	Enabled   bool     `comment:"是否启用 pprof, 建议设置为 true"`  // 是否启用
	AccessIps []string `comment:"访问白名单" json:"access_ips"` // 允许访问的IP地址列表
}

// Data 结构体，包含 Database 和 Redis 两个字段
type Data struct {
	// Database 数据库
	Database Database `comment:"数据库支持 sqlite/postgres/mysql, 使用 sqlite 时 dsn 应当填写文件存储路径"`
	// Redis Redis数据库
	// Redis DataRedis
}

// Database 结构体，包含 Dsn、MaxIdleConns、MaxOpenConns、ConnMaxLifetime 和 SlowThreshold 五个字段
type Database struct {
	Dsn             string   // 数据源名称
	MaxIdleConns    int32    // 最大空闲连接数
	MaxOpenConns    int32    // 最大打开连接数
	ConnMaxLifetime Duration // 连接最大生命周期
	SlowThreshold   Duration // 慢查询阈值
}

// Log 结构体，包含 Dir、Level、MaxAge、RotationTime 和 RotationSize 五个字段
type Log struct {
	Dir     string   `comment:"日志存储目录，不能使用特殊符号"`
	Level   string   `comment:"记录级别 debug/info/warn/error"`
	MaxAge  Duration `comment:"弃用，请使用 MaxDays 替代"`
	MaxDays int      `comment:"保留日志天数，超过时间自动删除"`
	MaxSize int      `comment:"日志文件最大大小(MB)"`

	RotationTime Duration `comment:"多久时间，分割一个新的日志文件"`
}

type SIP struct {
	Host     string `comment:"对设备宣告的本机地址(可选), 为空时按连接来源自动探测, 探测不可达时回退到 Media.SDPIP" json:"host"`
	Port     int    `comment:"服务监听的 TCP/UDP 端口号" json:"port"`
	ID       string `comment:"GB/T 28181 20 位国标 ID" json:"id"`
	Password string `comment:"全局注册密码，每个设备可单独设置，空串则无限制接入" json:"password"`
}

// GetDomain 从 ID 前 10 位解析国标域
// 为什么: GB/T28181 ID 格式为 "行政区划(8) + 行业编码(2) + 类型编码(3) + 序号(7)", 前 10 位即域,
// 与 ID 冗余存在易导致配置不一致从而设备注册失败, 故仅保留 ID, 域运行时派生。
func (s *SIP) GetDomain() string {
	if len(s.ID) >= 10 {
		return s.ID[:10]
	}
	return s.ID
}

type Media struct {
	IP           string `comment:"媒体服务器 IP"`
	HTTPPort     int    `comment:"媒体服务器 HTTP 端口"`
	Secret       string `comment:"媒体服务器密钥"`
	Type         string `comment:"媒体服务器类型 zlm/lalmax"`
	WebHookIP    string `comment:"用于流媒体 webhook 回调"`
	RTPPortRange string `comment:"媒体服务器 RTP 端口范围"`
	SDPIP        string `comment:"媒体服务器 SDP IP"`
}

type Duration time.Duration

func (d *Duration) UnmarshalText(b []byte) error {
	x, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	*d = Duration(x)
	return nil
}

func (d Duration) MarshalText() ([]byte, error) {
	return []byte(d.Duration().String()), nil
}

func (d *Duration) Duration() time.Duration {
	return time.Duration(*d)
}
