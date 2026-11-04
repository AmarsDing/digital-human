package contentpkg

import (
	"context"
	"errors"
	"fmt"
)

// ErrUnavailable 表示发布所需的真实存储或恢复能力尚未接入。
var (
	ErrUnavailable    = errors.New("contentpkg: required store unavailable")
	errReplayNoCommit = errors.New("contentpkg: replay found no commit record")
)

// JournalStore 持久化执行进度；expectedStatus 提供账本状态 CAS。
type JournalStore interface {
	UpdateJournal(ctx context.Context, applyID, expectedStatus string, mutate func(*Journal)) (Journal, error)
}

// JournalRecoveryStore 枚举启动恢复需要重放的账本。
type JournalRecoveryStore interface {
	ListPendingJournals(ctx context.Context) ([]Journal, error)
}

func NewServiceWithFileStores(store Store, fileStores map[string]FileStore) *Service {
	s := NewService(store)
	s.fileStores = make(map[string]FileStore, len(fileStores))
	for name, fileStore := range fileStores {
		s.fileStores[name] = fileStore
	}
	return s
}

func applyPlan() []ApplyStep {
	return []ApplyStep{
		{Name: "koubo", FileStore: "koubo", Action: "stage_generation+flip_pointer", Status: "pending"},
		{Name: "choreo", FileStore: "choreo", Action: "stage_generation+flip_pointer", Status: "pending"},
		{Name: "refs", FileStore: "refs", Action: "stage_generation+flip_pointer", Status: "pending"},
	}
}

func (s *Service) checkApplyStores() error {
	if s.store == nil {
		return fmt.Errorf("%w: draft store", ErrUnavailable)
	}
	if _, ok := s.store.(JournalStore); !ok {
		return fmt.Errorf("%w: journal progress store", ErrUnavailable)
	}
	for _, step := range applyPlan() {
		if s.fileStores[step.FileStore] == nil {
			return fmt.Errorf("%w: filestore %s", ErrUnavailable, step.FileStore)
		}
	}
	return nil
}

// Apply 先持久化完整账本，再 stage 所有 generation。committed 账本是唯一提交点：
// 提交点前失败逆序清理 staged generation，提交点后失败只做 roll-forward。
func (s *Service) Apply(ctx context.Context, draftID, ownerID string, fencingToken, expectedRevision int64, preflightID string) (Journal, error) {
	if err := s.checkApplyStores(); err != nil {
		return Journal{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	d, ok, err := s.store.GetDraft(ctx, draftID)
	if err != nil {
		return Journal{}, err
	}
	if !ok {
		return Journal{}, ErrDraftNotFound
	}
	if d.OwnerID != ownerID {
		return Journal{}, fmt.Errorf("%w: owner %q != %q", ErrNotLeaseOwner, ownerID, d.OwnerID)
	}
	if d.LeaseUntilMs <= s.now().UnixMilli() {
		return Journal{}, fmt.Errorf("%w: lease expired", ErrConflict)
	}
	if fencingToken != d.FencingToken {
		return Journal{}, fmt.Errorf("%w: token %d != current %d", ErrStaleFencing, fencingToken, d.FencingToken)
	}
	if d.Revision != expectedRevision {
		return Journal{}, fmt.Errorf("%w: expected_revision %d != current %d", ErrConflict, expectedRevision, d.Revision)
	}
	if d.Status != DraftEditing {
		return Journal{}, fmt.Errorf("%w: draft status %q not editable", ErrConflict, d.Status)
	}
	if err := validateAssetRefs(d.Snapshot); err != nil {
		return Journal{}, err
	}
	if actual := HashSnapshot(d.Snapshot); d.SnapshotHash != actual {
		return Journal{}, fmt.Errorf("%w: stored snapshot hash %q != recomputed %q", ErrConflict, d.SnapshotHash, actual)
	}

	nowMs := s.now().UnixMilli()
	j := Journal{
		ApplyID: s.newID(), DraftID: draftID, BaseRevision: d.Revision,
		Snapshot: cloneMap(d.Snapshot), Steps: applyPlan(), OldValues: map[string]any{},
		Status: JournalPreparing, CreatedAtMs: nowMs, UpdatedAtMs: nowMs,
	}
	if err := s.store.CreateJournal(ctx, j); err != nil {
		return Journal{}, err
	}
	if _, err = s.store.CASUpdateDraft(ctx, draftID, expectedRevision, fencingToken, func(cur *Draft) {
		cur.Status = DraftStaging
		cur.PreflightID = preflightID
		cur.UpdatedAtMs = nowMs
	}); err != nil {
		j = s.finishJournalWithoutDraft(ctx, j, JournalRolledBack, err)
		return j, err
	}
	j, err = s.updateJournal(ctx, j, JournalPreparing, func(cur *Journal) {
		cur.Status = JournalStaging
		cur.OldValues["snapshot_hash"] = HashSnapshot(cur.Snapshot)
	})
	if err != nil {
		return s.rollback(ctx, j, err, expectedRevision, fencingToken)
	}

	j, err = s.stageGenerations(ctx, j)
	if err != nil {
		return s.rollback(ctx, j, err, expectedRevision, fencingToken)
	}
	if err := s.transitionDraft(ctx, draftID, expectedRevision, fencingToken, DraftStaged, DraftStaging); err != nil {
		return s.rollback(ctx, j, err, expectedRevision, fencingToken)
	}
	j, err = s.updateJournal(ctx, j, JournalStaging, func(cur *Journal) {
		cur.Status = JournalCommitPending
	})
	if err != nil {
		return s.rollback(ctx, j, err, expectedRevision, fencingToken)
	}
	if err := s.transitionDraft(ctx, draftID, expectedRevision, fencingToken, DraftCommitting, DraftStaged); err != nil {
		return s.rollback(ctx, j, err, expectedRevision, fencingToken)
	}

	committed, commitErr := s.updateJournal(ctx, j, JournalCommitPending, func(cur *Journal) {
		cur.Status = JournalCommitted
		cur.LastError = ""
	})
	if commitErr != nil {
		persisted, found, lookupErr := s.store.GetJournal(ctx, j.ApplyID)
		if lookupErr != nil {
			return j, errors.Join(commitErr, lookupErr)
		}
		if found && persisted.Status == JournalCommitted {
			j = persisted
		} else {
			return s.rollback(ctx, j, commitErr, expectedRevision, fencingToken)
		}
	} else {
		j = committed
	}
	return s.rollForward(ctx, j, expectedRevision, fencingToken)
}

func (s *Service) updateJournal(ctx context.Context, j Journal, expectedStatus string, mutate func(*Journal)) (Journal, error) {
	store, ok := s.store.(JournalStore)
	if !ok {
		return j, fmt.Errorf("%w: journal progress store", ErrUnavailable)
	}
	updated, err := store.UpdateJournal(ctx, j.ApplyID, expectedStatus, func(cur *Journal) {
		mutate(cur)
		cur.UpdatedAtMs = s.now().UnixMilli()
	})
	if err != nil {
		return j, err
	}
	return updated, nil
}

func (s *Service) stageGenerations(ctx context.Context, j Journal) (Journal, error) {
	for i := range j.Steps {
		step := &j.Steps[i]
		fileStore := s.fileStores[step.FileStore]
		old, exists, err := fileStore.Current(ctx)
		if err == nil && exists && !old.Valid() {
			err = fmt.Errorf("%w: current generation for %s is unbound", ErrConflict, step.Name)
		}
		if err != nil {
			return s.recordPrecommitFailure(ctx, j, i, err)
		}
		j, err = s.updateJournal(ctx, j, JournalStaging, func(cur *Journal) {
			cur.Steps[i].OldGeneration = old
			cur.Steps[i].OldGenerationExists = exists
			cur.Steps[i].Status = "staging"
			cur.OldValues[step.Name] = map[string]any{"generation": old, "exists": exists}
		})
		if err != nil {
			return s.recordPrecommitFailure(ctx, j, i, err)
		}
		next, err := fileStore.StageGeneration(ctx, j.ApplyID, j.Snapshot)
		if err == nil && !next.Valid() {
			err = fmt.Errorf("%w: staged generation for %s is unbound", ErrConflict, step.Name)
		}
		if err != nil {
			return s.recordPrecommitFailure(ctx, j, i, err)
		}
		j, err = s.updateJournal(ctx, j, JournalStaging, func(cur *Journal) {
			cur.Steps[i].Generation = next
			cur.Steps[i].Status = "staged"
			cur.Steps[i].Error = ""
		})
		if err != nil {
			return s.recordPrecommitFailure(ctx, j, i, err)
		}
	}
	return j, nil
}

func (s *Service) recordPrecommitFailure(ctx context.Context, j Journal, step int, cause error) (Journal, error) {
	updated, err := s.updateJournal(ctx, j, j.Status, func(cur *Journal) {
		if step >= 0 && step < len(cur.Steps) {
			cur.Steps[step].Status = "failed"
			cur.Steps[step].Error = cause.Error()
		}
		cur.LastError = cause.Error()
	})
	if err == nil {
		j = updated
	}
	return j, cause
}

func (s *Service) rollback(ctx context.Context, j Journal, cause error, expectedRevision, fencingToken int64) (Journal, error) {
	if j.Status == JournalCommitted {
		return s.rollForward(ctx, j, expectedRevision, fencingToken)
	}
	var compensation []error
	for i := len(j.Steps) - 1; i >= 0; i-- {
		step := j.Steps[i]
		fileStore := s.fileStores[step.FileStore]
		if fileStore == nil {
			compensation = append(compensation, fmt.Errorf("%w: filestore %s", ErrUnavailable, step.FileStore))
			continue
		}
		if err := fileStore.DiscardGeneration(ctx, j.ApplyID); err != nil {
			compensation = append(compensation, fmt.Errorf("discard %s: %w", step.Name, err))
		}
	}
	if len(compensation) > 0 {
		err := errors.Join(append([]error{cause}, compensation...)...)
		updated, updateErr := s.updateJournal(ctx, j, j.Status, func(cur *Journal) {
			cur.Status = JournalManualRequired
			cur.LastError = err.Error()
		})
		if updateErr == nil {
			j = updated
		} else {
			err = errors.Join(err, updateErr)
		}
		if draftErr := s.transitionDraft(ctx, j.DraftID, expectedRevision, fencingToken, DraftManualRequired, DraftStaging, DraftStaged, DraftCommitting); draftErr != nil {
			err = errors.Join(err, draftErr)
		}
		return j, err
	}

	updated, err := s.updateJournal(ctx, j, j.Status, func(cur *Journal) {
		cur.Status = JournalRolledBack
		cur.LastError = cause.Error()
		for i := range cur.Steps {
			if cur.Steps[i].Status == "staged" || cur.Steps[i].Status == "staging" {
				cur.Steps[i].Status = "rolled_back"
			}
		}
	})
	if err != nil {
		return j, errors.Join(cause, err)
	}
	j = updated
	if err := s.transitionDraft(ctx, j.DraftID, expectedRevision, fencingToken, DraftRolledBack, DraftStaging, DraftStaged, DraftCommitting); err != nil {
		return j, errors.Join(cause, err)
	}
	return j, cause
}

func (s *Service) finishJournalWithoutDraft(ctx context.Context, j Journal, status string, cause error) Journal {
	updated, err := s.updateJournal(ctx, j, j.Status, func(cur *Journal) {
		cur.Status = status
		cur.LastError = cause.Error()
	})
	if err != nil {
		return j
	}
	return updated
}

func (s *Service) rollForward(ctx context.Context, j Journal, expectedRevision, fencingToken int64) (Journal, error) {
	for i := range j.Steps {
		step := j.Steps[i]
		if !step.Generation.Valid() {
			return s.recordCommittedFailure(ctx, j, i, fmt.Errorf("%w: missing staged generation for %s", ErrUnavailable, step.Name))
		}
		fileStore := s.fileStores[step.FileStore]
		if fileStore == nil {
			return s.recordCommittedFailure(ctx, j, i, fmt.Errorf("%w: filestore %s", ErrUnavailable, step.FileStore))
		}
		current, _, err := fileStore.Current(ctx)
		if err == nil && current != step.Generation {
			expected := AssetRef{}
			if step.OldGenerationExists {
				expected = step.OldGeneration
			}
			err = fileStore.FlipCurrent(ctx, expected, step.Generation)
		}
		if err != nil {
			return s.recordCommittedFailure(ctx, j, i, err)
		}
		j, err = s.updateJournal(ctx, j, JournalCommitted, func(cur *Journal) {
			cur.Steps[i].Status = "done"
			cur.Steps[i].Error = ""
			cur.LastError = ""
		})
		if err != nil {
			return j, err
		}
	}
	j, err := s.updateJournal(ctx, j, JournalCommitted, func(cur *Journal) {
		cur.LastError = ""
	})
	if err != nil {
		return j, err
	}
	if err := s.transitionDraft(ctx, j.DraftID, expectedRevision, fencingToken, DraftCommitted, DraftStaging, DraftStaged, DraftCommitting); err != nil {
		return s.recordCommittedFailure(ctx, j, -1, err)
	}
	return j, nil
}

func (s *Service) recordCommittedFailure(ctx context.Context, j Journal, step int, cause error) (Journal, error) {
	updated, err := s.updateJournal(ctx, j, JournalCommitted, func(cur *Journal) {
		if step >= 0 && step < len(cur.Steps) {
			cur.Steps[step].Status = "failed"
			cur.Steps[step].Error = cause.Error()
		}
		cur.LastError = cause.Error()
	})
	if err != nil {
		return j, errors.Join(cause, err)
	}
	return updated, cause
}

// expectedRevision=0 表示由系统恢复流程按当前 revision/fencing 做 CAS，而不是旧写者续写。
func (s *Service) transitionDraft(ctx context.Context, id string, expectedRevision, fencingToken int64, target string, from ...string) error {
	d, ok, err := s.store.GetDraft(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return ErrDraftNotFound
	}
	if d.Status == target {
		return nil
	}
	if target == DraftRolledBack && d.Status == DraftEditing {
		return nil
	}
	allowed := false
	for _, status := range from {
		if d.Status == status {
			allowed = true
			break
		}
	}
	if !allowed {
		return fmt.Errorf("%w: draft status %q cannot become %q", ErrConflict, d.Status, target)
	}
	if expectedRevision == 0 {
		expectedRevision = d.Revision
		fencingToken = d.FencingToken
	}
	_, err = s.store.CASUpdateDraft(ctx, id, expectedRevision, fencingToken, func(cur *Draft) {
		cur.Status = target
		cur.UpdatedAtMs = s.now().UnixMilli()
	})
	return err
}

// ReplayJournal 崩溃重放：无 committed 记录一律回滚，有 committed 记录一律 roll-forward。
func (s *Service) ReplayJournal(ctx context.Context, applyID string) error {
	if s.store == nil {
		return errors.New("contentpkg: store not wired")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	j, ok, err := s.store.GetJournal(ctx, applyID)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("contentpkg: journal %q not found", applyID)
	}
	switch j.Status {
	case JournalCommitted:
		_, err = s.rollForward(ctx, j, 0, 0)
		return err
	case JournalPreparing, JournalStaging, JournalCommitPending:
		_, err = s.rollback(ctx, j, errReplayNoCommit, 0, 0)
		if err == errReplayNoCommit {
			return nil
		}
		return err
	case JournalRolledBack:
		return s.transitionDraft(ctx, j.DraftID, 0, 0, DraftRolledBack, DraftStaging, DraftStaged, DraftCommitting)
	case JournalManualRequired:
		return fmt.Errorf("contentpkg: journal %q requires manual recovery: %s", applyID, j.LastError)
	default:
		return fmt.Errorf("contentpkg: journal %q has unknown status %q", applyID, j.Status)
	}
}

// ReplayPending 供启动恢复扫描所有未完成账本；单项失败不阻断其他账本。
func (s *Service) ReplayPending(ctx context.Context) error {
	if s.store == nil {
		return errors.New("contentpkg: store not wired")
	}
	store, ok := s.store.(JournalRecoveryStore)
	if !ok {
		return fmt.Errorf("%w: journal recovery store", ErrUnavailable)
	}
	journals, err := store.ListPendingJournals(ctx)
	if err != nil {
		return err
	}
	var errs []error
	for _, j := range journals {
		if err := s.ReplayJournal(ctx, j.ApplyID); err != nil {
			errs = append(errs, fmt.Errorf("replay %s: %w", j.ApplyID, err))
		}
	}
	return errors.Join(errs...)
}
