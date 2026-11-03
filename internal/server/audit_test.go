package server

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"

	"github.com/teamwork/digital-human/internal/biz/audit"
	"github.com/teamwork/digital-human/internal/conf"
)

func newAuditSvc() (*audit.Service, *audit.MemorySink) {
	mem := audit.NewMemorySink(100)
	return audit.NewService(mem), mem
}

func TestAuditFilterRecordsPostWithoutBody(t *testing.T) {
	svc, _ := newAuditSvc()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := requestLoggerFilter(logger)(auditFilter(svc)(okHandler()))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/live/start?token=should-not-record", strings.NewReader(`{"secret":"should-not-record"}`))
	req.RemoteAddr = "127.0.0.1:5555"
	h.ServeHTTP(rec, req)

	rows, err := svc.List(context.Background(), 0)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 audit row, got %d", len(rows))
	}
	e := rows[0]
	if e.Route != "/api/v1/live/start" || e.Action != http.MethodPost {
		t.Fatalf("route/action: %+v", e)
	}
	if e.IP != "127.0.0.1" {
		t.Fatalf("ip: %q", e.IP)
	}
	if e.RequestID == "" {
		t.Fatal("request_id should be injected by requestLoggerFilter")
	}
	if e.Target != "" {
		t.Fatalf("target should be empty, got %q", e.Target)
	}
	for _, v := range []string{e.Route, e.Target, e.Actor, e.IP} {
		if strings.Contains(v, "should-not-record") {
			t.Fatalf("audit leaked body/query: %+v", e)
		}
	}
}

func TestAuditFilterSkipsGetAndIrrelevantPaths(t *testing.T) {
	svc, _ := newAuditSvc()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := requestLoggerFilter(logger)(auditFilter(svc)(okHandler()))

	for _, tc := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/live/status"},
		{http.MethodHead, "/api/v1/live/status"},
		{http.MethodOptions, "/api/v1/live/start"},
		{http.MethodPost, "/healthz"},
	} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(tc.method, tc.path, nil)
		req.RemoteAddr = "127.0.0.1:5555"
		h.ServeHTTP(rec, req)
	}

	rows, _ := svc.List(context.Background(), 0)
	if len(rows) != 0 {
		t.Fatalf("want no audit rows, got %+v", rows)
	}
}

func TestAuditFilterRecordsDanmakuPost(t *testing.T) {
	svc, _ := newAuditSvc()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	h := requestLoggerFilter(logger)(auditFilter(svc)(okHandler()))

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/danmaku", nil)
	req.RemoteAddr = "192.168.1.7:9000"
	h.ServeHTTP(rec, req)

	rows, _ := svc.List(context.Background(), 0)
	if len(rows) != 1 || rows[0].Route != "/api/danmaku" || rows[0].IP != "192.168.1.7" {
		t.Fatalf("danmaku audit: %+v", rows)
	}
}

// 被鉴权拒绝的写请求也必须留痕：审计在鉴权之前执行。
func TestAuditFilterRecordsRejectedWrite(t *testing.T) {
	svc, _ := newAuditSvc()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	cfg := &conf.Config{Security: conf.Security{Mode: "local", TicketTTL: time.Minute}}
	h := khttp.FilterChain(serverFilters(cfg, logger, NewMemoryTicketStore(time.Minute), svc)...)(okHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/live/start", nil)
	req.RemoteAddr = "203.0.113.9:5555"
	h.ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
	rows, _ := svc.List(context.Background(), 0)
	if len(rows) != 1 || rows[0].Route != "/api/v1/live/start" {
		t.Fatalf("rejected write should be audited: %+v", rows)
	}
}
