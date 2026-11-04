package repo

import (
	"context"
	"errors"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	"github.com/teamwork/digital-human/internal/biz/contentpkg"
	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/ent/enttest"
)

func newContentpkgEntTestClient(t *testing.T) *ent.Client {
	t.Helper()
	client := enttest.Open(t, dialect.SQLite, "file:"+uuid.NewString()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	return client
}

func TestEntContentpkgStoreCAS(t *testing.T) {
	ctx := context.Background()
	st := NewEntContentpkgStore(newContentpkgEntTestClient(t))
	svc := contentpkg.NewService(st)
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
	if _, err := st.CASUpdateDraft(ctx, d.ID, d.Revision+1, w.FencingToken+1, func(cur *contentpkg.Draft) {
		cur.Status = contentpkg.DraftManualRequired
	}); !errors.Is(err, contentpkg.ErrStaleFencing) {
		t.Fatalf("非当前 fencing 必须拒绝: %v", err)
	}
}

func TestEntContentpkgStoreApplyRequiresFileStores(t *testing.T) {
	ctx := context.Background()
	st := NewEntContentpkgStore(newContentpkgEntTestClient(t))
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
	if !errors.Is(err, contentpkg.ErrUnavailable) || j.ApplyID != "" {
		t.Fatalf("缺 FileStore 必须显式不可用: journal=%+v err=%v", j, err)
	}
	js, err := st.ListJournalsByDraft(ctx, d.ID)
	if err != nil || len(js) != 0 {
		t.Fatalf("不可用不得创建 journal: %v %+v", err, js)
	}
}

func newEntApplyFileStores() map[string]contentpkg.FileStore {
	return map[string]contentpkg.FileStore{
		"koubo":  contentpkg.NewMemoryFileStore(),
		"choreo": contentpkg.NewMemoryFileStore(),
		"refs":   contentpkg.NewMemoryFileStore(),
	}
}

func TestEntContentpkgStoreApplyPersistsRecoveryProgress(t *testing.T) {
	ctx := context.Background()
	st := NewEntContentpkgStore(newContentpkgEntTestClient(t))
	fileStores := newEntApplyFileStores()
	svc := contentpkg.NewServiceWithFileStores(st, fileStores)
	d, err := svc.CreateDraft(ctx, "tpl-1", 0, 1, map[string]any{"bgm": "a"})
	if err != nil {
		t.Fatal(err)
	}
	w, err := svc.OpenEdit(ctx, d.ID, "alice", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	j, err := svc.Apply(ctx, d.ID, "alice", w.FencingToken, d.Revision, "pf-ent")
	if err != nil {
		t.Fatalf("apply: %v journal=%+v", err, j)
	}
	got, ok, err := st.GetJournal(ctx, j.ApplyID)
	if err != nil || !ok {
		t.Fatalf("journal 持久化: ok=%v err=%v", ok, err)
	}
	if got.Status != contentpkg.JournalCommitted || got.CommittedAtMs == 0 || len(got.Steps) != 3 {
		t.Fatalf("committed journal: %+v", got)
	}
	for _, step := range got.Steps {
		if step.Status != "done" || !step.Generation.Valid() || step.OldGenerationExists {
			t.Fatalf("step recovery fields: %+v", step)
		}
		current, exists, err := fileStores[step.Name].Current(ctx)
		if err != nil || !exists || current != step.Generation {
			t.Fatalf("current %s: %+v exists=%v err=%v", step.Name, current, exists, err)
		}
	}
	pending, err := st.ListPendingJournals(ctx)
	if err != nil || len(pending) != 0 {
		t.Fatalf("committed journal must not be pending: %+v err=%v", pending, err)
	}
	if err := svc.ReplayJournal(ctx, j.ApplyID); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
}

func TestEntContentpkgStoreReplayPending(t *testing.T) {
	ctx := context.Background()
	st := NewEntContentpkgStore(newContentpkgEntTestClient(t))
	fileStores := newEntApplyFileStores()
	svc := contentpkg.NewServiceWithFileStores(st, fileStores)
	snapshot := map[string]any{"bgm": "new"}
	d := contentpkg.Draft{
		ID: "draft-recovery", Revision: 1, Status: contentpkg.DraftCommitting,
		Snapshot: snapshot, SnapshotHash: contentpkg.HashSnapshot(snapshot),
	}
	if err := st.CreateDraft(ctx, d); err != nil {
		t.Fatal(err)
	}
	j := contentpkg.Journal{
		ApplyID: "apply-recovery", DraftID: d.ID, BaseRevision: 1, Snapshot: snapshot,
		OldValues: map[string]any{}, Status: contentpkg.JournalCommitPending,
	}
	for _, name := range []string{"koubo", "choreo", "refs"} {
		ref, err := fileStores[name].StageGeneration(ctx, j.ApplyID, snapshot)
		if err != nil {
			t.Fatal(err)
		}
		j.Steps = append(j.Steps, contentpkg.ApplyStep{
			Name: name, FileStore: name, Action: "stage_generation+flip_pointer",
			Status: "staged", Generation: ref,
		})
	}
	if err := st.CreateJournal(ctx, j); err != nil {
		t.Fatal(err)
	}
	pending, err := st.ListPendingJournals(ctx)
	if err != nil || len(pending) != 1 || pending[0].ApplyID != j.ApplyID {
		t.Fatalf("pending journals: %+v err=%v", pending, err)
	}
	if err := svc.ReplayPending(ctx); err != nil {
		t.Fatalf("replay pending: %v", err)
	}
	got, _, _ := st.GetJournal(ctx, j.ApplyID)
	if got.Status != contentpkg.JournalRolledBack || got.RolledBackAtMs == 0 {
		t.Fatalf("rolled-back journal: %+v", got)
	}
	draft, _, _ := st.GetDraft(ctx, d.ID)
	if draft.Status != contentpkg.DraftRolledBack {
		t.Fatalf("rolled-back draft: %+v", draft)
	}
	for name, fileStore := range fileStores {
		if _, exists, err := fileStore.Current(ctx); err != nil || exists {
			t.Fatalf("%s pointer must remain empty: exists=%v err=%v", name, exists, err)
		}
	}
}
