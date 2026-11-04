package repo

import (
	"context"
	"sync"
	"time"

	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/data/ent"
)

// EntIdempotencyStore 实现 live.Store：key 即主键，唯一约束保证并发下首写胜出
// （等价 Redis SET NX 语义）；到期行由 Get 删除、Put 覆盖重建。
type EntIdempotencyStore struct {
	client *ent.Client
	ttl    time.Duration
	now    func() time.Time
}

func NewEntIdempotencyStore(client *ent.Client, ttl time.Duration) *EntIdempotencyStore {
	if ttl <= 0 {
		ttl = live.DefaultIdempotencyTTL
	}
	return &EntIdempotencyStore{client: client, ttl: ttl, now: time.Now}
}

func (s *EntIdempotencyStore) Get(ctx context.Context, key string) (live.Result, bool, error) {
	r, err := s.client.IdemKey.Get(ctx, key)
	if ent.IsNotFound(err) {
		return live.Result{}, false, nil
	}
	if err != nil {
		return live.Result{}, false, err
	}
	if s.now().UnixMilli() >= r.ExpiresAtMs {
		// 到期行即删，允许同 key 重新执行。
		_ = s.client.IdemKey.DeleteOneID(key).Exec(ctx)
		return live.Result{}, false, nil
	}
	return resultFromJSON(r.Result), true, nil
}

// Put 首写胜出：已存在且未过期的同 key 行不覆盖（与 Redis SET NX 一致）；
// 已过期则覆盖为新结果与新窗口。
func (s *EntIdempotencyStore) Put(ctx context.Context, key string, r live.Result, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = s.ttl
	}
	nowMs := s.now().UnixMilli()
	result := resultToJSON(r)
	_, err := s.client.IdemKey.Create().
		SetID(key).
		SetResult(result).
		SetExpiresAtMs(nowMs + ttl.Milliseconds()).
		SetCreatedAtMs(nowMs).
		Save(ctx)
	if err == nil {
		return nil
	}
	if !isUniqueViolation(err) {
		return err
	}
	existing, gerr := s.client.IdemKey.Get(ctx, key)
	if gerr != nil {
		// 并发下另一写者已删除过期行：重建一次。
		if ent.IsNotFound(gerr) {
			_, err = s.client.IdemKey.Create().
				SetID(key).
				SetResult(result).
				SetExpiresAtMs(nowMs + ttl.Milliseconds()).
				SetCreatedAtMs(nowMs).
				Save(ctx)
			if isUniqueViolation(err) {
				return nil // 又被并发写者抢先，首写胜出
			}
			return err
		}
		return gerr
	}
	if nowMs < existing.ExpiresAtMs {
		return nil // 未过期：首写胜出，不覆盖
	}
	return s.client.IdemKey.UpdateOneID(key).
		SetResult(result).
		SetExpiresAtMs(nowMs + ttl.Milliseconds()).
		Exec(ctx)
}

func resultToJSON(r live.Result) map[string]any {
	return map[string]any{
		"session_id": r.SessionID,
		"phase":      string(r.Phase),
		"live_id":    r.LiveID,
		"ok":         r.OK,
		"note":       r.Note,
	}
}

func resultFromJSON(m map[string]any) live.Result {
	get := func(k string) string {
		if v, ok := m[k].(string); ok {
			return v
		}
		return ""
	}
	return live.Result{
		SessionID: get("session_id"),
		Phase:     live.Phase(get("phase")),
		LiveID:    get("live_id"),
		OK:        m["ok"] == true,
		Note:      get("note"),
	}
}

// SwitchableIdempotencyStore 启动时用内存实现（重启即丢幂等结果，进程内仍幂等）；
// PG 运行期就绪后原子切换为 Ent。Redis 可用时装配层优先选 Redis 版，不走本类型。
type SwitchableIdempotencyStore struct {
	mu  sync.RWMutex
	mem live.Store
	db  live.Store
}

func NewSwitchableIdempotencyStore(mem live.Store) *SwitchableIdempotencyStore {
	return &SwitchableIdempotencyStore{mem: mem}
}

func (s *SwitchableIdempotencyStore) SetEnt(client *ent.Client) {
	s.mu.Lock()
	s.db = NewEntIdempotencyStore(client, 0)
	s.mu.Unlock()
}

func (s *SwitchableIdempotencyStore) current() live.Store {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db != nil {
		return s.db
	}
	return s.mem
}

func (s *SwitchableIdempotencyStore) Get(ctx context.Context, key string) (live.Result, bool, error) {
	return s.current().Get(ctx, key)
}

func (s *SwitchableIdempotencyStore) Put(ctx context.Context, key string, r live.Result, ttl time.Duration) error {
	return s.current().Put(ctx, key, r, ttl)
}
