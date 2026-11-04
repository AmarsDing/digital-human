package repo

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/teamwork/digital-human/internal/biz/contentpkg"
	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/ent/applyjournal"
	"github.com/teamwork/digital-human/internal/data/ent/livedraft"
)

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

// CASUpdateDraft 同时匹配 revision 与当前 fencing token，拒绝旧 token 和伪造的未来 token。
func (s *EntContentpkgStore) CASUpdateDraft(ctx context.Context, id string, expectedRevision, fencingToken int64, mutate func(*contentpkg.Draft)) (contentpkg.Draft, error) {
	row, err := s.client.LiveDraft.Query().Where(livedraft.ID(id)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return contentpkg.Draft{}, contentpkg.ErrDraftNotFound
		}
		return contentpkg.Draft{}, err
	}
	d := draftFromEnt(row)
	if fencingToken != d.FencingToken {
		return contentpkg.Draft{}, fmt.Errorf("%w: token %d != current %d", contentpkg.ErrStaleFencing, fencingToken, d.FencingToken)
	}
	mutate(&d)
	n, err := s.client.LiveDraft.Update().
		Where(
			livedraft.ID(id),
			livedraft.Revision(expectedRevision),
			livedraft.FencingToken(fencingToken),
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
	_, err := s.client.ApplyJournal.Create().
		SetID(j.ApplyID).
		SetDraftID(j.DraftID).
		SetBaseRevision(j.BaseRevision).
		SetSnapshotJSON(j.Snapshot).
		SetStepsJSON(journalStepsToEnt(j.Steps)).
		SetStepStatusJSON(journalStepStatusToEnt(j.Steps)).
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

// UpdateJournal 持久化 staged generation、旧 current pointer 与每步状态；状态条件即账本 CAS。
func (s *EntContentpkgStore) UpdateJournal(ctx context.Context, applyID, expectedStatus string, mutate func(*contentpkg.Journal)) (contentpkg.Journal, error) {
	row, err := s.client.ApplyJournal.Query().Where(applyjournal.ID(applyID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return contentpkg.Journal{}, fmt.Errorf("contentpkg: journal %q not found", applyID)
		}
		return contentpkg.Journal{}, err
	}
	j := journalFromEnt(row)
	if j.Status != expectedStatus {
		return contentpkg.Journal{}, fmt.Errorf("%w: journal status %q != expected %q", contentpkg.ErrConflict, j.Status, expectedStatus)
	}
	mutate(&j)
	if j.Status == contentpkg.JournalCommitted && j.CommittedAtMs == 0 {
		j.CommittedAtMs = j.UpdatedAtMs
	}
	if j.Status == contentpkg.JournalRolledBack && j.RolledBackAtMs == 0 {
		j.RolledBackAtMs = j.UpdatedAtMs
	}
	n, err := s.client.ApplyJournal.Update().
		Where(applyjournal.ID(applyID), applyjournal.Status(expectedStatus)).
		SetSnapshotJSON(j.Snapshot).
		SetStepsJSON(journalStepsToEnt(j.Steps)).
		SetStepStatusJSON(journalStepStatusToEnt(j.Steps)).
		SetOldValuesJSON(j.OldValues).
		SetStatus(j.Status).
		SetLastError(j.LastError).
		SetUpdatedAtMs(j.UpdatedAtMs).
		SetCommittedAtMs(j.CommittedAtMs).
		SetRolledBackAtMs(j.RolledBackAtMs).
		Save(ctx)
	if err != nil {
		return contentpkg.Journal{}, err
	}
	if n == 0 {
		return contentpkg.Journal{}, fmt.Errorf("%w: journal %q status changed", contentpkg.ErrConflict, applyID)
	}
	return j, nil
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

func (s *EntContentpkgStore) ListPendingJournals(ctx context.Context) ([]contentpkg.Journal, error) {
	rows, err := s.client.ApplyJournal.Query().
		Where(applyjournal.StatusIn(
			contentpkg.JournalPreparing,
			contentpkg.JournalStaging,
			contentpkg.JournalCommitPending,
			contentpkg.JournalCommitted,
			contentpkg.JournalRolledBack,
		)).
		Order(ent.Asc(applyjournal.FieldCreatedAtMs), ent.Asc(applyjournal.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]contentpkg.Journal, 0, len(rows))
	for _, row := range rows {
		j := journalFromEnt(row)
		pending := j.Status == contentpkg.JournalPreparing || j.Status == contentpkg.JournalStaging || j.Status == contentpkg.JournalCommitPending
		if j.Status == contentpkg.JournalCommitted || j.Status == contentpkg.JournalRolledBack {
			draft, err := s.client.LiveDraft.Query().Where(livedraft.ID(j.DraftID)).Only(ctx)
			if err != nil && !ent.IsNotFound(err) {
				return nil, err
			}
			if j.Status == contentpkg.JournalCommitted {
				pending = j.LastError != "" || ent.IsNotFound(err)
				for _, step := range j.Steps {
					if step.Status != "done" {
						pending = true
					}
				}
				if err == nil && draft.Status != contentpkg.DraftCommitted {
					pending = true
				}
			} else if err == nil && draft.Status != contentpkg.DraftRolledBack && draft.Status != contentpkg.DraftEditing {
				pending = true
			}
		}
		if pending {
			out = append(out, j)
		}
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

func journalStepsToEnt(steps []contentpkg.ApplyStep) []map[string]any {
	out := make([]map[string]any, 0, len(steps))
	for _, step := range steps {
		out = append(out, map[string]any{
			"name": step.Name, "filestore": step.FileStore, "action": step.Action, "status": step.Status,
			"generation":            assetRefToMap(step.Generation),
			"old_generation":        assetRefToMap(step.OldGeneration),
			"old_generation_exists": step.OldGenerationExists,
			"error":                 step.Error,
		})
	}
	return out
}

func journalStepStatusToEnt(steps []contentpkg.ApplyStep) map[string]any {
	out := map[string]any{}
	for _, step := range steps {
		out[step.Name] = step.Status
	}
	return out
}

func assetRefToMap(ref contentpkg.AssetRef) map[string]any {
	return map[string]any{"id": ref.ID, "revision": ref.Revision, "content_hash": ref.ContentHash}
}

func assetRefFromMap(value any) contentpkg.AssetRef {
	if ref, ok := value.(contentpkg.AssetRef); ok {
		return ref
	}
	m, ok := value.(map[string]any)
	if !ok {
		return contentpkg.AssetRef{}
	}
	return contentpkg.AssetRef{ID: strOf(m["id"]), Revision: int64Of(m["revision"]), ContentHash: strOf(m["content_hash"])}
}

func int64Of(value any) int64 {
	switch v := value.(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	default:
		return 0
	}
}

func boolOf(value any) bool {
	v, _ := value.(bool)
	return v
}

func journalFromEnt(r *ent.ApplyJournal) contentpkg.Journal {
	steps := make([]contentpkg.ApplyStep, 0, len(r.StepsJSON))
	for _, sm := range r.StepsJSON {
		steps = append(steps, contentpkg.ApplyStep{
			Name: strOf(sm["name"]), FileStore: strOf(sm["filestore"]), Action: strOf(sm["action"]), Status: strOf(sm["status"]),
			Generation: assetRefFromMap(sm["generation"]), OldGeneration: assetRefFromMap(sm["old_generation"]),
			OldGenerationExists: boolOf(sm["old_generation_exists"]), Error: strOf(sm["error"]),
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

// SwitchableContentpkgStore 内存启动 → PG 就绪热切。
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
func (s *SwitchableContentpkgStore) UpdateJournal(ctx context.Context, applyID, expectedStatus string, mutate func(*contentpkg.Journal)) (contentpkg.Journal, error) {
	store, ok := s.current().(contentpkg.JournalStore)
	if !ok {
		return contentpkg.Journal{}, fmt.Errorf("%w: journal progress store", contentpkg.ErrUnavailable)
	}
	return store.UpdateJournal(ctx, applyID, expectedStatus, mutate)
}
func (s *SwitchableContentpkgStore) ListPendingJournals(ctx context.Context) ([]contentpkg.Journal, error) {
	store, ok := s.current().(contentpkg.JournalRecoveryStore)
	if !ok {
		return nil, fmt.Errorf("%w: journal recovery store", contentpkg.ErrUnavailable)
	}
	return store.ListPendingJournals(ctx)
}

var _ contentpkg.Store = (*EntContentpkgStore)(nil)
var _ contentpkg.JournalStore = (*EntContentpkgStore)(nil)
var _ contentpkg.JournalRecoveryStore = (*EntContentpkgStore)(nil)
var _ contentpkg.Store = (*SwitchableContentpkgStore)(nil)
var _ contentpkg.JournalStore = (*SwitchableContentpkgStore)(nil)
var _ contentpkg.JournalRecoveryStore = (*SwitchableContentpkgStore)(nil)
