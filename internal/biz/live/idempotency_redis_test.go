package live

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

func redisGate(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("DH_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("DH_TEST_REDIS_ADDR 未设置：跳过 Redis 幂等契约测试，待真机验证")
	}
	rdb := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		_ = rdb.Close()
		t.Skipf("redis %s 不可达：跳过，待真机验证: %v", addr, err)
	}
	t.Cleanup(func() { _ = rdb.Close() })
	return rdb
}

func TestRedisIdempotencyStoreContract(t *testing.T) {
	rdb := redisGate(t)
	s := NewRedisIdempotencyStore(rdb, time.Minute)
	ctx := context.Background()
	key := "start:contract-" + time.Now().Format("150405.000000")

	if _, ok, err := s.Get(ctx, key); err != nil || ok {
		t.Fatalf("空键 Get: ok=%v err=%v", ok, err)
	}
	want := Result{SessionID: "s-1", Phase: PhaseLive, LiveID: "lv-1", OK: true}
	if err := s.Put(ctx, key, want, 0); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(ctx, key)
	if err != nil || !ok {
		t.Fatalf("Put 后 Get: ok=%v err=%v", ok, err)
	}
	if got != want {
		t.Fatalf("回读不一致: got=%+v want=%+v", got, want)
	}
	// 首写胜出：第二次 Put 不覆盖。
	other := Result{SessionID: "s-2", Phase: PhaseError, OK: false, Note: "x"}
	if err := s.Put(ctx, key, other, 0); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(ctx, key)
	if got != want {
		t.Fatalf("首写胜出被破坏: got=%+v want=%+v", got, want)
	}
}
