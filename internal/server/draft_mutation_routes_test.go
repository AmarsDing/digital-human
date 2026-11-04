package server

import (
	"net/http"
	"testing"
)

func responseDraft(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	body := decodeDraftResponse(t, raw)
	draft, ok := body["draft"].(map[string]any)
	if !ok {
		t.Fatalf("missing draft in response: %s", raw)
	}
	return draft
}

func TestDraftHTTPSaveValidatesLeaseAndRevision(t *testing.T) {
	svc, _ := newDraftRouteService()
	base := startInteractionRouteServer(t, PanelDeps{Contentpkg: svc})

	resp, raw := callJSON(t, base, http.MethodPost, "/api/v1/live-drafts", map[string]any{
		"template_id": "tpl-save", "base_revision": 1, "schema_version": 1,
		"snapshot": map[string]any{"title": "保存前"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d body=%s", resp.StatusCode, raw)
	}
	draftID := responseDraft(t, raw)["draft_id"].(string)
	resp, raw = callJSON(t, base, http.MethodPost, "/api/v1/live-drafts/"+draftID+"/open-edit", map[string]any{
		"owner_id": "owner-1", "lease_ttl_seconds": 300,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open-edit status=%d body=%s", resp.StatusCode, raw)
	}
	lease := responseDraft(t, raw)
	savePath := "/api/v1/live-drafts/" + draftID
	snapshot := map[string]any{"title": "保存后"}

	resp, raw = callJSON(t, base, http.MethodPut, savePath, map[string]any{
		"owner_id": "owner-2", "fencing_token": lease["fencing_token"],
		"expected_revision": lease["revision"], "snapshot": snapshot,
	})
	if resp.StatusCode != http.StatusConflict || decodeDraftResponse(t, raw)["error"] != "draft_owner_conflict" {
		t.Fatalf("owner conflict status=%d body=%s", resp.StatusCode, raw)
	}

	resp, raw = callJSON(t, base, http.MethodPut, savePath, map[string]any{
		"owner_id": "owner-1", "fencing_token": lease["fencing_token"],
		"expected_revision": 2, "snapshot": snapshot,
	})
	if resp.StatusCode != http.StatusConflict || decodeDraftResponse(t, raw)["error"] != "draft_revision_conflict" {
		t.Fatalf("revision conflict status=%d body=%s", resp.StatusCode, raw)
	}

	resp, raw = callJSON(t, base, http.MethodPut, savePath, map[string]any{
		"owner_id": "owner-1", "fencing_token": lease["fencing_token"].(float64) + 1,
		"expected_revision": lease["revision"], "snapshot": snapshot,
	})
	if resp.StatusCode != http.StatusConflict || decodeDraftResponse(t, raw)["error"] != "draft_fencing_conflict" {
		t.Fatalf("fencing conflict status=%d body=%s", resp.StatusCode, raw)
	}

	resp, raw = callJSON(t, base, http.MethodPut, savePath, map[string]any{
		"owner_id": "owner-1", "fencing_token": lease["fencing_token"],
		"expected_revision": lease["revision"], "snapshot": snapshot,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("save status=%d body=%s", resp.StatusCode, raw)
	}
	saved := responseDraft(t, raw)
	if saved["revision"].(float64) != 2 || saved["snapshot_hash"] == lease["snapshot_hash"] || saved["committed"] != false {
		t.Fatalf("saved draft=%s", raw)
	}
	if saved["snapshot"].(map[string]any)["title"] != "保存后" {
		t.Fatalf("snapshot not saved: %s", raw)
	}

	resp, raw = callJSON(t, base, http.MethodGet, "/api/v1/live-drafts/"+draftID+"?owner_id=owner-1", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("get saved draft status=%d body=%s", resp.StatusCode, raw)
	}
	persisted := responseDraft(t, raw)
	if persisted["revision"].(float64) != 2 || persisted["snapshot"].(map[string]any)["title"] != "保存后" {
		t.Fatalf("saved draft not persisted: %s", raw)
	}
}

func TestDraftHTTPCreateOpenEditApplyFlow(t *testing.T) {
	svc, _ := newDraftRouteService()
	base := startInteractionRouteServer(t, PanelDeps{Contentpkg: svc})

	resp, raw := callJSON(t, base, http.MethodPost, "/api/v1/live-drafts", map[string]any{})
	if resp.StatusCode != http.StatusBadRequest || decodeDraftResponse(t, raw)["error"] != "invalid_draft_request" {
		t.Fatalf("invalid create status=%d body=%s", resp.StatusCode, raw)
	}

	resp, raw = callJSON(t, base, http.MethodPost, "/api/v1/live-drafts", map[string]any{
		"template_id": "tpl-1", "base_revision": 1, "schema_version": 1,
		"snapshot": map[string]any{"title": "真实链路"},
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("create status=%d body=%s", resp.StatusCode, raw)
	}
	created := responseDraft(t, raw)
	draftID, _ := created["draft_id"].(string)
	if draftID == "" || created["revision"].(float64) != 1 || created["committed"] != false {
		t.Fatalf("created draft=%s", raw)
	}

	openPath := "/api/v1/live-drafts/" + draftID + "/open-edit"
	resp, raw = callJSON(t, base, http.MethodPost, openPath, map[string]any{})
	if resp.StatusCode != http.StatusBadRequest || decodeDraftResponse(t, raw)["error"] != "invalid_lease_request" {
		t.Fatalf("invalid open-edit status=%d body=%s", resp.StatusCode, raw)
	}
	resp, raw = callJSON(t, base, http.MethodPost, "/api/v1/live-drafts/missing/open-edit", map[string]any{"owner_id": "owner-1"})
	if resp.StatusCode != http.StatusNotFound || decodeDraftResponse(t, raw)["error"] != "draft_not_found" {
		t.Fatalf("missing open-edit status=%d body=%s", resp.StatusCode, raw)
	}

	resp, raw = callJSON(t, base, http.MethodPost, openPath, map[string]any{"owner_id": "owner-1", "lease_ttl_seconds": 300})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("open-edit status=%d body=%s", resp.StatusCode, raw)
	}
	lease := responseDraft(t, raw)
	if lease["fencing_token"].(float64) <= 0 || lease["revision"] != created["revision"] {
		t.Fatalf("lease cannot be applied directly: %s", raw)
	}
	resp, raw = callJSON(t, base, http.MethodPost, openPath, map[string]any{"owner_id": "owner-2"})
	if resp.StatusCode != http.StatusConflict || decodeDraftResponse(t, raw)["error"] != "draft_lease_conflict" {
		t.Fatalf("conflicting open-edit status=%d body=%s", resp.StatusCode, raw)
	}

	resp, raw = callJSON(t, base, http.MethodPost, "/api/v1/live-drafts/"+draftID+"/apply", map[string]any{
		"owner_id": "owner-1", "fencing_token": lease["fencing_token"],
		"expected_revision": lease["revision"], "preflight_id": "pf-http-flow",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", resp.StatusCode, raw)
	}
	resp, raw = callJSON(t, base, http.MethodGet, "/api/v1/live-drafts?owner_id=owner-1", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d body=%s", resp.StatusCode, raw)
	}
	items := decodeDraftResponse(t, raw)["drafts"].([]any)
	item := items[0].(map[string]any)
	if len(items) != 1 || item["draft_id"] != draftID || item["committed"] != true {
		t.Fatalf("committed draft not selectable: %s", raw)
	}
}
