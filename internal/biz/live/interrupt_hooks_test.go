package live

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/teamwork/digital-human/pkg/eventbus"
)

// TestInterruptInvokesHooks G22：Interrupt 触发 StopPlayback + ClearTTSQueue
// （对齐蓝本 engine.py interrupt/_barge_in：停播放、清 TTS 队列），不动 StopActionAudio。
func TestInterruptInvokesHooks(t *testing.T) {
	var stopPlayback, clearTTS, stopAction atomic.Int32
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), NewFakeController(quietLogger()))
	m.SetInterruptHooks(InterruptHooks{
		StopPlayback:    func(context.Context) { stopPlayback.Add(1) },
		ClearTTSQueue:   func(context.Context) { clearTTS.Add(1) },
		StopActionAudio: func(context.Context) { stopAction.Add(1) },
	})
	m.Interrupt(context.Background())
	if stopPlayback.Load() != 1 || clearTTS.Load() != 1 {
		t.Fatalf("Interrupt hooks: stopPlayback=%d clearTTS=%d, want 1/1", stopPlayback.Load(), clearTTS.Load())
	}
	if stopAction.Load() != 0 {
		t.Fatalf("Interrupt 不得触发 StopActionAudio: %d", stopAction.Load())
	}
}

// TestStopActionsInvokesHook G22：StopActions 触发 StopActionAudio（中断当前动作语音）。
func TestStopActionsInvokesHook(t *testing.T) {
	var stopPlayback, clearTTS, stopAction atomic.Int32
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), NewFakeController(quietLogger()))
	m.SetInterruptHooks(InterruptHooks{
		StopPlayback:    func(context.Context) { stopPlayback.Add(1) },
		ClearTTSQueue:   func(context.Context) { clearTTS.Add(1) },
		StopActionAudio: func(context.Context) { stopAction.Add(1) },
	})
	m.StopActions(context.Background())
	if stopAction.Load() != 1 {
		t.Fatalf("StopActions 必须触发 StopActionAudio: %d", stopAction.Load())
	}
	if stopPlayback.Load() != 0 || clearTTS.Load() != 0 {
		t.Fatalf("StopActions 不得动口播 hooks: %d/%d", stopPlayback.Load(), clearTTS.Load())
	}
}

func TestInterruptVoiceDoesNotStopSharedSongPlayerByDefault(t *testing.T) {
	for _, interruptSongs := range []bool{false, true} {
		t.Run(map[bool]string{false: "speech only", true: "include songs"}[interruptSongs], func(t *testing.T) {
			var stopPlayback, clearTTS atomic.Int32
			m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), NewFakeController(quietLogger()))
			m.SetInterruptHooks(InterruptHooks{
				StopPlayback:  func(context.Context) { stopPlayback.Add(1) },
				ClearTTSQueue: func(context.Context) { clearTTS.Add(1) },
			})
			m.InterruptVoice(context.Background(), interruptSongs)
			wantStops := int32(0)
			if interruptSongs {
				wantStops = 1
			}
			if stopPlayback.Load() != wantStops || clearTTS.Load() != 1 {
				t.Fatalf("stopPlayback=%d clearTTS=%d, want %d/1", stopPlayback.Load(), clearTTS.Load(), wantStops)
			}
		})
	}
}

type interruptSessionWorker struct{ calls atomic.Int32 }

func (w *interruptSessionWorker) OnSessionStart(context.Context, string) {}
func (w *interruptSessionWorker) OnSessionStop(context.Context, string)  {}
func (w *interruptSessionWorker) OnSessionInterrupt(context.Context)     { w.calls.Add(1) }

func TestInterruptCancelsSessionVoiceWorker(t *testing.T) {
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), NewFakeController(quietLogger()))
	worker := &interruptSessionWorker{}
	m.AddSessionHook(worker)
	m.Interrupt(context.Background())
	if worker.calls.Load() != 1 {
		t.Fatalf("OnSessionInterrupt calls = %d, want 1", worker.calls.Load())
	}
}

type handledInterruptSessionWorker struct{ calls atomic.Int32 }

func (w *handledInterruptSessionWorker) OnSessionStart(context.Context, string) {}
func (w *handledInterruptSessionWorker) OnSessionStop(context.Context, string)  {}
func (w *handledInterruptSessionWorker) OnSessionInterrupt(context.Context) bool {
	w.calls.Add(1)
	return true
}

func TestInterruptHandledBySessionWorkerDoesNotDuplicateGlobalInterrupt(t *testing.T) {
	var clearTTS atomic.Int32
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), NewFakeController(quietLogger()))
	worker := &handledInterruptSessionWorker{}
	m.AddSessionHook(worker)
	m.SetInterruptHooks(InterruptHooks{ClearTTSQueue: func(context.Context) { clearTTS.Add(1) }})
	m.Interrupt(context.Background())
	if worker.calls.Load() != 1 || clearTTS.Load() != 0 {
		t.Fatalf("worker=%d global clear=%d, want 1/0", worker.calls.Load(), clearTTS.Load())
	}
}

func TestInterruptVoiceForRejectsStaleSession(t *testing.T) {
	var clearTTS atomic.Int32
	bus := eventbus.New(64)
	m := newPreviewBasicManager(bus, NewMemorySink(64), NewFakeController(quietLogger()))
	m.session = &snapshot{id: "new-session", epoch: 9}
	m.phase = PhaseLive
	m.SetInterruptHooks(InterruptHooks{ClearTTSQueue: func(context.Context) { clearTTS.Add(1) }})
	events, unsubscribe := bus.SubscribeFor("new-session", 0)
	defer unsubscribe()
	if m.InterruptVoiceFor(context.Background(), "old-session", 3, false) {
		t.Fatal("stale session interrupt must be rejected")
	}
	if clearTTS.Load() != 0 {
		t.Fatal("stale session interrupt cleared the new session TTS")
	}
	select {
	case e := <-events:
		t.Fatalf("stale interrupt polluted new session: %+v", e)
	default:
	}
}

// TestInterruptWithoutHooksNoPanic 未装配 hooks 时 Interrupt/StopActions 安全无操作。
func TestInterruptWithoutHooksNoPanic(t *testing.T) {
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), NewFakeController(quietLogger()))
	m.Interrupt(context.Background())
	m.StopActions(context.Background())
}
