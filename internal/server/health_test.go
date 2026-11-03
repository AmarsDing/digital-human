package server

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
)

// 三态语义：
//  1. 依赖就绪（postgres && redis）→ 200 + status=ok；
//  2. 缺依赖 + 默认（require_dependencies=false）→ 200 + status=degraded + missing 名单
//     （E9：无 PG 模式是支持的降级运行，探活不应 503 摘除实例）；
//  3. 缺依赖 + Strict（require_dependencies=true）→ 503（恢复旧行为）。
func TestHealthStatusOKWhenDependenciesReady(t *testing.T) {
	h := NewHealth("test", func(context.Context) DataStatus {
		return DataStatus{Postgres: true, Redis: true}
	})
	code, p := h.Status(context.Background())
	if code != http.StatusOK {
		t.Fatalf("ready: want 200, got %d", code)
	}
	if p["ok"] != true || p["degraded"] != false || p["status"] != "ok" {
		t.Fatalf("ready payload: %+v", p)
	}
	if m := p["missing"].([]string); len(m) != 0 {
		t.Fatalf("ready missing: %+v", m)
	}
}

func TestHealthStatusDegradedDefaultsTo200(t *testing.T) {
	var pg, rd atomic.Bool
	h := NewHealth("test", func(context.Context) DataStatus {
		return DataStatus{Postgres: pg.Load(), Redis: rd.Load()}
	})
	ctx := context.Background()

	// 两者都缺 -> 200 + degraded + missing 全名单（不再 503）。
	code, p := h.Status(ctx)
	if code != http.StatusOK {
		t.Fatalf("both down: want 200, got %d", code)
	}
	if p["ok"] != false || p["degraded"] != true || p["status"] != "degraded" {
		t.Fatalf("both down payload: %+v", p)
	}
	m := p["missing"].([]string)
	if len(m) != 2 || m[0] != "postgres" || m[1] != "redis" {
		t.Fatalf("both down missing: %+v", m)
	}
	if p["data"].(DataStatus) != (DataStatus{}) {
		t.Fatalf("both down data: %+v", p["data"])
	}

	// 只接齐 PG -> 仍 200 + degraded，missing 只剩 redis。
	pg.Store(true)
	code, p = h.Status(ctx)
	if code != http.StatusOK {
		t.Fatalf("pg only: want 200, got %d", code)
	}
	if p["status"] != "degraded" {
		t.Fatalf("pg only payload: %+v", p)
	}
	m = p["missing"].([]string)
	if len(m) != 1 || m[0] != "redis" {
		t.Fatalf("pg only missing: %+v", m)
	}

	// Redis 也接齐 -> 自动转 ok（探测是运行期实时判定，无需重启）。
	rd.Store(true)
	code, p = h.Status(ctx)
	if code != http.StatusOK || p["status"] != "ok" || p["ok"] != true {
		t.Fatalf("both ready: code=%d payload=%+v", code, p)
	}

	// 依赖再次掉线 -> 回到 degraded 200。
	rd.Store(false)
	code, p = h.Status(ctx)
	if code != http.StatusOK || p["status"] != "degraded" {
		t.Fatalf("redis lost again: code=%d payload=%+v", code, p)
	}
}

func TestHealthStatusStrictReturns503(t *testing.T) {
	var pg, rd atomic.Bool
	h := NewHealth("test", func(context.Context) DataStatus {
		return DataStatus{Postgres: pg.Load(), Redis: rd.Load()}
	})
	h.Strict = true
	ctx := context.Background()

	// 缺依赖 + strict -> 503（旧行为恢复）；payload 仍带三态标注。
	code, p := h.Status(ctx)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("strict both down: want 503, got %d", code)
	}
	if p["ok"] != false || p["status"] != "degraded" {
		t.Fatalf("strict payload: %+v", p)
	}

	// 只接齐 PG 仍 503；接齐后自动转 200。
	pg.Store(true)
	if code, _ = h.Status(ctx); code != http.StatusServiceUnavailable {
		t.Fatalf("strict pg only: want 503, got %d", code)
	}
	rd.Store(true)
	if code, p = h.Status(ctx); code != http.StatusOK || p["status"] != "ok" {
		t.Fatalf("strict ready: code=%d payload=%+v", code, p)
	}
}

func TestHealthNilProbeDegraded200ByDefault(t *testing.T) {
	h := NewHealth("test", nil)
	code, p := h.Status(context.Background())
	if code != http.StatusOK {
		t.Fatalf("nil probe: want 200, got %d", code)
	}
	if p["ok"] != false || p["degraded"] != true || p["status"] != "degraded" {
		t.Fatalf("nil probe payload: %+v", p)
	}

	// nil probe + strict -> 503。
	h.Strict = true
	if code, _ = h.Status(context.Background()); code != http.StatusServiceUnavailable {
		t.Fatalf("nil probe strict: want 503, got %d", code)
	}
}

func TestHealthPayloadReportsPhaseAndVersion(t *testing.T) {
	h := NewHealth("v1", func(context.Context) DataStatus {
		return DataStatus{Postgres: true, Redis: true}
	})
	h.Phase = func() string { return "live" }
	_, p := h.Status(context.Background())
	if p["version"] != "v1" || p["phase"] != "live" {
		t.Fatalf("metadata: %+v", p)
	}
}
