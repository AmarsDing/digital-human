package live

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/teamwork/digital-human/pkg/eventbus"
)

// 不同 key 但已 live：必须在同一临界区内 stop(reconnect) 再开，而不是返回 409 或并存两场。
func TestRestartDifferentKeyWhileLiveStopsReconnect(t *testing.T) {
	ctrl := &recController{}
	m := newPreviewManager(eventbus.New(64), NewMemorySink(64), ctrl,
		nil, NewMemoryIdempotencyStore(time.Hour), nil)
	ctx := context.Background()

	sid1, phase1, _, err := m.StartIdempotent(ctx, "k-1", "p", "a", "")
	if err != nil || phase1 != PhaseLive {
		t.Fatalf("first start: phase=%s err=%v", phase1, err)
	}
	sid2, phase2, _, err := m.StartIdempotent(ctx, "k-2", "p", "a", "")
	if err != nil || phase2 != PhaseLive {
		t.Fatalf("restart: phase=%s err=%v", phase2, err)
	}
	if sid1 == sid2 {
		t.Fatalf("不同 key 必须创建新场次，got %s", sid1)
	}
	if ctrl.started != 2 {
		t.Fatalf("controller start=%d want 2", ctrl.started)
	}
	if len(ctrl.stops) != 1 || ctrl.stops[0] != "reconnect" {
		t.Fatalf("want one stop(reconnect) before restart, got %v", ctrl.stops)
	}
	if snap := m.Snapshot(); snap.SessionID != sid2 || snap.Phase != PhaseLive {
		t.Fatalf("final snapshot=%q/%s want %q/live", snap.SessionID, snap.Phase, sid2)
	}
}

// 并发不同 key：opMu 串行化后最终只能剩一场，前一场都以 stop(reconnect) 收尾。
func TestConcurrentDifferentKeyKeepsExactlyOneLiveSession(t *testing.T) {
	ctrl := &counterController{}
	m := newPreviewManager(eventbus.New(64), NewMemorySink(64), ctrl,
		nil, NewMemoryIdempotencyStore(time.Hour), nil)
	ctx := context.Background()

	const n = 10
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			key := "k-" + string(rune('a'+i))
			if _, _, _, err := m.StartIdempotent(ctx, key, "p", "a", ""); err != nil {
				t.Errorf("start %s: %v", key, err)
			}
		}(i)
	}
	wg.Wait()

	started, stops := ctrl.counts()
	if started != n {
		t.Fatalf("controller start=%d want %d", started, n)
	}
	if stops != n-1 {
		t.Fatalf("controller stop=%d want %d (each restart stops previous)", stops, n-1)
	}
	if snap := m.Snapshot(); snap.Phase != PhaseLive || snap.SessionID == "" {
		t.Fatalf("final phase=%s session=%q, want one live session", snap.Phase, snap.SessionID)
	}
}

// 控制器未实现 StatusProvider 时，运行态必须是 unknown/unconfigured，不能伪造 owner/connected。
func TestRuntimeStatusUnknownWithoutStatusProvider(t *testing.T) {
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), &recController{})
	rt := m.RuntimeStatus(context.Background())
	if rt.FeedKnown || rt.FeedOwner || rt.FeedKind != "unknown" {
		t.Fatalf("feed must be unknown: %+v", rt)
	}
	if rt.WSKnown || rt.WSState != "unknown" {
		t.Fatalf("ws must be unknown: %+v", rt)
	}
	if rt.RTCKnown || rt.RTCMode != "unconfigured" {
		t.Fatalf("rtc must be unconfigured: %+v", rt)
	}
}

// WS 状态取真实连接计数：有连接才 connected，断开后回落 disconnected。
func TestWSStatusTracksRealConnections(t *testing.T) {
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), &recController{})

	if st := m.WSStatusFor("s-1"); st.WSState != "disconnected" || !st.WSKnown {
		t.Fatalf("no connection: %+v", st)
	}
	m.WSConnected("s-1")
	if st := m.WSStatusFor("s-1"); st.WSState != "connected" {
		t.Fatalf("after connect: %+v", st)
	}
	if st := m.WSStatusFor("s-2"); st.WSState != "disconnected" {
		t.Fatalf("other session must stay disconnected: %+v", st)
	}
	m.WSDisconnected("s-1")
	if st := m.WSStatusFor("s-1"); st.WSState != "disconnected" {
		t.Fatalf("after disconnect: %+v", st)
	}
}

// SnapshotFor：session 不匹配必须报错；transcript_since 必须真正分页。
func TestSnapshotForSessionAndTranscriptPaging(t *testing.T) {
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), &recController{})
	ctx := context.Background()
	sid, phase, _, err := m.Start(ctx, "p", "a", "")
	if err != nil || phase != PhaseLive {
		t.Fatalf("start: phase=%s err=%v", phase, err)
	}

	m.mu.Lock()
	m.session.transcript = []string{"第一句", "第二句", "第三句"}
	m.mu.Unlock()

	got, err := m.SnapshotFor(sid, 1)
	if err != nil {
		t.Fatalf("own session: %v", err)
	}
	if len(got.Transcript) != 2 || got.Transcript[0] != "第二句" {
		t.Fatalf("transcript_since=1 want 2 items from 第二句, got %v", got.Transcript)
	}

	if _, err := m.SnapshotFor("other-session", 0); err == nil {
		t.Fatal("跨 session 读取状态必须返回 ErrSessionMismatch")
	} else if !errors.Is(err, ErrSessionMismatch) {
		t.Fatalf("want ErrSessionMismatch, got %v", err)
	}
}

// 结束后的 duration 必须冻结在 endedAt，而不是随查询时间继续增长。
func TestSnapshotDurationFrozenAfterStop(t *testing.T) {
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), &recController{})
	if _, _, _, err := m.Start(context.Background(), "p", "a", ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	time.Sleep(500 * time.Millisecond)
	if _, _, err := m.Stop(context.Background(), "manual"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	snap := m.Snapshot()
	if snap.Phase != PhaseEnded {
		t.Fatalf("phase=%s want ended", snap.Phase)
	}
	first := snap.DurationS
	time.Sleep(1200 * time.Millisecond)
	if second := m.Snapshot().DurationS; second != first {
		t.Fatalf("duration not frozen: first=%ds second=%ds", first, second)
	}
}

// 事件游标按 session_id 过滤并真正分页：after_seq/limit 必须生效，且不串场次。
func TestListEventsFiltersBySessionAndPaginates(t *testing.T) {
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), &recController{})
	ctx := context.Background()

	sid1, _, _, err := m.Start(ctx, "p", "a", "")
	if err != nil {
		t.Fatalf("start s1: %v", err)
	}
	m.Interrupt(ctx)
	if _, _, _, err := m.Start(ctx, "p", "a", ""); err != nil { // 触发 stop(reconnect) 并换场次
		t.Fatalf("restart: %v", err)
	}

	all, err := m.ListEvents(ctx, "", 0, 100)
	if err != nil || len(all) == 0 {
		t.Fatalf("all events: n=%d err=%v", len(all), err)
	}

	rows, err := m.ListEvents(ctx, sid1, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) == 0 {
		t.Fatal("s1 事件不应为空")
	}
	for _, e := range rows {
		if e.SessionID != sid1 {
			t.Fatalf("串场次事件: %+v", e)
		}
	}

	// after_seq + limit 分页：第二页只能拿到更晚的事件。
	page1, err := m.ListEvents(ctx, sid1, 0, 2)
	if err != nil || len(page1) != 2 {
		t.Fatalf("page1: n=%d err=%v", len(page1), err)
	}
	page2, err := m.ListEvents(ctx, sid1, page1[len(page1)-1].Seq, 2)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range page2 {
		if e.Seq <= page1[len(page1)-1].Seq {
			t.Fatalf("分页游标失效: seq=%d after=%d", e.Seq, page1[len(page1)-1].Seq)
		}
		if e.SessionID != sid1 {
			t.Fatalf("第二页串场次: %+v", e)
		}
	}
}
