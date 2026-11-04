package voice

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

// Speaker 复用直播 Manager 的唯一天然出流口与场次快照。
type Speaker interface {
	Say(ctx context.Context, text, mode string) (string, error)
	Snapshot() live.Snapshot
}

type Coordinator struct {
	asr     Recognizer
	newVAD  func() VAD
	chatter Chat
	speaker Speaker
	bus     *eventbus.Bus
	cfg     Config

	mu         sync.Mutex
	active     map[string]*Turn
	textActive map[string]*textTurn
	stopping   map[string]bool
}

type textTurn struct {
	cancel context.CancelFunc
}

func NewCoordinator(asr Recognizer, vad VAD, chatter Chat, speaker Speaker, bus *eventbus.Bus, cfg Config) *Coordinator {
	if asr == nil {
		asr = NewUnavailableRecognizer("asr_not_configured")
	}
	return &Coordinator{
		asr: asr, newVAD: func() VAD { return NewEnergyVAD(cfg.VAD) },
		chatter: chatter, speaker: speaker, bus: bus, cfg: cfg,
		active: map[string]*Turn{}, textActive: map[string]*textTurn{}, stopping: map[string]bool{},
	}
}

// NewCoordinatorWithVAD 允许装配 sherpa/其他 VAD；nil 时回退到配置的能量 VAD。
func NewCoordinatorWithVAD(asr Recognizer, factory func() VAD, chatter Chat, speaker Speaker, bus *eventbus.Bus, cfg Config) *Coordinator {
	c := NewCoordinator(asr, nil, chatter, speaker, bus, cfg)
	if factory != nil {
		c.newVAD = factory
	}
	return c
}

// Subscribe 供独立语音 WS 转发本场次回合事件；连接层仍按当前 turn_id 隔离客户端。
func (c *Coordinator) Subscribe(sessionID string) (<-chan eventbus.Event, func()) {
	return c.bus.SubscribeFor(sessionID, 0)
}

func (c *Coordinator) Begin(parent context.Context, req BeginRequest, onPartial func(Partial)) (*Turn, error) {
	if c.speaker == nil || c.chatter == nil || c.bus == nil {
		return nil, errors.New("voice coordinator not configured")
	}
	snap := c.speaker.Snapshot()
	if snap.Phase != live.PhaseLive || snap.SessionID == "" || snap.SessionID != req.SessionID {
		return nil, ErrNotLive
	}
	if req.Epoch != 0 && req.Epoch != snap.Epoch {
		return nil, ErrNotLive
	}
	if req.Mode == "" {
		req.Mode = "ptt"
	}
	if req.Mode != "ptt" && !(req.Mode == "continuous" && c.cfg.AllowContinuous) {
		return nil, fmt.Errorf("%w: mode", ErrInvalidAudio)
	}
	if req.Format != "pcm_s16le" || req.SampleRate != 16000 || req.Channels != 1 || req.FrameMS != 20 {
		return nil, fmt.Errorf("%w: require 16kHz mono s16le 20ms", ErrInvalidAudio)
	}
	if strings.TrimSpace(req.OwnerID) == "" {
		return nil, fmt.Errorf("%w: owner required", ErrInvalidAudio)
	}

	vad := c.newVAD()
	if req.Mode == "continuous" {
		if energy, ok := vad.(*EnergyVAD); ok {
			energy.cfg.MinStartSpeech = c.cfg.BargeIn
		}
	}
	ctx, cancel := context.WithTimeout(parent, c.cfg.TurnTimeout)
	t := &Turn{
		id: uuid.NewString(), clientID: req.ClientTurnID, sessionID: req.SessionID, epoch: snap.Epoch,
		owner: req.OwnerID, mode: req.Mode, coord: c, ctx: ctx, cancel: cancel,
		vad: vad, onPartial: onPartial, done: make(chan struct{}), startedAt: time.Now(),
	}
	c.mu.Lock()
	if c.stopping[req.SessionID] {
		c.mu.Unlock()
		cancel()
		return nil, ErrNotLive
	}
	old := c.active[req.SessionID]
	text := c.textActive[req.SessionID]
	if old != nil && old.owner != req.OwnerID && !c.cfg.AllowCrossOwnerPreempt {
		c.mu.Unlock()
		cancel()
		return nil, ErrTurnActive
	}
	c.active[req.SessionID] = t
	if text != nil {
		delete(c.textActive, req.SessionID)
	}
	c.mu.Unlock()
	if old != nil {
		old.Cancel("interrupted")
	}
	if text != nil {
		text.cancel()
		c.interruptVoiceFor(context.WithoutCancel(parent), req.SessionID, snap.Epoch)
	}

	go t.watch()
	rec, err := c.asr.Start(ctx, t.partial)
	if err != nil {
		t.fail("asr", "asr_unavailable", errors.Join(ErrASRUnavailable, err), false)
		return nil, errors.Join(ErrASRUnavailable, err)
	}
	t.mu.Lock()
	t.rec = rec
	cancelled := t.cancelled || t.terminal
	t.mu.Unlock()
	if cancelled {
		_ = rec.Close()
		return t, context.Canceled
	}
	c.publish(t, "voice.listening", map[string]any{"turn_id": t.id, "client_turn_id": t.clientID, "mode": t.mode})
	return t, nil
}

// Turn 是一个连接内唯一活动回合；音频不排队、不跨回合保存。
type Turn struct {
	id, clientID, sessionID, owner, mode string
	epoch                                int64
	coord                                *Coordinator
	ctx                                  context.Context
	cancel                               context.CancelFunc
	rec                                  Recognition
	vad                                  VAD
	onPartial                            func(Partial)
	done                                 chan struct{}
	startedAt                            time.Time

	mu          sync.Mutex
	ended       bool
	cancelled   bool
	terminal    bool
	reason      string
	audio       time.Duration
	revision    uint64
	lastPartial time.Time
	speechEnd   time.Time
	asrFinal    time.Time
	llmFirst    time.Time
	ttsFirst    time.Time
	endOnce     sync.Once
	doneOnce    sync.Once
	closeOnce   sync.Once
}

func (t *Turn) ID() string            { return t.id }
func (t *Turn) Done() <-chan struct{} { return t.done }
func (t *Turn) Frames() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return int(t.audio / (20 * time.Millisecond))
}

// Abort 终止协议/采集错误回合，发布可归类错误而不是伪报取消成功。
func (t *Turn) Abort(code string, err error) {
	t.fail("asr", code, err, false)
}

func (t *Turn) WriteAudio(pcm []byte) error {
	t.mu.Lock()
	if t.ended || t.cancelled || t.terminal {
		t.mu.Unlock()
		return ErrTurnClosed
	}
	if len(pcm) == 0 || len(pcm)%640 != 0 {
		t.mu.Unlock()
		return fmt.Errorf("%w: audio must contain whole 640-byte frames", ErrInvalidAudio)
	}
	t.audio += time.Duration(len(pcm)/640) * 20 * time.Millisecond
	over := t.audio > t.coord.cfg.MaxAudio
	rec, vad, mode := t.rec, t.vad, t.mode
	t.mu.Unlock()
	if over {
		t.fail("asr", "audio_too_long", errors.New("超过单回合最长音频"), false)
		return ErrInvalidAudio
	}
	if rec == nil {
		return ErrASRUnavailable
	}
	if err := rec.WritePCM(t.ctx, pcm); err != nil {
		if t.ctx.Err() == nil {
			t.fail("asr", "asr_unavailable", err, true)
			return errors.Join(ErrASRUnavailable, err)
		}
		return err
	}
	state := vad.Push(pcm)
	if mode == "continuous" && state.Endpoint {
		t.End()
	}
	return nil
}

// End 异步 final，避免语音 socket 被 LLM/TTS 长请求占用。
func (t *Turn) End() {
	t.endOnce.Do(func() {
		t.mu.Lock()
		if t.cancelled || t.terminal {
			t.mu.Unlock()
			return
		}
		t.ended = true
		t.speechEnd = time.Now()
		t.mu.Unlock()
		go t.run()
	})
}

func (t *Turn) Cancel(reason string) {
	t.mu.Lock()
	if t.cancelled || t.terminal {
		t.mu.Unlock()
		return
	}
	t.cancelled = true
	t.terminal = true
	if reason == "" {
		reason = "cancelled"
	}
	t.reason = reason
	t.mu.Unlock()
	t.cancel()
	t.coord.interruptVoiceFor(context.WithoutCancel(t.ctx), t.sessionID, t.epoch)
	t.closeRecognition()
	t.coord.publish(t, "voice.completed", map[string]any{"turn_id": t.id, "reason": reason, "timings": t.timings()})
	t.clearAndDone()
}

func (t *Turn) watch() {
	select {
	case <-t.ctx.Done():
		reason := "cancelled"
		if errors.Is(t.ctx.Err(), context.DeadlineExceeded) {
			reason = "timeout"
		}
		t.Cancel(reason)
	case <-t.done:
	}
}

func (t *Turn) run() {
	defer t.clearAndDone()
	defer t.closeRecognition()
	asrCtx, asrCancel := context.WithTimeout(t.ctx, t.coord.cfg.ASRTimeout)
	result, err := t.rec.Finalize(asrCtx)
	asrCancel()
	if err != nil {
		t.fail("asr", "asr_unavailable", err, true)
		return
	}
	if !t.activeValid() {
		t.complete()
		return
	}
	t.mu.Lock()
	t.asrFinal = time.Now()
	speech := t.vad.SpeechDuration()
	t.mu.Unlock()
	text := strings.TrimSpace(result.Text)
	if text == "" || speech < t.coord.cfg.VAD.MinSpeech {
		t.fail("asr", "no_speech", errors.New("未检测到有效语音"), false)
		return
	}
	t.coord.publish(t, "voice.final", map[string]any{
		"turn_id": t.id, "text": text, "duration_ms": t.audio.Milliseconds(),
		"asr_source": string(t.coord.asr.Capability(context.Background()).Source), "timings": t.timings(),
	})
	if !t.liveValid() {
		t.fail("live", "not_live", ErrNotLive, false)
		return
	}
	t.coord.publish(t, "voice.thinking", map[string]any{"turn_id": t.id, "timings": t.timings()})

	llmCtx, llmCancel := context.WithTimeout(t.ctx, t.coord.cfg.LLMTimeout)
	defer llmCancel()
	ttsStarted := false
	speakingPublished := false
	var tokenOnce, audioOnce sync.Once
	_, err = t.coord.chatter.ChatWithTokenHook(llmCtx, t.sessionID+":operator", text,
		func(ctx context.Context, sentence string) error {
			if !t.activeValid() {
				return context.Canceled
			}
			if !t.liveValid() {
				return ErrNotLive
			}
			if !ttsStarted {
				ttsStarted = true
			}
			if !t.activeValid() {
				return context.Canceled
			}
			ctx = live.WithTranscriptPersistence(ctx, t.coord.cfg.PersistText)
			ctx = live.WithFirstPCMHook(ctx, func() {
				audioOnce.Do(func() {
					if !t.activeValid() || !t.liveValid() {
						return
					}
					t.mu.Lock()
					t.ttsFirst = time.Now()
					t.mu.Unlock()
					t.coord.publish(t, "voice.first_audio", map[string]any{"turn_id": t.id, "timings": t.timings()})
				})
			})
			utteranceID, sayErr := t.coord.speaker.Say(ctx, sentence, "chat")
			if sayErr == nil && !speakingPublished && t.activeValid() {
				speakingPublished = true
				t.coord.publish(t, "voice.speaking", map[string]any{
					"turn_id": t.id, "utterance_id": utteranceID, "timings": t.timings(),
				})
			}
			return sayErr
		}, func() {
			tokenOnce.Do(func() {
				if !t.activeValid() {
					return
				}
				t.mu.Lock()
				t.llmFirst = time.Now()
				t.mu.Unlock()
			})
		})
	if err != nil {
		if t.ctx.Err() != nil {
			t.complete()
			return
		}
		if ttsStarted {
			t.fail("tts", "tts_unavailable", err, true)
		} else {
			t.fail("llm", "llm_unavailable", err, true)
		}
		return
	}
	t.complete()
}

func (t *Turn) liveValid() bool {
	snap := t.coord.speaker.Snapshot()
	return snap.Phase == live.PhaseLive && snap.SessionID == t.sessionID && snap.Epoch == t.epoch
}

func (t *Turn) activeValid() bool {
	if t.ctx.Err() != nil {
		return false
	}
	t.mu.Lock()
	cancelled := t.cancelled || t.terminal
	t.mu.Unlock()
	if cancelled {
		return false
	}
	t.coord.mu.Lock()
	current := t.coord.active[t.sessionID] == t
	t.coord.mu.Unlock()
	return current
}

func (t *Turn) complete() {
	t.mu.Lock()
	if t.terminal {
		t.mu.Unlock()
		return
	}
	t.terminal = true
	reason := t.reason
	if reason == "" {
		reason = "completed"
		if t.ctx.Err() != nil {
			reason = "cancelled"
			if errors.Is(t.ctx.Err(), context.DeadlineExceeded) {
				reason = "timeout"
			}
		}
	}
	t.mu.Unlock()
	t.coord.publish(t, "voice.completed", map[string]any{"turn_id": t.id, "reason": reason, "timings": t.timings()})
	t.clearAndDone()
}

func (t *Turn) fail(stage, code string, err error, retryable bool) {
	if t.ctx.Err() != nil {
		t.complete()
		return
	}
	t.mu.Lock()
	if t.terminal {
		t.mu.Unlock()
		return
	}
	if t.ctx.Err() != nil {
		t.mu.Unlock()
		t.complete()
		return
	}
	t.terminal = true
	t.cancelled = true
	t.reason = "error"
	t.mu.Unlock()
	t.coord.publish(t, "voice.error", map[string]any{
		"turn_id": t.id, "stage": stage, "error_code": code,
		"message": err.Error(), "retryable": retryable, "timings": t.timings(),
	})
	t.cancel()
	t.closeRecognition()
	t.clearAndDone()
}

func (t *Turn) closeRecognition() {
	t.mu.Lock()
	rec := t.rec
	t.mu.Unlock()
	if rec != nil {
		t.closeOnce.Do(func() { _ = rec.Close() })
	}
}

func (t *Turn) partial(text string) {
	if strings.TrimSpace(text) == "" {
		return
	}
	t.mu.Lock()
	if t.ended || t.cancelled || t.terminal || (t.revision > 0 && time.Since(t.lastPartial) < t.coord.cfg.PartialInterval) {
		t.mu.Unlock()
		return
	}
	t.revision++
	rev, last := t.revision, time.Now()
	t.lastPartial = last
	t.mu.Unlock()
	if t.onPartial != nil {
		t.onPartial(Partial{TurnID: t.id, Revision: rev, Text: text})
	}
}

func (t *Turn) timings() map[string]any {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := map[string]any{}
	for name, at := range map[string]time.Time{
		"speech_end_at_ms": t.speechEnd, "asr_final_at_ms": t.asrFinal,
		"llm_first_token_at_ms": t.llmFirst, "tts_first_audio_at_ms": t.ttsFirst,
	} {
		if !at.IsZero() {
			out[name] = at.UnixMilli()
		}
	}
	if !t.speechEnd.IsZero() && !t.ttsFirst.IsZero() {
		out["speech_end_to_first_audio_ms"] = t.ttsFirst.Sub(t.speechEnd).Milliseconds()
	}
	return out
}

func (c *Coordinator) clear(t *Turn) {
	c.mu.Lock()
	if c.active[t.sessionID] == t {
		delete(c.active, t.sessionID)
	}
	c.mu.Unlock()
}
func (t *Turn) clearAndDone() {
	t.coord.clear(t)
	t.doneOnce.Do(func() { close(t.done) })
}
func (c *Coordinator) publish(t *Turn, typ string, payload map[string]any) {
	if !c.cfg.PersistText {
		delete(payload, "text")
	}
	c.bus.Publish(context.WithoutCancel(t.ctx), t.sessionID, typ, t.epoch, payload)
}

// OnSessionStart 实现 live.DanmakuWorker 生命周期接口，并允许新场次建立语音回合。
func (c *Coordinator) OnSessionStart(_ context.Context, sessionID string) {
	c.mu.Lock()
	delete(c.stopping, sessionID)
	c.mu.Unlock()
}
func (c *Coordinator) OnSessionInterrupt(ctx context.Context) bool {
	snap := c.speaker.Snapshot()
	if snap.SessionID == "" {
		return false
	}
	return c.cancelSession(ctx, snap.SessionID, false)
}
func (c *Coordinator) OnSessionStopping(ctx context.Context, sessionID string) {
	c.cancelSession(ctx, sessionID, true)
}
func (c *Coordinator) OnSessionStop(ctx context.Context, sessionID string) {
	c.cancelSession(ctx, sessionID, true)
}
func (c *Coordinator) cancelSession(ctx context.Context, sessionID string, stopping bool) bool {
	c.mu.Lock()
	if stopping {
		c.stopping[sessionID] = true
	}
	t := c.active[sessionID]
	text := c.textActive[sessionID]
	if text != nil {
		delete(c.textActive, sessionID)
	}
	c.mu.Unlock()
	if t != nil {
		t.Cancel("interrupted")
	}
	if text != nil {
		text.cancel()
		snap := c.speaker.Snapshot()
		c.interruptVoiceFor(context.WithoutCancel(ctx), sessionID, snap.Epoch)
	}
	return t != nil || text != nil
}

func (c *Coordinator) interruptVoiceFor(ctx context.Context, sessionID string, epoch int64) {
	if interrupter, ok := c.speaker.(interface {
		InterruptVoiceFor(context.Context, string, int64, bool) bool
	}); ok {
		interrupter.InterruptVoiceFor(ctx, sessionID, epoch, c.cfg.InterruptSongs)
		return
	}
	snap := c.speaker.Snapshot()
	if snap.SessionID != sessionID || snap.Epoch != epoch {
		return
	}
	if interrupter, ok := c.speaker.(interface {
		InterruptVoice(context.Context, bool)
	}); ok {
		interrupter.InterruptVoice(ctx, c.cfg.InterruptSongs)
	}
}

// TextChatActive 向低优先级段子仲裁暴露真实打字聊天回合状态。
func (c *Coordinator) TextChatActive(sessionID string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.textActive[sessionID] != nil
}

// ChatText 将运营文本对话纳入同一场次所有权和按场次记忆；语音活动期拒绝抢占。
func (c *Coordinator) ChatText(ctx context.Context, text string) (string, error) {
	snap := c.speaker.Snapshot()
	if snap.Phase != live.PhaseLive || snap.SessionID == "" {
		return "", ErrNotLive
	}
	textCtx, cancel := context.WithCancel(ctx)
	call := &textTurn{cancel: cancel}
	c.mu.Lock()
	if c.stopping[snap.SessionID] {
		c.mu.Unlock()
		cancel()
		return "", ErrNotLive
	}
	if c.active[snap.SessionID] != nil || c.textActive[snap.SessionID] != nil {
		c.mu.Unlock()
		cancel()
		return "", ErrTurnActive
	}
	c.textActive[snap.SessionID] = call
	c.mu.Unlock()
	defer func() {
		cancel()
		c.mu.Lock()
		if c.textActive[snap.SessionID] == call {
			delete(c.textActive, snap.SessionID)
		}
		c.mu.Unlock()
	}()
	return c.chatter.ChatWithTokenHook(textCtx, snap.SessionID+":operator", text, func(ctx context.Context, sentence string) error {
		if err := textCtx.Err(); err != nil {
			return err
		}
		c.mu.Lock()
		current := c.textActive[snap.SessionID] == call
		c.mu.Unlock()
		if !current {
			return context.Canceled
		}
		currentSnap := c.speaker.Snapshot()
		if currentSnap.Phase != live.PhaseLive || currentSnap.SessionID != snap.SessionID || currentSnap.Epoch != snap.Epoch {
			return ErrNotLive
		}
		_, err := c.speaker.Say(ctx, sentence, "chat")
		return err
	}, nil)
}
