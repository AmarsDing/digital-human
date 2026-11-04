package live

import (
	"context"
	"net/http"
	"strings"

	"github.com/teamwork/digital-human/internal/biz/contentpkg"
)

// DraftCandidate 是 draft_required 中可直接用于预检或 apply 的真实候选项。
type DraftCandidate struct {
	DraftID   string `json:"draft_id"`
	Revision  int64  `json:"revision"`
	Status    string `json:"status"`
	Committed bool   `json:"committed"`
	Summary   string `json:"summary"`
}

// DraftNextAction 给出可执行动作；无候选项的字段通过 omitempty 省略，不用空数组或空串占位。
type DraftNextAction struct {
	Action     string           `json:"action"`
	Reason     string           `json:"reason"`
	Message    string           `json:"message"`
	Method     string           `json:"method,omitempty"`
	Path       string           `json:"path,omitempty"`
	Candidates []DraftCandidate `json:"candidates,omitempty"`
}

// DraftRequiredGuidance 按 owner 的真实草稿状态区分选草稿、提交草稿与创建草稿。
func (p *Preflight) DraftRequiredGuidance(ctx context.Context, ownerID string) DraftNextAction {
	ownerID = strings.TrimSpace(ownerID)
	if ownerID == "" {
		return DraftNextAction{
			Action: "provide_owner_id", Reason: "owner_required",
			Message: "请在预检请求中携带 owner_id，以查询当前操作者可用的草稿",
			Method:  http.MethodPost, Path: "/api/v1/live/preflight",
		}
	}
	p.mu.Lock()
	drafts := p.drafts
	p.mu.Unlock()
	store, ok := drafts.(interface {
		ListDrafts(context.Context, string) ([]contentpkg.Draft, error)
	})
	if !ok {
		return DraftNextAction{
			Action: "retry_list_drafts", Reason: "draft_source_unavailable",
			Message: "草稿查询源未就绪，请检查服务配置后重试；当前不能公开开播",
			Method:  http.MethodGet, Path: "/api/v1/live-drafts",
		}
	}
	items, err := store.ListDrafts(ctx, ownerID)
	if err != nil {
		return DraftNextAction{
			Action: "retry_list_drafts", Reason: "draft_query_failed",
			Message: "草稿查询失败，请稍后重试；当前不能公开开播",
			Method:  http.MethodGet, Path: "/api/v1/live-drafts",
		}
	}
	if len(items) == 0 {
		return DraftNextAction{
			Action: "create_draft", Reason: "no_drafts",
			Message: "当前 owner 没有草稿，请先通过内容包编辑器创建并保存草稿，再打开编辑租约并提交",
		}
	}

	committed := make([]DraftCandidate, 0, len(items))
	editable := make([]DraftCandidate, 0, len(items))
	for _, d := range items {
		candidate := DraftCandidate{
			DraftID: d.ID, Revision: d.Revision, Status: d.Status,
			Committed: d.Status == contentpkg.DraftCommitted, Summary: contentpkg.SummarizeDraft(d),
		}
		switch d.Status {
		case contentpkg.DraftCommitted:
			committed = append(committed, candidate)
		case contentpkg.DraftEditing:
			editable = append(editable, candidate)
		}
	}
	if len(committed) > 0 {
		return DraftNextAction{
			Action: "select_draft", Reason: "committed_drafts_available",
			Message: "请选择已提交草稿，并将其 draft_id 与 revision 作为 expected_revision 重新预检",
			Method:  http.MethodPost, Path: "/api/v1/live/preflight", Candidates: committed,
		}
	}
	if len(editable) > 0 {
		return DraftNextAction{
			Action: "apply_draft", Reason: "draft_not_committed",
			Message: "草稿尚未提交，请先按详情中的 owner_id、fencing_token 与 revision 调用 apply",
			Method:  http.MethodPost, Path: "/api/v1/live-drafts/" + editable[0].DraftID + "/apply", Candidates: editable,
		}
	}
	return DraftNextAction{
		Action: "review_draft_status", Reason: "draft_not_ready",
		Message: "现有草稿正在提交、已回滚或需要人工处理，请先检查草稿状态；未提交成功的草稿不能公开开播",
		Method:  http.MethodGet, Path: "/api/v1/live-drafts",
	}
}
