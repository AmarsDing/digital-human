package repo

import (
	"context"
	"fmt"
	"sync"

	"github.com/teamwork/digital-human/internal/biz/contentpkg"
	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/ent/applyjournal"
	"github.com/teamwork/digital-human/internal/data/ent/livedraft"
)

// EntDraftStore contentpkg.Store 的 Ent 实现：
// CAS 用条件更新（WHERE revision=? AND fencing_token<=?），0 行受影响 → 409 语义。
type EntContentpkgStore struct {
	client *ent.Client
}

func NewEntContentpkgStore(client *ent.Client) *EntContentpkgStore {
	return &EntContentpkgStore{client: client}
}

func (s *EntContentpkgStore) CreateDraft(ctx context.Context, d contentpkg.Draft) error {
	_, err := s.client.LiveDraft.Create().
		SetID(d.ID).
		SetTemplateID(d.TemplateID).
		SetBaseTemplateRevision(d.BaseTemplateRevision).
		SetRevision(d.Revision).
		SetSchemaVersion(d.SchemaVersion).
		SetOwnerID(d.OwnerID).
		SetLeaseUntilMs(d.LeaseUntilMs).
		SetFencingToken(d.FencingToken).
		SetStatus(d.Status).
		SetSnapshotJSON(d.Snapshot).
		SetSnapshotHash(d.SnapshotHash).
		SetPreflightID(d.PreflightID).
		SetCreatedAtMs(d.CreatedAtMs).
		SetUpdatedAtMs(d.UpdatedAtMs).
		Save(ctx)
	return err
}

func (s *EntContentpkgStore) GetDraft(ctx context.Context, id string) (contentpkg.Draft, bool, error) {
	row, err := s.client.LiveDraft.Query().Where(livedraft.ID(id)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return contentpkg.Draft{}, false, nil
		}
		return contentpkg.Draft{}, false, err
	}
	return draftFromEnt(row), true, nil
}

// CASUpdateDraft fencing 语义：fencingToken >= 库存值 才允许（旧 token 拒绝）。
func (s *EntContentpkgStore) CASUpdateDraft(ctx context.Context, id string, expectedRevision, fencingToken int64, mutate func(*contentpkg.Draft)) (contentpkg.Draft, error) {
	row, err := s.client.LiveDraft.Query().Where(livedraft.ID(id)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return contentpkg.Draft{}, contentpkg.ErrDraftNotFound
		}
		return contentpkg.Draft{}, err
	}
	d := draftFromEnt(row)
	if fencingToken < d.FencingToken {
		return contentpkg.Draft{}, fmt.Errorf("%w: token %d < current %d", contentpkg.ErrStaleFencing, fencingToken, d.FencingToken)
	}
	mutate(&d)
	n, err := s.client.LiveDraft.Update().
		Where(
			livedraft.ID(id),
			livedraft.Revision(expectedRevision),
		).
		SetTemplateID(d.TemplateID).
		SetBaseTemplateRevision(d.BaseTemplateRevision).
		SetRevision(d.Revision).
		SetSchemaVersion(d.SchemaVersion).
		SetOwnerID(d.OwnerID).
		SetLeaseUntilMs(d.LeaseUntilMs).
		SetFencingToken(d.FencingToken).
		SetStatus(d.Status).
		SetSnapshotJSON(d.Snapshot).
		SetSnapshotHash(d.SnapshotHash).
		SetPreflightID(d.PreflightID).
		SetUpdatedAtMs(d.UpdatedAtMs).
		Save(ctx)
	if err != nil {
		return contentpkg.Draft{}, err
	}
	if n == 0 {
		return contentpkg.Draft{}, fmt.Errorf("%w: expected_revision %d lost race", contentpkg.ErrConflict, expectedRevision)
	}
	return d, nil
}

func (s *EntContentpkgStore) UpdateDraftLease(ctx context.Context, id, ownerID string, leaseUntilMs, newFencingToken int64) error {
	n, err := s.client.LiveDraft.Update().
		Where(livedraft.ID(id)).
		SetOwnerID(ownerID).
		SetLeaseUntilMs(leaseUntilMs).
		SetFencingToken(newFencingToken).
		Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return contentpkg.ErrDraftNotFound
	}
	return nil
}

func (s *EntContentpkgStore) CreateJournal(ctx context.Context, j contentpkg.Journal) error {
	steps := make([]map[string]any, 0, len(j.Steps))
	for _, st := range j.Steps {
		steps = append(steps, map[string]any{
			"name": st.Name, "filestore": st.FileStore, "action": st.Action, "status": st.Status,
		})
	}
	stepStatus := map[string]any{}
	for _, st := range j.Steps {
		stepStatus[st.Name] = st.Status
	}
	_, err := s.client.ApplyJournal.Create().
		SetID(j.ApplyID).
		SetDraftID(j.DraftID).
		SetBaseRevision(j.BaseRevision).
		SetSnapshotJSON(j.Snapshot).
		SetStepsJSON(steps).
		SetStepStatusJSON(stepStatus).
		SetOldValuesJSON(j.OldValues).
		SetStatus(j.Status).
		SetLastError(j.LastError).
		SetCreatedAtMs(j.CreatedAtMs).
		SetUpdatedAtMs(j.UpdatedAtMs).
		SetCommittedAtMs(j.CommittedAtMs).
		SetRolledBackAtMs(j.RolledBackAtMs).
		Save(ctx)
	return err
}

func (s *EntContentpkgStore) GetJournal(ctx context.Context, applyID string) (contentpkg.Journal, bool, error) {
	row, err := s.client.ApplyJournal.Query().Where(applyjournal.ID(applyID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return contentpkg.Journal{}, false, nil
		}
		return contentpkg.Journal{}, false, err
	}
	return journalFromEnt(row), true, nil
}

func (s *EntContentpkgStore) UpdateJournalStatus(ctx context.Context, applyID, status, lastErr string, nowMs int64) error {
	u := s.client.ApplyJournal.Update().
		Where(applyjournal.ID(applyID)).
		SetStatus(status).
		SetLastError(lastErr).
		SetUpdatedAtMs(nowMs)
	switch status {
	case contentpkg.JournalCommitted:
		u.SetCommittedAtMs(nowMs)
	case contentpkg.JournalRolledBack:
		u.SetRolledBackAtMs(nowMs)
	}
	n, err := u.Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return fmt.Errorf("contentpkg: journal %q not found", applyID)
	}
	return nil
}

func (s *EntContentpkgStore) ListJournalsByDraft(ctx context.Context, draftID string) ([]contentpkg.Journal, error) {
	rows, err := s.client.ApplyJournal.Query().
		Where(applyjournal.DraftID(draftID)).
		Order(ent.Asc(applyjournal.FieldCreatedAtMs)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]contentpkg.Journal, 0, len(rows))
	for _, r := range rows {
		out = append(out, journalFromEnt(r))
	}
	return out, nil
}

func draftFromEnt(r *ent.LiveDraft) contentpkg.Draft {
	return contentpkg.Draft{
		ID: r.ID, TemplateID: r.TemplateID, BaseTemplateRevision: r.BaseTemplateRevision,
		Revision: r.Revision, SchemaVersion: r.SchemaVersion, OwnerID: r.OwnerID,
		LeaseUntilMs: r.LeaseUntilMs, FencingToken: r.FencingToken, Status: r.Status,
		Snapshot: r.SnapshotJSON, SnapshotHash: r.SnapshotHash, PreflightID: r.PreflightID,
		CreatedAtMs: r.CreatedAtMs, UpdatedAtMs: r.UpdatedAtMs,
	}
}

func journalFromEnt(r *ent.ApplyJournal) contentpkg.Journal {
	steps := make([]contentpkg.ApplyStep, 0, len(r.StepsJSON))
	for _, sm := range r.StepsJSON {
		steps = append(steps, contentpkg.ApplyStep{
			Name:      strOf(sm["name"]),
			FileStore: strOf(sm["filestore"]),
			Action:    strOf(sm["action"]),
			Status:    strOf(sm["status"]),
		})
	}
	return contentpkg.Journal{
		ApplyID: r.ID, DraftID: r.DraftID, BaseRevision: r.BaseRevision,
		Snapshot: r.SnapshotJSON, Steps: steps, OldValues: r.OldValuesJSON,
		Status: r.Status, LastError: r.LastError,
		CreatedAtMs: r.CreatedAtMs, UpdatedAtMs: r.UpdatedAtMs,
		CommittedAtMs: r.CommittedAtMs, RolledBackAtMs: r.RolledBackAtMs,
	}
}

func strOf(v any) string {
	s, _ := v.(string)
	return s
}

// SwitchableContentpkgStore 内存启动 → PG 就绪热切（与 SessionStore 同先例）。
type SwitchableContentpkgStore struct {
	mu  sync.RWMutex
	mem contentpkg.Store
	db  contentpkg.Store
}

func NewSwitchableContentpkgStore(mem contentpkg.Store) *SwitchableContentpkgStore {
	return &SwitchableContentpkgStore{mem: mem}
}

func (s *SwitchableContentpkgStore) SetEnt(client *ent.Client) {
	s.mu.Lock()
	s.db = NewEntContentpkgStore(client)
	s.mu.Unlock()
}

func (s *SwitchableContentpkgStore) current() contentpkg.Store {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db != nil {
		return s.db
	}
	return s.mem
}

func (s *SwitchableContentpkgStore) CreateDraft(ctx context.Context, d contentpkg.Draft) error {
	return s.current().CreateDraft(ctx, d)
}
func (s *SwitchableContentpkgStore) GetDraft(ctx context.Context, id string) (contentpkg.Draft, bool, error) {
	return s.current().GetDraft(ctx, id)
}
func (s *SwitchableContentpkgStore) CASUpdateDraft(ctx context.Context, id string, expectedRevision, fencingToken int64, mutate func(*contentpkg.Draft)) (contentpkg.Draft, error) {
	return s.current().CASUpdateDraft(ctx, id, expectedRevision, fencingToken, mutate)
}
func (s *SwitchableContentpkgStore) UpdateDraftLease(ctx context.Context, id, ownerID string, leaseUntilMs, newFencingToken int64) error {
	return s.current().UpdateDraftLease(ctx, id, ownerID, leaseUntilMs, newFencingToken)
}
func (s *SwitchableContentpkgStore) CreateJournal(ctx context.Context, j contentpkg.Journal) error {
	return s.current().CreateJournal(ctx, j)
}
func (s *SwitchableContentpkgStore) GetJournal(ctx context.Context, applyID string) (contentpkg.Journal, bool, error) {
	return s.current().GetJournal(ctx, applyID)
}
func (s *SwitchableContentpkgStore) UpdateJournalStatus(ctx context.Context, applyID, status, lastErr string, nowMs int64) error {
	return s.current().UpdateJournalStatus(ctx, applyID, status, lastErr, nowMs)
}
func (s *SwitchableContentpkgStore) ListJournalsByDraft(ctx context.Context, draftID string) ([]contentpkg.Journal, error) {
	return s.current().ListJournalsByDraft(ctx, draftID)
}

var _ contentpkg.Store = (*EntContentpkgStore)(nil)
var _ contentpkg.Store = (*SwitchableContentpkgStore)(nil)
