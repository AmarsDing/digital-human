package server

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"
)

// 判定条件：postgres && redis 同时为 true 才 200；两者接齐（无需重启）后自动从 503 转 200。
func TestHealthStatusFlipsWhenDependenciesReady(t *testing.T) {
	var pg, rd atomic.Bool
	h := NewHealth("test", func(context.Context) DataStatus {
		return DataStatus{Postgres: pg.Load(), Redis: rd.Load()}
	})

	ctx := context.Background()

	// 初始：两者都不可用 -> 503
	code, p := h.Status(ctx)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("both down: want 503, got %d", code)
	}
	if p["ok"] != false || p["degraded"] != true {
		t.Fatalf("both down payload: %+v", p)
	}
	if p["data"].(DataStatus) != (DataStatus{}) {
		t.Fatalf("both down data: %+v", p["data"])
	}

	// 只接齐 PG -> 仍 503（必须两项都就绪）
	pg.Store(true)
	code, p = h.Status(ctx)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("pg only: want 503, got %d", code)
	}
	if p["ok"] != false {
		t.Fatalf("pg only ok: %+v", p)
	}

	// Redis 也接齐 -> 自动 200
	rd.Store(true)
	code, p = h.Status(ctx)
	if code != http.StatusOK {
		t.Fatalf("both ready: want 200, got %d", code)
	}
	if p["ok"] != true || p["degraded"] != false {
		t.Fatalf("both ready payload: %+v", p)
	}
	if p["data"].(DataStatus) != (DataStatus{Postgres: true, Redis: true}) {
		t.Fatalf("both ready data: %+v", p["data"])
	}

	// 依赖再次掉线 -> 回到 503（探测是运行期实时判定，不是一次性快照）
	rd.Store(false)
	code, _ = h.Status(ctx)
	if code != http.StatusServiceUnavailable {
		t.Fatalf("redis lost again: want 503, got %d", code)
	}
}

func TestHealthWithoutProbeIsDegraded(t *testing.T) {
	h := NewHealth("test", nil)
	code, p := h.Status(context.Background())
	if code != http.StatusServiceUnavailable {
		t.Fatalf("nil probe: want 503, got %d", code)
	}
	if p["ok"] != false || p["degraded"] != true {
		t.Fatalf("nil probe payload: %+v", p)
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
