package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"

	khttp "github.com/go-kratos/kratos/v2/transport/http"

	"github.com/teamwork/digital-human/internal/biz/persona"
)

// startPersonaServer 只挂人设路由 + fake 清记忆钩子，验证 F38/G04 全链路（无 PG 内存存储）。
func startPersonaServer(t *testing.T, svc *persona.Service, reset *int) (string, func()) {
	t.Helper()
	resetCalls := 0
	deps := PanelDeps{
		Personas: svc,
		ResetLLM: func(context.Context) int {
			resetCalls++
			if reset != nil {
				*reset = resetCalls
			}
			return 3 // fake 已清轮数，透传给响应
		},
	}
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := khttp.NewServer(khttp.Listener(lis))
	registerPanelRoutes(srv, deps)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	stop := func() {
		cancel()
		_ = srv.Stop(context.Background())
		<-done
	}
	return "http://" + lis.Addr().String(), stop
}

func decodeMap(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("响应非 JSON: %s", raw)
	}
	return m
}

// 蓝本 bridge.py:1090-1140 四路由语义：list（storage 标识）→ save → select（清记忆立即生效）→ delete。
func TestPersonaRoutesEndToEnd(t *testing.T) {
	svc := persona.NewService(persona.NewMemoryStore())
	resetCalls := 0
	base, stop := startPersonaServer(t, svc, &resetCalls)
	defer stop()

	// 空库列表：storage=memory，active 空。
	resp, raw := callJSON(t, base, http.MethodGet, "/api/v1/personas", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list = %d %s", resp.StatusCode, raw)
	}
	m := decodeMap(t, raw)
	if m["storage"] != "memory" || m["active"] != "" {
		t.Fatalf("list 响应 = %v", m)
	}

	// save：校验失败 400（蓝本 personas.save ValueError）。
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/personas/save", map[string]any{"id": "BAD ID", "system_prompt": "x"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 id = %d", resp.StatusCode)
	}
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/personas/save", map[string]any{"id": "a", "system_prompt": ""})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空 prompt = %d", resp.StatusCode)
	}

	resp, raw = callJSON(t, base, http.MethodPost, "/api/v1/personas/save",
		map[string]any{"id": "xiaoya", "name": "小雅", "system_prompt": "你是小雅"})
	if resp.StatusCode != 200 {
		t.Fatalf("save = %d %s", resp.StatusCode, raw)
	}
	resp, raw = callJSON(t, base, http.MethodPost, "/api/v1/personas/save",
		map[string]any{"id": "ab", "name": "阿北", "system_prompt": "你是阿北"})
	if resp.StatusCode != 200 {
		t.Fatalf("save ab = %d %s", resp.StatusCode, raw)
	}

	// select：404 → 200 且触发清记忆（G04 无 restart_required）。
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/personas/select", map[string]any{"id": "ghost"})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("select 不存在 = %d", resp.StatusCode)
	}
	resp, raw = callJSON(t, base, http.MethodPost, "/api/v1/personas/select", map[string]any{"id": "xiaoya"})
	if resp.StatusCode != 200 {
		t.Fatalf("select = %d %s", resp.StatusCode, raw)
	}
	m = decodeMap(t, raw)
	if m["active"] != "xiaoya" || m["cleared_turns"].(float64) != 3 {
		t.Fatalf("select 响应 = %v", m)
	}
	if resetCalls != 1 {
		t.Fatalf("清记忆钩子调用 %d 次, want 1", resetCalls)
	}

	// 列表回读：active + 当前人设完整 prompt（蓝本 prompt/prompt_chars）。
	_, raw = callJSON(t, base, http.MethodGet, "/api/v1/personas", nil)
	m = decodeMap(t, raw)
	if m["active"] != "xiaoya" || m["prompt"] != "你是小雅" || m["prompt_chars"].(float64) != 4 {
		t.Fatalf("list 响应 = %v", m)
	}

	// delete：删 active 409；切走后 200。
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/personas/delete", map[string]any{"id": "xiaoya"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("删 active = %d", resp.StatusCode)
	}
	_, _ = callJSON(t, base, http.MethodPost, "/api/v1/personas/select", map[string]any{"id": "ab"})
	resp, raw = callJSON(t, base, http.MethodPost, "/api/v1/personas/delete", map[string]any{"id": "xiaoya"})
	if resp.StatusCode != 200 {
		t.Fatalf("delete = %d %s", resp.StatusCode, raw)
	}
	if got := svc.SystemPrompt(context.Background()); got != "你是阿北" {
		t.Fatalf("切换后 resolver prompt = %q", got)
	}
}

// ProfileResolver 经 personas 服务空值容忍：无 active 时 GET prompt 为空串。
func TestPersonaListEmptyPrompt(t *testing.T) {
	svc := persona.NewService(persona.NewMemoryStore())
	base, stop := startPersonaServer(t, svc, nil)
	defer stop()
	if _, err := svc.Save(context.Background(), "a", persona.SaveInput{SystemPrompt: "P"}); err != nil {
		t.Fatal(err)
	}
	_, raw := callJSON(t, base, http.MethodGet, "/api/v1/personas", nil)
	m := decodeMap(t, raw)
	if m["prompt"] != "" || m["prompt_chars"].(float64) != 0 {
		t.Fatalf("无 active 时 prompt = %v", m)
	}
}
