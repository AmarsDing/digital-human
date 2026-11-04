package server

import (
	"errors"
	"net/http"
	"strings"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"

	"github.com/teamwork/digital-human/internal/biz/contentpkg"
)

type draftListItem struct {
	DraftID     string `json:"draft_id"`
	Revision    int64  `json:"revision"`
	Status      string `json:"status"`
	Committed   bool   `json:"committed"`
	Summary     string `json:"summary"`
	UpdatedAtMs int64  `json:"updated_at_ms"`
}

type draftDetail struct {
	draftListItem
	TemplateID    string         `json:"template_id"`
	OwnerID       string         `json:"owner_id"`
	FencingToken  int64          `json:"fencing_token"`
	LeaseUntilMs  int64          `json:"lease_until_ms"`
	Snapshot      map[string]any `json:"snapshot"`
	SnapshotHash  string         `json:"snapshot_hash"`
	PreflightID   string         `json:"preflight_id,omitempty"`
	SchemaVersion int64          `json:"schema_version"`
}

func newDraftListItem(d contentpkg.Draft) draftListItem {
	return draftListItem{
		DraftID: d.ID, Revision: d.Revision, Status: d.Status,
		Committed: d.Status == contentpkg.DraftCommitted,
		Summary:   contentpkg.SummarizeDraft(d), UpdatedAtMs: d.UpdatedAtMs,
	}
}

func newDraftDetail(d contentpkg.Draft) draftDetail {
	return draftDetail{
		draftListItem: newDraftListItem(d), TemplateID: d.TemplateID, OwnerID: d.OwnerID,
		FencingToken: d.FencingToken, LeaseUntilMs: d.LeaseUntilMs,
		Snapshot: d.Snapshot, SnapshotHash: d.SnapshotHash,
		PreflightID: d.PreflightID, SchemaVersion: d.SchemaVersion,
	}
}

// registerDraftRoutes 增加草稿创建、编辑租约、按 owner_id 查询范围过滤的列表与详情；
// 提交仍复用 panels.go 中既有 apply 路由。
// owner_id 仅供多会话/多实例区分，不是安全边界；共享管理令牌持有者可传入任意值。
func registerDraftRoutes(srv *khttp.Server, svc *contentpkg.Service) {
	if svc == nil {
		return
	}
	r := srv.Route("/")
	r.PUT("/api/v1/live-drafts/{id}", func(c khttp.Context) error {
		var body struct {
			OwnerID          string         `json:"owner_id"`
			FencingToken     int64          `json:"fencing_token"`
			ExpectedRevision int64          `json:"expected_revision"`
			Snapshot         map[string]any `json:"snapshot"`
		}
		if err := decodeJSONBody(c, &body); err != nil || strings.TrimSpace(body.OwnerID) == "" || body.FencingToken <= 0 || body.ExpectedRevision <= 0 || body.Snapshot == nil {
			return c.JSON(http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_draft_request"})
		}
		d, err := svc.Save(c.Request().Context(), param(c, "id"), body.OwnerID, body.FencingToken, body.ExpectedRevision, body.Snapshot)
		if err != nil {
			return draftApplyError(c, err)
		}
		return c.JSON(http.StatusOK, map[string]any{"ok": true, "draft": newDraftDetail(d)})
	})
	r.POST("/api/v1/live-drafts", func(c khttp.Context) error {
		var body struct {
			TemplateID    string         `json:"template_id"`
			BaseRevision  int64          `json:"base_revision"`
			SchemaVersion int64          `json:"schema_version"`
			Snapshot      map[string]any `json:"snapshot"`
		}
		if err := decodeJSONBody(c, &body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_draft_request"})
		}
		body.TemplateID = strings.TrimSpace(body.TemplateID)
		if body.TemplateID == "" || body.BaseRevision <= 0 || body.SchemaVersion <= 0 || body.Snapshot == nil {
			return c.JSON(http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_draft_request"})
		}
		d, err := svc.CreateDraft(c.Request().Context(), body.TemplateID, body.BaseRevision, body.SchemaVersion, body.Snapshot)
		if err != nil {
			if errors.Is(err, contentpkg.ErrConflict) {
				return c.JSON(http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_draft_request"})
			}
			if errors.Is(err, contentpkg.ErrUnavailable) {
				return c.JSON(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "content_storage_unavailable"})
			}
			return internal(c, err)
		}
		return c.JSON(http.StatusOK, map[string]any{"ok": true, "draft": newDraftDetail(d)})
	})
	r.POST("/api/v1/live-drafts/{id}/open-edit", func(c khttp.Context) error {
		var body struct {
			OwnerID         string `json:"owner_id"`
			LeaseTTLSeconds *int64 `json:"lease_ttl_seconds"`
		}
		if err := decodeJSONBody(c, &body); err != nil {
			return c.JSON(http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_lease_request"})
		}
		body.OwnerID = strings.TrimSpace(body.OwnerID)
		ttlSeconds := int64(300)
		if body.LeaseTTLSeconds != nil {
			ttlSeconds = *body.LeaseTTLSeconds
		}
		if body.OwnerID == "" || ttlSeconds <= 0 || ttlSeconds > int64((time.Duration(1<<63-1))/time.Second) {
			return c.JSON(http.StatusBadRequest, map[string]any{"ok": false, "error": "invalid_lease_request"})
		}
		d, err := svc.OpenEdit(c.Request().Context(), param(c, "id"), body.OwnerID, time.Duration(ttlSeconds)*time.Second)
		if err != nil {
			return draftOpenEditError(c, err)
		}
		return c.JSON(http.StatusOK, map[string]any{"ok": true, "draft": newDraftDetail(d)})
	})
	r.GET("/api/v1/live-drafts", func(c khttp.Context) error {
		ownerID := strings.TrimSpace(c.Request().URL.Query().Get("owner_id"))
		if ownerID == "" {
			return c.JSON(http.StatusBadRequest, map[string]any{"ok": false, "error": "owner_id_required"})
		}
		drafts, err := svc.ListOwnedDrafts(c.Request().Context(), ownerID)
		if err != nil {
			return internal(c, err)
		}
		items := make([]draftListItem, 0, len(drafts))
		for _, d := range drafts {
			items = append(items, newDraftListItem(d))
		}
		return c.JSON(http.StatusOK, map[string]any{"ok": true, "drafts": items, "count": len(items)})
	})
	r.GET("/api/v1/live-drafts/{id}", func(c khttp.Context) error {
		ownerID := strings.TrimSpace(c.Request().URL.Query().Get("owner_id"))
		if ownerID == "" {
			return c.JSON(http.StatusBadRequest, map[string]any{"ok": false, "error": "owner_id_required"})
		}
		d, err := svc.GetOwnedDraft(c.Request().Context(), param(c, "id"), ownerID)
		if err != nil {
			if errors.Is(err, contentpkg.ErrDraftNotFound) {
				return c.JSON(http.StatusNotFound, map[string]any{"ok": false, "error": "draft_not_found"})
			}
			return internal(c, err)
		}
		detail := draftDetail{
			draftListItem: newDraftListItem(d), TemplateID: d.TemplateID, OwnerID: d.OwnerID,
			FencingToken: d.FencingToken, LeaseUntilMs: d.LeaseUntilMs,
			Snapshot: d.Snapshot, SnapshotHash: d.SnapshotHash,
			PreflightID: d.PreflightID, SchemaVersion: d.SchemaVersion,
		}
		return c.JSON(http.StatusOK, map[string]any{"ok": true, "draft": detail})
	})
}

func draftOpenEditError(c khttp.Context, err error) error {
	if errors.Is(err, contentpkg.ErrConflict) {
		code := "draft_lease_conflict"
		if strings.Contains(err.Error(), "not editable") {
			code = "draft_not_editable"
		}
		return c.JSON(http.StatusConflict, map[string]any{"ok": false, "error": code, "retry_action": "refresh_draft"})
	}
	return draftApplyError(c, err)
}

func draftApplyError(c khttp.Context, err error) error {
	switch {
	case errors.Is(err, contentpkg.ErrDraftNotFound):
		return c.JSON(http.StatusNotFound, map[string]any{"ok": false, "error": "draft_not_found"})
	case errors.Is(err, contentpkg.ErrStaleFencing):
		return c.JSON(http.StatusConflict, map[string]any{"ok": false, "error": "draft_fencing_conflict", "retry_action": "refresh_draft"})
	case errors.Is(err, contentpkg.ErrNotLeaseOwner):
		return c.JSON(http.StatusConflict, map[string]any{"ok": false, "error": "draft_owner_conflict", "retry_action": "refresh_draft"})
	case errors.Is(err, contentpkg.ErrConflict):
		code := "draft_apply_conflict"
		switch {
		case strings.Contains(err.Error(), "expected_revision"):
			code = "draft_revision_conflict"
		case strings.Contains(err.Error(), "not editable"):
			code = "draft_not_editable"
		case strings.Contains(err.Error(), "lease expired"):
			code = "draft_lease_expired"
		}
		return c.JSON(http.StatusConflict, map[string]any{"ok": false, "error": code, "retry_action": "refresh_draft"})
	case errors.Is(err, contentpkg.ErrUnavailable):
		return c.JSON(http.StatusServiceUnavailable, map[string]any{"ok": false, "error": "content_storage_unavailable"})
	default:
		return internal(c, err)
	}
}
