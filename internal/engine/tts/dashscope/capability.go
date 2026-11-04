package dashscope

import (
	"context"

	"github.com/teamwork/digital-human/internal/engine/tts"
)

// Capability 只有真实探测取得非空 PCM 才由 CapabilityCache 判为 real。
func (s *Synthesizer) Capability(ctx context.Context) tts.CapabilityState {
	return s.cache.Capability(ctx, s.identity, s.Probe)
}

// Probe 执行一次真实合成并返回实际 PCM 字节数；空音频保持 unknown。
func (s *Synthesizer) Probe(ctx context.Context) (int, error) {
	result, err := s.run(ctx, tts.JobRequest{Text: "你好，这是一次语音能力探测。", VoiceID: s.cfg.Voice}, func(context.Context, []byte) error {
		return nil
	})
	if err != nil {
		return 0, err
	}
	return result.PCMBytes, nil
}
