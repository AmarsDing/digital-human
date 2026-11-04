// Package voice 实现运营语音回合：流式 ASR → LLM → 现有 Say/TTS 出流。
// 原始 PCM 只经当前回合内存传递，不写日志、不落盘；fake 仅由显式 dev 配置装配。
package voice

import (
	"context"
	"errors"
	"time"

	"github.com/teamwork/digital-human/internal/biz/capability"
	"github.com/teamwork/digital-human/internal/biz/chat"
)

var (
	ErrTurnActive     = errors.New("voice_turn_active")
	ErrASRUnavailable = errors.New("asr_unavailable")
	ErrNotLive        = errors.New("not_live")
	ErrInvalidAudio   = errors.New("invalid_audio")
	ErrTurnClosed     = errors.New("voice_turn_closed")
)

// Capability 描述一个真实、显式 fake 或未配置的能力，未通过探测绝不报 ready。
type Capability struct {
	Source    capability.Status `json:"source"`
	Ready     bool              `json:"ready"`
	Device    string            `json:"device,omitempty"`
	Model     string            `json:"model,omitempty"`
	ErrorCode string            `json:"error_code,omitempty"`
	Message   string            `json:"message,omitempty"`
}

// Partial 仅走临时语音 WS，不进入事件历史或持久化。
type Partial struct {
	TurnID   string `json:"turn_id"`
	Revision uint64 `json:"revision"`
	Text     string `json:"text"`
}

type Result struct {
	Text string
}

// Recognizer 是可插拔流式 ASR 端口。Start 成功前不得调用 LLM/TTS。
type Recognizer interface {
	Capability(ctx context.Context) Capability
	Start(ctx context.Context, onPartial func(string)) (Recognition, error)
}

type Recognition interface {
	WritePCM(ctx context.Context, pcm []byte) error
	Finalize(ctx context.Context) (Result, error)
	Close() error
}

// Chat 复用现有对话记忆、人设、降级与流式短句合并。
type Chat interface {
	ChatWithTokenHook(ctx context.Context, key, userText string, sink chat.SentenceSink, onToken func()) (string, error)
}

// VADConfig 默认 PTT 也统计有效语音；continuous 模式用静默阈值自动 final。
type VADConfig struct {
	Silence        time.Duration
	MinSpeech      time.Duration
	MinStartSpeech time.Duration
	AbsoluteDBFS   float64
}

// Config 冻结阶段一默认决策；所有项均可由 yaml 覆盖。
type Config struct {
	ASRTimeout             time.Duration
	LLMTimeout             time.Duration
	TurnTimeout            time.Duration
	MaxAudio               time.Duration
	PartialInterval        time.Duration
	BargeIn                time.Duration
	AllowContinuous        bool
	AllowCrossOwnerPreempt bool
	InterruptSongs         bool
	PersistText            bool
	RetainAudio            bool
	VAD                    VADConfig
}

func DefaultConfig() Config {
	return Config{
		ASRTimeout: 5 * time.Second, LLMTimeout: 45 * time.Second, TurnTimeout: 90 * time.Second,
		MaxAudio: 20 * time.Second, PartialInterval: 200 * time.Millisecond,
		BargeIn: 250 * time.Millisecond, AllowContinuous: true,
		InterruptSongs: false, PersistText: true, RetainAudio: false,
		VAD: VADConfig{Silence: 600 * time.Millisecond, MinSpeech: 300 * time.Millisecond,
			MinStartSpeech: 120 * time.Millisecond, AbsoluteDBFS: -45},
	}
}

// BeginRequest 是语音 WS begin 控制帧的服务层参数。
type BeginRequest struct {
	SessionID    string
	Epoch        int64
	OwnerID      string
	ClientTurnID string
	Mode         string
	Format       string
	SampleRate   int
	Channels     int
	FrameMS      int
}
