package contentpkg

import (
	"context"
	"errors"
	"testing"
	"time"
)

func newSvc(t *testing.T) (*Service, Draft) {
	t.Helper()
	svc := NewService(NewMemoryStore())
	d, err := svc.CreateDraft(context.Background(), "tpl-1", 7, 1, map[string]any{"bgm": "a"})
	if err != nil {
		t.Fatal(err)
	}
	return svc, d
}

// TestCASTwoWritersOnlyOneWins CAS 双写者：只有一个成功（撞 409 ErrConflict）。
func TestCASTwoWritersOnlyOneWins(t *testing.T) {
	ctx := context.Background()
	svc, d := newSvc(t)
	w1, err := svc.OpenEdit(ctx, d.ID, "alice", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	w2, err := svc.OpenEdit(ctx, d.ID, "alice", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if w2.FencingToken != w1.FencingToken+1 {
		t.Fatalf("续约必须递增 fencing: %d -> %d", w1.FencingToken, w2.FencingToken)
	}
	_, err1 := svc.Save(ctx, d.ID, "alice", w2.FencingToken, d.Revision, map[string]any{"bgm": "b"})
	_, err2 := svc.Save(ctx, d.ID, "alice", w2.FencingToken, d.Revision, map[string]any{"bgm": "c"})
	if (err1 == nil) == (err2 == nil) {
		t.Fatalf("双写者必须只一个成功: err1=%v err2=%v", err1, err2)
	}
	if err1 == nil {
		if !errors.Is(err2, ErrConflict) {
			t.Fatalf("撞车必须 ErrConflict(409): %v", err2)
		}
	} else if !errors.Is(err1, ErrConflict) {
		t.Fatalf("撞车必须 ErrConflict(409): %v", err1)
	}
}

// TestStaleFencingRejected fencing token 过期旧写者被拒。
func TestStaleFencingRejected(t *testing.T) {
	ctx := context.Background()
	svc, d := newSvc(t)
	old, err := svc.OpenEdit(ctx, d.ID, "alice", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	svc.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	bob, err := svc.OpenEdit(ctx, d.ID, "bob", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if bob.FencingToken <= old.FencingToken {
		t.Fatalf("接管必须递增 fencing: %d -> %d", old.FencingToken, bob.FencingToken)
	}
	if _, err := svc.Save(ctx, d.ID, "alice", old.FencingToken, d.Revision, map[string]any{}); !errors.Is(err, ErrNotLeaseOwner) {
		t.Fatalf("alice 已不是 owner: %v", err)
	}
	older := bob
	if _, err := svc.OpenEdit(ctx, d.ID, "bob", time.Minute); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Save(ctx, d.ID, "bob", older.FencingToken, d.Revision, map[string]any{}); !errors.Is(err, ErrStaleFencing) {
		t.Fatalf("旧 fencing token 必须 ErrStaleFencing: %v", err)
	}
}

// TestApplyRequiresFileStores 未接真实 FileStore 时必须显式不可用，且不得留下半提交状态。
func TestApplyRequiresFileStores(t *testing.T) {
	ctx := context.Background()
	svc, d := newSvc(t)
	w, err := svc.OpenEdit(ctx, d.ID, "alice", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Apply(ctx, d.ID, "alice", w.FencingToken, d.Revision, "pf_1"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("缺 FileStore 必须显式不可用: %v", err)
	}
	got, ok, _ := svc.store.GetDraft(ctx, d.ID)
	if !ok || got.Status != DraftEditing {
		t.Fatalf("不可用不得推进草稿: %+v", got)
	}
	js, err := svc.store.ListJournalsByDraft(ctx, d.ID)
	if err != nil || len(js) != 0 {
		t.Fatalf("不可用不得创建 journal: %+v, %v", js, err)
	}
}

// TestAssetRefValid 资产引用必须至少绑定 revision 或 content_hash。
func TestAssetRefValid(t *testing.T) {
	if (AssetRef{ID: "x"}).Valid() {
		t.Fatal("缺 revision/hash 不得 valid")
	}
	if !(AssetRef{ID: "x", Revision: 2}).Valid() {
		t.Fatal("revision 绑定应 valid")
	}
	if !(AssetRef{ID: "x", ContentHash: "h"}).Valid() {
		t.Fatal("content_hash 绑定应 valid")
	}
}
