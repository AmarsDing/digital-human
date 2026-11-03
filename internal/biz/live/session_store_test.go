package live

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/teamwork/digital-human/pkg/eventbus"
)

// recSessionStore 记录每次 Save，便于断言生命周期节点。
type recSessionStore struct {
	*MemorySessionStore

	mu    sync.Mutex
	saves []SessionRecord
}

func newRecSessionStore() *recSessionStore {
	return &recSessionStore{MemorySessionStore: NewMemorySessionStore()}
}

func (s *recSessionStore) Save(ctx context.Context, r SessionRecord) error {
	s.mu.Lock()
	s.saves = append(s.saves, r)
	s.mu.Unlock()
	return s.MemorySessionStore.Save(ctx, r)
}

func (s *recSessionStore) savedPhases() []Phase {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Phase, 0, len(s.saves))
	for _, r := range s.saves {
		out = append(out, r.Phase)
	}
	return out
}

func TestStartPersistsStartingAndLive(t *testing.T) {
	ctrl := &recController{}
	store := newRecSessionStore()
	m := NewManagerWithStores(eventbus.New(64), NewMemorySink(64), ctrl, store, nil)

	sid, phase, liveID, err := m.Start(context.Background(), "persona-1", "avatar-1", "")
	if err != nil || phase != PhaseLive {
		t.Fatalf("start: phase=%s err=%v", phase, err)
	}
	phases := store.savedPhases()
	if len(phases) != 2 || phases[0] != PhaseStarting || phases[1] != PhaseLive {
		t.Fatalf("persist phases: %v", phases)
	}
	rec, ok := store.Get(sid)
	if !ok {
		t.Fatal("session record missing")
	}
	if rec.LiveID != liveID || rec.PersonaID != "persona-1" || rec.AvatarID != "avatar-1" {
		t.Fatalf("record fields: %+v", rec)
	}
	if rec.Mode != "managed" || rec.RecordingState != "none" || rec.StartedAtMs == 0 || rec.UpdatedAtMs == 0 {
		t.Fatalf("record defaults: %+v", rec)
	}
}

func TestStopPersistsStoppingAndEnded(t *testing.T) {
	ctrl := &recController{}
	store := newRecSessionStore()
	m := NewManagerWithStores(eventbus.New(64), NewMemorySink(64), ctrl, store, nil)

	sid, _, _, err := m.Start(context.Background(), "p", "a", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if _, _, err := m.Stop(context.Background(), "manual"); err != nil {
		t.Fatalf("stop: %v", err)
	}
	phases := store.savedPhases()
	if len(phases) != 4 || phases[2] != PhaseStopping || phases[3] != PhaseEnded {
		t.Fatalf("persist phases: %v", phases)
	}
	rec, ok := store.Get(sid)
	if !ok {
		t.Fatal("session record missing")
	}
	if rec.Phase != PhaseEnded || rec.EndedAtMs == 0 || rec.DurationS < 0 {
		t.Fatalf("ended record: %+v", rec)
	}
}

// Recover 语义（P2-13 结论）：判据是归属而非时间窗——除当前 Manager 在场
// session 外，所有 starting/live/stopping 行都是已死进程的崩溃遗留，哪怕
// 5 秒前刚更新（崩溃前最后一次写入）也必须标记，否则会成为永久幽灵 live。
func TestRecoverMarksAllUnfinishedExceptCurrentSession(t *testing.T) {
	store := NewMemorySessionStore()
	now := time.Now().UnixMilli()
	old := now - 120000

	records := []SessionRecord{
		{ID: "crash-starting", Phase: PhaseStarting, UpdatedAtMs: old},
		{ID: "crash-live", Phase: PhaseLive, UpdatedAtMs: old},
		{ID: "crash-stopping", Phase: PhaseStopping, UpdatedAtMs: old},
		{ID: "idle-old", Phase: PhaseIdle, UpdatedAtMs: old},
		{ID: "ended-old", Phase: PhaseEnded, UpdatedAtMs: old},
		{ID: "fresh-live", Phase: PhaseLive, UpdatedAtMs: now - 5000}, // 刚崩溃 5s 的遗留，同样要标记
	}
	for _, r := range records {
		if err := store.Save(context.Background(), r); err != nil {
			t.Fatalf("seed %s: %v", r.ID, err)
		}
	}

	b := eventbus.New(64)
	m := NewManagerWithStores(b, NewMemorySink(64), &recController{}, store, nil)
	if err := m.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}

	for _, id := range []string{"crash-starting", "crash-live", "crash-stopping", "fresh-live"} {
		r, ok := store.Get(id)
		if !ok || r.Phase != PhaseError || r.ErrorCode != "crash_recovered" {
			t.Fatalf("%s not marked crashed: %+v", id, r)
		}
	}
	for _, id := range []string{"idle-old", "ended-old"} {
		r, _ := store.Get(id)
		if r.Phase == PhaseError {
			t.Fatalf("%s should not be touched: %+v", id, r)
		}
	}

	events, cancel := b.Subscribe(0)
	defer cancel()
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		e := <-events
		if e.Type != "live.crash_recovered" {
			t.Fatalf("unexpected event type %q", e.Type)
		}
		seen[e.SessionID] = true
	}
	if len(seen) != 4 {
		t.Fatalf("want 4 crash events, got %v", seen)
	}
}

// 运行中再入（DB 晚就绪回调）时，本进程正在进行的场次不得被误标。
func TestRecoverSkipsCurrentSession(t *testing.T) {
	store := NewMemorySessionStore()
	b := eventbus.New(64)
	m := NewManagerWithStores(b, NewMemorySink(64), &recController{}, store, nil)

	sid, phase, _, err := m.Start(context.Background(), "", "", "")
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if phase != PhaseLive {
		t.Fatalf("phase=%s", phase)
	}
	// 模拟另一进程的遗留场次（比当前场次更新鲜，时间窗判据下会被漏标）。
	stale := SessionRecord{ID: "other-live", Phase: PhaseLive, UpdatedAtMs: time.Now().UnixMilli()}
	if err := store.Save(context.Background(), stale); err != nil {
		t.Fatal(err)
	}

	if err := m.Recover(context.Background()); err != nil {
		t.Fatalf("recover: %v", err)
	}
	r, ok := store.Get(sid)
	if !ok || r.Phase != PhaseLive {
		t.Fatalf("current session must stay live: %+v", r)
	}
	r, _ = store.Get("other-live")
	if r.Phase != PhaseError || r.ErrorCode != "crash_recovered" {
		t.Fatalf("other-live should be marked crashed: %+v", r)
	}
}

func TestRecoverNoopWithoutStore(t *testing.T) {
	m := NewManager(eventbus.New(8), NewMemorySink(8), &recController{})
	if err := m.Recover(context.Background()); err != nil {
		t.Fatalf("recover without store: %v", err)
	}
}
