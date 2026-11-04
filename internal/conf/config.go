package conf

import (
	"fmt"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是进程唯一配置入口；敏感值（Vidu Key / RTC secret / 采集器 secret）不写进 yaml。
type Config struct {
	Server   Server   `yaml:"server"`
	Data     Data     `yaml:"data"`
	Security Security `yaml:"security"`
	Engine   Engine   `yaml:"engine"`
	RTC      RTC      `yaml:"rtc"`
	Billing  Billing  `yaml:"billing"`
	Log      Log      `yaml:"log"`
	// Stream DZ 批推流。stream key 不落 yaml：只走 DH_STREAM_KEY / DH_RTMP_URL 或面板（DPAPI 加密文件）。
	Stream    Stream    `yaml:"stream"`
	Collector Collector `yaml:"collector"`
	Health    Health    `yaml:"health"`
	// Live 深化方案 v2 §7.6 严格 Start 配置。
	Live Live `yaml:"live"`
}

// Live 开播模式配置。
type Live struct {
	// AllowLocalPreview 默认 false；仅同时满足 engine.mode=dev 才开放本地预览。
	AllowLocalPreview bool `yaml:"allow_local_preview"`
}

// Health 探活语义配置。
type Health struct {
	// RequireDependencies 默认 false：进程可服务即 200，PG/Redis 缺失以
	// status=degraded + missing 显式标注（E9：无 PG 模式下探活不应摘除实例）；
	// true 恢复旧行为：依赖未齐 /healthz 返回 503。
	RequireDependencies bool `yaml:"require_dependencies"`
}

// Stream 推流配置；密钥类字段禁止进 yaml。
type Stream struct {
	Enabled           bool   `yaml:"enabled"`             // 默认 false；未开启时 GetPushStatus 返回 state=unconfigured
	Source            string `yaml:"source"`              // synthetic | stage（默认 synthetic）
	StageURL          string `yaml:"stage_url"`           // source=stage 时舞台页地址，默认 http://127.0.0.1:8795/stage
	AllowGPU          bool   `yaml:"allow_gpu"`           // NVENC/浏览器 GPU，默认 false（避免与 OmniVoice 抢显存）
	AllowUnpushedLive bool   `yaml:"allow_unpushed_live"` // 允许未推流本地预览，默认 false（fail-closed）
}

// Collector 弹幕采集配置；secret 走 DH_COLLECTOR_SECRET（已有）。
type Collector struct {
	Enabled bool `yaml:"enabled"` // 默认 false；未开启时面板采集状态恒 off
}

// Billing F36 额度轮询落账配置。
type Billing struct {
	// CreditsPollS Vidu credits 轮询秒数；蓝本口径 max(30, credits_poll_s)，默认 60。
	CreditsPollS int `yaml:"credits_poll_s"`
}

// RTC 凭据签发配置；默认 disabled，缺凭据时 API 明确返回 unconfigured。
type RTC struct {
	Mode       string        `yaml:"mode"` // disabled | dev | managed
	Provider   string        `yaml:"provider"`
	RoomPrefix string        `yaml:"room_prefix"`
	TokenTTL   time.Duration `yaml:"token_ttl"`
	APIKey     string        `yaml:"-"`
	APISecret  string        `yaml:"-"`
}

// Engine 选择真实引擎或显式 dev fake；默认 real，缺凭据即 fail-closed。
// 凭据只从环境变量注入，不写入 yaml。
type Engine struct {
	Mode          string `yaml:"mode"` // real | dev
	ViduKey       string `yaml:"-"`
	OmniVoiceAddr string `yaml:"-"`
	// RecordingEnabled F34 云录制开关；也可用环境变量 DH_RECORDING_ENABLED 注入。
	RecordingEnabled bool `yaml:"recording_enabled"`
	// ViduBaseURL / ViduWSURL 仅联调/验收覆盖用（默认 https://api.vidu.cn 与其 wss）。
	ViduBaseURL string `yaml:"vidu_base_url"`
	ViduWSURL   string `yaml:"vidu_ws_url"`
}

type Server struct {
	HTTPAddr        string        `yaml:"http_addr"`
	ReadTimeout     time.Duration `yaml:"read_timeout"`
	WriteTimeout    time.Duration `yaml:"write_timeout"`
	ShutdownTimeout time.Duration `yaml:"shutdown_timeout"`
	MetricsLoopback bool          `yaml:"metrics_loopback"`
}

type Data struct {
	PostgresDSN     string        `yaml:"postgres_dsn"`
	RedisAddr       string        `yaml:"redis_addr"`
	RedisPassword   string        `yaml:"redis_password"`
	ConnectTimeout  time.Duration `yaml:"connect_timeout"`
	Required        bool          `yaml:"required"` // true 时启动即失败；默认 false（M0 起可降级运行）
	EventRetentionD int           `yaml:"event_retention_days"`
}

type Security struct {
	Mode             string        `yaml:"mode"` // local | lan | collector
	AdminTokenSHA256 string        `yaml:"admin_token_sha256"`
	AllowedOrigins   []string      `yaml:"allowed_origins"`
	CollectorSecret  string        `yaml:"collector_secret"` // 也可用环境变量 DH_COLLECTOR_SECRET 注入
	TicketTTL        time.Duration `yaml:"ticket_ttl"`
}

type Log struct {
	Level  string `yaml:"level"`
	Format string `yaml:"format"` // json | text
}

// Load 读取 yaml，叠加默认值与环境变量覆盖。环境变量只用于密钥类字段。
func Load(path string) (*Config, error) {
	c := defaults()
	if path != "" {
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read config %s: %w", path, err)
		}
		if err := yaml.Unmarshal(b, c); err != nil {
			return nil, fmt.Errorf("parse config %s: %w", path, err)
		}
	}
	if v := os.Getenv("DH_COLLECTOR_SECRET"); v != "" {
		c.Security.CollectorSecret = v
	}
	if v := os.Getenv("DH_ADMIN_TOKEN_SHA256"); v != "" {
		c.Security.AdminTokenSHA256 = strings.ToLower(v)
	}
	if v := os.Getenv("DH_ENGINE_MODE"); v != "" {
		c.Engine.Mode = strings.ToLower(v)
	}
	if v := os.Getenv("DH_VIDU_KEY"); v != "" {
		c.Engine.ViduKey = v
	}
	if v := os.Getenv("DH_OMNIVOICE_ADDR"); v != "" {
		c.Engine.OmniVoiceAddr = v
	}
	if v := os.Getenv("DH_RECORDING_ENABLED"); v != "" {
		c.Engine.RecordingEnabled = v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
	}
	if v := os.Getenv("DH_VIDU_BASE_URL"); v != "" {
		c.Engine.ViduBaseURL = v
	}
	if v := os.Getenv("DH_VIDU_WS_URL"); v != "" {
		c.Engine.ViduWSURL = v
	}
	if v := os.Getenv("DH_RTC_MODE"); v != "" {
		c.RTC.Mode = strings.ToLower(v)
	}
	if v := os.Getenv("DH_RTC_PROVIDER"); v != "" {
		c.RTC.Provider = strings.ToLower(v)
	}
	if v := os.Getenv("DH_RTC_API_KEY"); v != "" {
		c.RTC.APIKey = v
	}
	if v := os.Getenv("DH_RTC_API_SECRET"); v != "" {
		c.RTC.APISecret = v
	}
	if v := os.Getenv("DH_RTC_ROOM_PREFIX"); v != "" {
		c.RTC.RoomPrefix = v
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func defaults() *Config {
	return &Config{
		Server: Server{
			HTTPAddr:        "127.0.0.1:8795",
			ReadTimeout:     15 * time.Second,
			WriteTimeout:    30 * time.Second,
			ShutdownTimeout: 5 * time.Second,
			MetricsLoopback: true,
		},
		Data: Data{
			ConnectTimeout:  5 * time.Second,
			EventRetentionD: 90,
		},
		Security: Security{
			Mode:      "local",
			TicketTTL: 60 * time.Second,
		},
		Engine: Engine{Mode: "real"},
		RTC:    RTC{Mode: "disabled", Provider: "artc", RoomPrefix: "dh-", TokenTTL: 10 * time.Minute},
		Billing: Billing{
			CreditsPollS: 60,
		},
		Log:       Log{Level: "info", Format: "json"},
		Stream:    Stream{Source: "synthetic", StageURL: "http://127.0.0.1:8795/stage"},
		Collector: Collector{},
	}
}

// Validate 拒绝危险组合：lan 模式必须配置 token hash 与 HTTPS 前置说明。
func (c *Config) Validate() error {
	switch c.Security.Mode {
	case "local":
		if !isLoopbackAddr(c.Server.HTTPAddr) {
			return fmt.Errorf("security.mode=local 要求监听 loopback，当前 %q；改用 lan 模式", c.Server.HTTPAddr)
		}
	case "lan":
		if len(c.Security.AdminTokenSHA256) != 64 {
			return fmt.Errorf("security.mode=lan 需要 64 位 admin_token_sha256")
		}
		if c.Server.HTTPAddr == "" || isLoopbackAddr(c.Server.HTTPAddr) {
			// 允许本机反向代理把外部流量转进来，但需显式配置
		}
	case "collector":
		if c.Security.CollectorSecret == "" {
			return fmt.Errorf("security.mode=collector 需要 collector_secret（环境变量 DH_COLLECTOR_SECRET）")
		}
	default:
		return fmt.Errorf("unknown security.mode %q", c.Security.Mode)
	}
	switch c.Engine.Mode {
	case "real", "dev":
	case "":
		c.Engine.Mode = "real"
	default:
		return fmt.Errorf("unknown engine.mode %q（仅支持 real|dev）", c.Engine.Mode)
	}
	switch c.RTC.Mode {
	case "disabled", "dev", "managed":
	case "":
		c.RTC.Mode = "disabled"
	default:
		return fmt.Errorf("unknown rtc.mode %q（仅支持 disabled|dev|managed）", c.RTC.Mode)
	}
	if c.RTC.Provider == "" {
		c.RTC.Provider = "artc"
	}
	if c.RTC.Provider != "artc" && c.RTC.Provider != "trtc" {
		return fmt.Errorf("unknown rtc.provider %q（仅支持 artc|trtc）", c.RTC.Provider)
	}
	switch c.Stream.Source {
	case "", "synthetic", "stage":
	default:
		return fmt.Errorf("unknown stream.source %q（仅支持 synthetic|stage）", c.Stream.Source)
	}
	return nil
}

func isLoopbackAddr(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	return host == "127.0.0.1" || host == "localhost" || host == "::1" || host == "[::1]"
}
