package server

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
)

// redisGate 本机无 Redis 时跳过：契约断言留待真机（CI/部署机）执行。
func redisGate(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("DH_TEST_REDIS_ADDR")
	if addr == "" {
		t.Skip("DH_TEST_REDIS_ADDR 未设置：跳过 Redis 契约测试，待真机验证")
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

func TestRedisTicketStoreContract(t *testing.T) {
	rdb := redisGate(t)
	s := NewRedisTicketStore(rdb, time.Minute)

	tk, exp, err := s.Issue("sess-1", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if tk == "" || exp.Before(time.Now()) {
		t.Fatalf("ticket=%q exp=%v", tk, exp)
	}
	// Peek 不消费。
	if ok, sid := s.Peek(tk); !ok || sid != "sess-1" {
		t.Fatalf("Peek: ok=%v sid=%q", ok, sid)
	}
	if ok, sid := s.Peek(tk); !ok || sid != "sess-1" {
		t.Fatal("Peek 不得消费 ticket")
	}
	// Consume 一次性：第二次必须失败。
	if ok, sid := s.Consume(tk); !ok || sid != "sess-1" {
		t.Fatalf("Consume: ok=%v sid=%q", ok, sid)
	}
	if ok, _ := s.Consume(tk); ok {
		t.Fatal("重复消费必须失败（GETDEL 原子语义）")
	}
	// 未知 ticket。
	if ok, _ := s.Consume("no-such-ticket"); ok {
		t.Fatal("未知 ticket 不得通过")
	}
}

func TestRedisTicketStoreTTLExpiry(t *testing.T) {
	rdb := redisGate(t)
	s := NewRedisTicketStore(rdb, time.Minute)
	tk, _, err := s.Issue("sess-ttl", time.Second)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1500 * time.Millisecond)
	if ok, _ := s.Consume(tk); ok {
		t.Fatal("过期 ticket 不得消费成功")
	}
}

func TestRedisNonceStoreContract(t *testing.T) {
	rdb := redisGate(t)
	s := NewRedisNonceStore(rdb)
	ctx := context.Background()
	nonce := "contract-nonce-" + time.Now().Format("150405.000000")
	if !s.Take(ctx, nonce, time.Minute) {
		t.Fatal("首次 nonce 必须放行")
	}
	if s.Take(ctx, nonce, time.Minute) {
		t.Fatal("窗口内重放必须拒绝")
	}
}
