// Package archive 提供场次列表/详情/事件导出，以及成本台账的汇总视图。
//
// 蓝本事实：stage 复盘只读 engine.status（tokens/memory/credits/diamonds/gifts），
// 事件源是 bridge 的 /api/events + WS；本包把场次记录、CostLedger、事件流三者拼成复盘数据。
// 导出字段全部来自已有存储，不做推算；credits/tokens 为实测，estimated_cost 显式标记估算。
package archive

import (
	"bytes"
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/teamwork/digital-human/internal/biz/avatar"
	"github.com/teamwork/digital-human/internal/biz/billing"
	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

var ErrNotFound = errors.New("场次不存在")

type Summary struct {
	live.SessionRecord
	Cost *billing.Ledger `json:"cost,omitempty"`
}

type Detail struct {
	Session        live.SessionRecord `json:"session"`
	Cost           *billing.Ledger    `json:"cost,omitempty"`
	Events         []eventbus.Event   `json:"events,omitempty"`
	EventN         int                `json:"event_count"`
	EventTruncated bool               `json:"event_truncated"`
	// Persona 详情页人设展示（M5 A20）：快照优先（冻结名），无快照旧场次回落
	// ProfileResolver 读当前档案；档案已软删时 Deleted=true + 仅 ID（前端空态）。
	Persona *PersonaBrief `json:"persona,omitempty"`
}

// PersonaBrief 详情页人设最小视图；Deleted=true 表示档案已软删（空态语义）。
type PersonaBrief struct {
	ID      string `json:"id"`
	Name    string `json:"name,omitempty"`
	Deleted bool   `json:"deleted,omitempty"`
}

type Service struct {
	store    live.SessionStore
	mgr      *live.Manager
	billing  *billing.Service
	resolver avatar.ProfileResolver
}

func NewService(store live.SessionStore, mgr *live.Manager, bill *billing.Service) *Service {
	return &Service{store: store, mgr: mgr, billing: bill}
}

// SetPersonaResolver 注入档案解析器（A20 回落链路）；nil 时无快照旧场次仅回 persona_id。
func (s *Service) SetPersonaResolver(r avatar.ProfileResolver) {
	s.resolver = r
}

// List 按开始时间倒序分页；limit<=0 取 20，最大 200。
func (s *Service) List(ctx context.Context, limit, offset int, phase string) ([]Summary, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 200 {
		limit = 200
	}
	rows, err := s.store.List(ctx, limit, offset, strings.TrimSpace(phase))
	if err != nil {
		return nil, err
	}
	out := make([]Summary, 0, len(rows))
	for _, r := range rows {
		item := Summary{SessionRecord: r}
		if s.billing != nil {
			if l, ok, err := s.billing.Get(ctx, r.ID); err == nil && ok {
				cp := l
				item.Cost = &cp
			}
		}
		out = append(out, item)
	}
	return out, nil
}

// Detail 取单场详情；includeEvents=false 时不拉事件（列表页性能）。
func (s *Service) Detail(ctx context.Context, id string, includeEvents bool) (Detail, error) {
	rec, ok, err := s.store.GetRecord(ctx, id)
	if err != nil {
		return Detail{}, err
	}
	if !ok {
		return Detail{}, ErrNotFound
	}
	out := Detail{Session: rec, Persona: s.personaBrief(ctx, rec)}
	if s.billing != nil {
		if l, ok, err := s.billing.Get(ctx, id); err == nil && ok {
			cp := l
			out.Cost = &cp
		}
	}
	if includeEvents {
		ev, truncated, err := s.fetchEvents(ctx, id)
		if err != nil {
			return Detail{}, err
		}
		out.Events = ev
		out.EventN = len(ev)
		out.EventTruncated = truncated
	}
	return out, nil
}

// personaBrief 详情页人设来源决策（A20）：快照 persona_name 存在 → 冻结名（快照语义，
// 不随档案后续改动漂移）；否则回落 ProfileResolver 读当前档案——ErrNotFound（含软删）
// → Deleted=true 空态；其他读取错误仅回 ID，不阻断详情主流程。persona_id 为空 → nil。
func (s *Service) personaBrief(ctx context.Context, rec live.SessionRecord) *PersonaBrief {
	pid := strings.TrimSpace(rec.PersonaID)
	if pid == "" {
		return nil
	}
	if name, ok := rec.ConfigSnapshot["persona_name"].(string); ok && name != "" {
		return &PersonaBrief{ID: pid, Name: name}
	}
	if s.resolver == nil {
		return &PersonaBrief{ID: pid}
	}
	prof, err := s.resolver.Resolve(ctx, pid)
	switch {
	case err == nil:
		return &PersonaBrief{ID: pid, Name: prof.PersonaName}
	case errors.Is(err, avatar.ErrNotFound):
		return &PersonaBrief{ID: pid, Deleted: true}
	default:
		return &PersonaBrief{ID: pid}
	}
}

// Count 返回符合 phase 过滤的场次总数（分页 total）。
func (s *Service) Count(ctx context.Context, phase string) (int, error) {
	return s.store.Count(ctx, strings.TrimSpace(phase))
}

// fetchEvents 最多拉取 20 页（10000 条）；达到上限时 truncated=true，绝不静默截断。
func (s *Service) fetchEvents(ctx context.Context, id string) ([]eventbus.Event, bool, error) {
	if s.mgr == nil {
		return []eventbus.Event{}, false, nil
	}
	const page = 500
	out := []eventbus.Event{}
	after := int64(0)
	for i := 0; i < 20; i++ {
		rows, err := s.mgr.ListEvents(ctx, id, after, page)
		if err != nil {
			return nil, false, err
		}
		out = append(out, rows...)
		if len(rows) < page {
			return out, false, nil
		}
		after = rows[len(rows)-1].Seq
	}
	return out, true, nil
}

// Export 导出单场事件：format=json|csv；返回文件字节与建议文件名。
func (s *Service) Export(ctx context.Context, id, format string) ([]byte, string, string, error) {
	rec, ok, err := s.store.GetRecord(ctx, id)
	if err != nil {
		return nil, "", "", err
	}
	if !ok {
		return nil, "", "", ErrNotFound
	}
	ev, truncated, err := s.fetchEvents(ctx, id)
	if err != nil {
		return nil, "", "", err
	}
	var cost *billing.Ledger
	if s.billing != nil {
		if l, ok, err := s.billing.Get(ctx, id); err == nil && ok {
			cost = &l
		}
	}
	if strings.EqualFold(format, "csv") {
		raw, err := eventsCSV(ev, truncated)
		if err != nil {
			return nil, "", "", err
		}
		return raw, "session-" + id + "-events.csv", "text/csv; charset=utf-8", nil
	}
	doc := map[string]any{
		"session":        rec,
		"cost":           cost,
		"events":         ev,
		"event_count":    len(ev),
		"truncated":      truncated,
		"exported_at_ms": time.Now().UnixMilli(),
		"format_version": 1,
	}
	raw, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		return nil, "", "", err
	}
	return raw, "session-" + id + "-events.json", "application/json; charset=utf-8", nil
}

func eventsCSV(ev []eventbus.Event, truncated bool) ([]byte, error) {
	var buf bytes.Buffer
	w := csv.NewWriter(&buf)
	if err := w.Write([]string{"seq", "ts_ms", "event_id", "type", "request_id", "epoch", "payload_json"}); err != nil {
		return nil, err
	}
	for _, e := range ev {
		payload, err := json.Marshal(e.Payload)
		if err != nil {
			return nil, err
		}
		row := []string{
			strconv.FormatInt(e.Seq, 10),
			strconv.FormatInt(e.TsMs, 10),
			csvSafe(e.EventID), csvSafe(e.Type), csvSafe(e.RequestID), strconv.FormatInt(e.Epoch, 10), csvSafe(string(payload)),
		}
		if err := w.Write(row); err != nil {
			return nil, err
		}
	}
	if truncated {
		// 达到事件上限时显式标记，绝不静默截断。
		if err := w.Write([]string{"# truncated=true"}); err != nil {
			return nil, err
		}
	}
	w.Flush()
	if err := w.Error(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// csvSafe 防公式注入：以 = + - @ 开头的单元格加 ' 前缀（Excel/WPS 会按公式解析这些前缀）。
func csvSafe(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@':
		return "'" + s
	}
	return s
}
