// Package persona 人设 CRUD 与切换（F38/G04/G05）：Ent Persona 表为存储，无 PG 时内存兜底。
// v3 约束：system_prompt 唯一真相源是本服务；chat 只经 ProfileResolver（SystemPrompt）只读，
// 不新增任何 settings 侧 prompt 写通路。语义对齐蓝本 src/personas.py 与 bridge.py:1090-1140。
package persona

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// 哨兵错误：供 server 层映射 400/404/409。
var (
	ErrNotFound       = errors.New("persona_not_found")
	ErrInvalidID      = errors.New("人设 id 只能用 a-z 0-9 _ - （1~32 位）")
	ErrEmptyPrompt    = errors.New("system_prompt 不能为空")
	ErrActiveConflict = errors.New("不能删当前正在用的人设，先切到别的")
)

// idRE 复刻蓝本 personas.py _ID_RE。
var idRE = regexp.MustCompile(`^[a-z0-9_-]{1,32}$`)

// Persona 一套人设；字段对齐蓝本 personas/<id>.json 与 Ent content.go Persona 表。
type Persona struct {
	ID           string `json:"id"`
	Slug         string `json:"slug"`
	Name         string `json:"name"`
	Tagline      string `json:"tagline,omitempty"`
	VoiceID      string `json:"voice_id,omitempty"`
	AvatarNote   string `json:"avatar_note,omitempty"`
	SystemPrompt string `json:"system_prompt"`
	IsActive     bool   `json:"is_active"`
	UpdatedAtMs  int64  `json:"updated_at_ms"`
}

// SaveInput 对齐蓝本 PersonaSaveBody；ID 由路径/顶层字段单独传入。
type SaveInput struct {
	Name         string
	Tagline      string
	VoiceID      string
	AvatarNote   string
	SystemPrompt string
}

// Store 人设持久化端口；内存与 Ent 实现可热切换（repo.SwitchablePersonaStore）。
type Store interface {
	List(ctx context.Context) ([]Persona, error)
	Get(ctx context.Context, id string) (Persona, error)
	// Save 按 ID upsert；IsActive 由实现方保留（新建默认 false）。
	Save(ctx context.Context, p Persona) error
	Delete(ctx context.Context, id string) error
	// SetActive 排他置位（其他人设全部 is_active=false）；id 不存在返回 ErrNotFound。
	SetActive(ctx context.Context, id string) error
	ActiveID(ctx context.Context) (string, error)
	// Backend 返回当前生效存储标识（"memory" | "ent"），随切换动态变化。
	Backend() string
}

// Service 人设 CRUD + 切换 + ProfileResolver 落地。
type Service struct {
	store Store
	// syncActive 切换成功后的旁路同步（装配层用于回写 settings persona.active_id 供 UI 显示）；
	// nil 容忍，同步失败不影响切换结果。
	syncActive func(ctx context.Context, id string)
}

func NewService(store Store) *Service { return &Service{store: store} }

// SetActiveSync 注册切换后的旁路同步钩子（只调用一次，通常在装配层）。
func (s *Service) SetActiveSync(fn func(ctx context.Context, id string)) { s.syncActive = fn }

// Storage 返回当前存储标识（"memory" | "ent"），供列表响应携带。
func (s *Service) Storage() string { return s.store.Backend() }

// List 返回全部人设（按 ID 升序，对齐蓝本文件名排序）与当前 active id；无 active 返回空串。
func (s *Service) List(ctx context.Context) ([]Persona, string, error) {
	items, err := s.store.List(ctx)
	if err != nil {
		return nil, "", err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	active, err := s.store.ActiveID(ctx)
	if err != nil {
		return nil, "", err
	}
	return items, active, nil
}

// Get 取单条；不存在返回 ErrNotFound。
func (s *Service) Get(ctx context.Context, id string) (Persona, error) {
	p, err := s.store.Get(ctx, id)
	if err != nil {
		return Persona{}, err
	}
	return p, nil
}

// Select 切换人设（蓝本 personas_select）：404 校验 + 排他置位 + 旁路同步。
// 清记忆由调用方（server 路由）随后执行，保持本包不依赖 chat。
func (s *Service) Select(ctx context.Context, id string) (Persona, error) {
	p, err := s.store.Get(ctx, id)
	if err != nil {
		return Persona{}, err
	}
	if err := s.store.SetActive(ctx, p.ID); err != nil {
		return Persona{}, err
	}
	p.IsActive = true
	if s.syncActive != nil {
		s.syncActive(ctx, p.ID)
	}
	return p, nil
}

// Save 新建/覆盖一套人设（蓝本 personas.save）：id 归一小写、长度截断、prompt 必填；
// 已存在时保留 is_active 置位。
func (s *Service) Save(ctx context.Context, id string, in SaveInput) (Persona, error) {
	id = strings.ToLower(strings.TrimSpace(id))
	if !idRE.MatchString(id) {
		return Persona{}, ErrInvalidID
	}
	prompt := strings.TrimSpace(in.SystemPrompt)
	if prompt == "" {
		return Persona{}, ErrEmptyPrompt
	}
	p := Persona{
		ID:           id,
		Slug:         id,
		Name:         truncateRunes(strings.TrimSpace(in.Name), 40),
		Tagline:      truncateRunes(strings.TrimSpace(in.Tagline), 120),
		VoiceID:      truncateRunes(strings.TrimSpace(in.VoiceID), 60),
		AvatarNote:   truncateRunes(strings.TrimSpace(in.AvatarNote), 200),
		SystemPrompt: prompt,
		UpdatedAtMs:  time.Now().UnixMilli(),
	}
	if p.Name == "" {
		p.Name = id
	}
	if old, err := s.store.Get(ctx, id); err == nil {
		p.IsActive = old.IsActive
	} else if !errors.Is(err, ErrNotFound) {
		return Persona{}, err
	}
	if err := s.store.Save(ctx, p); err != nil {
		return Persona{}, err
	}
	return p, nil
}

// Delete 删除人设（蓝本 personas_delete）：删除 active 返回 ErrActiveConflict（409），
// 不存在返回 ErrNotFound（404）。
func (s *Service) Delete(ctx context.Context, id string) error {
	if _, err := s.store.Get(ctx, id); err != nil {
		return err
	}
	active, err := s.store.ActiveID(ctx)
	if err != nil {
		return err
	}
	if active == id {
		return ErrActiveConflict
	}
	return s.store.Delete(ctx, id)
}

// SystemPrompt 实现 chat.ProfileResolver（G05 单一真相源）：
// 返回 active 人设的 system_prompt；无 active、存储错误或 prompt 为空时返回空串（nil 容忍语义）。
func (s *Service) SystemPrompt(ctx context.Context) string {
	active, err := s.store.ActiveID(ctx)
	if err != nil || active == "" {
		return ""
	}
	p, err := s.store.Get(ctx, active)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(p.SystemPrompt)
}

func truncateRunes(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n])
}

// MemoryStore 无 PG 兜底实现；进程重启即丢失（与 settings.MemoryStore 同口径）。
type MemoryStore struct {
	mu     sync.Mutex
	items  map[string]Persona
	active string
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{items: map[string]Persona{}}
}

func (m *MemoryStore) Backend() string { return "memory" }

func (m *MemoryStore) List(_ context.Context) ([]Persona, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Persona, 0, len(m.items))
	for _, p := range m.items {
		out = append(out, p)
	}
	return out, nil
}

func (m *MemoryStore) Get(_ context.Context, id string) (Persona, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.items[id]
	if !ok {
		return Persona{}, ErrNotFound
	}
	return p, nil
}

func (m *MemoryStore) Save(_ context.Context, p Persona) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items[p.ID] = p
	if p.IsActive {
		m.active = p.ID
	}
	return nil
}

func (m *MemoryStore) Delete(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.items[id]; !ok {
		return ErrNotFound
	}
	delete(m.items, id)
	if m.active == id {
		m.active = ""
	}
	return nil
}

func (m *MemoryStore) SetActive(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.items[id]
	if !ok {
		return ErrNotFound
	}
	for k, v := range m.items {
		v.IsActive = k == id
		m.items[k] = v
	}
	p.IsActive = true
	m.items[id] = p
	m.active = id
	return nil
}

func (m *MemoryStore) ActiveID(context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.active, nil
}
