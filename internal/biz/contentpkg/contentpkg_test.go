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
	w2, err := svc.OpenEdit(ctx, d.ID, "alice", time.Minute) // 同 owner 续约
	if err != nil {
		t.Fatal(err)
	}
	if w2.FencingToken != w1.FencingToken+1 {
		t.Fatalf("续约必须递增 fencing: %d -> %d", w1.FencingToken, w2.FencingToken)
	}
	// 两个写者基于同一 expected_revision 提交：只能一个成功。
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

// TestStaleFencingRejected fencing token 过期旧写者被拒（§7.3：lease 接管后旧写者不得提交）。
func TestStaleFencingRejected(t *testing.T) {
	ctx := context.Background()
	svc, d := newSvc(t)
	old, err := svc.OpenEdit(ctx, d.ID, "alice", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	// bob 接管（alice lease 过期后）：fencing 递增。
	svc2 := svc
	svc2.now = func() time.Time { return time.Now().Add(2 * time.Minute) }
	bob, err := svc2.OpenEdit(ctx, d.ID, "bob", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if bob.FencingToken <= old.FencingToken {
		t.Fatalf("接管必须递增 fencing: %d -> %d", old.FencingToken, bob.FencingToken)
	}
	// 旧写者 alice 用旧 token 提交 → ErrStaleFencing。
	if _, err := svc2.Save(ctx, d.ID, "alice", old.FencingToken, d.Revision, map[string]any{}); !errors.Is(err, ErrNotLeaseOwner) {
		t.Fatalf("alice 已不是 owner: %v", err)
	}
	// 即使 owner 正确但 token 旧（构造场景：bob 再次续约后旧 bob token）。
	older := bob
	bob2, err := svc2.OpenEdit(ctx, d.ID, "bob", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc2.Save(ctx, d.ID, "bob", older.FencingToken, d.Revision, map[string]any{}); !errors.Is(err, ErrStaleFencing) {
		t.Fatalf("旧 fencing token 必须 ErrStaleFencing: %v", err)
	}
	_ = bob2
}

// TestApplyExplicitNotImplemented apply 跨 FileStore 提交点空实现：显式 ErrNotImplemented，
// journal 落显式失败标注（不静默半成功）；prepare 的 journal 已先持久化。
func TestApplyExplicitNotImplemented(t *testing.T) {
	ctx := context.Background()
	svc, d := newSvc(t)
	w, err := svc.OpenEdit(ctx, d.ID, "alice", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Save(ctx, d.ID, "alice", w.FencingToken, d.Revision, map[string]any{"bgm": "b"}); err != nil {
		t.Fatal(err)
	}
	j, err := svc.Apply(ctx, d.ID, "alice", w.FencingToken, d.Revision+1, "pf_1")
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("commit 空实现必须显式 ErrNotImplemented: %v", err)
	}
	if j.ApplyID == "" || j.Status != JournalStaging || j.LastError == "" {
		t.Fatalf("journal 必须显式失败标注: %+v", j)
	}
	if len(j.Steps) != 3 || j.Steps[0].Name != "koubo" {
		t.Fatalf("固定顺序计划 Koubo → Choreo → Ref: %+v", j.Steps)
	}
	// 崩溃重放骨架同样显式未实现。
	if err := svc.ReplayJournal(ctx, j.ApplyID); !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("replay 必须显式 ErrNotImplemented: %v", err)
	}
	// 草稿进入 staging（prepare 已生效，不是静默半成功：状态可查）。
	got, ok, _ := svc.store.GetDraft(ctx, d.ID)
	if !ok || got.Status != DraftStaging || got.PreflightID != "pf_1" {
		t.Fatalf("草稿 prepare 状态: %+v", got)
	}
}

// TestAssetRefValid §7.1：三字段齐全才可进公开链路。
func TestAssetRefValid(t *testing.T) {
	if (AssetRef{ID: "x"}).Valid() {
		t.Fatal("缺 revision/hash 不得 valid")
	}
	if !(AssetRef{ID: "x", Revision: 2, ContentHash: "h"}).Valid() {
		t.Fatal("齐全应 valid")
	}
}
