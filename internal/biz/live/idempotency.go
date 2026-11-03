package live

import (
	"context"
	"sync"
	"time"
)

// Result 幂等结果；同一 key 命中后直接回放，避免重复启动/停止。
type Result struct {
	SessionID string `json:"session_id"`
	Phase     Phase  `json:"phase"`
	LiveID    string `json:"live_id,omitempty"`
	OK        bool   `json:"ok"`
	Note      string `json:"note,omitempty"`
}

// Store 幂等结果存储端口；key 为空表示不启用幂等。
type Store interface {
	Get(ctx context.Context, key string) (Result, bool, error)
	Put(ctx context.Context, key string, r Result, ttl time.Duration) error
}

// DefaultIdempotencyTTL 默认幂等窗口 24h。
const DefaultIdempotencyTTL = 24 * time.Hour

type idemItem struct {
	result  Result
	expires time.Time
}

// MemoryIdempotencyStore 进程内幂等存储，带 TTL 清理。
type MemoryIdempotencyStore struct {
	mu    sync.Mutex
	items map[string]idemItem
	ttl   time.Duration
	now   func() time.Time
}

func NewMemoryIdempotencyStore(ttl time.Duration) *MemoryIdempotencyStore {
	if ttl <= 0 {
		ttl = DefaultIdempotencyTTL
	}
	return &MemoryIdempotencyStore{items: map[string]idemItem{}, ttl: ttl, now: time.Now}
}

func (s *MemoryIdempotencyStore) Get(_ context.Context, key string) (Result, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	it, ok := s.items[key]
	if !ok {
		return Result{}, false, nil
	}
	if !s.now().Before(it.expires) {
		delete(s.items, key)
		return Result{}, false, nil
	}
	return it.result, true, nil
}

func (s *MemoryIdempotencyStore) Put(_ context.Context, key string, r Result, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = s.ttl
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	for k, it := range s.items { // 顺带清理过期项，避免无限增长
		if !now.Before(it.expires) {
			delete(s.items, k)
		}
	}
	s.items[key] = idemItem{result: r, expires: now.Add(ttl)}
	return nil
}
