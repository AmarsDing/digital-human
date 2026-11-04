// Package contentpkg 实现版本化草稿、跨 FileStore generation 发布与崩溃恢复。
package contentpkg

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// AssetRef 版本化资产引用：至少绑定 revision 或 content_hash 才允许进入公开链路。
type AssetRef struct {
	ID          string `json:"id"`
	Revision    int64  `json:"revision"`
	ContentHash string `json:"content_hash"`
}

func (r AssetRef) Valid() bool { return r.ID != "" && (r.Revision > 0 || r.ContentHash != "") }

const (
	DraftEditing        = "editing"
	DraftStaging        = "staging"
	DraftStaged         = "staged"
	DraftCommitting     = "committing"
	DraftCommitted      = "committed"
	DraftRolledBack     = "rolled_back"
	DraftManualRequired = "manual_required"
)

const (
	JournalPreparing      = "preparing"
	JournalStaging        = "staging"
	JournalCommitPending  = "commit_pending"
	JournalCommitted      = "committed"
	JournalRolledBack     = "rolled_back"
	JournalManualRequired = "manual_required"
)

var (
	ErrDraftNotFound = errors.New("contentpkg: draft not found")
	ErrConflict      = errors.New("contentpkg: conflict")
	ErrStaleFencing  = errors.New("contentpkg: stale fencing token")
	ErrNotLeaseOwner = errors.New("contentpkg: not lease owner")
	// ErrNotImplemented 保留给旧调用方做错误兼容；已接入 FileStore 的发布不再返回它。
	ErrNotImplemented = errors.New("contentpkg: cross-filestore commit not implemented")
)

type Draft struct {
	ID                   string
	TemplateID           string
	BaseTemplateRevision int64
	Revision             int64
	SchemaVersion        int64
	OwnerID              string
	LeaseUntilMs         int64
	FencingToken         int64
	Status               string
	Snapshot             map[string]any
	SnapshotHash         string
	PreflightID          string
	CreatedAtMs          int64
	UpdatedAtMs          int64
}

type ApplyStep struct {
	Name                string   `json:"name"`
	FileStore           string   `json:"filestore"`
	Action              string   `json:"action"`
	Status              string   `json:"status"`
	Generation          AssetRef `json:"generation"`
	OldGeneration       AssetRef `json:"old_generation"`
	OldGenerationExists bool     `json:"old_generation_exists"`
	Error               string   `json:"error,omitempty"`
}

type Journal struct {
	ApplyID        string
	DraftID        string
	BaseRevision   int64
	Snapshot       map[string]any
	Steps          []ApplyStep
	OldValues      map[string]any
	Status         string
	LastError      string
	CreatedAtMs    int64
	UpdatedAtMs    int64
	CommittedAtMs  int64
	RolledBackAtMs int64
}

// Store 保持原有草稿/账本端口；发布进度与启动恢复使用额外窄接口。
type Store interface {
	CreateDraft(ctx context.Context, d Draft) error
	GetDraft(ctx context.Context, id string) (Draft, bool, error)
	CASUpdateDraft(ctx context.Context, id string, expectedRevision, fencingToken int64, mutate func(*Draft)) (Draft, error)
	UpdateDraftLease(ctx context.Context, id, ownerID string, leaseUntilMs, newFencingToken int64) error
	CreateJournal(ctx context.Context, j Journal) error
	GetJournal(ctx context.Context, applyID string) (Journal, bool, error)
	UpdateJournalStatus(ctx context.Context, applyID, status, lastErr string, nowMs int64) error
	ListJournalsByDraft(ctx context.Context, draftID string) ([]Journal, error)
}

type Service struct {
	store      Store
	now        func() time.Time
	newID      func() string
	fileStores map[string]FileStore
	mu         sync.Mutex
}

func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now, newID: newUUID}
}

func (s *Service) CreateDraft(ctx context.Context, templateID string, baseRevision, schemaVersion int64, snapshot map[string]any) (Draft, error) {
	if s.store == nil {
		return Draft{}, errors.New("contentpkg: store not wired")
	}
	if err := validateAssetRefs(snapshot); err != nil {
		return Draft{}, err
	}
	nowMs := s.now().UnixMilli()
	d := Draft{
		ID: s.newID(), TemplateID: templateID, BaseTemplateRevision: baseRevision,
		Revision: 1, SchemaVersion: schemaVersion, Status: DraftEditing,
		Snapshot: cloneMap(snapshot), SnapshotHash: HashSnapshot(snapshot),
		CreatedAtMs: nowMs, UpdatedAtMs: nowMs,
	}
	if err := s.store.CreateDraft(ctx, d); err != nil {
		return Draft{}, err
	}
	return d, nil
}

func (s *Service) OpenEdit(ctx context.Context, draftID, ownerID string, leaseTTL time.Duration) (Draft, error) {
	if s.store == nil {
		return Draft{}, errors.New("contentpkg: store not wired")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok, err := s.store.GetDraft(ctx, draftID)
	if err != nil {
		return Draft{}, err
	}
	if !ok {
		return Draft{}, ErrDraftNotFound
	}
	if d.Status != DraftEditing {
		return Draft{}, fmt.Errorf("%w: draft status %q is not editable", ErrConflict, d.Status)
	}
	nowMs := s.now().UnixMilli()
	if d.LeaseUntilMs > nowMs && d.OwnerID != "" && d.OwnerID != ownerID {
		return Draft{}, fmt.Errorf("%w: lease held by %q until %d", ErrConflict, d.OwnerID, d.LeaseUntilMs)
	}
	newToken := d.FencingToken + 1
	if err := s.store.UpdateDraftLease(ctx, draftID, ownerID, nowMs+leaseTTL.Milliseconds(), newToken); err != nil {
		return Draft{}, err
	}
	d, _, err = s.store.GetDraft(ctx, draftID)
	return d, err
}

func (s *Service) Save(ctx context.Context, draftID, ownerID string, fencingToken, expectedRevision int64, snapshot map[string]any) (Draft, error) {
	if s.store == nil {
		return Draft{}, errors.New("contentpkg: store not wired")
	}
	if err := validateAssetRefs(snapshot); err != nil {
		return Draft{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok, err := s.store.GetDraft(ctx, draftID)
	if err != nil {
		return Draft{}, err
	}
	if !ok {
		return Draft{}, ErrDraftNotFound
	}
	if d.OwnerID != ownerID {
		return Draft{}, fmt.Errorf("%w: owner %q != %q", ErrNotLeaseOwner, ownerID, d.OwnerID)
	}
	if d.LeaseUntilMs <= s.now().UnixMilli() {
		return Draft{}, fmt.Errorf("%w: lease expired", ErrConflict)
	}
	if fencingToken != d.FencingToken {
		return Draft{}, fmt.Errorf("%w: token %d != current %d", ErrStaleFencing, fencingToken, d.FencingToken)
	}
	if d.Revision != expectedRevision {
		return Draft{}, fmt.Errorf("%w: expected_revision %d != current %d", ErrConflict, expectedRevision, d.Revision)
	}
	if d.Status != DraftEditing {
		return Draft{}, fmt.Errorf("%w: draft status %q not editable", ErrConflict, d.Status)
	}
	nowMs := s.now().UnixMilli()
	return s.store.CASUpdateDraft(ctx, draftID, expectedRevision, fencingToken, func(cur *Draft) {
		cur.Snapshot = cloneMap(snapshot)
		cur.SnapshotHash = HashSnapshot(snapshot)
		cur.Revision = expectedRevision + 1
		cur.UpdatedAtMs = nowMs
	})
}

func validateAssetRefs(value any) error {
	switch v := value.(type) {
	case AssetRef:
		if !v.Valid() {
			return fmt.Errorf("%w: asset %q is not bound to revision or content hash", ErrConflict, v.ID)
		}
	case *AssetRef:
		if v != nil {
			return validateAssetRefs(*v)
		}
	case map[string]any:
		for _, item := range v {
			if err := validateAssetRefs(item); err != nil {
				return err
			}
		}
	case []any:
		for _, item := range v {
			if err := validateAssetRefs(item); err != nil {
				return err
			}
		}
	case []AssetRef:
		for _, item := range v {
			if err := validateAssetRefs(item); err != nil {
				return err
			}
		}
	case map[string]AssetRef:
		for _, item := range v {
			if err := validateAssetRefs(item); err != nil {
				return err
			}
		}
	}
	return nil
}

func newUUID() string {
	return fmt.Sprintf("cp-%d-%s", time.Now().UnixNano(), randHex(4))
}
