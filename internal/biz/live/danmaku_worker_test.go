package live

import (
	"context"
	"sync"
	"testing"

	"github.com/teamwork/digital-human/pkg/eventbus"
)

// recDMWorker 记录 F24 弹幕批读 worker 的生命周期回调。
type recDMWorker struct {
	mu      sync.Mutex
	started []string
	stopped []string
}

func (w *recDMWorker) OnSessionStart(_ context.Context, sessionID string) {
	w.mu.Lock()
	w.started = append(w.started, sessionID)
	w.mu.Unlock()
}

func (w *recDMWorker) OnSessionStop(_ context.Context, sessionID string) {
	w.mu.Lock()
	w.stopped = append(w.stopped, sessionID)
	w.mu.Unlock()
}

func (w *recDMWorker) snapshot() (started, stopped []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.started...), append([]string(nil), w.stopped...)
}

// TestDanmakuWorkerLifecycle 弹幕批读 worker 随开播启动、停播停止（SetCreditsPoller 同款 Setter 模式）。
func TestDanmakuWorkerLifecycle(t *testing.T) {
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), &recController{})
	w := &recDMWorker{}
	m.SetDanmakuWorker(w)

	sessionID, phase, _, err := m.Start(context.Background(), "p", "a", "")
	if err != nil || phase != PhaseLive {
		t.Fatalf("start: phase=%s err=%v", phase, err)
	}
	started, _ := w.snapshot()
	if len(started) != 1 || started[0] != sessionID {
		t.Fatalf("OnSessionStart = %v, want [%s]", started, sessionID)
	}

	if ok, _, err := m.Stop(context.Background(), "manual"); err != nil || !ok {
		t.Fatalf("stop err=%v", err)
	}
	_, stopped := w.snapshot()
	if len(stopped) != 1 || stopped[0] != sessionID {
		t.Fatalf("OnSessionStop = %v, want [%s]", stopped, sessionID)
	}
}

// TestDanmakuWorkerNilSafe 未注入 worker 时开播/停播不受影响。
func TestDanmakuWorkerNilSafe(t *testing.T) {
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), &recController{})
	if _, phase, _, err := m.Start(context.Background(), "p", "a", ""); err != nil || phase != PhaseLive {
		t.Fatalf("start: phase=%s err=%v", phase, err)
	}
	if ok, _, err := m.Stop(context.Background(), "manual"); err != nil || !ok {
		t.Fatalf("stop err=%v", err)
	}
}
