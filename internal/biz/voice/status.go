package voice

import (
	"context"

	"github.com/teamwork/digital-human/internal/biz/capability"
	"github.com/teamwork/digital-human/internal/biz/live"
)

// Status 是语音能力真实边界；unknown 表示已装配但未端到端探测，不冒充 real。
type Status struct {
	Ready          bool       `json:"ready"`
	ASR            Capability `json:"asr"`
	LLM            Capability `json:"llm"`
	TTS            Capability `json:"tts"`
	Live           Capability `json:"live"`
	DefaultMode    string     `json:"default_mode"`
	Continuous     bool       `json:"continuous"`
	AudioRetained  bool       `json:"audio_retained"`
	InterruptSongs bool       `json:"interrupt_songs"`
	MaxAudioMS     int64      `json:"max_audio_ms"`
}

type StatusService struct {
	ASR           Recognizer
	LLMConfigured func(context.Context) bool
	Capabilities  func(context.Context) capability.Matrix
	Speaker       Speaker
	Config        Config
}

func (s *StatusService) Status(ctx context.Context) Status {
	asr := NewUnavailableRecognizer("asr_not_configured").Capability(ctx)
	if s.ASR != nil {
		asr = s.ASR.Capability(ctx)
	}
	llmReady := s.LLMConfigured != nil && s.LLMConfigured(ctx)
	llm := Capability{Source: capability.Unconfigured, Ready: false, ErrorCode: "llm_not_configured"}
	if llmReady {
		llm = Capability{Source: capability.Unknown, Ready: true}
	}
	tts := Capability{Source: capability.Unconfigured, Ready: false, ErrorCode: "tts_unavailable"}
	if s.Capabilities != nil {
		tts.Source = s.Capabilities(ctx).Get("tts")
		tts.Ready = tts.Source == capability.Real || tts.Source == capability.Fake
		if tts.Ready {
			tts.ErrorCode = ""
		}
	}
	liveCap := Capability{Source: capability.Unconfigured, Ready: false, ErrorCode: "not_live"}
	if s.Speaker != nil && s.Speaker.Snapshot().Phase == live.PhaseLive {
		liveCap = Capability{Source: capability.Unknown, Ready: true}
	}
	return Status{
		Ready: asr.Ready && llm.Ready && tts.Ready && liveCap.Ready,
		ASR:   asr, LLM: llm, TTS: tts, Live: liveCap,
		DefaultMode: "ptt", Continuous: s.Config.AllowContinuous,
		AudioRetained: s.Config.RetainAudio, InterruptSongs: s.Config.InterruptSongs,
		MaxAudioMS: s.Config.MaxAudio.Milliseconds(),
	}
}
