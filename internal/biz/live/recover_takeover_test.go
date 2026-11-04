package live

import (
	"context"
	"testing"

	"github.com/teamwork/digital-human/pkg/eventbus"
)

// 故障注入：旧进程开播到 live 后崩溃（不 Stop，直接弃用 Manager），新进程
// 以新 Manager 接管同一 store。Recover 必须把旧场次标为 error(crash_recovered)
// 并发布事件，且绝不自动恢复喂流；新 Manager 自己的在场场次不得被误标。
func TestRecoverAfterCrashNewManagerTakesOver(t *testing.T) {
	ctx := context.Background()
	store := NewMemorySessionStore()

	// 旧进程：开播成功后"崩溃"——不调 Stop，Manager 直接废弃。
	bus1 := eventbus.New(64)
	ctrl1 := &recController{}
	m1 := newPreviewManager(bus1, NewMemorySink(64), ctrl1, store, nil, nil)
	sid1, phase, _, err := m1.Start(ctx, "p", "a", "")
	if err != nil || phase != PhaseLive {
		t.Fatalf("old manager start: phase=%s err=%v", phase, err)
	}
	// 崩溃点：无 Stop。

	// 新进程：新 bus/sink/controller，接管同一 store，先开自己的在场场次。
	bus2 := eventbus.New(64)
	sink2 := NewMemorySink(64)
	ctrl2 := &recController{}
	m2 := newPreviewManager(bus2, sink2, ctrl2, store, nil, nil)
	sid2, phase2, _, err := m2.Start(ctx, "p", "a", "")
	if err != nil || phase2 != PhaseLive {
		t.Fatalf("new manager start: phase=%s err=%v", phase2, err)
	}

	if err := m2.Recover(ctx); err != nil {
		t.Fatalf("recover: %v", err)
	}

	// 旧场次被标 crash_recovered。
	r1, ok := store.Get(sid1)
	if !ok || r1.Phase != PhaseError || r1.ErrorCode != "crash_recovered" {
		t.Fatalf("旧场次未标 crash_recovered: %+v", r1)
	}
	if r1.EndedAtMs == 0 {
		t.Fatalf("旧场次缺 ended_at: %+v", r1)
	}

	// crash 事件已发布且落 sink（REST/WS 同源）。
	events, cancel := bus2.SubscribeFor(sid1, 0)
	defer cancel()
	e := <-events
	if e.Type != "live.crash_recovered" || e.SessionID != sid1 {
		t.Fatalf("crash 事件: %+v", e)
	}
	if e.Payload["error_code"] != "crash_recovered" || e.Payload["from_phase"] != string(PhaseLive) {
		t.Fatalf("crash 事件 payload: %+v", e.Payload)
	}
	// crash 事件已发布且落 sink（REST/WS 同源）。Recover 显式 Append 与 bus
	// appender 可能各写一次，同一 event_id 幂等（PG 唯一约束去重），按 event_id 去重后必须唯一。
	listed, err := sink2.List(ctx, sid1, 0, 10)
	if err != nil || len(listed) == 0 {
		t.Fatalf("crash 事件未落 sink: %v %+v", err, listed)
	}
	uniq := map[string]bool{}
	for _, le := range listed {
		if le.Type != "live.crash_recovered" || le.SessionID != sid1 {
			t.Fatalf("sink 中混入其他事件: %+v", le)
		}
		uniq[le.EventID] = true
	}
	if len(uniq) != 1 {
		t.Fatalf("sink 中 crash 事件 event_id 不唯一: %v", uniq)
	}

	// 新 Manager 在场场次不被误标，自身状态不受影响。
	r2, ok := store.Get(sid2)
	if !ok || r2.Phase != PhaseLive || r2.ErrorCode != "" {
		t.Fatalf("在场场次被误标: %+v", r2)
	}
	if snap := m2.Snapshot(); snap.Phase != PhaseLive || snap.SessionID != sid2 {
		t.Fatalf("新 Manager 状态异常: %+v", snap)
	}

	// 绝不自动恢复喂流：Recover 不得触发任何引擎 Start（两侧各只有开播那一次）。
	if ctrl1.started != 1 || ctrl2.started != 1 {
		t.Fatalf("Recover 不得触发引擎 Start: old=%d new=%d", ctrl1.started, ctrl2.started)
	}
}
