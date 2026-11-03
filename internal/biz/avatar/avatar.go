// Package avatar 数字人档案（开播配置唯一真相源，v3 方案 §1/§7/§8）：
// AvatarStore 是档案持久化端口（十原语一次冻结，mem/Ent 双实现语义对齐）；
// ProfileResolver 是全系统唯一档案读取入口——开播链路、LLM/弹幕应答链路、
// 设置页只读派生一律经它，禁止旁路直读 settings。
//
// 与 chat.ProfileResolver 的关系（书面决定，2026-11-03 G 批 M1）：
// chat.ProfileResolver 形状为 SystemPrompt(ctx) string（无 personaID、无 error、
// nil 容忍），与本包 Resolve/Active（带 personaID、带 error）不兼容。
// 决定：chat 包已定接口语义不动（persona.Service 继续实现它）；本包 Service
// 不实现 chat.ProfileResolver；F 批 LLM 链路切换到本端口时由消费侧做适配层，
// 属 M5/交接事项，本批不改 chat 包。
package avatar

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"
)

// 哨兵错误：供 server 层映射 400/404/409。
var (
	ErrNotFound         = errors.New("avatar_not_found")
	ErrSlugConflict     = errors.New("avatar_slug_conflict")
	ErrRevisionConflict = errors.New("avatar_revision_conflict")
	ErrTooManyRefs      = errors.New("avatar_too_many_refs")
	// ErrAvatarInUse 删除当前 active 档案（v2 §3.1 #5 → 409）。
	ErrAvatarInUse = errors.New("avatar_in_use")
)

// MaxRefs 单个档案的参考图上限（蓝本 type11 ≤3）。
const MaxRefs = 3

// Asset 素材最小元数据；Ent schema 已有 Asset 表（M2 落 Ent 实现，列一一映射）。
// URI 为 Vidu ssupload URI（kind=avatar/ref 经 sync 管线同步后填入，Ent vidu_uri 列），
// 非本地路径；Path 为本机内容寻址落盘相对路径（图片路由读取用）。
type Asset struct {
	ID          string `json:"id"`
	SHA256      string `json:"sha256"`
	Kind        string `json:"kind"` // image | audio | avatar | ref
	Name        string `json:"name,omitempty"`
	URI         string `json:"uri,omitempty"`
	Path        string `json:"path,omitempty"`
	ExpiresAtMs int64  `json:"expires_at_ms,omitempty"`
}

// Ref 档案参考图关联（PersonaRef）：ord 有序，≤MaxRefs。
type Ref struct {
	AssetID string `json:"asset_id"`
	Ord     int    `json:"ord"`
	Asset   Asset  `json:"asset"` // 联查填充
}

// ViduSyncState sync-vidu 任务状态（§2：202 异步 + ListAvatars/GetAvatar 轮询闭环）。
// ViduAvatarID 仅在 create_avatar 成功、persist 未完成的窗口期内记录平台资产 ID，
// 供崩溃后开机对账做孤儿资产 best-effort 清理（§2.2 补偿③）；done 后清空。
// AssetSHA256 记录本次同步的主形象 sha256，供跨重启 dedup 复放与断点续传判定。
type ViduSyncState struct {
	State        string `json:"state"` // idle | queued | uploading | creating | done | error
	Stage        string `json:"stage,omitempty"`
	Error        string `json:"error,omitempty"`
	JobID        string `json:"job_id,omitempty"`
	UpdatedAtMs  int64  `json:"updated_at_ms,omitempty"`
	ViduAvatarID string `json:"vidu_avatar_id,omitempty"`
	AssetSHA256  string `json:"asset_sha256,omitempty"`
}

// Avatar 数字人档案：开播所需的最小集合 = 形象 + 音色（蓝本 selfcheck.py）。
// Tagline/AvatarNote 是 F 批 personas 兼容壳字段（v3 §1.2：档案服务为 Persona 行
// 唯一写方，委托链路须无损往返）；UpdatedAtMs 由存储实现方在写入时刷新。
type Avatar struct {
	ID              string        `json:"id"`
	Slug            string        `json:"slug"`
	Name            string        `json:"name"`
	Tagline         string        `json:"tagline,omitempty"`
	AvatarNote      string        `json:"avatar_note,omitempty"`
	SystemPrompt    string        `json:"system_prompt"`
	Greeting        string        `json:"greeting"`
	VoiceID         string        `json:"voice_id"`
	ViduAvatarID    string        `json:"vidu_avatar_id,omitempty"`
	ViduImageURI    string        `json:"vidu_image_uri,omitempty"`
	ActionLibraryID string        `json:"action_library_id,omitempty"`
	Revision        int64         `json:"revision"`
	IsActive        bool          `json:"is_active"`
	DeletedAtMs     int64         `json:"deleted_at_ms,omitempty"`
	UpdatedAtMs     int64         `json:"updated_at_ms,omitempty"`
	ViduSync        ViduSyncState `json:"vidu_sync_state"`
	ImageAsset      Asset         `json:"image_asset"` // 主形象图（kind=avatar|image）
	Refs            []Ref         `json:"refs"`        // 参考图，ord 升序，≤MaxRefs
}

// AvatarStore 档案持久化端口（v3 方案 §7.1 十原语一次冻结）；
// mem/Ent 双实现语义对齐：CAS、SetActive 事务、refs ≤3 有序。mem 重启即丢。
type AvatarStore interface {
	// Create 新建档案；slug 与未删除行撞 → ErrSlugConflict（已软删行的 slug 可复用）。
	// Revision 由实现方初始化为 1；IsActive=true 时按 SetActive 同事务语义排他置位。
	Create(ctx context.Context, a Avatar) error
	// Get 按 id 取档案（含有序 refs）；已软删视为不存在。
	Get(ctx context.Context, id string) (Avatar, bool, error)
	// List 分页 + total；includeDeleted=true 时含已软删行（对齐 SessionStore 分页先例）。
	List(ctx context.Context, limit, offset int, includeDeleted bool) ([]Avatar, int, error)
	// GetBySlug slug 预检原语；已软删视为不存在。
	GetBySlug(ctx context.Context, slug string) (Avatar, bool, error)
	// UpdateWithRevision CAS：WHERE id=? AND revision=?，撞 → ErrRevisionConflict；
	// 成功时 revision=baseRevision+1；id 不存在 → ErrNotFound。
	UpdateWithRevision(ctx context.Context, a Avatar, baseRevision int64) error
	// SetActive 事务语义：清其他 + 置本；id 不存在或已删 → ErrNotFound。
	SetActive(ctx context.Context, id string) error
	// SoftDelete 软删；当前 active 被删时同时清 active 置位。
	SoftDelete(ctx context.Context, id string) error
	// AddRef 绑定参考图；persona/asset 不存在 → ErrNotFound；>MaxRefs → ErrTooManyRefs；
	// 同 asset 重复绑定视为更新 ord（幂等）。
	AddRef(ctx context.Context, personaID, assetID string, ord int) error
	// RemoveRef 解绑参考图；删后剩余 refs 重排 ord（0..n-1，保持相对顺序）。
	RemoveRef(ctx context.Context, personaID, assetID string) error
	// FindAssetBySHA256 dedup 原语：同 sha256 素材复用，不重复上传。
	FindAssetBySHA256(ctx context.Context, sha string) (Asset, bool, error)
}

// AssetRegistry 素材登记端口（AvatarStore 十原语之外的附加端口，M2 上传/读取用）。
// mem/Ent 双实现均随 AvatarStore 一并提供；同 sha256 重复登记复用首次记录（幂等）。
type AssetRegistry interface {
	PutAsset(ctx context.Context, a Asset) error
	GetAsset(ctx context.Context, id string) (Asset, bool, error)
}

// ImageStore 图片管线存储端口 = dedup 原语 + 素材登记（M2 上传与图片路由）。
type ImageStore interface {
	FindAssetBySHA256(ctx context.Context, sha string) (Asset, bool, error)
	AssetRegistry
}

// AssetViduWriter sync-vidu 管线回写端口（M3 §2.2）：上传成功后将 ssupload URI 与
// 90 天到期时刻写到素材行（ref 与主形象共用 vidu_uri/expires_at_ms 落点，附录 H10）。
// mem/Ent/Switchable 三实现均提供；sync 管线按类型断言取用，未实现即 fail-closed 报错。
type AssetViduWriter interface {
	SetAssetViduURI(ctx context.Context, assetID, uri string, expiresAtMs int64) error
}

// ResolvedProfile 开播/应答所需的档案解析结果（v3 方案 §8）。
type ResolvedProfile struct {
	PersonaID       string   `json:"persona_id"`
	PersonaName     string   `json:"persona_name,omitempty"`
	PersonaRevision int64    `json:"persona_revision,omitempty"`
	SystemPrompt    string   `json:"system_prompt"`
	VoiceID         string   `json:"voice_id"`
	Greeting        string   `json:"greeting"`
	ViduAvatarID    string   `json:"vidu_avatar_id,omitempty"`
	ViduImageURI    string   `json:"vidu_image_uri,omitempty"`
	ActionLibraryID string   `json:"action_library_id,omitempty"`
	RefImageURIs    []string `json:"ref_image_uris,omitempty"` // ≤3 有序，Vidu ssupload URI
	// 以下三个字段服务 M5 开播预检与告警（v3 §3.3）：不进快照展示语义，omitempty。
	ViduSynced       bool  `json:"vidu_synced"`                   // sync 管线完成（Vidu URI 就绪）
	ImageExpiresAtMs int64 `json:"image_expires_at_ms,omitempty"` // 主形象 ssupload URI 到期时刻（0=未知/不过期）
	UnsyncedRefCount int   `json:"unsynced_ref_count,omitempty"`  // 未同步 Vidu 被跳过的参考图数
}

// ImageExpired 主形象 Vidu ssupload URI 已过 90 天到期时刻（ExpiresAtMs>0 且早于 now）。
func (p ResolvedProfile) ImageExpired(now time.Time) bool {
	return p.ImageExpiresAtMs > 0 && now.UnixMilli() > p.ImageExpiresAtMs
}

// ProfileResolver 全系统唯一档案读取入口（v3 方案 §8）：开播链路、LLM/弹幕
// 应答链路、设置页只读派生一律经它。M1 第一天冻结，F/G 批接口对照表同步。
type ProfileResolver interface {
	// Resolve 按 personaID 解析；不存在或已软删 → ErrNotFound。
	Resolve(ctx context.Context, personaID string) (ResolvedProfile, error)
	// Active 解析当前 active 档案；无 active → ErrNotFound（调用方自行决定回落）。
	Active(ctx context.Context) (ResolvedProfile, error)
}

// Service 档案读取服务，实现 ProfileResolver。
type Service struct {
	store AvatarStore
}

func NewService(store AvatarStore) *Service { return &Service{store: store} }

// Resolve 实现 ProfileResolver。
func (s *Service) Resolve(ctx context.Context, personaID string) (ResolvedProfile, error) {
	a, ok, err := s.store.Get(ctx, personaID)
	if err != nil {
		return ResolvedProfile{}, err
	}
	if !ok {
		return ResolvedProfile{}, ErrNotFound
	}
	return profileOf(a), nil
}

// Active 实现 ProfileResolver：以 List+IsActive 定位（十原语无 GetActive，
// M1/M2 均按此实现；数据量为个位数档案，全量列举代价可忽略）。
func (s *Service) Active(ctx context.Context) (ResolvedProfile, error) {
	items, _, err := s.store.List(ctx, 1000, 0, false)
	if err != nil {
		return ResolvedProfile{}, err
	}
	for _, a := range items {
		if a.IsActive {
			return profileOf(a), nil
		}
	}
	return ResolvedProfile{}, ErrNotFound
}

func profileOf(a Avatar) ResolvedProfile {
	p := ResolvedProfile{
		PersonaID:        a.ID,
		PersonaName:      a.Name,
		PersonaRevision:  a.Revision,
		SystemPrompt:     a.SystemPrompt,
		VoiceID:          a.VoiceID,
		Greeting:         a.Greeting,
		ViduAvatarID:     a.ViduAvatarID,
		ViduImageURI:     a.ViduImageURI,
		ActionLibraryID:  a.ActionLibraryID,
		ViduSynced:       a.ViduSync.State == "done" || a.ViduAvatarID != "" || a.ViduImageURI != "",
		ImageExpiresAtMs: a.ImageAsset.ExpiresAtMs,
	}
	for _, r := range a.Refs {
		if r.Asset.URI != "" {
			p.RefImageURIs = append(p.RefImageURIs, r.Asset.URI)
		} else {
			p.UnsyncedRefCount++
		}
	}
	return p
}

// MemoryAvatarStore 进程内实现（M1 提前自 M2）；重启即丢，语义与 Ent 实现对齐。
type MemoryAvatarStore struct {
	mu      sync.Mutex
	avatars map[string]Avatar
	assets  map[string]Asset  // id → asset
	shaIdx  map[string]string // sha256 → asset id
	refs    map[string][]Ref  // personaID → refs（ord 升序）
}

func NewMemoryStore() *MemoryAvatarStore {
	return &MemoryAvatarStore{
		avatars: map[string]Avatar{},
		assets:  map[string]Asset{},
		shaIdx:  map[string]string{},
		refs:    map[string][]Ref{},
	}
}

// PutAsset 登记素材（dedup 与 AddRef 的前置，AssetRegistry 端口实现）；
// 供上传链路与素材管线使用。同 sha256 重复登记复用首次记录。
func (m *MemoryAvatarStore) PutAsset(_ context.Context, a Asset) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.shaIdx[a.SHA256]; ok && a.SHA256 != "" {
		return nil
	}
	m.assets[a.ID] = a
	if a.SHA256 != "" {
		m.shaIdx[a.SHA256] = a.ID
	}
	return nil
}

// GetAsset 按 id 取素材（AssetRegistry 端口实现）。
func (m *MemoryAvatarStore) GetAsset(_ context.Context, id string) (Asset, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.assets[id]
	return a, ok, nil
}

// SetAssetViduURI 回写素材的 Vidu ssupload URI 与 90 天到期时刻（AssetViduWriter 端口实现）。
func (m *MemoryAvatarStore) SetAssetViduURI(_ context.Context, assetID, uri string, expiresAtMs int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.assets[assetID]
	if !ok {
		return ErrNotFound
	}
	a.URI = uri
	a.ExpiresAtMs = expiresAtMs
	m.assets[assetID] = a
	// 主形象图是内联在 Avatar.ImageAsset 上的副本，同步刷新保持一致。
	for id, av := range m.avatars {
		if av.ImageAsset.ID == assetID {
			av.ImageAsset = a
			m.avatars[id] = av
		}
	}
	for pid, refs := range m.refs {
		for i, r := range refs {
			if r.AssetID == assetID {
				refs[i].Asset = a
			}
		}
		m.refs[pid] = refs
	}
	return nil
}

// Backend 存储后端标识（"memory"）。
func (m *MemoryAvatarStore) Backend() string { return "memory" }

func (m *MemoryAvatarStore) Create(_ context.Context, a Avatar) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, old := range m.avatars {
		if old.DeletedAtMs == 0 && old.Slug == a.Slug {
			return ErrSlugConflict
		}
	}
	a.Revision = 1
	a.DeletedAtMs = 0
	a.Refs = nil
	a.UpdatedAtMs = time.Now().UnixMilli()
	if a.ImageAsset.ID != "" {
		m.assets[a.ImageAsset.ID] = a.ImageAsset
		if a.ImageAsset.SHA256 != "" {
			m.shaIdx[a.ImageAsset.SHA256] = a.ImageAsset.ID
		}
	}
	m.avatars[a.ID] = a
	if a.IsActive {
		m.setActiveLocked(a.ID)
	}
	return nil
}

func (m *MemoryAvatarStore) Get(_ context.Context, id string) (Avatar, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.avatars[id]
	if !ok || a.DeletedAtMs != 0 {
		return Avatar{}, false, nil
	}
	a.Refs = m.refsLocked(id)
	return a, true, nil
}

func (m *MemoryAvatarStore) List(_ context.Context, limit, offset int, includeDeleted bool) ([]Avatar, int, error) {
	if limit <= 0 {
		limit = 50
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Avatar, 0, len(m.avatars))
	for _, a := range m.avatars {
		if a.DeletedAtMs != 0 && !includeDeleted {
			continue
		}
		a.Refs = m.refsLocked(a.ID)
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	total := len(out)
	if offset >= total {
		return []Avatar{}, total, nil
	}
	out = out[offset:]
	if len(out) > limit {
		out = out[:limit]
	}
	return out, total, nil
}

func (m *MemoryAvatarStore) GetBySlug(_ context.Context, slug string) (Avatar, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, a := range m.avatars {
		if a.DeletedAtMs == 0 && a.Slug == slug {
			a.Refs = m.refsLocked(a.ID)
			return a, true, nil
		}
	}
	return Avatar{}, false, nil
}

func (m *MemoryAvatarStore) UpdateWithRevision(_ context.Context, a Avatar, baseRevision int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	cur, ok := m.avatars[a.ID]
	if !ok || cur.DeletedAtMs != 0 {
		return ErrNotFound
	}
	if cur.Revision != baseRevision {
		return ErrRevisionConflict
	}
	if cur.Slug != a.Slug {
		for _, old := range m.avatars {
			if old.ID != a.ID && old.DeletedAtMs == 0 && old.Slug == a.Slug {
				return ErrSlugConflict
			}
		}
	}
	a.Revision = baseRevision + 1
	a.DeletedAtMs = cur.DeletedAtMs
	a.Refs = nil
	a.UpdatedAtMs = time.Now().UnixMilli()
	if a.ImageAsset.ID != "" {
		m.assets[a.ImageAsset.ID] = a.ImageAsset
		if a.ImageAsset.SHA256 != "" {
			m.shaIdx[a.ImageAsset.SHA256] = a.ImageAsset.ID
		}
	}
	m.avatars[a.ID] = a
	if a.IsActive {
		m.setActiveLocked(a.ID)
	}
	return nil
}

func (m *MemoryAvatarStore) SetActive(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.avatars[id]
	if !ok || a.DeletedAtMs != 0 {
		return ErrNotFound
	}
	m.setActiveLocked(id)
	return nil
}

// setActiveLocked 事务语义（同一临界区）：清其他 + 置本。
func (m *MemoryAvatarStore) setActiveLocked(id string) {
	now := time.Now().UnixMilli()
	for k, v := range m.avatars {
		next := k == id
		if v.IsActive != next {
			v.IsActive = next
			v.UpdatedAtMs = now
			m.avatars[k] = v
		}
	}
}

func (m *MemoryAvatarStore) SoftDelete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.avatars[id]
	if !ok || a.DeletedAtMs != 0 {
		return ErrNotFound
	}
	a.DeletedAtMs = time.Now().UnixMilli()
	a.UpdatedAtMs = a.DeletedAtMs
	a.IsActive = false
	m.avatars[id] = a
	return nil
}

func (m *MemoryAvatarStore) AddRef(_ context.Context, personaID, assetID string, ord int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.avatars[personaID]
	if !ok || a.DeletedAtMs != 0 {
		return ErrNotFound
	}
	asset, ok := m.assets[assetID]
	if !ok {
		return ErrNotFound
	}
	refs := m.refs[personaID]
	for i, r := range refs {
		if r.AssetID == assetID { // 幂等：重复绑定 = 更新 ord
			refs[i].Ord = ord
			m.refs[personaID] = sortRefsLocked(refs)
			return nil
		}
	}
	if len(refs) >= MaxRefs {
		return ErrTooManyRefs
	}
	refs = append(refs, Ref{AssetID: assetID, Ord: ord, Asset: asset})
	m.refs[personaID] = sortRefsLocked(refs)
	return nil
}

func (m *MemoryAvatarStore) RemoveRef(_ context.Context, personaID, assetID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	refs := m.refs[personaID]
	out := make([]Ref, 0, len(refs))
	for _, r := range refs {
		if r.AssetID != assetID {
			out = append(out, r)
		}
	}
	// 删后重排 ord：0..n-1，保持相对顺序。
	for i := range out {
		out[i].Ord = i
	}
	m.refs[personaID] = out
	return nil
}

func (m *MemoryAvatarStore) FindAssetBySHA256(_ context.Context, sha string) (Asset, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.shaIdx[sha]
	if !ok {
		return Asset{}, false, nil
	}
	a, ok := m.assets[id]
	return a, ok, nil
}

func (m *MemoryAvatarStore) refsLocked(personaID string) []Ref {
	refs := m.refs[personaID]
	out := make([]Ref, len(refs))
	copy(out, refs)
	return out
}

func sortRefsLocked(refs []Ref) []Ref {
	sort.SliceStable(refs, func(i, j int) bool {
		if refs[i].Ord == refs[j].Ord {
			return refs[i].AssetID < refs[j].AssetID
		}
		return refs[i].Ord < refs[j].Ord
	})
	return refs
}
