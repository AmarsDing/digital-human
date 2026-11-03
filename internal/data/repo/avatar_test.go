package repo

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3" // enttest sqlite 内存驱动（PG 不可用时的 Ent 验证底座）

	"github.com/teamwork/digital-human/internal/biz/avatar"
	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/ent/enttest"
	"github.com/teamwork/digital-human/internal/data/ent/setting"
)

func newTestLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newEntTestClient(t *testing.T) *ent.Client {
	t.Helper()
	client := enttest.Open(t, dialect.SQLite, "file:"+uuid.NewString()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func seedAsset(t *testing.T, store *EntAvatarStore, id, sha string) avatar.Asset {
	t.Helper()
	a := avatar.Asset{ID: id, SHA256: sha, Kind: "image", Name: id + ".png", Path: "storage/images/" + id + ".png"}
	if err := store.PutAsset(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	return a
}

// TestEntAvatarStorePrimitives 十原语逐字对齐验证（v3 §7.1）：CRUD/CAS/排他 active/
// 软删 slug 复用/refs 有序与删后重排/FindAssetBySHA256。
func TestEntAvatarStorePrimitives(t *testing.T) {
	ctx := context.Background()
	store := NewEntAvatarStore(newEntTestClient(t))

	// Create：revision=1 强制，is_active=true → 排他。
	a1 := avatar.Avatar{ID: "a1", Slug: "s1", Name: "一号", IsActive: true, ViduSync: avatar.ViduSyncState{State: "idle"}}
	if err := store.Create(ctx, a1); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.Get(ctx, "a1")
	if err != nil || !ok {
		t.Fatalf("get = %v %v", ok, err)
	}
	if got.Revision != 1 || !got.IsActive || got.DeletedAtMs != 0 || got.UpdatedAtMs == 0 {
		t.Fatalf("create 落库 = %+v", got)
	}
	// slug 冲突。
	if err := store.Create(ctx, avatar.Avatar{ID: "a2", Slug: "s1", Name: "x"}); !errors.Is(err, avatar.ErrSlugConflict) {
		t.Fatalf("slug 冲突 = %v", err)
	}
	// 排他：a2 active → a1 清。
	if err := store.Create(ctx, avatar.Avatar{ID: "a2", Slug: "s2", Name: "二号", IsActive: true}); err != nil {
		t.Fatal(err)
	}
	got, _, _ = store.Get(ctx, "a1")
	if got.IsActive {
		t.Fatal("排他 active 失败")
	}

	// GetBySlug：命中未删行。
	if _, ok, _ := store.GetBySlug(ctx, "s2"); !ok {
		t.Fatal("GetBySlug 未命中")
	}

	// CAS：revision 撞 → ErrRevisionConflict；对 → revision+1。
	got, _, _ = store.Get(ctx, "a2")
	got.Name = "二号改"
	if err := store.UpdateWithRevision(ctx, got, 99); !errors.Is(err, avatar.ErrRevisionConflict) {
		t.Fatalf("CAS 撞 = %v", err)
	}
	got.Greeting = "hi"
	got.Tagline = "tag"
	if err := store.UpdateWithRevision(ctx, got, 1); err != nil {
		t.Fatal(err)
	}
	got2, _, _ := store.Get(ctx, "a2")
	if got2.Revision != 2 || got2.Name != "二号改" || got2.Greeting != "hi" || got2.Tagline != "tag" {
		t.Fatalf("CAS 后 = %+v", got2)
	}
	// 已软删行 CAS → ErrNotFound。
	if err := store.SoftDelete(ctx, "a1"); err != nil {
		t.Fatal(err)
	}
	if err := store.UpdateWithRevision(ctx, avatar.Avatar{ID: "a1", Slug: "s1", Name: "z"}, 1); !errors.Is(err, avatar.ErrNotFound) {
		t.Fatalf("删后 CAS = %v", err)
	}

	// 软删语义：Get 不可见、slug 可复用、is_active 清除。
	if _, ok, _ := store.Get(ctx, "a1"); ok {
		t.Fatal("软删后 Get 仍可见")
	}
	if _, ok, _ := store.GetBySlug(ctx, "s1"); ok {
		t.Fatal("软删后 GetBySlug 仍可见")
	}
	if err := store.Create(ctx, avatar.Avatar{ID: "a3", Slug: "s1", Name: "复用"}); err != nil {
		t.Fatalf("软删 slug 复用 = %v", err)
	}
	// 重复软删 → ErrNotFound。
	if err := store.SoftDelete(ctx, "a1"); !errors.Is(err, avatar.ErrNotFound) {
		t.Fatalf("重复软删 = %v", err)
	}

	// List：分页/总数/include_deleted。
	items, total, err := store.List(ctx, 10, 0, false)
	if err != nil || total != 2 || len(items) != 2 {
		t.Fatalf("list = %d %d %v", total, len(items), err)
	}
	_, total, _ = store.List(ctx, 10, 0, true)
	if total != 3 {
		t.Fatalf("includeDeleted total = %d", total)
	}
	items, total, _ = store.List(ctx, 1, 1, false)
	if total != 2 || len(items) != 1 {
		t.Fatalf("分页 = %d %d", total, len(items))
	}

	// SetActive：排他；不存在/已删 → ErrNotFound。
	if err := store.SetActive(ctx, "a3"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = store.Get(ctx, "a2")
	if got.IsActive {
		t.Fatal("SetActive 未清其他")
	}
	if err := store.SetActive(ctx, "a1"); !errors.Is(err, avatar.ErrNotFound) {
		t.Fatalf("SetActive 已删 = %v", err)
	}

	// refs：有序（ord, asset_id 升序）、≤3、幂等改 ord、RemoveRef 重排。
	seedAsset(t, store, "r1", "sha1")
	seedAsset(t, store, "r2", "sha2")
	seedAsset(t, store, "r3", "sha3")
	seedAsset(t, store, "r4", "sha4")
	if err := store.AddRef(ctx, "a3", "r2", 2); err != nil {
		t.Fatal(err)
	}
	if err := store.AddRef(ctx, "a3", "r1", 0); err != nil {
		t.Fatal(err)
	}
	got, _, _ = store.Get(ctx, "a3")
	if len(got.Refs) != 2 || got.Refs[0].AssetID != "r1" || got.Refs[1].AssetID != "r2" {
		t.Fatalf("refs 有序 = %+v", got.Refs)
	}
	if got.Refs[0].Asset.SHA256 != "sha1" || got.Refs[0].Asset.Path == "" {
		t.Fatalf("refs 素材联查 = %+v", got.Refs[0])
	}
	if err := store.AddRef(ctx, "a3", "r2", 1); err != nil { // 幂等改 ord
		t.Fatal(err)
	}
	if err := store.AddRef(ctx, "a3", "r3", 2); err != nil {
		t.Fatal(err)
	}
	if err := store.AddRef(ctx, "a3", "r4", 3); !errors.Is(err, avatar.ErrTooManyRefs) {
		t.Fatalf("第 4 张 = %v", err)
	}
	// 引用不存在素材/档案 → ErrNotFound。
	if err := store.AddRef(ctx, "a3", "nope", 0); !errors.Is(err, avatar.ErrNotFound) {
		t.Fatalf("ref 素材不存在 = %v", err)
	}
	if err := store.AddRef(ctx, "nope", "r1", 0); !errors.Is(err, avatar.ErrNotFound) {
		t.Fatalf("ref 档案不存在 = %v", err)
	}
	if err := store.RemoveRef(ctx, "a3", "r1"); err != nil {
		t.Fatal(err)
	}
	got, _, _ = store.Get(ctx, "a3")
	if len(got.Refs) != 2 {
		t.Fatalf("RemoveRef 后 = %+v", got.Refs)
	}
	for i, r := range got.Refs { // 删后重排 0..n-1
		if r.Ord != i {
			t.Fatalf("删后 ord 重排 = %+v", got.Refs)
		}
	}

	// FindAssetBySHA256 + GetAsset。
	hit, ok, err := store.FindAssetBySHA256(ctx, "sha2")
	if err != nil || !ok || hit.ID != "r2" {
		t.Fatalf("FindAssetBySHA256 = %v %v", ok, err)
	}
	if _, ok, _ := store.FindAssetBySHA256(ctx, "nope"); ok {
		t.Fatal("FindAssetBySHA256 误命中")
	}
	if _, ok, _ := store.GetAsset(ctx, "r1"); !ok {
		t.Fatal("GetAsset 未命中")
	}
}

// TestMigrateAvatarsIdempotent §7.2 迁移：回填映射 + __meta 幂等 + 重启重入。
func TestMigrateAvatarsIdempotent(t *testing.T) {
	ctx := context.Background()
	client := newEntTestClient(t)
	put := func(id string, kv map[string]any) {
		if _, err := client.Setting.Create().SetID(id).SetValueJSON(kv).
			SetUpdatedAtMs(time.Now().UnixMilli()).Save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	put("session", map[string]any{"avatar_id": "vidu-av-1", "image_uri": "vidu://img-1"})
	put("assets", map[string]any{"avatar_id": "vidu-av-2"}) // 与 session 冲突 → session 优先
	put("voice", map[string]any{"voice_id": "voice-a"})
	put("action_library", map[string]any{"library_id": "lib-1"})
	put("persona", map[string]any{"content": "你是默认人设"})
	put("llm", map[string]any{"system": "死配置原值"}) // 不迁移，仅日志

	logger := newTestLogger()
	if err := MigrateAvatars(ctx, client, logger); err != nil {
		t.Fatal(err)
	}

	// 默认档案回填：session 优先的 vidu_avatar_id、image_uri、voice、library、content。
	store := NewEntAvatarStore(client)
	got, ok, err := store.GetBySlug(ctx, "default")
	if err != nil || !ok {
		t.Fatalf("默认档案缺失: %v %v", ok, err)
	}
	if !got.IsActive || got.ViduAvatarID != "vidu-av-1" || got.ViduImageURI != "vidu://img-1" ||
		got.VoiceID != "voice-a" || got.ActionLibraryID != "lib-1" || got.SystemPrompt != "你是默认人设" {
		t.Fatalf("默认档案回填 = %+v", got)
	}
	// __meta 幂等标记。
	row, err := client.Setting.Get(ctx, "__meta")
	if err != nil {
		t.Fatal(err)
	}
	if done, _ := row.ValueJSON["avatar_migration_v1"].(bool); !done {
		t.Fatalf("缺幂等标记: %v", row.ValueJSON)
	}
	// 重启重入：再次执行不重复建档案、不报错。
	if err := MigrateAvatars(ctx, client, logger); err != nil {
		t.Fatal(err)
	}
	_, total, _ := store.List(ctx, 10, 0, false)
	if total != 1 {
		t.Fatalf("重入后档案数 = %d", total)
	}
	// 标记被删但档案已存在 → 不重建（档案行为天然幂等屏障）。
	if _, err := client.Setting.Delete().Where(setting.ID("__meta")).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := MigrateAvatars(ctx, client, logger); err != nil {
		t.Fatal(err)
	}
	_, total, _ = store.List(ctx, 10, 0, false)
	if total != 1 {
		t.Fatalf("标记丢失重入后档案数 = %d", total)
	}
}

// TestMigrateAvatarsActiveID persona.active_id → 置既有档案 is_active；无匹配忽略不报错。
func TestMigrateAvatarsActiveID(t *testing.T) {
	ctx := context.Background()
	client := newEntTestClient(t)
	store := NewEntAvatarStore(client)
	if err := store.Create(ctx, avatar.Avatar{ID: "p1", Slug: "s1", Name: "既有"}); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Setting.Create().SetID("persona").
		SetValueJSON(map[string]any{"active_id": "p1"}).
		SetUpdatedAtMs(time.Now().UnixMilli()).Save(ctx); err != nil {
		t.Fatal(err)
	}
	if err := MigrateAvatars(ctx, client, newTestLogger()); err != nil {
		t.Fatal(err)
	}
	got, _, _ := store.Get(ctx, "p1")
	if !got.IsActive {
		t.Fatal("persona.active_id 未置 active")
	}
	// 无匹配 active_id：忽略记日志，不报错。
	if _, err := client.Setting.Update().Where(setting.ID("persona")).
		SetValueJSON(map[string]any{"active_id": "ghost"}).Save(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := client.Setting.Delete().Where(setting.ID("__meta")).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if err := MigrateAvatars(ctx, client, newTestLogger()); err != nil {
		t.Fatalf("无匹配 active_id 迁移 = %v", err)
	}
}

// TestEntSessionStoreNilSnapshotKeeps repo 侧 nil 不清 config_snapshot 契约（§7.3-1）。
func TestEntSessionStoreNilSnapshotKeeps(t *testing.T) {
	ctx := context.Background()
	s := NewEntSessionStore(newEntTestClient(t))
	snap := map[string]any{"v": 1, "model": "vidu-s2"}
	rec := live.SessionRecord{ID: "s1", Phase: live.PhaseLive, ConfigSnapshot: snap, UpdatedAtMs: time.Now().UnixMilli()}
	if err := s.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	// nil 快照 Update 不得清列。
	rec.Phase = live.PhaseStopping
	rec.ConfigSnapshot = nil
	if err := s.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.GetRecord(ctx, "s1")
	if err != nil || !ok {
		t.Fatalf("get = %v %v", ok, err)
	}
	if got.ConfigSnapshot == nil || got.ConfigSnapshot["model"] != "vidu-s2" {
		t.Fatalf("nil 快照不得清库: %+v", got.ConfigSnapshot)
	}
	// 非 nil 允许覆盖。
	rec.Phase = live.PhaseEnded
	rec.ConfigSnapshot = map[string]any{"v": 1, "model": "m2"}
	if err := s.Save(ctx, rec); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.GetRecord(ctx, "s1")
	if got.ConfigSnapshot["model"] != "m2" {
		t.Fatalf("非 nil 覆盖: %+v", got.ConfigSnapshot)
	}
}
