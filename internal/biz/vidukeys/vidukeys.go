// Package vidukeys 实现 F42/G18 Vidu 多 key 管理（蓝本 bridge.py:1458,1476 /api/vidu/keys）。
//
// 蓝本语义：GET 返回 {active: tail8, active_masked, keys: [tail8...]}；POST action=add 校验
// vda_ 前缀与长度、activate=true 立即生效，action=activate 按尾 8 位切换并立即生效，
// action=delete 禁止删当前生效 key。key 只存密文与尾 8 位（对齐安全批：完整 key 永不落库明文），
// 切换后既有资产失效仅由 UI 提示（v3 H6）。
package vidukeys

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/teamwork/digital-human/pkg/secrets"
)

var (
	// ErrInvalidKey key 不是 vda_ 前缀或长度不足 16（蓝本 400）。
	ErrInvalidKey = errors.New("invalid_vidu_key")
	// ErrDuplicate 同尾 8 位 key 已在列表（蓝本 400）。
	ErrDuplicate = errors.New("vidu_key_exists")
	// ErrNotFound 按尾 8 位找不到 key（蓝本 404）。
	ErrNotFound = errors.New("vidu_key_not_found")
	// ErrActiveKey 删除当前生效 key（任务拍板 409；蓝本为 400，语义同“禁止”）。
	ErrActiveKey = errors.New("cannot_delete_active_key")
	// ErrUnavailable 加密设施不可用（非 Windows 且缺 DH_SECRET_KEY）。
	ErrUnavailable = errors.New("vidu_keys_unavailable")
)

// StoredKey 是存储层行：cipher 为密文，tail8 为展示/检索键。
type StoredKey struct {
	ID        string
	Cipher    []byte
	Tail8     string
	IsActive  bool
	AddedAtMs int64
}

// Store 持久化端口；Memory 兜底 + Ent 生产实现（repo 层），PG 热切由 Switchable 完成。
type Store interface {
	List(ctx context.Context) ([]StoredKey, error)
	Add(ctx context.Context, k StoredKey) error
	// SetActive 清其它行 active 后置位 tail8；tail8 不存在返回 ErrNotFound。
	SetActive(ctx context.Context, tail8 string) error
	// Delete 删除 tail8；不存在返回 ErrNotFound。active 校验在服务层。
	Delete(ctx context.Context, tail8 string) error
}

// Entry 是面向面板的脱敏视图（永不带明文/密文）。
type Entry struct {
	Tail8     string `json:"tail8"`
	IsActive  bool   `json:"is_active"`
	AddedAtMs int64  `json:"added_at_ms"`
}

// Tail8 取 key 尾 8 位（对齐蓝本 k[-8:]）。
func Tail8(plain string) string {
	plain = strings.TrimSpace(plain)
	if len(plain) <= 8 {
		return plain
	}
	return plain[len(plain)-8:]
}

// MaskKey 前 6…后 4 掩码（对齐蓝本 active_masked）。
func MaskKey(plain string) string {
	if len(plain) <= 10 {
		return plain
	}
	return plain[:6] + "…" + plain[len(plain)-4:]
}

// Service 多 key 管理；apply 在激活成功后被调用（热改 vidu client），可为 nil。
type Service struct {
	store Store
	apply func(plain string)
	now   func() time.Time
}

func NewService(store Store, apply func(plain string)) *Service {
	return &Service{store: store, apply: apply, now: time.Now}
}

// List 返回 active 尾 8 位与全部尾 8 位列表（按加入时间升序）。
func (s *Service) List(ctx context.Context) (active string, keys []Entry, err error) {
	rows, err := s.store.List(ctx)
	if err != nil {
		return "", nil, err
	}
	keys = make([]Entry, 0, len(rows))
	for _, r := range rows {
		if r.IsActive {
			active = r.Tail8
		}
		keys = append(keys, Entry{Tail8: r.Tail8, IsActive: r.IsActive, AddedAtMs: r.AddedAtMs})
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i].AddedAtMs < keys[j].AddedAtMs })
	return active, keys, nil
}

// Add 校验并入库；activate=true 时同时置为生效并热改 client。重复尾 8 位 → ErrDuplicate。
func (s *Service) Add(ctx context.Context, plain string, activate bool) (Entry, error) {
	plain = strings.TrimSpace(plain)
	if !strings.HasPrefix(plain, "vda_") || len(plain) < 16 {
		return Entry{}, ErrInvalidKey
	}
	if !secrets.Available() {
		return Entry{}, ErrUnavailable
	}
	tail8 := Tail8(plain)
	rows, err := s.store.List(ctx)
	if err != nil {
		return Entry{}, err
	}
	for _, r := range rows {
		if r.Tail8 == tail8 {
			return Entry{}, ErrDuplicate
		}
	}
	cipher, err := secrets.Encrypt([]byte(plain))
	if err != nil {
		return Entry{}, err
	}
	k := StoredKey{
		ID:        newID(),
		Cipher:    cipher,
		Tail8:     tail8,
		IsActive:  activate,
		AddedAtMs: s.now().UnixMilli(),
	}
	if activate {
		// 先清旧 active 再插入，保证任意时刻至多一行 active。
		for _, r := range rows {
			if r.IsActive {
				if err := s.store.SetActive(ctx, ""); err != nil {
					return Entry{}, err
				}
				break
			}
		}
	}
	if err := s.store.Add(ctx, k); err != nil {
		return Entry{}, err
	}
	if activate {
		s.applyKey(plain)
	}
	return Entry{Tail8: tail8, IsActive: activate, AddedAtMs: k.AddedAtMs}, nil
}

// Activate 按尾 8 位切换生效 key 并热改 client（蓝本 activate 动作）。
func (s *Service) Activate(ctx context.Context, tail8 string) error {
	tail8 = strings.TrimSpace(tail8)
	if tail8 == "" {
		return ErrNotFound
	}
	if err := s.store.SetActive(ctx, tail8); err != nil {
		return err
	}
	// 解密并热改 client；解密失败不阻断激活（下次重启仍可按库生效）。
	if rows, err := s.store.List(ctx); err == nil {
		for _, r := range rows {
			if r.Tail8 == tail8 {
				if plain, derr := secrets.Decrypt(r.Cipher); derr == nil {
					s.applyKey(string(plain))
				}
				break
			}
		}
	}
	return nil
}

// Delete 删除指定 key；当前生效 key → ErrActiveKey（409）。
func (s *Service) Delete(ctx context.Context, tail8 string) error {
	tail8 = strings.TrimSpace(tail8)
	rows, err := s.store.List(ctx)
	if err != nil {
		return err
	}
	found := false
	for _, r := range rows {
		if r.Tail8 == tail8 {
			if r.IsActive {
				return ErrActiveKey
			}
			found = true
			break
		}
	}
	if !found {
		return ErrNotFound
	}
	return s.store.Delete(ctx, tail8)
}

// ApplyActive 启动时 DB 就绪后调用：把库中 active key 热改进 client（覆盖 yaml 配置）。
// 无 active 行时不动 client（保持 yaml key）。
func (s *Service) ApplyActive(ctx context.Context) error {
	rows, err := s.store.List(ctx)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if !r.IsActive {
			continue
		}
		plain, derr := secrets.Decrypt(r.Cipher)
		if derr != nil {
			return derr
		}
		s.applyKey(string(plain))
		return nil
	}
	return nil
}

// Adopt 蓝本 GET /api/vidu/keys 首访语义：列表为空且 yaml 有 key 时自动落库为 active。
// 返回是否发生了落库。
func (s *Service) Adopt(ctx context.Context, currentPlain string) (bool, error) {
	currentPlain = strings.TrimSpace(currentPlain)
	if currentPlain == "" {
		return false, nil
	}
	rows, err := s.store.List(ctx)
	if err != nil {
		return false, err
	}
	if len(rows) > 0 {
		return false, nil
	}
	_, err = s.Add(ctx, currentPlain, true)
	if err != nil {
		return false, err
	}
	return true, nil
}

func (s *Service) applyKey(plain string) {
	if s.apply != nil {
		s.apply(plain)
	}
}

// newID 生成主键；与 uuid 解耦，够用即可（时间戳+随机后缀由 Ent 侧默认更稳，这里用时间戳纳秒）。
func newID() string {
	return time.Now().Format("20060102150405.000000000")
}

// ---------- 内存实现（无 PG 降级/测试） ----------

type MemoryStore struct {
	mu   sync.Mutex
	rows []StoredKey
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

func (m *MemoryStore) List(context.Context) ([]StoredKey, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]StoredKey, len(m.rows))
	copy(out, m.rows)
	return out, nil
}

func (m *MemoryStore) Add(_ context.Context, k StoredKey) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rows = append(m.rows, k)
	return nil
}

func (m *MemoryStore) SetActive(_ context.Context, tail8 string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	found := false
	for i := range m.rows {
		if m.rows[i].Tail8 == tail8 {
			found = true
		}
	}
	if tail8 != "" && !found {
		return ErrNotFound
	}
	for i := range m.rows {
		m.rows[i].IsActive = m.rows[i].Tail8 == tail8
	}
	return nil
}

func (m *MemoryStore) Delete(_ context.Context, tail8 string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := range m.rows {
		if m.rows[i].Tail8 == tail8 {
			m.rows = append(m.rows[:i], m.rows[i+1:]...)
			return nil
		}
	}
	return ErrNotFound
}
