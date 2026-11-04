package tts

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/teamwork/digital-human/pkg/secrets"
)

const (
	CloudProviderDashscope = "dashscope"
	cloudSecretPrefix      = "enc:v1:"
)

// CloudProtocol 标识模型实际使用的 DashScope 事件协议。
type CloudProtocol string

const (
	CloudProtocolQwen3Realtime CloudProtocol = "qwen3_realtime"
	// CloudProtocolQwenAudio 仅保留分派点；未经真实响应验证前不实现该 codec。
	CloudProtocolQwenAudio CloudProtocol = "qwen_audio"
)

// CloudConfig 是云 TTS 的运行配置；APIKey 只能来自环境、仓库外配置或 enc:v1 密文。
type CloudConfig struct {
	Provider      string        `yaml:"provider"`
	BaseURL       string        `yaml:"base_url"`
	Model         string        `yaml:"model"`
	APIKey        string        `yaml:"-"`
	Voice         string        `yaml:"voice"`
	SampleRate    int           `yaml:"sample_rate"`
	Timeout       time.Duration `yaml:"timeout"`
	ProbeCacheTTL time.Duration `yaml:"probe_cache_ttl"`
}

// CloudError 提供稳定错误码和中文可读文案。
type CloudError struct {
	Code    string
	Message string
	cause   error
}

func (e *CloudError) Error() string {
	if e.cause == nil {
		return e.Message
	}
	return fmt.Sprintf("%s: %v", e.Message, e.cause)
}

func (e *CloudError) Unwrap() error { return e.cause }

func newCloudError(code, message string, cause error) *CloudError {
	return &CloudError{Code: code, Message: message, cause: cause}
}

// ErrorCode 返回稳定的云 TTS 错误码。
func ErrorCode(err error) string {
	var cloudErr *CloudError
	if errors.As(err, &cloudErr) {
		return cloudErr.Code
	}
	return ""
}

// ProtocolForModel 只识别协议，不为未验证的 Qwen-Audio 提供实现。
func ProtocolForModel(model string) (CloudProtocol, error) {
	switch model {
	case "qwen3-tts-flash-realtime":
		return CloudProtocolQwen3Realtime, nil
	case "qwen-audio-3.1-tts-flash":
		return CloudProtocolQwenAudio, nil
	default:
		return "", newCloudError("cloud_tts_protocol_unimplemented", "云 TTS 模型协议尚未实现", nil)
	}
}

// Normalize 应用默认值并拒绝不完整或无法选择协议的配置。
func (c CloudConfig) Normalize() (CloudConfig, error) {
	c.Provider = strings.ToLower(strings.TrimSpace(c.Provider))
	c.BaseURL = strings.TrimSpace(c.BaseURL)
	c.Model = strings.TrimSpace(c.Model)
	c.APIKey = strings.TrimSpace(c.APIKey)
	c.Voice = strings.TrimSpace(c.Voice)
	if c.Provider == "" {
		c.Provider = CloudProviderDashscope
	}
	if c.SampleRate == 0 {
		c.SampleRate = 24000
	}
	if c.Timeout == 0 {
		c.Timeout = 30 * time.Second
	}
	if c.ProbeCacheTTL == 0 {
		c.ProbeCacheTTL = 30 * time.Second
	}
	if c.BaseURL == "" || c.APIKey == "" || c.Model == "" || c.Voice == "" {
		return c, newCloudError("cloud_tts_unconfigured", "云 TTS 配置不完整", nil)
	}
	if c.Provider != CloudProviderDashscope {
		return c, newCloudError("cloud_tts_unconfigured", "云 TTS provider 不受支持", nil)
	}
	u, err := url.Parse(c.BaseURL)
	if err != nil || u.Host == "" || (u.Scheme != "wss" && u.Scheme != "ws") || u.RawQuery != "" || u.Fragment != "" {
		return c, newCloudError("cloud_tts_unconfigured", "云 TTS base_url 必须是有效的 WS 地址", err)
	}
	switch c.SampleRate {
	case 8000, 16000, 22050, 24000, 44100, 48000:
	default:
		return c, newCloudError("cloud_tts_unconfigured", "云 TTS sample_rate 不受支持", nil)
	}
	if c.Timeout < 0 || c.ProbeCacheTTL < 0 {
		return c, newCloudError("cloud_tts_unconfigured", "云 TTS 超时和探测缓存必须大于 0", nil)
	}
	if _, err := ProtocolForModel(c.Model); err != nil {
		return c, err
	}
	if _, err := c.ResolveAPIKey(); err != nil {
		return c, err
	}
	return c, nil
}

// Capability 在配置不可用时返回 unconfigured；配置存在不代表真实可用。
func (c CloudConfig) Capability() CapabilityState {
	if _, err := c.Normalize(); err != nil {
		return CapabilityUnconfigured
	}
	return CapabilityUnknown
}

// ResolveAPIKey 解密 enc:v1 凭据；普通字符串视为仓库外注入的运行凭据。
func (c CloudConfig) ResolveAPIKey() (string, error) {
	return c.resolveAPIKey(secrets.Decrypt)
}

func (c CloudConfig) resolveAPIKey(decrypt func([]byte) ([]byte, error)) (string, error) {
	if !strings.HasPrefix(c.APIKey, cloudSecretPrefix) {
		if strings.TrimSpace(c.APIKey) == "" {
			return "", newCloudError("cloud_tts_unconfigured", "云 TTS API Key 未配置", nil)
		}
		return c.APIKey, nil
	}
	cipher, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(c.APIKey, cloudSecretPrefix))
	if err != nil {
		return "", newCloudError("cloud_tts_unconfigured", "云 TTS API Key 密文无效", err)
	}
	plain, err := decrypt(cipher)
	if err != nil {
		return "", newCloudError("cloud_tts_unconfigured", "云 TTS API Key 解密失败", err)
	}
	if len(plain) == 0 {
		return "", newCloudError("cloud_tts_unconfigured", "云 TTS API Key 为空", nil)
	}
	return string(plain), nil
}

// Identity 返回不含明文 Key 的能力缓存身份；任何影响真实能力的配置变化都会改变它。
func (c CloudConfig) Identity() (string, error) {
	normalized, err := c.Normalize()
	if err != nil {
		return "", err
	}
	key, err := normalized.ResolveAPIKey()
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(strings.Join([]string{
		normalized.Provider,
		normalized.BaseURL,
		normalized.Model,
		normalized.Voice,
		fmt.Sprint(normalized.SampleRate),
		key,
	}, "\x00")))
	return hex.EncodeToString(sum[:]), nil
}
