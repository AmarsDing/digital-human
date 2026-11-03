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
	m := NewManager(eventbus.New(64), NewMemorySink(64), NewFakeController(quietLogger()))
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
	m := NewManager(eventbus.New(64), NewMemorySink(64), NewFakeController(quietLogger()))
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

// TestInterruptWithoutHooksNoPanic 未装配 hooks 时 Interrupt/StopActions 安全无操作。
func TestInterruptWithoutHooksNoPanic(t *testing.T) {
	m := NewManager(eventbus.New(64), NewMemorySink(64), NewFakeController(quietLogger()))
	m.Interrupt(context.Background())
	m.StopActions(context.Background())
}
