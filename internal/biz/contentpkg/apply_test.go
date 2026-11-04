package contentpkg

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
)

var errInjected = errors.New("injected filestore failure")

type faultFileStore struct {
	*MemoryFileStore
	mu       sync.Mutex
	failures map[string]int
	calls    []string
}

func newFaultFileStore() *faultFileStore {
	return &faultFileStore{MemoryFileStore: NewMemoryFileStore(), failures: map[string]int{}}
}

func (f *faultFileStore) failOnce(op string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, op)
	if f.failures[op] > 0 {
		f.failures[op]--
		return errInjected
	}
	return nil
}

func (f *faultFileStore) Current(ctx context.Context) (AssetRef, bool, error) {
	if err := f.failOnce("current"); err != nil {
		return AssetRef{}, false, err
	}
	return f.MemoryFileStore.Current(ctx)
}

func (f *faultFileStore) StageGeneration(ctx context.Context, applyID string, snapshot map[string]any) (AssetRef, error) {
	if err := f.failOnce("stage"); err != nil {
		return AssetRef{}, err
	}
	return f.MemoryFileStore.StageGeneration(ctx, applyID, snapshot)
}

func (f *faultFileStore) FlipCurrent(ctx context.Context, expected, next AssetRef) error {
	if err := f.failOnce("flip"); err != nil {
		return err
	}
	return f.MemoryFileStore.FlipCurrent(ctx, expected, next)
}

func (f *faultFileStore) DiscardGeneration(ctx context.Context, applyID string) error {
	if err := f.failOnce("discard"); err != nil {
		return err
	}
	return f.MemoryFileStore.DiscardGeneration(ctx, applyID)
}

func (f *faultFileStore) callCount(op string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, call := range f.calls {
		if call == op {
			n++
		}
	}
	return n
}

func seedCurrent(t *testing.T, ctx context.Context, store *MemoryFileStore, name string) AssetRef {
	t.Helper()
	ref, err := store.StageGeneration(ctx, "old-"+name, map[string]any{"generation": "old", "store": name})
	if err != nil {
		t.Fatal(err)
	}
	if err := store.FlipCurrent(ctx, AssetRef{}, ref); err != nil {
		t.Fatal(err)
	}
	return ref
}

func newApplyFixture(t *testing.T) (*Service, Draft, map[string]*faultFileStore, map[string]AssetRef) {
	t.Helper()
	ctx := context.Background()
	stores := map[string]*faultFileStore{
		"koubo": newFaultFileStore(), "choreo": newFaultFileStore(), "refs": newFaultFileStore(),
	}
	old := make(map[string]AssetRef, len(stores))
	fileStores := make(map[string]FileStore, len(stores))
	for name, store := range stores {
		old[name] = seedCurrent(t, ctx, store.MemoryFileStore, name)
		fileStores[name] = store
	}
	svc := NewServiceWithFileStores(NewMemoryStore(), fileStores)
	d, err := svc.CreateDraft(ctx, "tpl-1", 7, 1, map[string]any{"bgm": "old"})
	if err != nil {
		t.Fatal(err)
	}
	w, err := svc.OpenEdit(ctx, d.ID, "alice", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Save(ctx, d.ID, "alice", w.FencingToken, d.Revision, map[string]any{"bgm": "new"}); err != nil {
		t.Fatal(err)
	}
	d, _, err = svc.store.GetDraft(ctx, d.ID)
	if err != nil {
		t.Fatal(err)
	}
	d.OwnerID = w.OwnerID
	d.FencingToken = w.FencingToken
	return svc, d, stores, old
}

func TestApplyStagesAndCommitsAcrossFileStores(t *testing.T) {
	ctx := context.Background()
	svc, d, stores, old := newApplyFixture(t)
	j, err := svc.Apply(ctx, d.ID, d.OwnerID, d.FencingToken, d.Revision, "pf-1")
	if err != nil {
		t.Fatalf("apply: %v (journal=%+v)", err, j)
	}
	if j.Status != JournalCommitted || j.LastError != "" || len(j.Steps) != 3 {
		t.Fatalf("committed journal: %+v", j)
	}
	for i, step := range j.Steps {
		if step.Status != "done" || !step.Generation.Valid() || !step.OldGenerationExists {
			t.Fatalf("step %d not fully recorded: %+v", i, step)
		}
		if step.OldGeneration != old[step.Name] {
			t.Fatalf("step %s old generation: got %+v want %+v", step.Name, step.OldGeneration, old[step.Name])
		}
		current, ok, err := stores[step.Name].Current(ctx)
		if err != nil || !ok || current != step.Generation {
			t.Fatalf("current %s: ref=%+v ok=%v err=%v want=%+v", step.Name, current, ok, err, step.Generation)
		}
		if current == old[step.Name] || current.Revision != old[step.Name].Revision+1 {
			t.Fatalf("current %s did not advance: old=%+v new=%+v", step.Name, old[step.Name], current)
		}
	}
	draft, ok, err := svc.store.GetDraft(ctx, d.ID)
	if err != nil || !ok || draft.Status != DraftCommitted || draft.PreflightID != "pf-1" {
		t.Fatalf("committed draft: %+v ok=%v err=%v", draft, ok, err)
	}
	before := j.Steps[0].Generation
	if err := svc.ReplayJournal(ctx, j.ApplyID); err != nil {
		t.Fatalf("idempotent replay: %v", err)
	}
	after, _, _ := stores["koubo"].Current(ctx)
	if after != before {
		t.Fatalf("replay changed committed pointer: %+v -> %+v", before, after)
	}
}

func TestApplyStageFailureCompensatesEveryStep(t *testing.T) {
	for _, failed := range []string{"koubo", "choreo", "refs"} {
		t.Run(failed, func(t *testing.T) {
			ctx := context.Background()
			svc, d, stores, old := newApplyFixture(t)
			stores[failed].failures["stage"] = 1
			j, err := svc.Apply(ctx, d.ID, d.OwnerID, d.FencingToken, d.Revision, "")
			if !errors.Is(err, errInjected) {
				t.Fatalf("stage failure: %v", err)
			}
			if j.Status != JournalRolledBack {
				t.Fatalf("journal must roll back: %+v", j)
			}
			for name, store := range stores {
				current, ok, currentErr := store.Current(ctx)
				if currentErr != nil || !ok || current != old[name] {
					t.Fatalf("%s pointer changed: %+v ok=%v err=%v", name, current, ok, currentErr)
				}
				if store.callCount("flip") != 0 {
					t.Fatalf("%s flipped before commit", name)
				}
			}
			for _, name := range []string{"koubo", "choreo", "refs"} {
				shouldDiscard := name == "koubo" || (name == "choreo" && failed != "koubo") || (name == "refs" && failed == "refs")
				if shouldDiscard && stores[name].callCount("discard") == 0 {
					t.Fatalf("%s staged generation was not compensated", name)
				}
			}
			draft, _, _ := svc.store.GetDraft(ctx, d.ID)
			if draft.Status != DraftRolledBack {
				t.Fatalf("draft must roll back: %+v", draft)
			}
		})
	}
}

func TestApplyCompensationFailureRequiresManual(t *testing.T) {
	ctx := context.Background()
	svc, d, stores, _ := newApplyFixture(t)
	stores["choreo"].failures["stage"] = 1
	stores["koubo"].failures["discard"] = 1
	j, err := svc.Apply(ctx, d.ID, d.OwnerID, d.FencingToken, d.Revision, "")
	if err == nil || j.Status != JournalManualRequired {
		t.Fatalf("compensation failure must be manual: journal=%+v err=%v", j, err)
	}
	draft, _, _ := svc.store.GetDraft(ctx, d.ID)
	if draft.Status != DraftManualRequired {
		t.Fatalf("draft must require manual recovery: %+v", draft)
	}
}

func TestApplyFlipFailureRollsForwardEveryStep(t *testing.T) {
	for _, failed := range []string{"koubo", "choreo", "refs"} {
		t.Run(failed, func(t *testing.T) {
			ctx := context.Background()
			svc, d, stores, _ := newApplyFixture(t)
			stores[failed].failures["flip"] = 1
			j, err := svc.Apply(ctx, d.ID, d.OwnerID, d.FencingToken, d.Revision, "")
			if !errors.Is(err, errInjected) || j.Status != JournalCommitted {
				t.Fatalf("post-commit failure must remain committed: journal=%+v err=%v", j, err)
			}
			if err := svc.ReplayJournal(ctx, j.ApplyID); err != nil {
				t.Fatalf("roll-forward replay: %v", err)
			}
			for name, store := range stores {
				current, ok, currentErr := store.Current(ctx)
				if currentErr != nil || !ok || current.ID != j.ApplyID {
					t.Fatalf("%s was not rolled forward: %+v ok=%v err=%v", name, current, ok, currentErr)
				}
			}
			draft, _, _ := svc.store.GetDraft(ctx, d.ID)
			if draft.Status != DraftCommitted {
				t.Fatalf("draft after replay: %+v", draft)
			}
		})
	}
}

func newCrashFixture(t *testing.T, status string, flipped int) (*Service, Journal, map[string]*MemoryFileStore, map[string]AssetRef, map[string]AssetRef) {
	t.Helper()
	ctx := context.Background()
	st := NewMemoryStore()
	stores := map[string]*MemoryFileStore{"koubo": NewMemoryFileStore(), "choreo": NewMemoryFileStore(), "refs": NewMemoryFileStore()}
	fileStores := map[string]FileStore{}
	old := map[string]AssetRef{}
	next := map[string]AssetRef{}
	j := Journal{ApplyID: "apply-crash", DraftID: "draft-crash", BaseRevision: 4, Status: status, CreatedAtMs: 1, UpdatedAtMs: 1}
	for _, name := range []string{"koubo", "choreo", "refs"} {
		store := stores[name]
		fileStores[name] = store
		old[name] = seedCurrent(t, ctx, store, name)
		staged, err := store.StageGeneration(ctx, j.ApplyID, map[string]any{"generation": "new"})
		if err != nil {
			t.Fatal(err)
		}
		next[name] = staged
		j.Steps = append(j.Steps, ApplyStep{
			Name: name, FileStore: name, Action: "stage_generation+flip_pointer", Status: "staged",
			OldGeneration: old[name], OldGenerationExists: true, Generation: staged,
		})
	}
	for i := 0; i < flipped; i++ {
		name := j.Steps[i].Name
		if err := stores[name].FlipCurrent(ctx, old[name], next[name]); err != nil {
			t.Fatal(err)
		}
		j.Steps[i].Status = "done"
	}
	if err := st.CreateJournal(ctx, j); err != nil {
		t.Fatal(err)
	}
	draftStatus := DraftCommitting
	if status != JournalCommitted {
		draftStatus = DraftStaging
	}
	if err := st.CreateDraft(ctx, Draft{ID: j.DraftID, Revision: 4, FencingToken: 7, Status: draftStatus, Snapshot: map[string]any{"generation": "new"}}); err != nil {
		t.Fatal(err)
	}
	return NewServiceWithFileStores(st, fileStores), j, stores, old, next
}

func TestReplayCrashBeforeCommitRollsBack(t *testing.T) {
	ctx := context.Background()
	svc, j, stores, old, _ := newCrashFixture(t, JournalCommitPending, 0)
	if err := svc.ReplayJournal(ctx, j.ApplyID); err != nil {
		t.Fatalf("rollback replay: %v", err)
	}
	for name, store := range stores {
		current, _, _ := store.Current(ctx)
		if current != old[name] {
			t.Fatalf("%s pointer changed during rollback: %+v", name, current)
		}
	}
	got, _, _ := svc.store.GetJournal(ctx, j.ApplyID)
	if got.Status != JournalRolledBack {
		t.Fatalf("journal after rollback: %+v", got)
	}
	draft, _, _ := svc.store.GetDraft(ctx, j.DraftID)
	if draft.Status != DraftRolledBack {
		t.Fatalf("draft after rollback: %+v", draft)
	}
}

func TestReplayCrashAfterRollbackRecordFinalizesDraft(t *testing.T) {
	ctx := context.Background()
	svc, j, _, _, _ := newCrashFixture(t, JournalRolledBack, 0)
	if err := svc.ReplayPending(ctx); err != nil {
		t.Fatalf("resume rollback: %v", err)
	}
	draft, _, _ := svc.store.GetDraft(ctx, j.DraftID)
	if draft.Status != DraftRolledBack {
		t.Fatalf("rollback record exists but draft was not finalized: %+v", draft)
	}
}

func TestReplayCrashAfterCommitRollsForward(t *testing.T) {
	ctx := context.Background()
	svc, j, stores, _, next := newCrashFixture(t, JournalCommitted, 1)
	if err := svc.ReplayJournal(ctx, j.ApplyID); err != nil {
		t.Fatalf("roll-forward replay: %v", err)
	}
	for name, store := range stores {
		current, _, _ := store.Current(ctx)
		if current != next[name] {
			t.Fatalf("%s pointer after roll-forward: got %+v want %+v", name, current, next[name])
		}
	}
	got, _, _ := svc.store.GetJournal(ctx, j.ApplyID)
	if got.Status != JournalCommitted || got.LastError != "" {
		t.Fatalf("journal after roll-forward: %+v", got)
	}
	draft, _, _ := svc.store.GetDraft(ctx, j.DraftID)
	if draft.Status != DraftCommitted {
		t.Fatalf("draft after roll-forward: %+v", draft)
	}
}

func TestApplyConcurrentCASOnlyOneCommits(t *testing.T) {
	ctx := context.Background()
	svc, d, _, _ := newApplyFixture(t)
	start := make(chan struct{})
	errs := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			<-start
			_, err := svc.Apply(ctx, d.ID, d.OwnerID, d.FencingToken, d.Revision, "")
			errs <- err
		}()
	}
	close(start)
	var success, conflict int
	for i := 0; i < 2; i++ {
		err := <-errs
		switch {
		case err == nil:
			success++
		case errors.Is(err, ErrConflict):
			conflict++
		default:
			t.Fatalf("concurrent apply: %v", err)
		}
	}
	if success != 1 || conflict != 1 {
		t.Fatalf("success=%d conflict=%d", success, conflict)
	}
	js, err := svc.store.ListJournalsByDraft(ctx, d.ID)
	if err != nil || len(js) != 1 || js[0].Status != JournalCommitted {
		t.Fatalf("journals: %+v err=%v", js, err)
	}
}

func TestApplyRejectsStaleFencing(t *testing.T) {
	ctx := context.Background()
	svc, d, _, _ := newApplyFixture(t)
	if _, err := svc.OpenEdit(ctx, d.ID, d.OwnerID, time.Minute); err != nil {
		t.Fatal(err)
	}
	j, err := svc.Apply(ctx, d.ID, d.OwnerID, d.FencingToken, d.Revision, "")
	if !errors.Is(err, ErrStaleFencing) || j.ApplyID != "" {
		t.Fatalf("stale apply: journal=%+v err=%v", j, err)
	}
	js, _ := svc.store.ListJournalsByDraft(ctx, d.ID)
	if len(js) != 0 {
		t.Fatalf("stale apply created journal: %+v", js)
	}
}

func TestApplyDetectsSnapshotMutationAtSameRevision(t *testing.T) {
	ctx := context.Background()
	svc, d, _, _ := newApplyFixture(t)
	if _, err := svc.store.CASUpdateDraft(ctx, d.ID, d.Revision, d.FencingToken, func(cur *Draft) {
		cur.Snapshot = map[string]any{"bgm": "tampered"}
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Apply(ctx, d.ID, d.OwnerID, d.FencingToken, d.Revision, ""); !errors.Is(err, ErrConflict) {
		t.Fatalf("same-revision snapshot mutation must conflict: %v", err)
	}
}

func TestReplayPendingRecoversInterruptedJournals(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     string
		flipped    int
		wantStatus string
	}{
		{name: "before commit", status: JournalCommitPending, flipped: 0, wantStatus: JournalRolledBack},
		{name: "after commit", status: JournalCommitted, flipped: 1, wantStatus: JournalCommitted},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			svc, j, stores, old, next := newCrashFixture(t, tc.status, tc.flipped)
			if err := svc.ReplayPending(ctx); err != nil {
				t.Fatalf("replay pending: %v", err)
			}
			got, _, _ := svc.store.GetJournal(ctx, j.ApplyID)
			if got.Status != tc.wantStatus {
				t.Fatalf("journal after recovery: %+v", got)
			}
			for name, store := range stores {
				current, _, _ := store.Current(ctx)
				want := old[name]
				if tc.wantStatus == JournalCommitted {
					want = next[name]
				}
				if current != want {
					t.Fatalf("%s current: got %+v want %+v", name, current, want)
				}
			}
		})
	}
}

func TestPendingCommittedJournalWithMissingDraft(t *testing.T) {
	store := NewMemoryStore()
	j := Journal{
		ApplyID: "committed-without-draft", DraftID: "missing", Status: JournalCommitted,
		Steps: []ApplyStep{{Name: "koubo", Status: "done"}},
	}
	if err := store.CreateJournal(context.Background(), j); err != nil {
		t.Fatal(err)
	}
	pending, err := store.ListPendingJournals(context.Background())
	if err != nil || len(pending) != 1 {
		t.Fatalf("committed journal without its draft must remain recoverable: %+v err=%v", pending, err)
	}
}

func TestHashSnapshotExportedAndStable(t *testing.T) {
	a := map[string]any{"z": 1, "a": "x"}
	b := map[string]any{"a": "x", "z": 1}
	if got := HashSnapshot(a); got == "" || got != HashSnapshot(b) || got != hashSnapshot(a) {
		t.Fatalf("exported stable hash: %q", got)
	}
	if HashSnapshot(a) == HashSnapshot(map[string]any{"a": "y", "z": 1}) {
		t.Fatal("different snapshots must not share a hash")
	}
}

func TestFileStoreRejectsUnboundGeneration(t *testing.T) {
	store := NewMemoryFileStore()
	if err := store.FlipCurrent(context.Background(), AssetRef{}, AssetRef{ID: "unbound"}); !errors.Is(err, ErrConflict) {
		t.Fatalf("unbound generation must be rejected: %v", err)
	}
}

func TestJournalPlanOrder(t *testing.T) {
	svc, d, _, _ := newApplyFixture(t)
	j, err := svc.Apply(context.Background(), d.ID, d.OwnerID, d.FencingToken, d.Revision, "")
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%s>%s>%s", j.Steps[0].Name, j.Steps[1].Name, j.Steps[2].Name)
	if got != "koubo>choreo>refs" {
		t.Fatalf("plan order: %s", got)
	}
}
