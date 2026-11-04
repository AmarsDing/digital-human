package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/audit"
	"github.com/teamwork/digital-human/internal/biz/contentgate"
	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/biz/tts"
)

func TestContentGateRealWireAssemblyDev(t *testing.T) {
	t.Chdir(t.TempDir())
	rulesPath := filepath.Join(t.TempDir(), "rules.yaml")
	if err := os.WriteFile(rulesPath, []byte("layers:\n  vulgar:\n    decision: block\n    words: [低俗词]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := lis.Addr().String()
	_ = lis.Close()
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	config := fmt.Sprintf(`server:
  http_addr: %q
  read_timeout: 5s
  write_timeout: 5s
  shutdown_timeout: 5s
engine:
  mode: dev
live:
  allow_local_preview: true
content_gate:
  enabled: true
  rules_path: %q
  allow_review: false
  circuit_breaker_threshold: 3
voice:
  asr:
    provider: fake
`, addr, filepath.ToSlash(rulesPath))
	if err := os.WriteFile(configPath, []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}

	app, cleanup, err := InitApp(configPath, VersionInfo("content-gate-test"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Server.Start(ctx) }()
	t.Cleanup(func() {
		_, _, _ = app.Manager.Stop(context.Background(), "test_cleanup")
		cancel()
		_ = app.Server.Stop(context.Background())
		<-done
		cleanup()
	})

	sessionID, phase, _, err := app.Manager.Start(context.Background(), "", "", "")
	if err != nil || phase != live.PhaseLive {
		t.Fatalf("dev 开播失败: session=%s phase=%s err=%v", sessionID, phase, err)
	}
	before := app.Manager.Snapshot()
	base := "http://" + addr
	post := func(path, body string) []byte {
		t.Helper()
		resp, err := http.Post(base+path, "application/json", bytes.NewBufferString(body))
		if err != nil {
			t.Fatal(err)
		}
		raw, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("POST %s = %d: %s", path, resp.StatusCode, raw)
		}
		return raw
	}

	const original = "唯一装配原文-低俗词-不得合成"
	post("/api/v1/koubo/split", `{"text":"`+original+`","max_chars":120}`)
	post("/api/v1/koubo/tts", `{"id":"s1"}`)

	fake, ok := app.panel.synth.(*tts.FakeSynth)
	if !ok {
		t.Fatalf("dev 必须装配 FakeSynth，实际为 %T", app.panel.synth)
	}
	texts := fake.Texts()
	if len(texts) != 1 || texts[0] != contentgate.SafeSpokenTemplate {
		t.Fatalf("TTS 实际收到 %+v，必须且只能收到静态模板 %q", texts, contentgate.SafeSpokenTemplate)
	}
	for _, text := range texts {
		if text == original || strings.Contains(text, "低俗词") {
			t.Fatalf("TTS 不得收到原文或命中词: %q", text)
		}
	}
	t.Logf("fake TTS 实际收到文本: %q", texts[0])

	resp, err := http.Get(base + "/api/v1/content-gate/audits?limit=10")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("审计查询 = %d: %s", resp.StatusCode, raw)
	}
	var listed struct {
		Items []audit.ContentDecision `json:"items"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Items) != 1 || listed.Items[0].Outlet != "koubo.tts" || listed.Items[0].Category != "vulgar" || listed.Items[0].Disposition != "speak_template" {
		t.Fatalf("真实装配审计错误: %s", raw)
	}
	if strings.Contains(string(raw), original) || strings.Contains(string(raw), "低俗词") {
		t.Fatalf("真实装配审计不得包含原文全文或命中词: %s", raw)
	}

	after := app.Manager.Snapshot()
	if after.Phase != live.PhaseLive || after.SessionID != before.SessionID {
		t.Fatalf("终检不得中断直播: before=%+v after=%+v", before, after)
	}
	health, err := http.Get(base + "/healthz")
	if err != nil {
		t.Fatal(err)
	}
	_ = health.Body.Close()
	if health.StatusCode != http.StatusOK {
		t.Fatalf("终检后服务探活 = %d", health.StatusCode)
	}
}
