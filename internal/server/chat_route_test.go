package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"

	khttp "github.com/go-kratos/kratos/v2/transport/http"

	"github.com/teamwork/digital-human/internal/biz/voice"
	"github.com/teamwork/digital-human/internal/llm"
)

// chatTestSrv 只注册指定面板依赖的轻量 HTTP 服务。
type chatTestSrv struct {
	base string
	stop func()
}

// newBareDepsServer 以给定 PanelDeps 注册全部面板路由（nil 依赖的路由自动跳过）。
func newBareDepsServer(t *testing.T, deps PanelDeps) chatTestSrv {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := khttp.NewServer(khttp.Listener(lis))
	registerPanelRoutes(srv, deps)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	return chatTestSrv{
		base: "http://" + lis.Addr().String(),
		stop: func() {
			cancel()
			_ = srv.Stop(context.Background())
			<-done
		},
	}
}

func newChatTestServer(t *testing.T, fn func(context.Context, string) (string, error)) chatTestSrv {
	t.Helper()
	return newBareDepsServer(t, PanelDeps{Chat: fn})
}

// N1 /api/v1/chat（蓝本 bridge.py:469 /api/chat → engine.chat）：
// 无进行中会话 409、LLM 未配置 fail-closed 503、上游失败 502。
func TestChatRouteErrors(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want int
		code string
	}{
		{"not_live_409", ErrChatNotLive, http.StatusConflict, "not_live"},
		{"voice_not_live_409", voice.ErrNotLive, http.StatusConflict, "not_live"},
		{"voice_turn_active_409", voice.ErrTurnActive, http.StatusConflict, "voice_turn_active"},
		{"llm_not_configured_503", llm.ErrLLMNotConfigured, http.StatusServiceUnavailable, "llm_not_configured"},
		{"llm_stream_502", errors.New("upstream boom"), http.StatusBadGateway, "upstream boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newChatTestServer(t, func(context.Context, string) (string, error) { return "", tc.err })
			defer srv.stop()
			resp, raw := callJSON(t, srv.base, "POST", "/api/v1/chat", map[string]any{"text": "在吗"})
			mustStatus(t, resp, tc.want, "/api/v1/chat")
			var body map[string]any
			if err := json.Unmarshal(raw, &body); err != nil {
				t.Fatal(err)
			}
			if body["ok"] != false || body["error"] != tc.code {
				t.Fatalf("body=%v want ok=false error=%q", body, tc.code)
			}
		})
	}
}

func TestChatRouteOK(t *testing.T) {
	var got []string
	srv := newChatTestServer(t, func(_ context.Context, text string) (string, error) {
		got = append(got, text)
		return "完整应答", nil
	})
	defer srv.stop()

	// text 与蓝本 content 字段均接受；text 先 trim。
	resp, raw := callJSON(t, srv.base, "POST", "/api/v1/chat", map[string]any{"text": "  你好  "})
	mustStatus(t, resp, http.StatusOK, "/api/v1/chat")
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["ok"] != true || body["reply"] != "完整应答" {
		t.Fatalf("body=%v", body)
	}
	if len(got) != 1 || got[0] != "你好" {
		t.Fatalf("text 应 trim 后透传, got=%v", got)
	}

	resp, _ = callJSON(t, srv.base, "POST", "/api/v1/chat", map[string]any{"content": "蓝本字段"})
	mustStatus(t, resp, http.StatusOK, "/api/v1/chat")

	// 空文本 400 text_required。
	resp, raw = callJSON(t, srv.base, "POST", "/api/v1/chat", map[string]any{"text": "   "})
	mustStatus(t, resp, http.StatusBadRequest, "/api/v1/chat")
	if !strings.Contains(string(raw), "text_required") {
		t.Fatalf("body=%s want text_required", raw)
	}
}
