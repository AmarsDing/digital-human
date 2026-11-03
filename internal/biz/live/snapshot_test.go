package live

import (
	"context"
	"sync"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/avatar"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

// recStore 逐次记录 Save 调用的 phase 与快照（快照三细节证据用，v3 §7.3）。
type recStore struct {
	mu    sync.Mutex
	calls []SessionRecord
}

func (s *recStore) Save(_ context.Context, r SessionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.calls = append(s.calls, r)
	return nil
}

func (s *recStore) ListStale(context.Context, int64) ([]SessionRecord, error) { return nil, nil }
func (s *recStore) MarkCrashed(context.Context, string) error                 { return nil }
func (s *recStore) List(context.Context, int, int, string) ([]SessionRecord, error) {
	return nil, nil
}
func (s *recStore) Count(context.Context, string) (int, error) { return 0, nil }
func (s *recStore) GetRecord(context.Context, string) (SessionRecord, bool, error) {
	return SessionRecord{}, false, nil
}

func (s *recStore) snapshotAt(phase Phase) (map[string]any, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, r := range s.calls {
		if r.Phase == phase {
			return r.ConfigSnapshot, true
		}
	}
	return nil, false
}

// TestConfigSnapshotWrittenOnceAtLive 快照三细节（v3 §7.3）：
// 1) starting persist 快照必 nil；2) persist(PhaseLive) 一次性写入 {"v":1,...}；
// 3) 后续 phase persist（stopping/ended）不清快照。
func TestConfigSnapshotWrittenOnceAtLive(t *testing.T) {
	ctx := context.Background()
	store := avatar.NewMemoryStore()
	if err := store.Create(ctx, avatar.Avatar{
		ID: "p1", Slug: "s1", Name: "小雅", VoiceID: "voice-a", Greeting: "档案问候",
		ViduImageURI: "vidu://img", ActionLibraryID: "lib-1",
	}); err != nil {
		t.Fatal(err)
	}
	rec := &recStore{}
	ctrl := NewFakeController(quietLogger())
	m := NewManagerWithStores(eventbus.New(64), NewMemorySink(64), ctrl, rec, nil, avatar.NewService(store))

	if _, phase, _, err := m.Start(ctx, "p1", "", ""); err != nil || phase != PhaseLive {
		t.Fatalf("start: phase=%s err=%v", phase, err)
	}

	// 细节 1：starting persist 快照必 nil。
	snap, ok := rec.snapshotAt(PhaseStarting)
	if !ok {
		t.Fatal("缺 starting persist 记录")
	}
	if snap != nil {
		t.Fatalf("starting persist 快照必须 nil: %+v", snap)
	}

	// 细节 2：live persist 有快照，内嵌 {"v":1} 与白名单键。
	snap, ok = rec.snapshotAt(PhaseLive)
	if !ok || snap == nil {
		t.Fatalf("live persist 快照缺失: ok=%v snap=%+v", ok, snap)
	}
	if snap["v"] != 1 {
		t.Fatalf("快照版本: %+v", snap)
	}
	if snap["persona_id"] != "p1" || snap["persona_name"] != "小雅" || snap["persona_revision"] != int64(1) {
		t.Fatalf("快照 persona 键: %+v", snap)
	}
	if snap["voice_id"] != "voice-a" || snap["vidu_image_uri"] != "vidu://img" ||
		snap["action_library_id"] != "lib-1" || snap["greeting"] != "档案问候" || snap["model"] != "vidu-s2" {
		t.Fatalf("快照开播键: %+v", snap)
	}

	// 细节 3：stopping/ended persist 不清快照（携带同一份）。
	if _, _, err := m.Stop(ctx, "test"); err != nil {
		t.Fatal(err)
	}
	for _, ph := range []Phase{PhaseStopping, PhaseEnded} {
		snap, ok = rec.snapshotAt(ph)
		if !ok || snap == nil || snap["v"] != 1 || snap["persona_id"] != "p1" {
			t.Fatalf("%s persist 快照必须保留: ok=%v snap=%+v", ph, ok, snap)
		}
	}
}

// TestConfigSnapshotLegacyPath 旧路径（无 resolver/无档案）：live 快照仍写，
// 只含入参可得键（v/model/greeting），persona 键缺失。
func TestConfigSnapshotLegacyPath(t *testing.T) {
	ctx := context.Background()
	rec := &recStore{}
	ctrl := NewFakeController(quietLogger())
	m := NewManagerWithStores(eventbus.New(64), NewMemorySink(64), ctrl, rec, nil, nil)

	if _, phase, _, err := m.Start(ctx, "", "", "旧路径问候"); err != nil || phase != PhaseLive {
		t.Fatalf("start: phase=%s err=%v", phase, err)
	}
	snap, ok := rec.snapshotAt(PhaseLive)
	if !ok || snap == nil {
		t.Fatalf("live 快照缺失: ok=%v", ok)
	}
	if snap["v"] != 1 || snap["model"] != "vidu-s2" || snap["greeting"] != "旧路径问候" {
		t.Fatalf("旧路径快照: %+v", snap)
	}
	if _, has := snap["persona_id"]; has {
		t.Fatalf("无档案不得写 persona_id: %+v", snap)
	}
}

// TestMemorySessionStoreNilSnapshotKeeps 存储侧 nil 不清快照契约（v3 §7.3-1）。
func TestMemorySessionStoreNilSnapshotKeeps(t *testing.T) {
	ctx := context.Background()
	s := NewMemorySessionStore()
	snap := map[string]any{"v": 1, "model": "vidu-s2"}
	if err := s.Save(ctx, SessionRecord{ID: "x", Phase: PhaseLive, ConfigSnapshot: snap}); err != nil {
		t.Fatal(err)
	}
	// nil 快照 Save 不得清空已存快照。
	if err := s.Save(ctx, SessionRecord{ID: "x", Phase: PhaseStopping}); err != nil {
		t.Fatal(err)
	}
	got, ok := s.Get("x")
	if !ok || got.ConfigSnapshot == nil || got.ConfigSnapshot["model"] != "vidu-s2" {
		t.Fatalf("nil 快照不得清库: %+v", got)
	}
	// 非 nil 允许覆盖。
	snap2 := map[string]any{"v": 1, "model": "m2"}
	if err := s.Save(ctx, SessionRecord{ID: "x", Phase: PhaseEnded, ConfigSnapshot: snap2}); err != nil {
		t.Fatal(err)
	}
	got, _ = s.Get("x")
	if got.ConfigSnapshot["model"] != "m2" {
		t.Fatalf("非 nil 覆盖: %+v", got)
	}
}
