package server

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/contentpkg"
	"github.com/teamwork/digital-human/internal/biz/live"
)

func newDraftRouteService() (*contentpkg.Service, *contentpkg.MemoryStore) {
	store := contentpkg.NewMemoryStore()
	return contentpkg.NewServiceWithFileStores(store, map[string]contentpkg.FileStore{
		"koubo": contentpkg.NewMemoryFileStore(), "choreo": contentpkg.NewMemoryFileStore(), "refs": contentpkg.NewMemoryFileStore(),
	}), store
}

func createOwnedDraft(t *testing.T, svc *contentpkg.Service, ownerID, title string) contentpkg.Draft {
	t.Helper()
	d, err := svc.CreateDraft(context.Background(), "tpl-"+title, 1, 1, map[string]any{"title": title})
	if err != nil {
		t.Fatal(err)
	}
	d, err = svc.OpenEdit(context.Background(), d.ID, ownerID, time.Hour)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func decodeDraftResponse(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("响应不是 JSON: %s: %v", raw, err)
	}
	return body
}

func TestDraftHTTPListAndGetEnforceOwner(t *testing.T) {
	svc, _ := newDraftRouteService()
	committed := createOwnedDraft(t, svc, "owner-1", "首场直播")
	if _, err := svc.Apply(context.Background(), committed.ID, committed.OwnerID, committed.FencingToken, committed.Revision, "pf-route-list"); err != nil {
		t.Fatal(err)
	}
	createOwnedDraft(t, svc, "owner-2", "他人草稿")
	base := startInteractionRouteServer(t, PanelDeps{Contentpkg: svc})

	resp, raw := callJSON(t, base, http.MethodGet, "/api/v1/live-drafts?owner_id=owner-1", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d body=%s", resp.StatusCode, raw)
	}
	body := decodeDraftResponse(t, raw)
	items, ok := body["drafts"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("owner-1 只能看到自己的草稿: %s", raw)
	}
	item := items[0].(map[string]any)
	if item["draft_id"] != committed.ID || item["committed"] != true || item["summary"] == "" || item["revision"].(float64) <= 0 {
		t.Fatalf("列表项不足以选择并预检: %s", raw)
	}

	resp, raw = callJSON(t, base, http.MethodGet, "/api/v1/live-drafts?owner_id=owner-2", nil)
	body = decodeDraftResponse(t, raw)
	items, ok = body["drafts"].([]any)
	if resp.StatusCode != http.StatusOK || !ok || len(items) != 1 || items[0].(map[string]any)["draft_id"] == committed.ID {
		t.Fatalf("列表不得跨 owner: status=%d body=%s", resp.StatusCode, raw)
	}

	path := "/api/v1/live-drafts/" + committed.ID + "?owner_id=owner-1"
	resp, raw = callJSON(t, base, http.MethodGet, path, nil)
	body = decodeDraftResponse(t, raw)
	detail, ok := body["draft"].(map[string]any)
	if resp.StatusCode != http.StatusOK || !ok || detail["snapshot"].(map[string]any)["title"] != "首场直播" || detail["fencing_token"].(float64) <= 0 {
		t.Fatalf("详情必须返回快照和 apply 所需租约: status=%d body=%s", resp.StatusCode, raw)
	}

	for _, path := range []string{
		"/api/v1/live-drafts/" + committed.ID + "?owner_id=owner-2",
		"/api/v1/live-drafts/missing?owner_id=owner-1",
	} {
		resp, raw = callJSON(t, base, http.MethodGet, path, nil)
		body = decodeDraftResponse(t, raw)
		if resp.StatusCode != http.StatusNotFound || body["error"] != "draft_not_found" {
			t.Fatalf("越权与不存在统一 404: path=%s status=%d body=%s", path, resp.StatusCode, raw)
		}
	}

	resp, raw = callJSON(t, base, http.MethodGet, "/api/v1/live-drafts", nil)
	if resp.StatusCode != http.StatusBadRequest || decodeDraftResponse(t, raw)["error"] != "owner_id_required" {
		t.Fatalf("缺 owner_id 必须 400: status=%d body=%s", resp.StatusCode, raw)
	}
}

func TestDraftHTTPApplyValidationAndConflicts(t *testing.T) {
	svc, _ := newDraftRouteService()
	d := createOwnedDraft(t, svc, "owner-1", "待提交")
	base := startInteractionRouteServer(t, PanelDeps{Contentpkg: svc})
	path := "/api/v1/live-drafts/" + d.ID + "/apply"
	valid := map[string]any{
		"owner_id": d.OwnerID, "fencing_token": d.FencingToken,
		"expected_revision": d.Revision, "preflight_id": "pf-apply-contract",
	}

	resp, raw := callJSON(t, base, http.MethodPost, path, "not-an-object")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 JSON 必须 400: status=%d body=%s", resp.StatusCode, raw)
	}
	resp, raw = callJSON(t, base, http.MethodPost, path, map[string]any{})
	if resp.StatusCode != http.StatusBadRequest || decodeDraftResponse(t, raw)["error"] != "invalid_apply_request" {
		t.Fatalf("缺参数必须 400: status=%d body=%s", resp.StatusCode, raw)
	}

	resp, raw = callJSON(t, base, http.MethodPost, "/api/v1/live-drafts/missing/apply", valid)
	if resp.StatusCode != http.StatusNotFound || decodeDraftResponse(t, raw)["error"] != "draft_not_found" {
		t.Fatalf("不存在草稿必须 404: status=%d body=%s", resp.StatusCode, raw)
	}

	for _, tc := range []struct {
		name   string
		mutate func(map[string]any)
		code   string
	}{
		{name: "revision", mutate: func(b map[string]any) { b["expected_revision"] = d.Revision + 1 }, code: "draft_revision_conflict"},
		{name: "fencing", mutate: func(b map[string]any) { b["fencing_token"] = d.FencingToken + 1 }, code: "draft_fencing_conflict"},
		{name: "owner", mutate: func(b map[string]any) { b["owner_id"] = "owner-2" }, code: "draft_owner_conflict"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := map[string]any{}
			for k, v := range valid {
				body[k] = v
			}
			tc.mutate(body)
			resp, raw := callJSON(t, base, http.MethodPost, path, body)
			if resp.StatusCode != http.StatusConflict || decodeDraftResponse(t, raw)["error"] != tc.code {
				t.Fatalf("冲突必须可识别: status=%d body=%s want=%s", resp.StatusCode, raw, tc.code)
			}
		})
	}

	resp, raw = callJSON(t, base, http.MethodPost, path, valid)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("正常 apply: status=%d body=%s", resp.StatusCode, raw)
	}
	resp, raw = callJSON(t, base, http.MethodPost, path, valid)
	if resp.StatusCode != http.StatusConflict || decodeDraftResponse(t, raw)["error"] != "draft_not_editable" {
		t.Fatalf("已提交草稿不得重复 apply: status=%d body=%s", resp.StatusCode, raw)
	}
}

func TestPreflightDraftRequiredProvidesStateSpecificNextAction(t *testing.T) {
	for _, tc := range []struct {
		name       string
		prepare    func(*testing.T, *contentpkg.Service) string
		ownerID    string
		action     string
		reason     string
		wantDraft  bool
		applyRoute bool
	}{
		{
			name: "committed candidates",
			prepare: func(t *testing.T, svc *contentpkg.Service) string {
				d := createOwnedDraft(t, svc, "owner-1", "已提交")
				if _, err := svc.Apply(context.Background(), d.ID, d.OwnerID, d.FencingToken, d.Revision, "pf-guidance"); err != nil {
					t.Fatal(err)
				}
				return d.ID
			},
			ownerID: "owner-1", action: "select_draft", reason: "committed_drafts_available", wantDraft: true,
		},
		{
			name: "uncommitted candidate",
			prepare: func(t *testing.T, svc *contentpkg.Service) string {
				return createOwnedDraft(t, svc, "owner-1", "未提交").ID
			},
			ownerID: "owner-1", action: "apply_draft", reason: "draft_not_committed", wantDraft: true, applyRoute: true,
		},
		{
			name: "no drafts", prepare: func(*testing.T, *contentpkg.Service) string { return "" },
			ownerID: "owner-1", action: "create_draft", reason: "no_drafts",
		},
		{
			name: "owner required", prepare: func(*testing.T, *contentpkg.Service) string { return "" },
			action: "provide_owner_id", reason: "owner_required",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc, store := newDraftRouteService()
			draftID := tc.prepare(t, svc)
			pf := live.NewPreflight(nil, nil, nil, nil)
			pf.SetDraftSource(store)
			f := startPreflightContractHTTP(t, nil, pf)
			defer f.stop()

			request := map[string]any{"mode": "public-live"}
			if tc.ownerID != "" {
				request["owner_id"] = tc.ownerID
			}
			resp, body := postLive(t, f.base, "/api/v1/live/preflight", "", request)
			if resp.StatusCode != http.StatusConflict || body["reason"] != "draft_required" {
				t.Fatalf("draft_required: status=%d body=%v", resp.StatusCode, body)
			}
			next, ok := body["next_action"].(map[string]any)
			if !ok || next["action"] != tc.action || next["reason"] != tc.reason || next["message"] == "" {
				t.Fatalf("下一步必须明确: %v", body)
			}
			if tc.wantDraft {
				candidates, ok := next["candidates"].([]any)
				if !ok || len(candidates) != 1 {
					t.Fatalf("候选不得为空或占位: %v", next)
				}
				candidate := candidates[0].(map[string]any)
				if candidate["draft_id"] != draftID || candidate["revision"].(float64) <= 0 || candidate["summary"] == "" {
					t.Fatalf("候选字段不完整: %v", candidate)
				}
			}
			if tc.applyRoute && !strings.Contains(next["path"].(string), "/api/v1/live-drafts/"+draftID+"/apply") {
				t.Fatalf("未提交草稿必须指向真实 apply 路径: %v", next)
			}
		})
	}
}

func TestStartDraftRequiredProvidesAuthenticatedListSource(t *testing.T) {
	f := publicPreflightContractHTTP(t, nil)
	defer f.stop()
	resp, body := postLive(t, f.base, "/api/v1/live/start", "", map[string]any{"mode": "public-live"})
	if resp.StatusCode != http.StatusConflict || body["reason"] != "draft_required" {
		t.Fatalf("Start draft_required: status=%d body=%v", resp.StatusCode, body)
	}
	metadata, ok := body["metadata"].(map[string]any)
	if !ok || metadata["next_action"] != "list_drafts" || metadata["method"] != http.MethodGet || metadata["path"] != "/api/v1/live-drafts" || metadata["owner_id_required"] != "true" {
		t.Fatalf("Start 必须给出带 owner 的候选来源: %v", body)
	}
	if _, err := url.Parse(metadata["path"].(string)); err != nil {
		t.Fatalf("候选来源路径非法: %v", err)
	}
}
