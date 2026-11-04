package dashscope

import (
	"context"
	"errors"
	"testing"

	"github.com/teamwork/digital-human/internal/engine/tts"
	"github.com/teamwork/digital-human/pkg/audio"
)

func TestCapabilityRequiresRealAudio(t *testing.T) {
	t.Run("empty audio stays unknown", func(t *testing.T) {
		srv, _ := newFakeRealtime(t, fakeRealtimeOptions{})
		s := newTestSynthesizer(t, srv)
		if got := s.Capability(context.Background()); got != tts.CapabilityUnknown {
			t.Fatalf("capability = %s, want unknown", got)
		}
	})

	t.Run("real PCM becomes real", func(t *testing.T) {
		srv, _ := newFakeRealtime(t, fakeRealtimeOptions{deltas: [][]byte{audio.TonePCM(100)}})
		s := newTestSynthesizer(t, srv)
		if got := s.Capability(context.Background()); got != tts.CapabilityReal {
			t.Fatalf("capability = %s, want real", got)
		}
	})
}

func TestModelUnavailableIsConfigurationFailure(t *testing.T) {
	srv, _ := newFakeRealtime(t, fakeRealtimeOptions{serverError: &fakeServerError{
		code:    "invalid_parameter_error",
		message: "model qwen3-tts-flash-realtime is not enabled in this workspace",
	}})
	s := newTestSynthesizer(t, srv)
	err := s.SynthesizeStream(context.Background(), tts.JobRequest{Text: "你好"}, func(context.Context, []byte) error {
		return nil
	})
	var serverErr *ServerError
	if !errors.As(err, &serverErr) {
		t.Fatalf("error = %T %v, want ServerError", err, err)
	}
	if got := ClassifyFailure(err); got != FailureModelUnavailable {
		t.Fatalf("failure class = %s, want model_unavailable", got)
	}
	if got := s.Capability(context.Background()); got != tts.CapabilityUnknown {
		t.Fatalf("capability = %s, want unknown", got)
	}
}

func TestSynthesisFailureClearsCachedReal(t *testing.T) {
	srv, _ := newFakeRealtime(t, fakeRealtimeOptions{deltas: [][]byte{audio.TonePCM(100)}})
	s := newTestSynthesizer(t, srv)
	if got := s.Capability(context.Background()); got != tts.CapabilityReal {
		t.Fatalf("capability = %s, want real", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := s.SynthesizeStream(ctx, tts.JobRequest{Text: "你好"}, func(context.Context, []byte) error {
		return nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	if got := s.cache.State(s.identity); got != tts.CapabilityUnknown {
		t.Fatalf("capability after failure = %s, want unknown", got)
	}
}

func TestProtocolFailureRequiresCodeInvestigation(t *testing.T) {
	srv, _ := newFakeRealtime(t, fakeRealtimeOptions{format: "wav"})
	s := newTestSynthesizer(t, srv)
	err := s.SynthesizeStream(context.Background(), tts.JobRequest{Text: "你好"}, func(context.Context, []byte) error {
		return nil
	})
	if err == nil {
		t.Fatal("expected negotiation error")
	}
	if got := ClassifyFailure(err); got != FailureProtocol {
		t.Fatalf("failure class = %s, want protocol", got)
	}
}
