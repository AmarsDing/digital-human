package engine

import (
	"context"

	"github.com/teamwork/digital-human/internal/engine/tts"
)

// StreamingSynthesizer 将云端音频分片按 24kHz/s16le/mono PCM 立即交给喂流层。
type StreamingSynthesizer interface {
	SynthesizeStream(ctx context.Context, req tts.JobRequest, emit func(context.Context, []byte) error) error
}
