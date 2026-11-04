package voice

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/capability"
	"github.com/teamwork/digital-human/internal/biz/chat"
	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/llm"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

type fakeChat struct {
	mu       sync.Mutex
	key      string
	text     string
	cancel   chan struct{}
	onStream func()
}

func (f *fakeChat) ChatWithTokenHook(ctx context.Context, key, text string, sink chat.SentenceSink, onToken func()) (string, error) {
	f.mu.Lock()
	f.key, f.text = key, text
	f.mu.Unlock()
	if f.onStream != nil {
		f.onStream()
	}
	if f.cancel != nil {
		select {
		case <-ctx.Done():
			close(f.cancel)
			return "", ctx.Err()
		case <-time.After(5 * time.Second):
			return "", errors.New("fake chat was not cancelled")
		}
	}
	if onToken != nil {
		onToken()
	}
	if err := sink(ctx, "收到"); err != nil {
		return "", err
	}
	return "收到", nil
}

type fakeSpeaker struct {
	mu         sync.Mutex
	texts      []string
	interrupts []bool
	cancel     chan struct{}
}

func (f *fakeSpeaker) Snapshot() live.Snapshot {
	return live.Snapshot{SessionID: "s-1", Epoch: 7, Phase: live.PhaseLive}
}

func (f *fakeSpeaker) InterruptVoice(_ context.Context, interruptSongs bool) {
	f.mu.Lock()
	f.interrupts = append(f.interrupts, interruptSongs)
	f.mu.Unlock()
}

func (f *fakeSpeaker) Say(ctx context.Context, text, _ string) (string, error) {
	f.mu.Lock()
	f.texts = append(f.texts, text)
	f.mu.Unlock()
	live.NotifyFirstPCM(ctx)
	if f.cancel != nil {
		<-ctx.Done()
		close(f.cancel)
		return "", ctx.Err()
	}
	return "u-1", nil
}

func loudFrames(n int) []byte {
	pcm := make([]byte, n*640)
	for i := 0; i < len(pcm); i += 2 {
		pcm[i], pcm[i+1] = 0x10, 0x27
	}
	return pcm
}

func newTestCoordinator(t *testing.T, recognizer Recognizer, chatter Chat, speaker *fakeSpeaker) (*Coordinator, *eventbus.Bus) {
	t.Helper()
	bus := eventbus.New(64)
	cfg := DefaultConfig()
	cfg.ASRTimeout = time.Second
	cfg.LLMTimeout = time.Second
	cfg.TurnTimeout = 5 * time.Second
	return NewCoordinator(recognizer, NewEnergyVAD(cfg.VAD), chatter, speaker, bus, cfg), bus
}

func beginTestTurn(t *testing.T, c *Coordinator, owner string) *Turn {
	t.Helper()
	turn, err := c.Begin(context.Background(), BeginRequest{
		SessionID: "s-1", OwnerID: owner, ClientTurnID: "client-1", Mode: "ptt",
		Format: "pcm_s16le", SampleRate: 16000, Channels: 1, FrameMS: 20,
	}, func(Partial) {})
	if err != nil {
		t.Fatal(err)
	}
	return turn
}

func waitTurn(t *testing.T, turn *Turn) {
	t.Helper()
	select {
	case <-turn.Done():
	case <-time.After(3 * time.Second):
		t.Fatal("turn did not finish")
	}
}

func TestFakeFullTurnPublishesLatencyAndUsesSessionMemory(t *testing.T) {
	chatter := &fakeChat{}
	speaker := &fakeSpeaker{}
	c, bus := newTestCoordinator(t, NewFakeRecognizer("介绍一下产品"), chatter, speaker)
	events, cancel := bus.SubscribeFor("s-1", 0)
	defer cancel()

	turn := beginTestTurn(t, c, "browser-1")
	if err := turn.WriteAudio(loudFrames(20)); err != nil {
		t.Fatal(err)
	}
	turn.End()
	waitTurn(t, turn)

	chatter.mu.Lock()
	key, text := chatter.key, chatter.text
	chatter.mu.Unlock()
	if key != "s-1:operator" || text != "介绍一下产品" {
		t.Fatalf("chat key/text = %q/%q", key, text)
	}
	if len(speaker.texts) != 1 || speaker.texts[0] != "收到" {
		t.Fatalf("speaker texts = %v", speaker.texts)
	}

	want := map[string]bool{
		"voice.listening": false, "voice.final": false, "voice.thinking": false,
		"voice.speaking": false, "voice.first_audio": false, "voice.completed": false,
	}
	var finalPayload, speakingPayload, completedPayload map[string]any
	for len(want) > 0 {
		select {
		case e := <-events:
			if _, ok := want[e.Type]; ok {
				delete(want, e.Type)
			}
			if e.Type == "voice.final" {
				finalPayload = e.Payload
			}
			if e.Type == "voice.speaking" {
				speakingPayload = e.Payload
			}
			if e.Type == "voice.completed" {
				completedPayload = e.Payload
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("missing events: %v", want)
		}
	}
	if finalPayload["text"] != "介绍一下产品" || finalPayload["asr_source"] != "fake" {
		t.Fatalf("voice.final = %v", finalPayload)
	}
	if speakingPayload["utterance_id"] != "u-1" {
		t.Fatalf("voice.speaking = %v", speakingPayload)
	}
	timings, ok := completedPayload["timings"].(map[string]any)
	if !ok {
		t.Fatalf("completed timings missing: %v", completedPayload)
	}
	for _, name := range []string{"speech_end_at_ms", "asr_final_at_ms", "llm_first_token_at_ms", "tts_first_audio_at_ms"} {
		if timings[name] == nil {
			t.Errorf("timing %s missing from %v", name, timings)
		}
	}
}

func TestNewTurnSameOwnerCancelsInflightLLM(t *testing.T) {
	started := make(chan struct{})
	chatter := &fakeChat{cancel: make(chan struct{}), onStream: func() { close(started) }}
	speaker := &fakeSpeaker{}
	c, _ := newTestCoordinator(t, NewFakeRecognizer("第一句话"), chatter, speaker)
	first := beginTestTurn(t, c, "browser-1")
	if err := first.WriteAudio(loudFrames(20)); err != nil {
		t.Fatal(err)
	}
	first.End()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("LLM did not start")
	}

	second := beginTestTurn(t, c, "browser-1")
	select {
	case <-chatter.cancel:
	case <-time.After(time.Second):
		t.Fatal("in-flight LLM context was not cancelled")
	}
	waitTurn(t, first)
	second.Cancel("cancelled")
	waitTurn(t, second)
}

func TestDifferentOwnerCannotStealTurn(t *testing.T) {
	c, _ := newTestCoordinator(t, NewFakeRecognizer("你好"), &fakeChat{}, &fakeSpeaker{})
	first := beginTestTurn(t, c, "browser-1")
	defer func() { first.Cancel("cancelled"); waitTurn(t, first) }()
	_, err := c.Begin(context.Background(), BeginRequest{
		SessionID: "s-1", OwnerID: "browser-2", Mode: "ptt", Format: "pcm_s16le",
		SampleRate: 16000, Channels: 1, FrameMS: 20,
	}, func(Partial) {})
	if !errors.Is(err, ErrTurnActive) {
		t.Fatalf("Begin error = %v, want ErrTurnActive", err)
	}
}

func TestCancelUsesSpeechOnlyInterruptPolicy(t *testing.T) {
	speaker := &fakeSpeaker{}
	c, _ := newTestCoordinator(t, NewFakeRecognizer("你好"), &fakeChat{}, speaker)
	turn := beginTestTurn(t, c, "browser-1")
	turn.Cancel("cancelled")
	waitTurn(t, turn)
	speaker.mu.Lock()
	defer speaker.mu.Unlock()
	if len(speaker.interrupts) != 1 || speaker.interrupts[0] {
		t.Fatalf("interrupt policy = %v, want [false]", speaker.interrupts)
	}
}

func TestContinuousModeUsesConfiguredBargeInThreshold(t *testing.T) {
	c, _ := newTestCoordinator(t, NewFakeRecognizer("你好"), &fakeChat{}, &fakeSpeaker{})
	turn, err := c.Begin(context.Background(), BeginRequest{
		SessionID: "s-1", OwnerID: "browser-1", Mode: "continuous", Format: "pcm_s16le",
		SampleRate: 16000, Channels: 1, FrameMS: 20,
	}, func(Partial) {})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { turn.Cancel("cancelled"); waitTurn(t, turn) }()
	vad, ok := turn.vad.(*EnergyVAD)
	if !ok || vad.cfg.MinStartSpeech != c.cfg.BargeIn {
		t.Fatalf("continuous VAD start threshold = %+v, want %s", vad.cfg, c.cfg.BargeIn)
	}
}

func TestCrossOwnerPreemptIsExplicitlyConfigurable(t *testing.T) {
	chatter := &fakeChat{}
	speaker := &fakeSpeaker{}
	c, _ := newTestCoordinator(t, NewFakeRecognizer("你好"), chatter, speaker)
	c.cfg.AllowCrossOwnerPreempt = true
	first := beginTestTurn(t, c, "browser-1")
	second, err := c.Begin(context.Background(), BeginRequest{
		SessionID: "s-1", OwnerID: "browser-2", Mode: "ptt", Format: "pcm_s16le",
		SampleRate: 16000, Channels: 1, FrameMS: 20,
	}, func(Partial) {})
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}
	waitTurn(t, first)
	second.Cancel("cancelled")
	waitTurn(t, second)
}

func TestUnavailableASRFailsClosed(t *testing.T) {
	chatter := &fakeChat{}
	c, bus := newTestCoordinator(t, NewUnavailableRecognizer("model_missing"), chatter, &fakeSpeaker{})
	events, cancel := bus.SubscribeFor("s-1", 0)
	defer cancel()
	_, err := c.Begin(context.Background(), BeginRequest{
		SessionID: "s-1", OwnerID: "browser-1", Mode: "ptt", Format: "pcm_s16le",
		SampleRate: 16000, Channels: 1, FrameMS: 20,
	}, func(Partial) {})
	if !errors.Is(err, ErrASRUnavailable) {
		t.Fatalf("Begin error = %v, want ErrASRUnavailable", err)
	}
	select {
	case e := <-events:
		if e.Type != "voice.error" || e.Payload["error_code"] != "asr_unavailable" {
			t.Fatalf("event = %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("missing asr_unavailable event")
	}
	if chatter.text != "" {
		t.Fatal("LLM must not be called when ASR is unavailable")
	}
}

func TestLLMNotConfiguredIsVisible(t *testing.T) {
	c, bus := newTestCoordinator(t, NewFakeRecognizer("你好"), chatError{err: llm.ErrLLMNotConfigured}, &fakeSpeaker{})
	events, cancel := bus.SubscribeFor("s-1", 0)
	defer cancel()
	turn := beginTestTurn(t, c, "browser-1")
	_ = turn.WriteAudio(loudFrames(20))
	turn.End()
	waitTurn(t, turn)
	for {
		e := <-events
		if e.Type == "voice.error" {
			if e.Payload["stage"] != "llm" || e.Payload["error_code"] != "llm_unavailable" {
				t.Fatalf("voice.error = %v", e.Payload)
			}
			return
		}
	}
}

type chatError struct{ err error }

func (c chatError) ChatWithTokenHook(context.Context, string, string, chat.SentenceSink, func()) (string, error) {
	return "", c.err
}

func TestVoiceBeginCancelsInflightTextChat(t *testing.T) {
	started := make(chan struct{})
	chatter := &fakeChat{cancel: make(chan struct{}), onStream: func() { close(started) }}
	speaker := &fakeSpeaker{}
	c, _ := newTestCoordinator(t, NewFakeRecognizer("新问题"), chatter, speaker)
	chatCtx, chatCancel := context.WithCancel(context.Background())
	defer chatCancel()
	result := make(chan error, 1)
	go func() {
		_, err := c.ChatText(chatCtx, "旧问题")
		result <- err
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("text chat did not start")
	}

	turn := beginTestTurn(t, c, "browser-1")
	defer func() { turn.Cancel("cancelled"); waitTurn(t, turn) }()
	select {
	case <-chatter.cancel:
	case <-time.After(time.Second):
		t.Fatal("voice begin did not cancel in-flight text chat")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ChatText error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled text chat did not return")
	}
	speaker.mu.Lock()
	defer speaker.mu.Unlock()
	if len(speaker.texts) != 0 {
		t.Fatalf("cancelled text chat emitted speech: %v", speaker.texts)
	}
}

func TestSessionStopCancelsInflightTextChat(t *testing.T) {
	started := make(chan struct{})
	chatter := &fakeChat{cancel: make(chan struct{}), onStream: func() { close(started) }}
	c, _ := newTestCoordinator(t, NewFakeRecognizer("你好"), chatter, &fakeSpeaker{})
	chatCtx, chatCancel := context.WithCancel(context.Background())
	defer chatCancel()
	result := make(chan error, 1)
	go func() {
		_, err := c.ChatText(chatCtx, "旧问题")
		result <- err
	}()
	<-started

	c.OnSessionStop(context.Background(), "s-1")
	select {
	case <-chatter.cancel:
	case <-time.After(time.Second):
		t.Fatal("session stop did not cancel in-flight text chat")
	}
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ChatText error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled text chat did not return")
	}
}

type lateTextChat struct{ started chan struct{} }

func (f *lateTextChat) ChatWithTokenHook(ctx context.Context, _ string, _ string, sink chat.SentenceSink, _ func()) (string, error) {
	close(f.started)
	<-ctx.Done()
	return "", sink(context.Background(), "过期回答")
}

func TestCompletedTurnIgnoresLateAbort(t *testing.T) {
	c, bus := newTestCoordinator(t, NewFakeRecognizer("你好"), &fakeChat{}, &fakeSpeaker{})
	events, unsubscribe := bus.SubscribeFor("s-1", 0)
	defer unsubscribe()
	turn := beginTestTurn(t, c, "browser-1")
	if err := turn.WriteAudio(loudFrames(20)); err != nil {
		t.Fatal(err)
	}
	turn.End()
	waitTurn(t, turn)
	turn.Abort("late_abort", errors.New("late failure"))

	completed := 0
	for {
		select {
		case e := <-events:
			if e.Type == "voice.error" {
				t.Fatalf("completed turn emitted late error: %v", e.Payload)
			}
			if e.Type == "voice.completed" {
				completed++
			}
		case <-time.After(100 * time.Millisecond):
			if completed != 1 {
				t.Fatalf("voice.completed count = %d, want 1", completed)
			}
			return
		}
	}
}

type blockingInterruptSpeaker struct {
	*fakeSpeaker
	once    sync.Once
	started chan struct{}
	release chan struct{}
}

func (s *blockingInterruptSpeaker) InterruptVoice(ctx context.Context, interruptSongs bool) {
	s.once.Do(func() {
		close(s.started)
		<-s.release
	})
	s.fakeSpeaker.InterruptVoice(ctx, interruptSongs)
}

func TestVoiceBeginReservesTurnBeforeCancellingPreviousTurn(t *testing.T) {
	speaker := &blockingInterruptSpeaker{
		fakeSpeaker: &fakeSpeaker{}, started: make(chan struct{}), release: make(chan struct{}),
	}
	bus := eventbus.New(64)
	c := NewCoordinator(NewFakeRecognizer("新回合"), nil, &fakeChat{}, speaker, bus, DefaultConfig())
	first := beginTestTurn(t, c, "browser-1")
	type beginResult struct {
		turn *Turn
		err  error
	}
	secondResult := make(chan beginResult, 1)
	go func() {
		turn, err := c.Begin(context.Background(), BeginRequest{
			SessionID: "s-1", OwnerID: "browser-1", Mode: "ptt", Format: "pcm_s16le",
			SampleRate: 16000, Channels: 1, FrameMS: 20,
		}, func(Partial) {})
		secondResult <- beginResult{turn: turn, err: err}
	}()
	<-speaker.started
	_, chatErr := c.ChatText(context.Background(), "不应插入的文本")
	close(speaker.release)
	var second *Turn
	select {
	case result := <-secondResult:
		if result.err != nil {
			t.Fatal(result.err)
		}
		second = result.turn
	case <-time.After(time.Second):
		t.Fatal("second Begin did not finish")
	}
	defer func() { second.Cancel("cancelled"); waitTurn(t, second) }()
	waitTurn(t, first)
	if !errors.Is(chatErr, ErrTurnActive) {
		t.Fatalf("ChatText error = %v, want ErrTurnActive", chatErr)
	}
}

func TestCancelledTextChatCannotEmitLateSpeech(t *testing.T) {
	chatter := &lateTextChat{started: make(chan struct{})}
	speaker := &fakeSpeaker{}
	c, _ := newTestCoordinator(t, NewFakeRecognizer("你好"), chatter, speaker)
	chatCtx, chatCancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		_, err := c.ChatText(chatCtx, "旧问题")
		result <- err
	}()
	<-chatter.started
	chatCancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ChatText error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancelled text chat did not return")
	}
	speaker.mu.Lock()
	defer speaker.mu.Unlock()
	if len(speaker.texts) != 0 {
		t.Fatalf("late text answer revived after cancellation: %v", speaker.texts)
	}
}

type trackedRecognition struct {
	mu        sync.Mutex
	closes    int
	finalizes int
	writeErr  error
	closed    chan struct{}
	closeOnce sync.Once
}

func newTrackedRecognition() *trackedRecognition {
	return &trackedRecognition{closed: make(chan struct{})}
}

func (r *trackedRecognition) WritePCM(context.Context, []byte) error { return r.writeErr }
func (r *trackedRecognition) Finalize(context.Context) (Result, error) {
	r.mu.Lock()
	r.finalizes++
	r.mu.Unlock()
	return Result{Text: "识别文本"}, nil
}
func (r *trackedRecognition) Close() error {
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closes++
		r.mu.Unlock()
		close(r.closed)
	})
	return nil
}

type trackedRecognizer struct {
	mu   sync.Mutex
	recs []*trackedRecognition
}

func (r *trackedRecognizer) Capability(context.Context) Capability {
	return Capability{Source: capability.Fake, Ready: true}
}
func (r *trackedRecognizer) Start(context.Context, func(string)) (Recognition, error) {
	rec := newTrackedRecognition()
	r.mu.Lock()
	r.recs = append(r.recs, rec)
	r.mu.Unlock()
	return rec, nil
}
func (r *trackedRecognizer) last(t *testing.T) *trackedRecognition {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.recs) == 0 {
		t.Fatal("recognizer did not start")
	}
	return r.recs[len(r.recs)-1]
}

func TestAbortClosesRecognitionAndAllowsNextTurn(t *testing.T) {
	recognizer := &trackedRecognizer{}
	c, _ := newTestCoordinator(t, recognizer, &fakeChat{}, &fakeSpeaker{})
	first := beginTestTurn(t, c, "browser-1")
	first.Abort("invalid_audio", errors.New("bad frame"))
	waitTurn(t, first)
	rec := recognizer.last(t)
	select {
	case <-rec.closed:
	default:
		t.Fatal("aborted recognition was not closed")
	}
	second := beginTestTurn(t, c, "browser-1")
	second.Cancel("cancelled")
	waitTurn(t, second)
}

func TestCancelledTurnDoesNotFinalize(t *testing.T) {
	recognizer := &trackedRecognizer{}
	c, _ := newTestCoordinator(t, recognizer, &fakeChat{}, &fakeSpeaker{})
	turn := beginTestTurn(t, c, "browser-1")
	turn.Cancel("cancelled")
	turn.End()
	waitTurn(t, turn)
	time.Sleep(30 * time.Millisecond)
	rec := recognizer.last(t)
	rec.mu.Lock()
	defer rec.mu.Unlock()
	if rec.finalizes != 0 {
		t.Fatalf("Finalize calls = %d, want 0", rec.finalizes)
	}
}

func TestTurnTimeoutClosesAndClearsTurn(t *testing.T) {
	recognizer := &trackedRecognizer{}
	c, _ := newTestCoordinator(t, recognizer, &fakeChat{}, &fakeSpeaker{})
	c.cfg.TurnTimeout = 30 * time.Millisecond
	turn := beginTestTurn(t, c, "browser-1")
	select {
	case <-turn.Done():
	case <-time.After(300 * time.Millisecond):
		t.Fatal("timed out turn was not cleared")
	}
	rec := recognizer.last(t)
	select {
	case <-rec.closed:
	default:
		t.Fatal("timed out recognition was not closed")
	}
	c.mu.Lock()
	_, active := c.active[turn.sessionID]
	c.mu.Unlock()
	if active {
		t.Fatal("timed out turn remains active")
	}
}

type lateVoiceChat struct {
	started  chan struct{}
	finished chan struct{}
}

func (f *lateVoiceChat) ChatWithTokenHook(ctx context.Context, _ string, _ string, sink chat.SentenceSink, _ func()) (string, error) {
	close(f.started)
	<-ctx.Done()
	err := sink(context.Background(), "过期回答")
	close(f.finished)
	return "", err
}

func TestCancelledVoiceTurnCannotEmitQueuedSpeech(t *testing.T) {
	chatter := &lateVoiceChat{started: make(chan struct{}), finished: make(chan struct{})}
	speaker := &fakeSpeaker{}
	c, bus := newTestCoordinator(t, NewFakeRecognizer("你好"), chatter, speaker)
	events, unsubscribe := bus.SubscribeFor("s-1", 0)
	defer unsubscribe()
	turn := beginTestTurn(t, c, "browser-1")
	if err := turn.WriteAudio(loudFrames(20)); err != nil {
		t.Fatal(err)
	}
	turn.End()
	<-chatter.started
	turn.Cancel("interrupted")
	select {
	case <-chatter.finished:
	case <-time.After(time.Second):
		t.Fatal("cancelled voice chat did not return")
	}
	speaker.mu.Lock()
	texts := append([]string(nil), speaker.texts...)
	speaker.mu.Unlock()
	if len(texts) != 0 {
		t.Fatalf("cancelled voice turn emitted speech: %v", texts)
	}
	for {
		select {
		case e := <-events:
			if e.Type == "voice.speaking" {
				t.Fatal("cancelled voice turn published voice.speaking")
			}
		case <-time.After(50 * time.Millisecond):
			return
		}
	}
}

func TestASRWriteFailureIsUnavailable(t *testing.T) {
	recognizer := &trackedRecognizer{}
	c, _ := newTestCoordinator(t, recognizer, &fakeChat{}, &fakeSpeaker{})
	turn := beginTestTurn(t, c, "browser-1")
	recognizer.last(t).writeErr = errors.New("sidecar write failed")
	err := turn.WriteAudio(loudFrames(1))
	if !errors.Is(err, ErrASRUnavailable) {
		t.Fatalf("WriteAudio error = %v, want ErrASRUnavailable", err)
	}
	waitTurn(t, turn)
}

func TestBeginRejectedAfterSessionStoppingWithStaleLiveSnapshot(t *testing.T) {
	c, _ := newTestCoordinator(t, NewFakeRecognizer("你好"), &fakeChat{}, &fakeSpeaker{})
	c.OnSessionStopping(context.Background(), "s-1")
	_, err := c.Begin(context.Background(), BeginRequest{
		SessionID: "s-1", OwnerID: "browser-1", Mode: "ptt", Format: "pcm_s16le",
		SampleRate: 16000, Channels: 1, FrameMS: 20,
	}, func(Partial) {})
	if !errors.Is(err, ErrNotLive) {
		t.Fatalf("Begin after stopping = %v, want ErrNotLive", err)
	}
}

func TestManualInterruptCancelsInflightTextChat(t *testing.T) {
	started := make(chan struct{})
	chatter := &fakeChat{cancel: make(chan struct{}), onStream: func() { close(started) }}
	c, _ := newTestCoordinator(t, NewFakeRecognizer("你好"), chatter, &fakeSpeaker{})
	result := make(chan error, 1)
	go func() {
		_, err := c.ChatText(context.Background(), "旧问题")
		result <- err
	}()
	<-started
	c.OnSessionInterrupt(context.Background())
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("ChatText error = %v, want context.Canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("manual interrupt did not cancel text chat")
	}
}

func TestCompleteUsesTimeoutReason(t *testing.T) {
	c, bus := newTestCoordinator(t, NewFakeRecognizer("你好"), &fakeChat{}, &fakeSpeaker{})
	events, unsubscribe := bus.SubscribeFor("s-1", 0)
	defer unsubscribe()
	ctx, cancel := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancel()
	<-ctx.Done()
	turn := &Turn{id: "timeout-turn", sessionID: "s-1", coord: c, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	c.active["s-1"] = turn
	turn.complete()
	select {
	case e := <-events:
		if e.Type != "voice.completed" || e.Payload["reason"] != "timeout" {
			t.Fatalf("timeout completion = %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("missing timeout completion")
	}
}

func TestCompleteUsesCancelledReasonWhenParentContextIsCancelled(t *testing.T) {
	c, bus := newTestCoordinator(t, NewFakeRecognizer("你好"), &fakeChat{}, &fakeSpeaker{})
	events, unsubscribe := bus.SubscribeFor("s-1", 0)
	defer unsubscribe()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	turn := &Turn{id: "cancelled-turn", sessionID: "s-1", coord: c, ctx: ctx, cancel: cancel, done: make(chan struct{})}
	c.active["s-1"] = turn
	turn.complete()
	select {
	case e := <-events:
		if e.Type != "voice.completed" || e.Payload["reason"] != "cancelled" {
			t.Fatalf("cancelled completion = %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("missing cancelled completion")
	}
}

func TestStatusUnknownTTSIsNotReady(t *testing.T) {
	svc := &StatusService{
		ASR:           NewFakeRecognizer("你好"),
		LLMConfigured: func(context.Context) bool { return true },
		Capabilities:  func(context.Context) capability.Matrix { return capability.Matrix{"tts": capability.Unknown} },
		Speaker:       &fakeSpeaker{},
		Config:        DefaultConfig(),
	}
	status := svc.Status(context.Background())
	if status.TTS.Ready || status.Ready || status.TTS.ErrorCode != "tts_unavailable" {
		t.Fatalf("unknown TTS must not be ready: %+v", status)
	}
}
