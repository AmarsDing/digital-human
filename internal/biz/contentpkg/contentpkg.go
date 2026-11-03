// Package contentpkg 实现深化方案 v2 §7.1/§7.3/§7.4 内容包原子性骨架：
//   - LiveDraft 版本化草稿：单写者 owner + fencing_token + expected_revision CAS（§7.3），
//     撞车 409，旧 fencing token 显式拒绝；
//   - ApplyJournal 提交点/补偿/崩溃重放账本（§7.4）；
//   - 跨 FileStore 提交点与补偿本期为空实现，**显式返回 ErrNotImplemented**，
//     绝不静默半成功（切片 0 DoD）。
package contentpkg

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// AssetRef 版本化资产引用（§7.1）：三字段齐全才允许进入公开链路。
type AssetRef struct {
	ID          string `json:"id"`
	Revision    int64  `json:"revision"`
	ContentHash string `json:"content_hash"`
}

// Valid 三字段齐全。
func (r AssetRef) Valid() bool { return r.ID != "" && r.Revision > 0 && r.ContentHash != "" }

// Draft 状态（§7.1）。
const (
	DraftEditing        = "editing"
	DraftStaging        = "staging"
	DraftStaged         = "staged"
	DraftCommitting     = "committing"
	DraftCommitted      = "committed"
	DraftRolledBack     = "rolled_back"
	DraftManualRequired = "manual_required"
)

// Journal 状态（§7.4）。
const (
	JournalPreparing      = "preparing"
	JournalStaging        = "staging"
	JournalCommitPending  = "commit_pending"
	JournalCommitted      = "committed"
	JournalRolledBack     = "rolled_back"
	JournalManualRequired = "manual_required"
)

var (
	// ErrDraftNotFound 草稿不存在。
	ErrDraftNotFound = errors.New("contentpkg: draft not found")
	// ErrConflict CAS 冲突（409）：revision 不匹配或 lease 被他人持有。
	ErrConflict = errors.New("contentpkg: conflict")
	// ErrStaleFencing 旧 fencing token 写者被拒（§7.3：lease 过期接管后旧写者不得继续提交）。
	ErrStaleFencing = errors.New("contentpkg: stale fencing token")
	// ErrNotLeaseOwner 非 lease 持有者。
	ErrNotLeaseOwner = errors.New("contentpkg: not lease owner")
	// ErrNotImplemented 跨 FileStore 提交点/补偿本期空实现——显式失败而非半成功。
	ErrNotImplemented = errors.New("contentpkg: cross-filestore commit not implemented (slice 0 skeleton)")
)

// Draft LiveDraft 记录（§7.1 逐字段）。
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

// ApplyStep 单步计划/执行记录（§7.4：固定顺序 Koubo → Choreo → Ref/其他）。
type ApplyStep struct {
	Name      string `json:"name"`      // koubo / choreo / refs ...
	FileStore string `json:"filestore"` // 目标 FileStore 实例
	Action    string `json:"action"`    // write_immutable / stage_generation / flip_pointer ...
	Status    string `json:"status"`    // pending/done/failed
}

// Journal ApplyJournal 记录（§7.4 逐字段）。
type Journal struct {
	ApplyID        string
	DraftID        string
	BaseRevision   int64
	Snapshot       map[string]any // 完整新快照（prepare 先持久化再动 FileStore）
	Steps          []ApplyStep
	OldValues      map[string]any // 可变 FileStore 旧 generation/hash（补偿逆序恢复用）
	Status         string
	LastError      string
	CreatedAtMs    int64
	UpdatedAtMs    int64
	CommittedAtMs  int64
	RolledBackAtMs int64
}

// Store 草稿/账本持久化端口。
type Store interface {
	CreateDraft(ctx context.Context, d Draft) error
	GetDraft(ctx context.Context, id string) (Draft, bool, error)
	// CASUpdateDraft 条件更新：WHERE id AND revision=expectedRevision AND fencing_token=fencingToken；
	// 影响 0 行 → 由调用方区分 ErrConflict/ErrStaleFencing。
	CASUpdateDraft(ctx context.Context, id string, expectedRevision, fencingToken int64, mutate func(*Draft)) (Draft, error)
	// ForceUpdateDraft lease 接管用：仅按 id 更新（先读后写，调用方负责递增 fencing）。
	UpdateDraftLease(ctx context.Context, id, ownerID string, leaseUntilMs, newFencingToken int64) error
	CreateJournal(ctx context.Context, j Journal) error
	GetJournal(ctx context.Context, applyID string) (Journal, bool, error)
	UpdateJournalStatus(ctx context.Context, applyID, status, lastErr string, nowMs int64) error
	ListJournalsByDraft(ctx context.Context, draftID string) ([]Journal, error)
}

// Service 草稿 CAS 与 apply 骨架服务。
type Service struct {
	store Store
	now   func() time.Time
	newID func() string
}

// NewService store 为 nil 时所有写操作显式报错（fail-closed）。
func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now, newID: newUUID}
}

// CreateDraft 以 revision=1/editing 建档。
func (s *Service) CreateDraft(ctx context.Context, templateID string, baseRevision, schemaVersion int64, snapshot map[string]any) (Draft, error) {
	if s.store == nil {
		return Draft{}, errors.New("contentpkg: store not wired")
	}
	nowMs := s.now().UnixMilli()
	d := Draft{
		ID: s.newID(), TemplateID: templateID, BaseTemplateRevision: baseRevision,
		Revision: 1, SchemaVersion: schemaVersion, Status: DraftEditing,
		Snapshot: snapshot, SnapshotHash: hashSnapshot(snapshot),
		CreatedAtMs: nowMs, UpdatedAtMs: nowMs,
	}
	if err := s.store.CreateDraft(ctx, d); err != nil {
		return Draft{}, err
	}
	return d, nil
}

// OpenEdit 申请/续约单写者 lease（§7.3）：
// lease 有效期内仅 holder 可续约；过期可被新 owner 接管，**fencing_token 必须递增**。
func (s *Service) OpenEdit(ctx context.Context, draftID, ownerID string, leaseTTL time.Duration) (Draft, error) {
	if s.store == nil {
		return Draft{}, errors.New("contentpkg: store not wired")
	}
	d, ok, err := s.store.GetDraft(ctx, draftID)
	if err != nil {
		return Draft{}, err
	}
	if !ok {
		return Draft{}, ErrDraftNotFound
	}
	nowMs := s.now().UnixMilli()
	if d.LeaseUntilMs > nowMs && d.OwnerID != "" && d.OwnerID != ownerID {
		return Draft{}, fmt.Errorf("%w: lease held by %q until %d", ErrConflict, d.OwnerID, d.LeaseUntilMs)
	}
	newToken := d.FencingToken + 1 // 接管与续约都递增，旧 token 一律作废
	if err := s.store.UpdateDraftLease(ctx, draftID, ownerID, nowMs+leaseTTL.Milliseconds(), newToken); err != nil {
		return Draft{}, err
	}
	d, _, err = s.store.GetDraft(ctx, draftID)
	return d, err
}

// Save CAS 保存快照（§7.3）：owner + fencing_token + expected_revision 三重校验，
// 撞车返回 ErrConflict（服务层映射 409）。
func (s *Service) Save(ctx context.Context, draftID, ownerID string, fencingToken, expectedRevision int64, snapshot map[string]any) (Draft, error) {
	if s.store == nil {
		return Draft{}, errors.New("contentpkg: store not wired")
	}
	d, ok, err := s.store.GetDraft(ctx, draftID)
	if err != nil {
		return Draft{}, err
	}
	if !ok {
		return Draft{}, ErrDraftNotFound
	}
	nowMs := s.now().UnixMilli()
	if d.OwnerID != ownerID {
		return Draft{}, fmt.Errorf("%w: owner %q != %q", ErrNotLeaseOwner, ownerID, d.OwnerID)
	}
	if d.LeaseUntilMs <= nowMs {
		return Draft{}, fmt.Errorf("%w: lease expired", ErrConflict)
	}
	if fencingToken < d.FencingToken {
		return Draft{}, fmt.Errorf("%w: token %d < current %d", ErrStaleFencing, fencingToken, d.FencingToken)
	}
	if d.Revision != expectedRevision {
		return Draft{}, fmt.Errorf("%w: expected_revision %d != current %d", ErrConflict, expectedRevision, d.Revision)
	}
	updated, err := s.store.CASUpdateDraft(ctx, draftID, expectedRevision, fencingToken, func(cur *Draft) {
		cur.Snapshot = snapshot
		cur.SnapshotHash = hashSnapshot(snapshot)
		cur.Revision = expectedRevision + 1
		cur.UpdatedAtMs = nowMs
	})
	if err != nil {
		return Draft{}, err
	}
	return updated, nil
}

// Apply 内容包发布骨架（§7.4）：
// prepare——CAS 草稿 editing→staging、写 journal（完整新快照 + 每步计划 + 旧值占位）；
// commit/rollback——跨 FileStore 提交点与补偿本期空实现，**显式返回 ErrNotImplemented**，
// journal 状态落 manual_required 语义之外的显式失败标注（status=staging + last_error）。
func (s *Service) Apply(ctx context.Context, draftID, ownerID string, fencingToken, expectedRevision int64, preflightID string) (Journal, error) {
	if s.store == nil {
		return Journal{}, errors.New("contentpkg: store not wired")
	}
	d, ok, err := s.store.GetDraft(ctx, draftID)
	if err != nil {
		return Journal{}, err
	}
	if !ok {
		return Journal{}, ErrDraftNotFound
	}
	nowMs := s.now().UnixMilli()
	if d.OwnerID != ownerID {
		return Journal{}, fmt.Errorf("%w: owner %q != %q", ErrNotLeaseOwner, ownerID, d.OwnerID)
	}
	if fencingToken < d.FencingToken {
		return Journal{}, fmt.Errorf("%w: token %d < current %d", ErrStaleFencing, fencingToken, d.FencingToken)
	}
	if d.Revision != expectedRevision {
		return Journal{}, fmt.Errorf("%w: expected_revision %d != current %d", ErrConflict, expectedRevision, d.Revision)
	}
	if d.Status != DraftEditing {
		return Journal{}, fmt.Errorf("%w: draft status %q not editable", ErrConflict, d.Status)
	}
	// prepare：先持久化 journal（完整新快照 + 固定顺序计划），再 CAS 草稿状态。
	j := Journal{
		ApplyID: s.newID(), DraftID: draftID, BaseRevision: d.Revision,
		Snapshot: d.Snapshot,
		Steps: []ApplyStep{
			{Name: "koubo", FileStore: "koubo", Action: "write_immutable+flip_pointer", Status: "pending"},
			{Name: "choreo", FileStore: "choreo", Action: "stage_generation+flip_pointer", Status: "pending"},
			{Name: "refs", FileStore: "refs", Action: "write_immutable+flip_pointer", Status: "pending"},
		},
		OldValues:   map[string]any{},
		Status:      JournalPreparing,
		CreatedAtMs: nowMs, UpdatedAtMs: nowMs,
	}
	if err := s.store.CreateJournal(ctx, j); err != nil {
		return Journal{}, err
	}
	if _, err := s.store.CASUpdateDraft(ctx, draftID, expectedRevision, fencingToken, func(cur *Draft) {
		cur.Status = DraftStaging
		cur.PreflightID = preflightID
		cur.UpdatedAtMs = nowMs
	}); err != nil {
		_ = s.store.UpdateJournalStatus(ctx, j.ApplyID, JournalManualRequired, "draft cas after journal: "+err.Error(), s.now().UnixMilli())
		return Journal{}, err
	}
	// commit/rollback 空实现：显式未实现错误，journal 落 staging + 原因；不静默半成功。
	errNotImpl := fmt.Errorf("apply %s: %w", j.ApplyID, ErrNotImplemented)
	_ = s.store.UpdateJournalStatus(ctx, j.ApplyID, JournalStaging, ErrNotImplemented.Error(), s.now().UnixMilli())
	j.Status = JournalStaging
	j.LastError = ErrNotImplemented.Error()
	return j, errNotImpl
}

// ReplayJournal 崩溃重放骨架（§7.4）：按 apply_id 查唯一 commit——
// 有 commit 记录则 roll-forward，无则回滚旧完整集合；本期显式 ErrNotImplemented。
func (s *Service) ReplayJournal(ctx context.Context, applyID string) error {
	if s.store == nil {
		return errors.New("contentpkg: store not wired")
	}
	if _, ok, err := s.store.GetJournal(ctx, applyID); err != nil {
		return err
	} else if !ok {
		return fmt.Errorf("contentpkg: journal %q not found", applyID)
	}
	return ErrNotImplemented
}

func newUUID() string {
	return fmt.Sprintf("cp-%d-%s", time.Now().UnixNano(), randHex(4))
}
