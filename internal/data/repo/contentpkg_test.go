package repo

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/contentpkg"
)

// TestEntContentpkgStoreCAS B4 DoD（Ent 后端）：CAS 双写者只一个成功；旧 fencing 被拒。
func TestEntContentpkgStoreCAS(t *testing.T) {
	ctx := context.Background()
	svc := contentpkg.NewService(NewEntContentpkgStore(newEntTestClient(t)))
	d, err := svc.CreateDraft(ctx, "tpl-1", 7, 1, map[string]any{"bgm": "a"})
	if err != nil {
		t.Fatal(err)
	}
	w, err := svc.OpenEdit(ctx, d.ID, "alice", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	_, e1 := svc.Save(ctx, d.ID, "alice", w.FencingToken, d.Revision, map[string]any{"bgm": "b"})
	_, e2 := svc.Save(ctx, d.ID, "alice", w.FencingToken, d.Revision, map[string]any{"bgm": "c"})
	if e1 != nil {
		t.Fatalf("首次成功，撞车必须失败: e1=%v e2=%v", e1, e2)
	}
	if !errors.Is(e2, contentpkg.ErrConflict) {
		t.Fatalf("撞车 409: %v", e2)
	}
}

// TestEntContentpkgStoreApplyExplicitNotImplemented Ent 后端 apply 显式未实现 + journal 可查。
func TestEntContentpkgStoreApplyExplicitNotImplemented(t *testing.T) {
	ctx := context.Background()
	st := NewEntContentpkgStore(newEntTestClient(t))
	svc := contentpkg.NewService(st)
	d, err := svc.CreateDraft(ctx, "tpl-1", 0, 1, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	w, err := svc.OpenEdit(ctx, d.ID, "alice", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	j, err := svc.Apply(ctx, d.ID, "alice", w.FencingToken, d.Revision, "")
	if !errors.Is(err, contentpkg.ErrNotImplemented) {
		t.Fatalf("必须显式 ErrNotImplemented: %v", err)
	}
	got, ok, err := st.GetJournal(ctx, j.ApplyID)
	if err != nil || !ok {
		t.Fatalf("journal 持久化: ok=%v err=%v", ok, err)
	}
	if got.Status != contentpkg.JournalStaging || len(got.Steps) != 3 {
		t.Fatalf("journal 显式失败标注: %+v", got)
	}
	js, err := st.ListJournalsByDraft(ctx, d.ID)
	if err != nil || len(js) != 1 {
		t.Fatalf("ListJournalsByDraft: %v %+v", err, js)
	}
}
