package dashscope

import (
	"context"
	"encoding/base64"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/teamwork/digital-human/internal/engine/tts"
	"github.com/teamwork/digital-human/pkg/audio"
)

type sessionSettings struct {
	Voice          string `json:"voice"`
	Mode           string `json:"mode"`
	ResponseFormat string `json:"response_format"`
	SampleRate     int    `json:"sample_rate"`
}

type clientEvent struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Session sessionSettings `json:"session"`
}

type fakeServerError struct {
	code    string
	message string
}

type fakeRealtimeOptions struct {
	format              string
	sampleRate          int
	deltas              [][]byte
	serverError         *fakeServerError
	malformedError      bool
	finishBeforeDone    bool
	holdAfterFirstDelta chan struct{}
	allowClose          bool
}

type fakeRealtimeState struct {
	mu     sync.Mutex
	events []clientEvent
}

func (s *fakeRealtimeState) add(event clientEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, event)
}

func (s *fakeRealtimeState) types() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.events))
	for i, event := range s.events {
		out[i] = event.Type
	}
	return out
}

func (s *fakeRealtimeState) update() clientEvent {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.events) == 0 {
		return clientEvent{}
	}
	return s.events[0]
}

func newFakeRealtime(t *testing.T, opts fakeRealtimeOptions) (*httptest.Server, *fakeRealtimeState) {
	t.Helper()
	state := &fakeRealtimeState{}
	upgrader := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Path; got != "/api-ws/v1/realtime" {
			t.Errorf("request path = %q", got)
			return
		}
		if got := r.URL.Query().Get("model"); got != "qwen3-tts-flash-realtime" {
			t.Errorf("model query = %q", got)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-secret" {
			t.Errorf("Authorization = %q", got)
			return
		}
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer conn.Close()
		if err := conn.WriteJSON(map[string]any{
			"type":    "session.created",
			"session": map[string]any{"response_format": "pcm", "sample_rate": audio.SampleRate},
		}); err != nil {
			t.Errorf("write session.created: %v", err)
			return
		}
		var update clientEvent
		if err := conn.ReadJSON(&update); err != nil {
			t.Errorf("read session.update: %v", err)
			return
		}
		state.add(update)
		if opts.serverError != nil || opts.malformedError {
			var serverError any
			if opts.serverError != nil {
				serverError = map[string]any{"code": opts.serverError.code, "message": opts.serverError.message}
			}
			_ = conn.WriteJSON(map[string]any{"type": "error", "error": serverError})
			return
		}
		format := opts.format
		if format == "" {
			format = "pcm"
		}
		rate := opts.sampleRate
		if rate == 0 {
			rate = audio.SampleRate
		}
		if err := conn.WriteJSON(map[string]any{
			"type":    "session.updated",
			"session": map[string]any{"response_format": format, "sample_rate": rate},
		}); err != nil {
			t.Errorf("write session.updated: %v", err)
			return
		}
		if format != "pcm" || rate != audio.SampleRate {
			return
		}
		for {
			var event clientEvent
			if err := conn.ReadJSON(&event); err != nil {
				if !opts.allowClose {
					t.Errorf("read client event: %v", err)
				}
				return
			}
			state.add(event)
			switch event.Type {
			case "input_text_buffer.commit":
				for i, delta := range opts.deltas {
					if err := conn.WriteJSON(map[string]any{
						"type":  "response.audio.delta",
						"delta": base64.StdEncoding.EncodeToString(delta),
					}); err != nil {
						t.Errorf("write audio delta: %v", err)
						return
					}
					if i == 0 && opts.holdAfterFirstDelta != nil {
						<-opts.holdAfterFirstDelta
					}
				}
			case "session.finish":
				if opts.finishBeforeDone {
					_ = conn.WriteJSON(map[string]any{"type": "session.finished"})
					return
				}
				_ = conn.WriteJSON(map[string]any{"type": "response.audio.done"})
				_ = conn.WriteJSON(map[string]any{"type": "response.done"})
				_ = conn.WriteJSON(map[string]any{"type": "session.finished"})
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	return srv, state
}

func newTestSynthesizer(t *testing.T, srv *httptest.Server) *Synthesizer {
	t.Helper()
	s, err := NewSynthesizer(tts.CloudConfig{
		Provider:   "dashscope",
		BaseURL:    "ws" + strings.TrimPrefix(srv.URL, "http") + "/api-ws/v1",
		Model:      "qwen3-tts-flash-realtime",
		APIKey:     "test-secret",
		Voice:      "Cherry",
		SampleRate: audio.SampleRate,
		Format:     "pcm",
		Timeout:    2 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSynthesizeStreamNegotiatesPCMAndEmitsBeforeDone(t *testing.T) {
	release := make(chan struct{})
	pcm := audio.TonePCM(100)
	srv, state := newFakeRealtime(t, fakeRealtimeOptions{
		deltas:              [][]byte{pcm[:1000], pcm[1000:]},
		holdAfterFirstDelta: release,
	})
	s := newTestSynthesizer(t, srv)
	first := make(chan []byte, 1)
	done := make(chan error, 1)
	go func() {
		done <- s.SynthesizeStream(context.Background(), tts.JobRequest{Text: "你好"}, func(_ context.Context, delta []byte) error {
			select {
			case first <- append([]byte(nil), delta...):
			default:
			}
			return nil
		})
	}()

	select {
	case delta := <-first:
		if len(delta) != 1000 {
			t.Fatalf("first delta bytes = %d, want 1000", len(delta))
		}
	case <-time.After(time.Second):
		t.Fatal("first audio delta was not emitted")
	}
	select {
	case err := <-done:
		t.Fatalf("synthesis returned before upstream done: %v", err)
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("SynthesizeStream: %v", err)
	}

	update := state.update()
	if update.Type != "session.update" || update.Session.Voice != "Cherry" || update.Session.Mode != "commit" ||
		update.Session.ResponseFormat != "pcm" || update.Session.SampleRate != audio.SampleRate {
		t.Fatalf("session.update = %+v", update)
	}
	want := []string{"session.update", "input_text_buffer.append", "input_text_buffer.commit", "session.finish"}
	got := state.types()
	if len(got) != len(want) {
		t.Fatalf("client events = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("client events = %v, want %v", got, want)
		}
	}
}

func TestSynthesizeStreamRejectsMismatchedNegotiation(t *testing.T) {
	for _, tt := range []struct {
		name       string
		format     string
		sampleRate int
	}{
		{name: "format", format: "wav", sampleRate: audio.SampleRate},
		{name: "sample rate", format: "pcm", sampleRate: 16000},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv, _ := newFakeRealtime(t, fakeRealtimeOptions{format: tt.format, sampleRate: tt.sampleRate})
			s := newTestSynthesizer(t, srv)
			err := s.SynthesizeStream(context.Background(), tts.JobRequest{Text: "你好"}, func(context.Context, []byte) error {
				t.Fatal("mismatched negotiation must not emit audio")
				return nil
			})
			if err == nil || !strings.Contains(err.Error(), "session.updated") {
				t.Fatalf("error = %v, want negotiated format/sample_rate failure", err)
			}
		})
	}
}

func TestSynthesizeStreamRejectsMalformedErrorEvent(t *testing.T) {
	srv, _ := newFakeRealtime(t, fakeRealtimeOptions{malformedError: true})
	s := newTestSynthesizer(t, srv)
	err := s.SynthesizeStream(context.Background(), tts.JobRequest{Text: "你好"}, func(context.Context, []byte) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "malformed error event") {
		t.Fatalf("error = %v, want malformed error event", err)
	}
	if got := ClassifyFailure(err); got != FailureProtocol {
		t.Fatalf("failure class = %s, want protocol", got)
	}
}

func TestSynthesizeStreamRejectsPrematureSessionFinished(t *testing.T) {
	srv, _ := newFakeRealtime(t, fakeRealtimeOptions{
		deltas:           [][]byte{audio.TonePCM(100)},
		finishBeforeDone: true,
	})
	s := newTestSynthesizer(t, srv)
	err := s.SynthesizeStream(context.Background(), tts.JobRequest{Text: "你好"}, func(context.Context, []byte) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "before response.done") {
		t.Fatalf("error = %v, want premature session.finished failure", err)
	}
}

func TestSynthesizeStreamRejectsUnalignedPCM(t *testing.T) {
	srv, _ := newFakeRealtime(t, fakeRealtimeOptions{deltas: [][]byte{{0}}})
	s := newTestSynthesizer(t, srv)
	err := s.SynthesizeStream(context.Background(), tts.JobRequest{Text: "你好"}, func(context.Context, []byte) error {
		return nil
	})
	if err == nil || !strings.Contains(err.Error(), "sample aligned") {
		t.Fatalf("error = %v, want s16le sample alignment failure", err)
	}
}

func TestSynthesizeStreamCancellationClosesUpstreamRead(t *testing.T) {
	release := make(chan struct{})
	srv, _ := newFakeRealtime(t, fakeRealtimeOptions{
		deltas:              [][]byte{audio.TonePCM(100)},
		holdAfterFirstDelta: release,
		allowClose:          true,
	})
	s := newTestSynthesizer(t, srv)
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() {
		done <- s.SynthesizeStream(ctx, tts.JobRequest{Text: "你好"}, func(context.Context, []byte) error {
			first <- struct{}{}
			return nil
		})
	}()
	select {
	case <-first:
	case <-time.After(time.Second):
		t.Fatal("first audio delta was not emitted")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
	close(release)
}

func TestQwenAudioRemainsDispatchOnly(t *testing.T) {
	_, err := NewSynthesizer(tts.CloudConfig{
		Provider:   "dashscope",
		BaseURL:    "ws://127.0.0.1:1",
		Model:      "qwen-audio-3.1-tts-flash",
		APIKey:     "test-secret",
		Voice:      "Cherry",
		SampleRate: audio.SampleRate,
		Format:     "pcm",
	})
	if got := tts.ErrorCode(err); got != "cloud_tts_protocol_unimplemented" {
		t.Fatalf("error = %v, code = %q, want cloud_tts_protocol_unimplemented", err, got)
	}
}

func TestCapabilityStartsUnknownAndRequiresNonEmptyPCM(t *testing.T) {
	t.Run("configured but never synthesized", func(t *testing.T) {
		srv, _ := newFakeRealtime(t, fakeRealtimeOptions{})
		s := newTestSynthesizer(t, srv)
		if got := s.CachedCapability(); got != tts.CapabilityUnknown {
			t.Fatalf("Capability() = %q, want unknown", got)
		}
	})

	t.Run("empty audio never becomes real", func(t *testing.T) {
		srv, _ := newFakeRealtime(t, fakeRealtimeOptions{})
		s := newTestSynthesizer(t, srv)
		if err := s.SynthesizeStream(context.Background(), tts.JobRequest{Text: "无声"}, func(context.Context, []byte) error {
			t.Fatal("empty upstream must not emit PCM")
			return nil
		}); err != nil {
			t.Fatalf("SynthesizeStream() error = %v", err)
		}
		if got := s.CachedCapability(); got != tts.CapabilityUnknown {
			t.Fatalf("Capability() after empty audio = %q, want unknown", got)
		}
	})

	t.Run("non-empty PCM is real and later failure clears it", func(t *testing.T) {
		srv, _ := newFakeRealtime(t, fakeRealtimeOptions{deltas: [][]byte{audio.TonePCM(100)}})
		s := newTestSynthesizer(t, srv)
		if err := s.SynthesizeStream(context.Background(), tts.JobRequest{Text: "有声"}, func(context.Context, []byte) error { return nil }); err != nil {
			t.Fatalf("SynthesizeStream() error = %v", err)
		}
		if got := s.CachedCapability(); got != tts.CapabilityReal {
			t.Fatalf("Capability() after PCM = %q, want real", got)
		}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := s.SynthesizeStream(ctx, tts.JobRequest{Text: "失败"}, func(context.Context, []byte) error { return nil }); err == nil {
			t.Fatal("canceled synthesis error = nil")
		}
		if got := s.CachedCapability(); got != tts.CapabilityUnknown {
			t.Fatalf("Capability() after failure = %q, want unknown", got)
		}
	})
}

func TestRejectsNon24KRealtimeConfiguration(t *testing.T) {
	_, err := NewSynthesizer(tts.CloudConfig{
		Provider:   "dashscope",
		BaseURL:    "ws://127.0.0.1:1",
		Model:      "qwen3-tts-flash-realtime",
		APIKey:     "test-secret",
		Voice:      "Cherry",
		SampleRate: 16000,
		Format:     "pcm",
	})
	if err == nil || !strings.Contains(err.Error(), "24000") {
		t.Fatalf("error = %v, want 24kHz contract failure", err)
	}
}
