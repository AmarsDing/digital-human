package tts

import (
	"context"

	"github.com/teamwork/digital-human/pkg/audio"
)

// SynthAdapter 把 OmniVoice 边车 Client 适配成 biz/tts 的 Synth 接口：
// 边车返回 wav 字节，这里解出 24k/s16le/mono 裸 PCM 供播放/试听。
// wav 口径不符时 fail-closed 报错，绝不静默重采样。
type SynthAdapter struct {
	c     *Client
	voice string
}

// NewSynthAdapter voice 为空时由调用方在每次 Synth 传入 voice 参数。
func NewSynthAdapter(c *Client, voice string) *SynthAdapter {
	return &SynthAdapter{c: c, voice: voice}
}

func (a *SynthAdapter) Name() string { return "omnivoice" }

// Synth 合成并返回 24k/s16le/mono PCM。
func (a *SynthAdapter) Synth(ctx context.Context, text, voice string) ([]byte, error) {
	return a.SynthWithParams(ctx, text, voice, 0)
}

// SynthWithParams 把音色和语速一并传给真实 OmniVoice 边车。
func (a *SynthAdapter) SynthWithParams(ctx context.Context, text, voice string, speed float64) ([]byte, error) {
	if voice == "" {
		voice = a.voice
	}
	wav, err := a.c.Synthesize(ctx, JobRequest{Text: text, VoiceID: voice, Speed: speed, Format: "wav"})
	if err != nil {
		return nil, err
	}
	return audio.PCMFromWav(wav)
}
