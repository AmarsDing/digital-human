package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"

	"github.com/teamwork/digital-human/internal/biz/audit"
	"github.com/teamwork/digital-human/internal/biz/contentgate"
	"github.com/teamwork/digital-human/internal/conf"
)

func TestContentGateAuditRoutesRequireAuthAndExportOnlyMetadata(t *testing.T) {
	svc := audit.NewService(audit.NewMemorySink(20))
	rules := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(rules, []byte("layers:\n  vulgar:\n    decision: block\n    words: [低俗词]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gate := contentgate.New(contentgate.Options{Enabled: true, RulesPath: rules, Recorder: svc})
	const original = "唯一原文-低俗词-不得进入审计导出"
	decision := gate.Check(context.Background(), contentgate.Input{Outlet: "koubo.tts", SessionID: "s1", Text: original})
	if decision.Disposition != contentgate.DispositionSpeakTemplate {
		t.Fatalf("unexpected decision: %+v", decision)
	}

	token := "content-audit-token"
	sum := sha256.Sum256([]byte(token))
	cfg := &conf.Config{
		Server:   conf.Server{ReadTimeout: 5 * time.Second},
		Security: conf.Security{Mode: "lan", AdminTokenSHA256: hex.EncodeToString(sum[:]), TicketTTL: time.Minute},
	}
	tickets := NewMemoryTicketStore(time.Minute)
	t.Cleanup(tickets.Close)
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := khttp.NewServer(
		khttp.Listener(lis), khttp.Timeout(0),
		khttp.Filter(serverFilters(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), tickets, svc)...),
	)
	registerContentGateAuditRoutes(srv, svc)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Stop(context.Background())
		<-done
	})

	base := "http://" + lis.Addr().String()
	for _, path := range []string{"/api/v1/content-gate/audits?limit=10", "/api/v1/content-gate/audits/export"} {
		resp, err := http.Get(base + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("无鉴权 %s = %d, want 401", path, resp.StatusCode)
		}
	}

	get := func(path string) (*http.Response, []byte) {
		req, err := http.NewRequest(http.MethodGet, base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("GET %s = %d: %s", path, resp.StatusCode, raw)
		}
		for _, forbidden := range []string{original, "低俗词", "text", "original"} {
			if strings.Contains(string(raw), forbidden) {
				t.Fatalf("审计响应夹带原文或文本字段 %q: %s", forbidden, raw)
			}
		}
		return resp, raw
	}

	_, raw := get("/api/v1/content-gate/audits?limit=10")
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(raw, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("want 1 audit item: %s", raw)
	}
	assertContentGateAuditFields(t, list.Items[0])

	resp, raw := get("/api/v1/content-gate/audits/export")
	if disposition := resp.Header.Get("Content-Disposition"); !strings.Contains(disposition, "attachment") || !strings.Contains(disposition, ".json") {
		t.Fatalf("导出响应头错误: %q", disposition)
	}
	var exported []map[string]any
	if err := json.Unmarshal(raw, &exported); err != nil {
		t.Fatal(err)
	}
	if len(exported) != 1 {
		t.Fatalf("want 1 exported audit: %s", raw)
	}
	assertContentGateAuditFields(t, exported[0])
}

func assertContentGateAuditFields(t *testing.T, row map[string]any) {
	t.Helper()
	allowed := map[string]bool{"id": true, "ts_ms": true, "outlet": true, "category": true, "disposition": true}
	if len(row) != len(allowed) {
		t.Fatalf("审计字段必须严格限定为元数据: %+v", row)
	}
	for key := range row {
		if !allowed[key] {
			t.Fatalf("审计存在未批准字段 %q: %+v", key, row)
		}
	}
	if row["id"] == "" || row["ts_ms"] == float64(0) || row["outlet"] != "koubo.tts" || row["category"] != "vulgar" || row["disposition"] != "speak_template" {
		t.Fatalf("审计字段值不完整: %+v", row)
	}
}
