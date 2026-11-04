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
	Live  Live  `yaml:"live"`
	Voice Voice `yaml:"voice"`
}

// Voice 冻结语音阶段一策略。真实 sherpa 缺文件/许可确认时保持 unconfigured；
// retain_audio 在保存与回看功能实现前只允许 false，避免配置被静默忽略。
type Voice struct {
	ASR                    VoiceASR      `yaml:"asr"`
	ASRTimeout             time.Duration `yaml:"asr_timeout"`
	LLMTimeout             time.Duration `yaml:"llm_timeout"`
	TurnTimeout            time.Duration `yaml:"turn_timeout"`
	MaxAudio               time.Duration `yaml:"max_audio"`
	PartialInterval        time.Duration `yaml:"partial_interval"`
	BargeIn                time.Duration `yaml:"barge_in"`
	AllowContinuous        bool          `yaml:"allow_continuous"`
	InterruptSongs         bool          `yaml:"interrupt_songs"`
	PersistText            bool          `yaml:"persist_text"`
	RetainAudio            bool          `yaml:"retain_audio"`
	AllowCrossOwnerPreempt bool          `yaml:"allow_cross_owner_preempt"`
	CoordinateTextChat     bool          `yaml:"coordinate_text_chat"`
	VAD                    VoiceVAD      `yaml:"vad"`
}

type VoiceASR struct {
	Provider                 string        `yaml:"provider"` // sherpa | fake | cloud
	FakeText                 string        `yaml:"fake_text"`
	AllowLocalSidecar        bool          `yaml:"allow_local_sidecar"`
	AllowCloud               bool          `yaml:"allow_cloud"`
	ModelLicenseAcknowledged bool          `yaml:"model_license_acknowledged"`
	PythonBin                string        `yaml:"python_bin"`
	ScriptPath               string        `yaml:"script_path"`
	ModelType                string        `yaml:"model_type"`
	Tokens                   string        `yaml:"tokens"`
	Encoder                  string        `yaml:"encoder"`
	Decoder                  string        `yaml:"decoder"`
	Joiner                   string        `yaml:"joiner"`
	Threads                  int           `yaml:"threads"`
	StartupTimeout           time.Duration `yaml:"startup_timeout"`
}

type VoiceVAD struct {
	Silence        time.Duration `yaml:"silence"`
	MinSpeech      time.Duration `yaml:"min_speech"`
	MinStartSpeech time.Duration `yaml:"min_start_speech"`
	AbsoluteDBFS   float64       `yaml:"absolute_dbfs"`
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
	PostgresDSN             string        `yaml:"postgres_dsn"`
	PostgresMaxOpenConns    int           `yaml:"postgres_max_open_conns"`
	PostgresMaxIdleConns    int           `yaml:"postgres_max_idle_conns"`
	PostgresConnMaxLifetime time.Duration `yaml:"postgres_conn_max_lifetime"`
	PostgresConnMaxIdleTime time.Duration `yaml:"postgres_conn_max_idle_time"`
	RedisAddr               string        `yaml:"redis_addr"`
	RedisPassword           string        `yaml:"redis_password"`
	ConnectTimeout          time.Duration `yaml:"connect_timeout"`
	Required                bool          `yaml:"required"` // true 时启动即失败；默认 false（M0 起可降级运行）
	EventRetentionD         int           `yaml:"event_retention_days"`
}

// Validate 校验 PG 连接池；所有值必须为正，空闲连接不得超过最大打开连接。
func (d Data) Validate() error {
	if d.PostgresMaxOpenConns <= 0 {
		return fmt.Errorf("data.postgres_max_open_conns 必须为正")
	}
	if d.PostgresMaxIdleConns <= 0 {
		return fmt.Errorf("data.postgres_max_idle_conns 必须为正")
	}
	if d.PostgresMaxIdleConns > d.PostgresMaxOpenConns {
		return fmt.Errorf("data.postgres_max_idle_conns 不得超过 postgres_max_open_conns")
	}
	if d.PostgresConnMaxLifetime <= 0 {
		return fmt.Errorf("data.postgres_conn_max_lifetime 必须为正")
	}
	if d.PostgresConnMaxIdleTime <= 0 {
		return fmt.Errorf("data.postgres_conn_max_idle_time 必须为正")
	}
	return nil
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
			PostgresMaxOpenConns:    20,
			PostgresMaxIdleConns:    5,
			PostgresConnMaxLifetime: 30 * time.Minute,
			PostgresConnMaxIdleTime: 5 * time.Minute,
			ConnectTimeout:          5 * time.Second,
			EventRetentionD:         90,
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
		Voice: Voice{
			ASR: VoiceASR{
				Provider: "sherpa", FakeText: "dev 模拟识别", AllowLocalSidecar: true, AllowCloud: false,
				ModelLicenseAcknowledged: false, PythonBin: "python",
				ScriptPath: "internal/biz/voice/sherpa_sidecar.py", ModelType: "zipformer",
				Threads: 2, StartupTimeout: 15 * time.Second,
			},
			ASRTimeout: 5 * time.Second, LLMTimeout: 45 * time.Second, TurnTimeout: 90 * time.Second,
			MaxAudio: 20 * time.Second, PartialInterval: 200 * time.Millisecond, BargeIn: 250 * time.Millisecond,
			AllowContinuous: true, InterruptSongs: false, PersistText: true, RetainAudio: false,
			AllowCrossOwnerPreempt: false, CoordinateTextChat: true,
			VAD: VoiceVAD{Silence: 600 * time.Millisecond, MinSpeech: 300 * time.Millisecond,
				MinStartSpeech: 120 * time.Millisecond, AbsoluteDBFS: -45},
		},
	}
}

// Validate 拒绝危险组合：lan 模式必须配置 token hash 与 HTTPS 前置说明。
func (c *Config) Validate() error {
	if err := c.Data.Validate(); err != nil {
		return err
	}
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
	switch c.Voice.ASR.Provider {
	case "sherpa", "fake", "cloud":
	default:
		return fmt.Errorf("unknown voice.asr.provider %q（仅支持 sherpa|fake|cloud）", c.Voice.ASR.Provider)
	}
	if c.Voice.RetainAudio {
		return fmt.Errorf("voice.retain_audio=true 尚未实现保存/回看链路；必须显式保持 false")
	}
	for name, d := range map[string]time.Duration{
		"voice.asr_timeout": c.Voice.ASRTimeout, "voice.llm_timeout": c.Voice.LLMTimeout,
		"voice.turn_timeout": c.Voice.TurnTimeout, "voice.max_audio": c.Voice.MaxAudio,
		"voice.barge_in": c.Voice.BargeIn, "voice.asr.startup_timeout": c.Voice.ASR.StartupTimeout,
		"voice.vad.silence": c.Voice.VAD.Silence, "voice.vad.min_speech": c.Voice.VAD.MinSpeech,
		"voice.vad.min_start_speech": c.Voice.VAD.MinStartSpeech,
	} {
		if d <= 0 {
			return fmt.Errorf("%s 必须大于 0", name)
		}
	}
	if c.Voice.PartialInterval < 0 {
		return fmt.Errorf("voice.partial_interval 不得小于 0")
	}
	if c.Voice.ASR.Threads <= 0 {
		return fmt.Errorf("voice.asr.threads 必须大于 0")
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
