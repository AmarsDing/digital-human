package engine

import (
	"context"
	"testing"

	"github.com/teamwork/digital-human/internal/engine/tts"
)

type fakeStreamingSynthesizer struct {
	pcm []byte
}

func (f fakeStreamingSynthesizer) SynthesizeStream(ctx context.Context, _ tts.JobRequest, emit func(context.Context, []byte) error) error {
	return emit(ctx, f.pcm)
}

func TestStreamingSynthesizerEmitsPCM(t *testing.T) {
	want := []byte{1, 2, 3, 4}
	var synth StreamingSynthesizer = fakeStreamingSynthesizer{pcm: want}
	var got []byte
	err := synth.SynthesizeStream(context.Background(), tts.JobRequest{Text: "测试"}, func(_ context.Context, pcm []byte) error {
		got = append([]byte(nil), pcm...)
		return nil
	})
	if err != nil {
		t.Fatalf("SynthesizeStream() error = %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("PCM = %v, want %v", got, want)
	}
}
