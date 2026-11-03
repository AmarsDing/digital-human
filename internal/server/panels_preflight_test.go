package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"

	khttp "github.com/go-kratos/kratos/v2/transport/http"

	"github.com/teamwork/digital-human/internal/biz/capability"
	"github.com/teamwork/digital-human/internal/biz/live"
)

// TestLivePreflightRoute B1 §7.5 契约：POST /api/v1/live/preflight 返回
// check_id/preflight_token/ready/mode/checks/observed_at/expires_at（30s 新鲜期）。
func TestLivePreflightRoute(t *testing.T) {
	pf := live.NewPreflight(nil, func(context.Context) capability.Matrix {
		return capability.Matrix{
			"action_sender": capability.Fake, "song_player": capability.Fake,
			"tts": capability.Fake, "vidu": capability.Fake,
			"push_gate": capability.Unconfigured, "storage_persistent": capability.Fake,
		}
	}, nil, func() bool { return false })
	// 路由独立于面板清单注册（见 preflight_route.go 注释）：手工装配最小 HTTP 服务。
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	kSrv := khttp.NewServer(khttp.Listener(lis))
	registerPreflightRoute(kSrv, pf)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- kSrv.Start(ctx) }()
	srv := chatTestSrv{
		base: "http://" + lis.Addr().String(),
		stop: func() {
			cancel()
			_ = kSrv.Stop(context.Background())
			<-done
		},
	}
	defer srv.stop()

	resp, raw := callJSON(t, srv.base, "POST", "/api/v1/live/preflight",
		map[string]any{"mode": "public-live", "persona_id": ""})
	mustStatus(t, resp, http.StatusOK, "/api/v1/live/preflight")
	var body struct {
		CheckID        string `json:"check_id"`
		PreflightToken string `json:"preflight_token"`
		Ready          bool   `json:"ready"`
		Mode           string `json:"mode"`
		ObservedAtMs   int64  `json:"observed_at_ms"`
		ExpiresAtMs    int64  `json:"expires_at_ms"`
		Checks         []struct {
			Code     string `json:"code"`
			Status   string `json:"status"`
			Blocking bool   `json:"blocking"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("契约解析: %v raw=%s", err, raw)
	}
	if body.CheckID == "" || body.PreflightToken == "" {
		t.Fatal("check_id/preflight_token 必须返回")
	}
	if body.Ready {
		t.Fatal("全 fake 装配下 public ready 必须为 false（§2.3 杜绝假 ready）")
	}
	if body.Mode != "public-live" {
		t.Fatalf("mode 回显: %s", body.Mode)
	}
	if body.ExpiresAtMs-body.ObservedAtMs != 30000 {
		t.Fatalf("30s 新鲜期: %d", body.ExpiresAtMs-body.ObservedAtMs)
	}
	seen := map[string]string{}
	for _, c := range body.Checks {
		seen[c.Code] = c.Status
	}
	if seen["action_sender"] != "fail" || seen["vidu"] != "fail" {
		t.Fatalf("fake 能力 public 必须 fail: %+v", seen)
	}

	// local-preview：fake 显式 warn，ready=true。
	resp2, raw2 := callJSON(t, srv.base, "POST", "/api/v1/live/preflight",
		map[string]any{"mode": "local-preview"})
	mustStatus(t, resp2, http.StatusOK, "preflight local")
	var body2 struct {
		Ready  bool `json:"ready"`
		Checks []struct {
			Code   string `json:"code"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(raw2, &body2); err != nil {
		t.Fatal(err)
	}
	if !body2.Ready {
		t.Fatal("local-preview fake 不阻断")
	}
	for _, c := range body2.Checks {
		if c.Code == "action_sender" && c.Status != "warn" {
			t.Fatalf("local fake 显式标注 warn: %+v", c)
		}
	}
}
