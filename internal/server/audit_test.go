package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
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

// owner_id 只是客户端提供的查询范围。同一个共享管理令牌可以切换到任意
// owner_id；此测试显式固定该边界，禁止把它误当作用户级权限校验。
func TestDraftOwnerQueryIsNotAuthorizationBoundary(t *testing.T) {
	svc, _ := newDraftRouteService()
	wantDraft := make(map[string]string, 2)
	for _, ownerID := range []string{"owner-1", "owner-2"} {
		wantDraft[ownerID] = createOwnedDraft(t, svc, ownerID, ownerID+" 草稿").ID
	}

	token := "shared-admin-token"
	sum := sha256.Sum256([]byte(token))
	cfg := &conf.Config{Security: conf.Security{
		Mode: "lan", AdminTokenSHA256: hex.EncodeToString(sum[:]), TicketTTL: time.Minute,
	}}
	tickets := NewMemoryTicketStore(time.Minute)
	t.Cleanup(tickets.Close)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := khttp.NewServer(
		khttp.Listener(lis), khttp.Timeout(0),
		khttp.Filter(serverFilters(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), tickets, nil)...),
	)
	registerDraftRoutes(srv, svc)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Stop(context.Background())
		<-done
	})

	for _, ownerID := range []string{"owner-1", "owner-2"} {
		req, err := http.NewRequest(http.MethodGet, "http://"+lis.Addr().String()+"/api/v1/live-drafts?owner_id="+ownerID, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, readErr := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if readErr != nil {
			t.Fatal(readErr)
		}
		body := decodeDraftResponse(t, raw)
		items, ok := body["drafts"].([]any)
		if resp.StatusCode != http.StatusOK || !ok || len(items) != 1 {
			t.Fatalf("同一共享令牌必须能切换 owner 查询范围: owner=%s status=%d body=%s", ownerID, resp.StatusCode, raw)
		}
		gotID := items[0].(map[string]any)["draft_id"]
		if gotID != wantDraft[ownerID] {
			t.Fatalf("owner 查询范围错误: owner=%s got=%v want=%s", ownerID, gotID, wantDraft[ownerID])
		}
	}
}

// 当前认证没有用户主体，不能判断 owner_id 是否与主体一致。所有草稿
// owner 请求都必须留下“未经主体验证”的审计，而不是伪装成越权拦截。
func TestAuditFilterMarksDraftOwnerScopeUnverified(t *testing.T) {
	for _, tc := range []struct {
		name   string
		method string
		path   string
	}{
		{name: "list", method: http.MethodGet, path: "/api/v1/live-drafts?owner_id=another-owner"},
		{name: "detail", method: http.MethodGet, path: "/api/v1/live-drafts/draft-1?owner_id=another-owner"},
		{name: "apply", method: http.MethodPost, path: "/api/v1/live-drafts/draft-1/apply"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, _ := newAuditSvc()
			logger := slog.New(slog.NewTextHandler(io.Discard, nil))
			h := requestLoggerFilter(logger)(auditFilter(svc)(okHandler()))
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(`{"owner_id":"another-owner","token":"do-not-record"}`))
			req.RemoteAddr = "127.0.0.1:5555"
			h.ServeHTTP(rec, req)

			rows, err := svc.List(context.Background(), 0)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, e := range rows {
				if e.Action != "draft_owner_unverified" {
					continue
				}
				found = true
				if e.Actor != "unknown_principal" || e.Target != "owner_id" || e.Route != strings.Split(tc.path, "?")[0] {
					t.Fatalf("owner 未验证审计字段错误: %+v", e)
				}
				for _, v := range []string{e.Route, e.Target, e.Actor, e.Action} {
					if strings.Contains(v, "another-owner") || strings.Contains(v, "do-not-record") {
						t.Fatalf("审计不得记录 owner 参数值或请求体: %+v", e)
					}
				}
			}
			if !found {
				t.Fatalf("缺少 draft_owner_unverified 审计: %+v", rows)
			}
		})
	}
}
