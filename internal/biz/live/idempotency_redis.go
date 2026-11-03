package live

import (
	"context"
	"encoding/json"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisIdemPrefix 幂等键 Redis 前缀。
const redisIdemPrefix = "dh:idem:"

// RedisIdempotencyStore 幂等结果的 Redis 实现。
// Put 用 SET NX EX（首写胜出，与 Ent 版唯一约束语义一致）；Get 直接回读结果。
// 与内存版差异：重启不丢、多实例共享；执行串行化仍由 Manager.idemMu 保证。
type RedisIdempotencyStore struct {
	rdb *redis.Client
	ttl time.Duration
}

func NewRedisIdempotencyStore(rdb *redis.Client, ttl time.Duration) *RedisIdempotencyStore {
	if ttl <= 0 {
		ttl = DefaultIdempotencyTTL
	}
	return &RedisIdempotencyStore{rdb: rdb, ttl: ttl}
}

func (s *RedisIdempotencyStore) Get(ctx context.Context, key string) (Result, bool, error) {
	raw, err := s.rdb.Get(ctx, redisIdemPrefix+key).Result()
	if err == redis.Nil {
		return Result{}, false, nil
	}
	if err != nil {
		return Result{}, false, err
	}
	var r Result
	if err := json.Unmarshal([]byte(raw), &r); err != nil {
		return Result{}, false, err
	}
	return r, true, nil
}

// Put 首写胜出：同 key 已存在（未过期）时不覆盖（SET NX 返回 false 不算错误）。
func (s *RedisIdempotencyStore) Put(ctx context.Context, key string, r Result, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = s.ttl
	}
	raw, err := json.Marshal(r)
	if err != nil {
		return err
	}
	_, err = s.rdb.SetNX(ctx, redisIdemPrefix+key, raw, ttl).Result()
	return err
}
