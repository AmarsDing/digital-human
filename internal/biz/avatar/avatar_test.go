package avatar

import (
	"context"
	"errors"
	"testing"
)

func seed(t *testing.T) (*MemoryAvatarStore, context.Context) {
	t.Helper()
	m := NewMemoryStore()
	ctx := context.Background()
	for _, a := range []Asset{
		{ID: "asset-1", SHA256: "sha-1", Kind: "ref", URI: "vidu://ref-1"},
		{ID: "asset-2", SHA256: "sha-2", Kind: "ref", URI: "vidu://ref-2"},
		{ID: "asset-3", SHA256: "sha-3", Kind: "ref", URI: "vidu://ref-3"},
		{ID: "asset-4", SHA256: "sha-4", Kind: "ref", URI: "vidu://ref-4"},
	} {
		if err := m.PutAsset(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	return m, ctx
}

func TestCreateSlugConflictAndSoftDeleteReuse(t *testing.T) {
	m, ctx := seed(t)
	a := Avatar{ID: "p1", Slug: "xiao-ya", Name: "小雅"}
	if err := m.Create(ctx, a); err != nil {
		t.Fatal(err)
	}
	if err := m.Create(ctx, Avatar{ID: "p2", Slug: "xiao-ya"}); !errors.Is(err, ErrSlugConflict) {
		t.Fatalf("slug 冲突: %v", err)
	}
	if err := m.SoftDelete(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	// 软删后 slug 可复用；Get 对已删行视为不存在。
	if err := m.Create(ctx, Avatar{ID: "p2", Slug: "xiao-ya"}); err != nil {
		t.Fatalf("软删后 slug 应可复用: %v", err)
	}
	if _, ok, _ := m.Get(ctx, "p1"); ok {
		t.Fatal("已软删档案 Get 应不存在")
	}
}

func TestUpdateWithRevisionCAS(t *testing.T) {
	m, ctx := seed(t)
	if err := m.Create(ctx, Avatar{ID: "p1", Slug: "s1", Name: "v1"}); err != nil {
		t.Fatal(err)
	}
	got, _, _ := m.Get(ctx, "p1")
	if got.Revision != 1 {
		t.Fatalf("initial revision=%d want 1", got.Revision)
	}
	got.Name = "v2"
	if err := m.UpdateWithRevision(ctx, got, 99); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("base 不符必须 CAS 冲突: %v", err)
	}
	if err := m.UpdateWithRevision(ctx, got, 1); err != nil {
		t.Fatal(err)
	}
	after, _, _ := m.Get(ctx, "p1")
	if after.Revision != 2 || after.Name != "v2" {
		t.Fatalf("CAS 后: %+v", after)
	}
	if err := m.UpdateWithRevision(ctx, after, 1); !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("旧 base 必须冲突: %v", err)
	}
	if err := m.UpdateWithRevision(ctx, Avatar{ID: "ghost"}, 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在: %v", err)
	}
}

func TestSetActiveExclusive(t *testing.T) {
	m, ctx := seed(t)
	_ = m.Create(ctx, Avatar{ID: "p1", Slug: "s1"})
	_ = m.Create(ctx, Avatar{ID: "p2", Slug: "s2"})
	_ = m.Create(ctx, Avatar{ID: "p3", Slug: "s3", IsActive: true})
	if err := m.SetActive(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[string]bool{"p1": true, "p2": false, "p3": false} {
		a, _, _ := m.Get(ctx, id)
		if a.IsActive != want {
			t.Fatalf("%s active=%v want %v", id, a.IsActive, want)
		}
	}
	if err := m.SetActive(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("不存在: %v", err)
	}
	// 删除 active 后不得残留 active 置位。
	if err := m.SoftDelete(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	svc := NewService(m)
	if _, err := svc.Active(ctx); !errors.Is(err, ErrNotFound) {
		t.Fatalf("active 被删后 Active 应 ErrNotFound: %v", err)
	}
}

func TestRefsOrderedCappedAndReorderAfterRemove(t *testing.T) {
	m, ctx := seed(t)
	_ = m.Create(ctx, Avatar{ID: "p1", Slug: "s1"})
	if err := m.AddRef(ctx, "p1", "asset-2", 5); err != nil {
		t.Fatal(err)
	}
	if err := m.AddRef(ctx, "p1", "asset-1", 2); err != nil {
		t.Fatal(err)
	}
	if err := m.AddRef(ctx, "p1", "asset-3", 9); err != nil {
		t.Fatal(err)
	}
	if err := m.AddRef(ctx, "p1", "asset-4", 10); !errors.Is(err, ErrTooManyRefs) {
		t.Fatalf("第 4 张必须 ErrTooManyRefs: %v", err)
	}
	// 有序：ord 升序。
	a, _, _ := m.Get(ctx, "p1")
	if len(a.Refs) != 3 || a.Refs[0].AssetID != "asset-1" || a.Refs[1].AssetID != "asset-2" || a.Refs[2].AssetID != "asset-3" {
		t.Fatalf("refs 无序: %+v", a.Refs)
	}
	// 重复绑定 = 更新 ord。
	if err := m.AddRef(ctx, "p1", "asset-1", 7); err != nil {
		t.Fatal(err)
	}
	a, _, _ = m.Get(ctx, "p1")
	if len(a.Refs) != 3 || a.Refs[0].AssetID != "asset-2" || a.Refs[1].AssetID != "asset-1" || a.Refs[1].Ord != 7 {
		t.Fatalf("重复绑定未按新 ord 重排: %+v", a.Refs)
	}
	// 删后重排 ord：0..n-1。
	if err := m.RemoveRef(ctx, "p1", "asset-2"); err != nil {
		t.Fatal(err)
	}
	a, _, _ = m.Get(ctx, "p1")
	if len(a.Refs) != 2 || a.Refs[0].Ord != 0 || a.Refs[1].Ord != 1 {
		t.Fatalf("删后未重排 ord: %+v", a.Refs)
	}
	if err := m.AddRef(ctx, "ghost", "asset-1", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("persona 不存在: %v", err)
	}
	if err := m.AddRef(ctx, "p1", "ghost-asset", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("asset 不存在: %v", err)
	}
}

func TestFindAssetBySHA256Dedup(t *testing.T) {
	m, ctx := seed(t)
	a, ok, err := m.FindAssetBySHA256(ctx, "sha-2")
	if err != nil || !ok || a.ID != "asset-2" {
		t.Fatalf("dedup 原语: %+v ok=%v err=%v", a, ok, err)
	}
	if _, ok, _ := m.FindAssetBySHA256(ctx, "nope"); ok {
		t.Fatal("未知 sha 不得命中")
	}
	// 同 sha 重复登记复用首次记录。
	if err := m.PutAsset(ctx, Asset{ID: "asset-dup", SHA256: "sha-1", Kind: "ref"}); err != nil {
		t.Fatal(err)
	}
	a, _, _ = m.FindAssetBySHA256(ctx, "sha-1")
	if a.ID != "asset-1" {
		t.Fatalf("同 sha 应复用首次记录: %+v", a)
	}
}

func TestListPagingAndIncludeDeleted(t *testing.T) {
	m, ctx := seed(t)
	for _, id := range []string{"p1", "p2", "p3"} {
		if err := m.Create(ctx, Avatar{ID: id, Slug: "s-" + id}); err != nil {
			t.Fatal(err)
		}
	}
	_ = m.SoftDelete(ctx, "p2")
	page, total, err := m.List(ctx, 1, 0, false)
	if err != nil || total != 2 || len(page) != 1 || page[0].ID != "p1" {
		t.Fatalf("分页: %+v total=%d err=%v", page, total, err)
	}
	_, total, _ = m.List(ctx, 50, 0, true)
	if total != 3 {
		t.Fatalf("includeDeleted total=%d want 3", total)
	}
	if _, ok, _ := m.GetBySlug(ctx, "s-p2"); ok {
		t.Fatal("已删 slug GetBySlug 应不存在")
	}
	if _, ok, _ := m.GetBySlug(ctx, "s-p1"); !ok {
		t.Fatal("未删 slug GetBySlug 应命中")
	}
}

func TestServiceResolveAndActive(t *testing.T) {
	m, ctx := seed(t)
	if err := m.Create(ctx, Avatar{
		ID: "p1", Slug: "s1", SystemPrompt: "你是小雅", VoiceID: "voice-a",
		Greeting: "大家好", ViduImageURI: "vidu://img", ActionLibraryID: "lib-1",
	}); err != nil {
		t.Fatal(err)
	}
	_ = m.AddRef(ctx, "p1", "asset-2", 1)
	_ = m.AddRef(ctx, "p1", "asset-1", 0)
	if err := m.SetActive(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	svc := NewService(m)
	p, err := svc.Resolve(ctx, "p1")
	if err != nil {
		t.Fatal(err)
	}
	if p.SystemPrompt != "你是小雅" || p.VoiceID != "voice-a" || p.Greeting != "大家好" ||
		p.ViduImageURI != "vidu://img" || p.ActionLibraryID != "lib-1" {
		t.Fatalf("profile: %+v", p)
	}
	if len(p.RefImageURIs) != 2 || p.RefImageURIs[0] != "vidu://ref-1" || p.RefImageURIs[1] != "vidu://ref-2" {
		t.Fatalf("refs 顺序: %+v", p.RefImageURIs)
	}
	if _, err := svc.Resolve(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("死 persona_id: %v", err)
	}
	ap, err := svc.Active(ctx)
	if err != nil || ap.PersonaID != "p1" {
		t.Fatalf("active: %+v err=%v", ap, err)
	}
}
