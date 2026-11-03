package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"strings"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/recording"
	"github.com/teamwork/digital-human/internal/biz/refs"
	"github.com/teamwork/digital-human/internal/biz/settings"
	"github.com/teamwork/digital-human/internal/biz/vidukeys"
)

func jsonContains(raw []byte, sub string) bool { return strings.Contains(string(raw), sub) }

// recordFakeClient 让 Supervisor 保持 enabled；轮询立即报错以快速结束 goroutine。
type recordFakeClient struct{}

func (recordFakeClient) RecordingStatus(context.Context, string) (recording.Status, error) {
	return recording.Status{}, errors.New("fake: no upstream")
}

func testRefPNG(t *testing.T) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, 8, 8))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newTestRefsService(t *testing.T) *refs.Service {
	t.Helper()
	s := refs.NewService(refs.NewFakeSignaler(), nil)
	s.SetDir(t.TempDir())
	return s
}

// ---- F34/N2 录播路由（蓝本 bridge.py:408-434） ----

func recordServer(t *testing.T, rec *recording.Supervisor) chatTestSrv {
	t.Helper()
	st := settings.NewService(settings.NewMemoryStore())
	return newBareDepsServer(t, PanelDeps{Settings: st, Recording: rec})
}

func TestRecordRouteToggleAndState(t *testing.T) {
	srv := recordServer(t, nil) // dev：无 Supervisor
	defer srv.stop()

	// GET：默认关 + 未装配 → unavailable。
	resp, raw := callJSON(t, srv.base, "GET", "/api/v1/record", nil)
	mustStatus(t, resp, http.StatusOK, "/api/v1/record")
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["enabled"] != false || body["state"] != recording.StateUnavailable {
		t.Fatalf("body=%v", body)
	}

	// POST 开 → 保存开关，note 提示下一场生效（蓝本语义）。
	resp, raw = callJSON(t, srv.base, "POST", "/api/v1/record", map[string]any{"enabled": true})
	mustStatus(t, resp, http.StatusOK, "/api/v1/record")
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["ok"] != true || body["enabled"] != true || body["note"] == "" {
		t.Fatalf("body=%v", body)
	}

	// GET 复验：enabled=true（持久化开关）。
	resp, raw = callJSON(t, srv.base, "GET", "/api/v1/record", nil)
	mustStatus(t, resp, http.StatusOK, "/api/v1/record")
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["enabled"] != true {
		t.Fatalf("enabled 应持久化, body=%v", body)
	}

	// fetch：dev 无 Supervisor → fail-closed 503。
	resp, raw = callJSON(t, srv.base, "POST", "/api/v1/record/fetch", map[string]any{})
	mustStatus(t, resp, http.StatusServiceUnavailable, "/api/v1/record/fetch")
	if !jsonContains(raw, "recording_unavailable") {
		t.Fatalf("body=%s", raw)
	}
}

func TestRecordFetchOK(t *testing.T) {
	// enabled 的 Supervisor（fake client 返回 error 让 goroutine 立即失败退出，不影响响应）。
	rec := recording.New(true, recordFakeClient{}, nil)
	srv := recordServer(t, rec)
	defer srv.stop()
	resp, raw := callJSON(t, srv.base, "POST", "/api/v1/record/fetch", map[string]any{"live_id": "L1"})
	mustStatus(t, resp, http.StatusOK, "/api/v1/record/fetch")
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["ok"] != true || body["live_id"] != "L1" || body["state"] != recording.StateProcessing {
		t.Fatalf("body=%v", body)
	}
	// GET 状态反映 Supervisor 运行态（processing）。
	resp, raw = callJSON(t, srv.base, "GET", "/api/v1/record", nil)
	mustStatus(t, resp, http.StatusOK, "/api/v1/record")
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["state"] == recording.StateUnavailable {
		t.Fatalf("Supervisor 装配后 state 不应是 unavailable, body=%v", body)
	}
}

// ---- F08/F36/G03/N3 额度路由（蓝本 bridge.py:1508） ----

func TestCreditsRoute(t *testing.T) {
	// 无凭据 → 200 明确空态 configured=false。
	srv := newBareDepsServer(t, PanelDeps{})
	defer srv.stop()
	resp, raw := callJSON(t, srv.base, "GET", "/api/v1/credits", nil)
	mustStatus(t, resp, http.StatusOK, "/api/v1/credits")
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["ok"] != true || body["configured"] != false {
		t.Fatalf("body=%v", body)
	}

	// 有凭据 → 透传 Vidu 原始字段 + remain 归一。
	srv2 := newBareDepsServer(t, PanelDeps{
		Credits: func(context.Context) (map[string]any, error) {
			return map[string]any{"remain_credit": 123, "total_credit": 500}, nil
		},
	})
	defer srv2.stop()
	resp, raw = callJSON(t, srv2.base, "GET", "/api/v1/credits", nil)
	mustStatus(t, resp, http.StatusOK, "/api/v1/credits")
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["configured"] != true || body["remain"] != float64(123) || body["total_credit"] != float64(500) {
		t.Fatalf("body=%v", body)
	}

	// Vidu 错误 → 502（对齐蓝本 RuntimeError→502）。
	srv3 := newBareDepsServer(t, PanelDeps{
		Credits: func(context.Context) (map[string]any, error) { return nil, errors.New("vidu down") },
	})
	defer srv3.stop()
	resp, raw = callJSON(t, srv3.base, "GET", "/api/v1/credits", nil)
	mustStatus(t, resp, http.StatusBadGateway, "/api/v1/credits")
	if !jsonContains(raw, "vidu down") {
		t.Fatalf("body=%s", raw)
	}
}

// ---- F42/G18 vidu/keys 路由（蓝本 bridge.py:1458,1476） ----

func TestViduKeysRoute(t *testing.T) {
	svc := vidukeys.NewService(vidukeys.NewMemoryStore(), nil)
	srv := newBareDepsServer(t, PanelDeps{ViduKeys: svc})
	defer srv.stop()

	// GET 空列表（ViduKeyCurrent nil → 不落库）。
	resp, raw := callJSON(t, srv.base, "GET", "/api/v1/vidu/keys", nil)
	mustStatus(t, resp, http.StatusOK, "/api/v1/vidu/keys")
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if body["ok"] != true || body["active"] != "" {
		t.Fatalf("body=%v", body)
	}

	// 非法 key（非 vda_ 前缀）→ 400 invalid_vidu_key（校验先于加密，不依赖 secrets）。
	resp, raw = callJSON(t, srv.base, "POST", "/api/v1/vidu/keys", map[string]any{"action": "add", "key": "sk-x"})
	mustStatus(t, resp, http.StatusBadRequest, "/api/v1/vidu/keys")
	if !jsonContains(raw, "invalid_vidu_key") {
		t.Fatalf("body=%s", raw)
	}

	// activate 不存在 → 404。
	resp, raw = callJSON(t, srv.base, "POST", "/api/v1/vidu/keys", map[string]any{"action": "activate", "key": "zzzzzzzz"})
	mustStatus(t, resp, http.StatusNotFound, "/api/v1/vidu/keys")
	if !jsonContains(raw, "vidu_key_not_found") {
		t.Fatalf("body=%s", raw)
	}

	// delete 不存在 → 404。
	resp, _ = callJSON(t, srv.base, "POST", "/api/v1/vidu/keys", map[string]any{"action": "delete", "key": "zzzzzzzz"})
	mustStatus(t, resp, http.StatusNotFound, "/api/v1/vidu/keys")

	// 未知 action → 400。
	resp, raw = callJSON(t, srv.base, "POST", "/api/v1/vidu/keys", map[string]any{"action": "wipe"})
	mustStatus(t, resp, http.StatusBadRequest, "/api/v1/vidu/keys")
	if !jsonContains(raw, "unknown_action") {
		t.Fatalf("body=%s", raw)
	}
}

// ---- F33 refs 文件列表/上传路由（蓝本 bridge.py:1348 + 上传扩展） ----

func TestRefsFilesRoute(t *testing.T) {
	srv := newBareDepsServer(t, PanelDeps{Refs: newTestRefsService(t)})
	defer srv.stop()

	// GET 列表（空目录 → 空）。
	resp, raw := callJSON(t, srv.base, "GET", "/api/v1/refs", nil)
	mustStatus(t, resp, http.StatusOK, "/api/v1/refs")
	if !jsonContains(raw, `"items":[]`) {
		t.Fatalf("body=%s", raw)
	}

	// 上传 png → 200；列表出现。
	resp, raw = uploadFile(t, srv.base, "/api/v1/refs/upload", "r.png", testRefPNG(t))
	mustStatus(t, resp, http.StatusOK, "/api/v1/refs/upload")
	if !jsonContains(raw, "r.png") {
		t.Fatalf("body=%s", raw)
	}
	resp, raw = callJSON(t, srv.base, "GET", "/api/v1/refs", nil)
	mustStatus(t, resp, http.StatusOK, "/api/v1/refs")
	if !jsonContains(raw, "r.png") {
		t.Fatalf("body=%s", raw)
	}

	// 上传非图片 → 400 unsupported_image_type。
	resp, raw = uploadFile(t, srv.base, "/api/v1/refs/upload", "x.png", []byte("not an image"))
	mustStatus(t, resp, http.StatusBadRequest, "/api/v1/refs/upload")
	if !jsonContains(raw, "unsupported_image_type") {
		t.Fatalf("body=%s", raw)
	}
}
