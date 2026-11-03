package repo

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/teamwork/digital-human/internal/biz/avatar"
	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/ent/asset"
	"github.com/teamwork/digital-human/internal/data/ent/persona"
	"github.com/teamwork/digital-human/internal/data/ent/personaref"
)

// EntAvatarStore Persona/PersonaRef/Asset 三表的 AvatarStore 实现
// （v3 §7.1 十原语逐字对齐；同时实现 AssetRegistry 素材登记端口）。
//
// 软删 slug 复用：Persona.slug 有 DB 唯一索引（含已删行），SoftDelete 时把 slug
// 改写为 `<slug>#deleted-<id>` 释放位（Get/List/GetBySlug 默认排除已删行，改写仅
// 影响 includeDeleted 视图），与 mem 语义对齐。
type EntAvatarStore struct {
	client *ent.Client
}

func NewEntAvatarStore(client *ent.Client) *EntAvatarStore { return &EntAvatarStore{client: client} }

// Backend 存储后端标识（"ent"）。
func (s *EntAvatarStore) Backend() string { return "ent" }

func syncToMap(v avatar.ViduSyncState) map[string]any {
	m := map[string]any{"state": v.State}
	if v.Stage != "" {
		m["stage"] = v.Stage
	}
	if v.Error != "" {
		m["error"] = v.Error
	}
	if v.JobID != "" {
		m["job_id"] = v.JobID
	}
	if v.UpdatedAtMs != 0 {
		m["updated_at_ms"] = v.UpdatedAtMs
	}
	if v.ViduAvatarID != "" {
		m["vidu_avatar_id"] = v.ViduAvatarID
	}
	if v.AssetSHA256 != "" {
		m["asset_sha256"] = v.AssetSHA256
	}
	return m
}

func syncFromMap(m map[string]any) avatar.ViduSyncState {
	v := avatar.ViduSyncState{State: "idle"}
	if m == nil {
		return v
	}
	if s, ok := m["state"].(string); ok && s != "" {
		v.State = s
	}
	if s, ok := m["stage"].(string); ok {
		v.Stage = s
	}
	if s, ok := m["error"].(string); ok {
		v.Error = s
	}
	if s, ok := m["job_id"].(string); ok {
		v.JobID = s
	}
	if s, ok := m["vidu_avatar_id"].(string); ok {
		v.ViduAvatarID = s
	}
	if s, ok := m["asset_sha256"].(string); ok {
		v.AssetSHA256 = s
	}
	switch t := m["updated_at_ms"].(type) {
	case float64:
		v.UpdatedAtMs = int64(t)
	case int64:
		v.UpdatedAtMs = t
	}
	return v
}

func entAssetToBiz(a *ent.Asset) avatar.Asset {
	return avatar.Asset{
		ID:          a.ID,
		SHA256:      a.Sha256,
		Kind:        a.Kind,
		Name:        a.Name,
		URI:         a.ViduURI,
		Path:        a.Path,
		ExpiresAtMs: a.ExpiresAtMs,
	}
}

func entAvatarBase(p *ent.Persona) avatar.Avatar {
	return avatar.Avatar{
		ID:              p.ID,
		Slug:            p.Slug,
		Name:            p.Name,
		Tagline:         p.Tagline,
		AvatarNote:      p.AvatarNote,
		SystemPrompt:    p.SystemPrompt,
		Greeting:        p.Greeting,
		VoiceID:         p.VoiceID,
		ViduAvatarID:    p.ViduAvatarID,
		ViduImageURI:    p.ViduImageURI,
		ActionLibraryID: p.ActionLibraryID,
		Revision:        p.Revision,
		IsActive:        p.IsActive,
		DeletedAtMs:     p.DeletedAtMs,
		UpdatedAtMs:     p.UpdatedAtMs,
		ViduSync:        syncFromMap(p.ViduSyncState),
	}
}

// fillAssociations 联查填充主形象图与有序 refs（数据量为个位数档案，逐条查代价可忽略）。
func (s *EntAvatarStore) fillAssociations(ctx context.Context, p *ent.Persona, a *avatar.Avatar) error {
	if p.AvatarAssetID != "" {
		if row, err := s.client.Asset.Get(ctx, p.AvatarAssetID); err == nil {
			a.ImageAsset = entAssetToBiz(row)
		} else if !ent.IsNotFound(err) {
			return err
		}
	}
	rows, err := s.client.PersonaRef.Query().
		Where(personaref.PersonaID(p.ID)).
		Order(personaref.ByOrd(), personaref.ByAssetID()).
		All(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		ref := avatar.Ref{AssetID: r.AssetID, Ord: r.Ord}
		if as, err := s.client.Asset.Get(ctx, r.AssetID); err == nil {
			ref.Asset = entAssetToBiz(as)
		} else if !ent.IsNotFound(err) {
			return err
		}
		a.Refs = append(a.Refs, ref)
	}
	return nil
}

// upsertAssetTx 素材登记（幂等）：sha256 命中复用首次记录；id 命中补全可变字段；
// 否则新建；并发撞 sha256 唯一索引时回查复用。
func upsertAssetTx(ctx context.Context, tx *ent.Tx, a avatar.Asset) (string, error) {
	if a.ID == "" && a.SHA256 == "" {
		return "", nil
	}
	if a.SHA256 != "" {
		if hit, err := tx.Asset.Query().Where(asset.Sha256(a.SHA256)).Only(ctx); err == nil {
			return hit.ID, nil
		} else if !ent.IsNotFound(err) {
			return "", err
		}
	}
	if a.ID != "" {
		if hit, err := tx.Asset.Get(ctx, a.ID); err == nil {
			if _, err := tx.Asset.UpdateOne(hit).
				SetName(a.Name).SetPath(a.Path).SetViduURI(a.URI).
				SetKind(a.Kind).Save(ctx); err != nil {
				return "", err
			}
			return hit.ID, nil
		} else if !ent.IsNotFound(err) {
			return "", err
		}
	}
	row, err := tx.Asset.Create().
		SetID(a.ID).SetKind(a.Kind).SetName(a.Name).SetPath(a.Path).
		SetSha256(a.SHA256).SetViduURI(a.URI).SetExpiresAtMs(a.ExpiresAtMs).
		SetUploadedAtMs(time.Now().UnixMilli()).
		Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) && a.SHA256 != "" {
			if hit, qerr := tx.Asset.Query().Where(asset.Sha256(a.SHA256)).Only(ctx); qerr == nil {
				return hit.ID, nil
			}
		}
		return "", err
	}
	return row.ID, nil
}

// setActiveTx 事务语义：清其他 + 置本；目标不存在或已删 → 影响 0 行。
func setActiveTx(ctx context.Context, tx *ent.Tx, id string, now int64) error {
	if _, err := tx.Persona.Update().
		Where(persona.IsActive(true)).
		SetIsActive(false).Save(ctx); err != nil {
		return err
	}
	n, err := tx.Persona.Update().
		Where(persona.ID(id), persona.DeletedAtMs(0)).
		SetIsActive(true).SetUpdatedAtMs(now).Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return avatar.ErrNotFound
	}
	return nil
}

func (s *EntAvatarStore) Create(ctx context.Context, a avatar.Avatar) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	n, err := tx.Persona.Query().
		Where(persona.Slug(a.Slug), persona.DeletedAtMs(0)).Count(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return avatar.ErrSlugConflict
	}
	assetID, err := upsertAssetTx(ctx, tx, a.ImageAsset)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	c := tx.Persona.Create().
		SetID(a.ID).SetSlug(a.Slug).SetName(a.Name).
		SetTagline(a.Tagline).SetAvatarNote(a.AvatarNote).
		SetSystemPrompt(a.SystemPrompt).SetGreeting(a.Greeting).
		SetVoiceID(a.VoiceID).SetViduAvatarID(a.ViduAvatarID).
		SetViduImageURI(a.ViduImageURI).SetActionLibraryID(a.ActionLibraryID).
		SetRevision(1).SetDeletedAtMs(0).SetIsActive(false).
		SetViduSyncState(syncToMap(a.ViduSync)).SetUpdatedAtMs(now)
	if assetID != "" {
		c.SetAvatarAssetID(assetID)
	}
	if _, err := c.Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return avatar.ErrSlugConflict // slug 唯一索引兜底（并发）
		}
		return err
	}
	if a.IsActive {
		if err := setActiveTx(ctx, tx, a.ID, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *EntAvatarStore) Get(ctx context.Context, id string) (avatar.Avatar, bool, error) {
	p, err := s.client.Persona.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return avatar.Avatar{}, false, nil
		}
		return avatar.Avatar{}, false, err
	}
	if p.DeletedAtMs != 0 {
		return avatar.Avatar{}, false, nil
	}
	a := entAvatarBase(p)
	if err := s.fillAssociations(ctx, p, &a); err != nil {
		return avatar.Avatar{}, false, err
	}
	return a, true, nil
}

func (s *EntAvatarStore) List(ctx context.Context, limit, offset int, includeDeleted bool) ([]avatar.Avatar, int, error) {
	if limit <= 0 {
		limit = 50
	}
	q := s.client.Persona.Query()
	if !includeDeleted {
		q = q.Where(persona.DeletedAtMs(0))
	}
	total, err := q.Clone().Count(ctx)
	if err != nil {
		return nil, 0, err
	}
	rows, err := q.Order(persona.ByID()).Limit(limit).Offset(offset).All(ctx)
	if err != nil {
		return nil, 0, err
	}
	out := make([]avatar.Avatar, 0, len(rows))
	for _, p := range rows {
		a := entAvatarBase(p)
		if err := s.fillAssociations(ctx, p, &a); err != nil {
			return nil, 0, err
		}
		out = append(out, a)
	}
	return out, total, nil
}

func (s *EntAvatarStore) GetBySlug(ctx context.Context, slug string) (avatar.Avatar, bool, error) {
	p, err := s.client.Persona.Query().
		Where(persona.Slug(slug), persona.DeletedAtMs(0)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return avatar.Avatar{}, false, nil
		}
		return avatar.Avatar{}, false, err
	}
	a := entAvatarBase(p)
	if err := s.fillAssociations(ctx, p, &a); err != nil {
		return avatar.Avatar{}, false, err
	}
	return a, true, nil
}

func (s *EntAvatarStore) UpdateWithRevision(ctx context.Context, a avatar.Avatar, baseRevision int64) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := tx.Persona.Query().Where(persona.ID(a.ID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return avatar.ErrNotFound
		}
		return err
	}
	if row.DeletedAtMs != 0 {
		return avatar.ErrNotFound
	}
	if row.Revision != baseRevision {
		return avatar.ErrRevisionConflict
	}
	if row.Slug != a.Slug {
		n, err := tx.Persona.Query().
			Where(persona.Slug(a.Slug), persona.DeletedAtMs(0), persona.IDNEQ(a.ID)).
			Count(ctx)
		if err != nil {
			return err
		}
		if n > 0 {
			return avatar.ErrSlugConflict
		}
	}
	assetID := row.AvatarAssetID
	if a.ImageAsset.ID != "" || a.ImageAsset.SHA256 != "" {
		if assetID, err = upsertAssetTx(ctx, tx, a.ImageAsset); err != nil {
			return err
		}
	}
	now := time.Now().UnixMilli()
	// CAS：WHERE id=? AND revision=?（并发改写 revision 时影响 0 行 → 冲突）。
	upd := tx.Persona.Update().
		Where(persona.ID(a.ID), persona.Revision(baseRevision)).
		SetSlug(a.Slug).SetName(a.Name).SetTagline(a.Tagline).SetAvatarNote(a.AvatarNote).
		SetSystemPrompt(a.SystemPrompt).SetGreeting(a.Greeting).SetVoiceID(a.VoiceID).
		SetViduAvatarID(a.ViduAvatarID).SetViduImageURI(a.ViduImageURI).
		SetActionLibraryID(a.ActionLibraryID).
		SetRevision(baseRevision + 1).SetIsActive(a.IsActive).
		SetViduSyncState(syncToMap(a.ViduSync)).SetUpdatedAtMs(now)
	if assetID != "" {
		upd.SetAvatarAssetID(assetID)
	} else {
		upd.ClearAvatarAssetID()
	}
	n, err := upd.Save(ctx)
	if err != nil {
		if ent.IsConstraintError(err) {
			return avatar.ErrSlugConflict
		}
		return err
	}
	if n == 0 {
		return avatar.ErrRevisionConflict
	}
	if a.IsActive {
		if err := setActiveTx(ctx, tx, a.ID, now); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *EntAvatarStore) SetActive(ctx context.Context, id string) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := setActiveTx(ctx, tx, id, time.Now().UnixMilli()); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *EntAvatarStore) SoftDelete(ctx context.Context, id string) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	row, err := tx.Persona.Query().Where(persona.ID(id)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return avatar.ErrNotFound
		}
		return err
	}
	if row.DeletedAtMs != 0 {
		return avatar.ErrNotFound
	}
	now := time.Now().UnixMilli()
	// slug 改写释放唯一位（DB 唯一索引含已删行），软删行 slug 可复用。
	if _, err := tx.Persona.UpdateOneID(id).
		SetDeletedAtMs(now).SetUpdatedAtMs(now).SetIsActive(false).
		SetSlug(row.Slug + "#deleted-" + row.ID).
		Save(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *EntAvatarStore) AddRef(ctx context.Context, personaID, assetID string, ord int) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	p, err := tx.Persona.Query().Where(persona.ID(personaID)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return avatar.ErrNotFound
		}
		return err
	}
	if p.DeletedAtMs != 0 {
		return avatar.ErrNotFound
	}
	if _, err := tx.Asset.Get(ctx, assetID); err != nil {
		if ent.IsNotFound(err) {
			return avatar.ErrNotFound
		}
		return err
	}
	refID := personaID + "/" + assetID
	if hit, err := tx.PersonaRef.Get(ctx, refID); err == nil {
		if hit.Ord != ord { // 幂等：重复绑定 = 更新 ord
			if _, err := tx.PersonaRef.UpdateOne(hit).SetOrd(ord).Save(ctx); err != nil {
				return err
			}
		}
		return tx.Commit()
	} else if !ent.IsNotFound(err) {
		return err
	}
	n, err := tx.PersonaRef.Query().Where(personaref.PersonaID(personaID)).Count(ctx)
	if err != nil {
		return err
	}
	if n >= avatar.MaxRefs {
		return avatar.ErrTooManyRefs
	}
	if _, err := tx.PersonaRef.Create().
		SetID(refID).SetPersonaID(personaID).SetAssetID(assetID).SetOrd(ord).
		Save(ctx); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *EntAvatarStore) RemoveRef(ctx context.Context, personaID, assetID string) error {
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.PersonaRef.Delete().
		Where(personaref.PersonaID(personaID), personaref.AssetID(assetID)).
		Exec(ctx); err != nil {
		return err
	}
	// 删后重排 ord：0..n-1，保持相对顺序（ord, asset_id 升序读取后按序改写）。
	rows, err := tx.PersonaRef.Query().
		Where(personaref.PersonaID(personaID)).
		Order(personaref.ByOrd(), personaref.ByAssetID()).
		All(ctx)
	if err != nil {
		return err
	}
	for i, r := range rows {
		if r.Ord == i {
			continue
		}
		if _, err := tx.PersonaRef.UpdateOne(r).SetOrd(i).Save(ctx); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func (s *EntAvatarStore) FindAssetBySHA256(ctx context.Context, sha string) (avatar.Asset, bool, error) {
	row, err := s.client.Asset.Query().Where(asset.Sha256(sha)).Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return avatar.Asset{}, false, nil
		}
		return avatar.Asset{}, false, err
	}
	return entAssetToBiz(row), true, nil
}

// PutAsset 素材登记（AssetRegistry）：同 sha256 复用首次记录；并发撞唯一索引幂等。
func (s *EntAvatarStore) PutAsset(ctx context.Context, a avatar.Asset) error {
	if a.SHA256 != "" {
		if n, err := s.client.Asset.Query().Where(asset.Sha256(a.SHA256)).Count(ctx); err != nil {
			return err
		} else if n > 0 {
			return nil
		}
	}
	if a.ID != "" {
		if _, err := s.client.Asset.Get(ctx, a.ID); err == nil {
			return nil
		} else if !ent.IsNotFound(err) {
			return err
		}
	}
	if _, err := s.client.Asset.Create().
		SetID(a.ID).SetKind(a.Kind).SetName(a.Name).SetPath(a.Path).
		SetSha256(a.SHA256).SetViduURI(a.URI).SetExpiresAtMs(a.ExpiresAtMs).
		SetUploadedAtMs(time.Now().UnixMilli()).
		Save(ctx); err != nil {
		if ent.IsConstraintError(err) {
			return nil // 并发同 sha256：复用首次记录
		}
		return err
	}
	return nil
}

// GetAsset 按 id 取素材（AssetRegistry）。
func (s *EntAvatarStore) GetAsset(ctx context.Context, id string) (avatar.Asset, bool, error) {
	row, err := s.client.Asset.Get(ctx, id)
	if err != nil {
		if ent.IsNotFound(err) {
			return avatar.Asset{}, false, nil
		}
		return avatar.Asset{}, false, err
	}
	return entAssetToBiz(row), true, nil
}

// SetAssetViduURI 回写素材的 Vidu ssupload URI 与 90 天到期时刻（AssetViduWriter，M3 sync 管线）。
func (s *EntAvatarStore) SetAssetViduURI(ctx context.Context, assetID, uri string, expiresAtMs int64) error {
	n, err := s.client.Asset.Update().
		Where(asset.ID(assetID)).
		SetViduURI(uri).SetExpiresAtMs(expiresAtMs).
		Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return avatar.ErrNotFound
	}
	return nil
}

// avatarFullStore 热切视角：AvatarStore 十原语 + AssetRegistry 附加端口。
type avatarFullStore interface {
	avatar.AvatarStore
	avatar.AssetRegistry
}

// SwitchableAvatarStore 与 SwitchablePersonaStore 同模式（v3 §7.4）：
// PG 未就绪走内存；就绪后经 SetEnt 热切；热切不回迁历史。
type SwitchableAvatarStore struct {
	mu  sync.RWMutex
	mem *avatar.MemoryAvatarStore
	db  *EntAvatarStore
}

func NewSwitchableAvatarStore(mem *avatar.MemoryAvatarStore) *SwitchableAvatarStore {
	return &SwitchableAvatarStore{mem: mem}
}

// SetEnt PG 就绪时注入 Ent 实现（后续读写走 PG，不回迁）。
func (s *SwitchableAvatarStore) SetEnt(client *ent.Client) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.db = NewEntAvatarStore(client)
}

func (s *SwitchableAvatarStore) current() avatarFullStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db != nil {
		return s.db
	}
	return s.mem
}

// Backend 当前生效后端（"memory"|"ent"）。
func (s *SwitchableAvatarStore) Backend() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db != nil {
		return "ent"
	}
	return "memory"
}

func (s *SwitchableAvatarStore) Create(ctx context.Context, a avatar.Avatar) error {
	return s.current().Create(ctx, a)
}

func (s *SwitchableAvatarStore) Get(ctx context.Context, id string) (avatar.Avatar, bool, error) {
	return s.current().Get(ctx, id)
}

func (s *SwitchableAvatarStore) List(ctx context.Context, limit, offset int, includeDeleted bool) ([]avatar.Avatar, int, error) {
	return s.current().List(ctx, limit, offset, includeDeleted)
}

func (s *SwitchableAvatarStore) GetBySlug(ctx context.Context, slug string) (avatar.Avatar, bool, error) {
	return s.current().GetBySlug(ctx, slug)
}

func (s *SwitchableAvatarStore) UpdateWithRevision(ctx context.Context, a avatar.Avatar, baseRevision int64) error {
	return s.current().UpdateWithRevision(ctx, a, baseRevision)
}

func (s *SwitchableAvatarStore) SetActive(ctx context.Context, id string) error {
	return s.current().SetActive(ctx, id)
}

func (s *SwitchableAvatarStore) SoftDelete(ctx context.Context, id string) error {
	return s.current().SoftDelete(ctx, id)
}

func (s *SwitchableAvatarStore) AddRef(ctx context.Context, personaID, assetID string, ord int) error {
	return s.current().AddRef(ctx, personaID, assetID, ord)
}

func (s *SwitchableAvatarStore) RemoveRef(ctx context.Context, personaID, assetID string) error {
	return s.current().RemoveRef(ctx, personaID, assetID)
}

func (s *SwitchableAvatarStore) FindAssetBySHA256(ctx context.Context, sha string) (avatar.Asset, bool, error) {
	return s.current().FindAssetBySHA256(ctx, sha)
}

func (s *SwitchableAvatarStore) PutAsset(ctx context.Context, a avatar.Asset) error {
	return s.current().PutAsset(ctx, a)
}

func (s *SwitchableAvatarStore) GetAsset(ctx context.Context, id string) (avatar.Asset, bool, error) {
	return s.current().GetAsset(ctx, id)
}

// SetAssetViduURI AssetViduWriter 端口转发（mem/Ent 均实现，断言恒真；防御性报错而非 panic）。
func (s *SwitchableAvatarStore) SetAssetViduURI(ctx context.Context, assetID, uri string, expiresAtMs int64) error {
	w, ok := s.current().(avatar.AssetViduWriter)
	if !ok {
		return errors.New("avatar store 未实现 AssetViduWriter")
	}
	return w.SetAssetViduURI(ctx, assetID, uri, expiresAtMs)
}
