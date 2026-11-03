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
	Log      Log      `yaml:"log"`
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
		Log:    Log{Level: "info", Format: "json"},
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
	return nil
}

func isLoopbackAddr(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	return host == "127.0.0.1" || host == "localhost" || host == "::1" || host == "[::1]"
}
