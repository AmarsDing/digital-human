package server

import (
	"context"
	"encoding/hex"
	"time"

	"github.com/redis/go-redis/v9"
)

// Redis key 前缀；集中在此避免散落。
const (
	redisTicketPrefix = "dh:ticket:"
	redisNoncePrefix  = "dh:nonce:"
)

// RedisTicketStore 一次性 WS ticket 的 Redis 实现。
// 与内存版差异：多实例共享、进程重启不丢；Issue 用 SET NX EX 防撞，
// Consume 用 GETDEL 原子取删（单实例内存版靠互斥锁，语义一致）。
type RedisTicketStore struct {
	rdb *redis.Client
	ttl time.Duration
}

func NewRedisTicketStore(rdb *redis.Client, ttl time.Duration) *RedisTicketStore {
	if ttl <= 0 {
		ttl = time.Minute
	}
	return &RedisTicketStore{rdb: rdb, ttl: ttl}
}

func (s *RedisTicketStore) Issue(sessionID string, ttl time.Duration) (string, time.Time, error) {
	if ttl <= 0 {
		ttl = s.ttl
	}
	buf := make([]byte, 32)
	if err := readRandom(buf); err != nil {
		return "", time.Time{}, err
	}
	t := hex.EncodeToString(buf)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := s.rdb.SetNX(ctx, redisTicketPrefix+t, sessionID, ttl).Err(); err != nil {
		return "", time.Time{}, err
	}
	return t, time.Now().Add(ttl), nil
}

// Peek 只读校验，不消费；无效/过期返回 false（Redis TTL 到期自动删除）。
func (s *RedisTicketStore) Peek(t string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	v, err := s.rdb.Get(ctx, redisTicketPrefix+t).Result()
	if err != nil {
		return false, ""
	}
	return true, v
}

// Consume 用 GETDEL 原子取出并删除，保证一次性语义（并发消费只有一个成功）。
func (s *RedisTicketStore) Consume(t string) (bool, string) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	v, err := s.rdb.GetDel(ctx, redisTicketPrefix+t).Result()
	if err != nil {
		return false, ""
	}
	return true, v
}

// NonceStore HMAC 重放窗口去重端口；Take 返回 true 表示首次使用。
type NonceStore interface {
	Take(ctx context.Context, nonce string, window time.Duration) bool
}

// MemoryNonceStore 进程内 nonce 去重：重启即清空、多实例不共享（差异见 README）。
type MemoryNonceStore struct {
	cache *nonceCache
}

func NewMemoryNonceStore(window time.Duration) *MemoryNonceStore {
	if window <= 0 {
		window = defaultNonceWindow
	}
	return &MemoryNonceStore{cache: newNonceCache(window)}
}

func (s *MemoryNonceStore) Take(_ context.Context, nonce string, _ time.Duration) bool {
	return s.cache.take(nonce)
}

// RedisNonceStore nonce 去重的 Redis 实现：SET NX EX=窗口，首见即 true。
// 多实例共享同一窗口语义，重启不丢。
type RedisNonceStore struct {
	rdb *redis.Client
}

func NewRedisNonceStore(rdb *redis.Client) *RedisNonceStore {
	return &RedisNonceStore{rdb: rdb}
}

func (s *RedisNonceStore) Take(ctx context.Context, nonce string, window time.Duration) bool {
	if window <= 0 {
		window = 10 * time.Minute
	}
	c, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	ok, err := s.rdb.SetNX(c, redisNoncePrefix+nonce, 1, window).Result()
	if err != nil {
		// Redis 抖动时 fail-closed：拒绝该请求而不是放行潜在重放。
		return false
	}
	return ok
}
