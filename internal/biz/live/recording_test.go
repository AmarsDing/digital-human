package live

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/teamwork/digital-human/pkg/eventbus"
)

type stubRecorder struct {
	enabled   bool
	state     string
	starts    atomic.Int32
	stops     atomic.Int32
	startErr  error
	stopState string
	stopErr   error
}

func (r *stubRecorder) Enabled() bool { return r.enabled }
func (r *stubRecorder) State() string { return r.state }
func (r *stubRecorder) OnSessionStart(context.Context, string) error {
	r.starts.Add(1)
	return r.startErr
}
func (r *stubRecorder) OnSessionStop(context.Context, string) (string, error) {
	r.stops.Add(1)
	if r.stopState != "" {
		r.state = r.stopState
	}
	return r.state, r.stopErr
}

// F34 开通路径：Start 随场启动录制，Stop 结束并轮询，persist 记录真实状态。
func TestManagerRecorderLifecycle(t *testing.T) {
	rec := &stubRecorder{enabled: true, state: "recording", stopState: "ready"}
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), &recController{})
	m.SetRecorder(rec)

	if _, _, _, err := m.Start(context.Background(), "p", "a", ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	if rec.starts.Load() != 1 {
		t.Fatalf("recording starts=%d want 1", rec.starts.Load())
	}
	if got := m.RecordingState(); got != "recording" {
		t.Fatalf("recording state=%s want recording", got)
	}
	if _, _, err := m.Stop(context.Background(), "manual"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	if rec.stops.Load() != 1 {
		t.Fatalf("recording stops=%d want 1", rec.stops.Load())
	}
	if got := m.RecordingState(); got != "ready" {
		t.Fatalf("recording state=%s want ready", got)
	}
}

// F34 未开通路径：recorder 报错不阻断开播，状态如实保留。
func TestManagerRecorderFailureDoesNotBlock(t *testing.T) {
	rec := &stubRecorder{enabled: false, state: "unavailable", startErr: errors.New("recording_not_enabled")}
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), &recController{})
	m.SetRecorder(rec)

	if _, phase, _, err := m.Start(context.Background(), "p", "a", ""); err != nil || phase != PhaseLive {
		t.Fatalf("start must succeed even when recording not enabled: phase=%s err=%v", phase, err)
	}
	if got := m.RecordingState(); got != "unavailable" {
		t.Fatalf("recording state=%s want unavailable", got)
	}
}

// 未配置录制时 RecordingState 为 none（真实值）。
func TestManagerNoRecorderReportsNone(t *testing.T) {
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), &recController{})
	if got := m.RecordingState(); got != "none" {
		t.Fatalf("recording state=%s want none", got)
	}
}
