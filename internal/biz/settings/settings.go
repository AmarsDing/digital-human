package settings

import (
	"context"
	"errors"
	"fmt"
	"sync"
)

// 哨兵错误：供服务层把校验失败/revision 冲突映射为 409。
var (
	ErrRevisionConflict = errors.New("settings_revision_conflict")
	ErrValidation       = errors.New("settings_validation_failed")
)

// 12 组设置，顺序与 stage 设置抽屉一致；effective 决定保存后何时生效。
type Group struct {
	ID        string         `json:"id"`
	Title     string         `json:"title"`
	Values    map[string]any `json:"values"`
	Effective string         `json:"effective"` // immediate | next_session
}

// DefaultGroups 只定义产品默认值，不复制蓝本 config 内容。
func DefaultGroups() []Group {
	return []Group{
		{ID: "session", Title: "会话", Effective: "next_session", Values: map[string]any{
			"idle_timeout_s": 300, "max_duration_s": 1800,
		}},
		{ID: "voice", Title: "声音", Effective: "immediate", Values: map[string]any{
			"voice_id": "", "speed": 1.0,
		}},
		{ID: "member", Title: "专属会员", Effective: "next_session", Values: map[string]any{
			"enabled": true, "threshold_diamond": 99, "cooldown_s": 30, "once_per_session": true,
		}},
		{ID: "persona", Title: "人设", Effective: "next_session", Values: map[string]any{
			"active_id": "",
		}},
		{ID: "llm", Title: "LLM", Effective: "next_session", Values: map[string]any{
			"model": "", "temperature": 0.7,
		}},
		{ID: "rtc", Title: "自备 RTC", Effective: "next_session", Values: map[string]any{
			"mode": "managed", "provider": "artc",
		}},
		{ID: "action_library", Title: "动作库", Effective: "next_session", Values: map[string]any{
			"library_id": "",
		}},
		{ID: "assets", Title: "资产库", Effective: "next_session", Values: map[string]any{
			"avatar_id": "",
		}},
		{ID: "vidu_key", Title: "Vidu Key", Effective: "next_session", Values: map[string]any{
			"active_tail8": "",
		}},
		{ID: "captions", Title: "画面内字幕层", Effective: "immediate", Values: map[string]any{
			"output_sentences": true, "offset_px": 200, "font_size": 17, "max_lines": 2,
		}},
		{ID: "danmaku", Title: "弹幕主播模式", Effective: "immediate", Values: map[string]any{
			"mode": "off", "read_interval_ms": 3000,
		}},
		{ID: "misc", Title: "其他", Effective: "next_session", Values: map[string]any{
			"record_enabled": false, "lan_confirm": true,
		}},
	}
}

type Store interface {
	Load(ctx context.Context) ([]Group, int64, error)
	Save(ctx context.Context, groups []Group, baseRevision int64) (int64, error)
}

// MemoryStore 无 PG 时使用；进程重启即丢失（M0 明确接受）。
type MemoryStore struct {
	mu       sync.Mutex
	groups   []Group
	revision int64
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{groups: DefaultGroups(), revision: 1}
}

func (s *MemoryStore) Load(_ context.Context) ([]Group, int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clone(s.groups), s.revision, nil
}

func (s *MemoryStore) Save(_ context.Context, groups []Group, baseRevision int64) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if baseRevision != 0 && baseRevision != s.revision {
		return s.revision, fmt.Errorf("%w: base=%d current=%d", ErrRevisionConflict, baseRevision, s.revision)
	}
	if err := Validate(groups); err != nil {
		return s.revision, fmt.Errorf("%w: %v", ErrValidation, err)
	}
	s.groups = clone(groups)
	s.revision++
	return s.revision, nil
}

// Service 负责 12 组校验与生效时机矩阵。
type Service struct {
	store Store
}

func NewService(store Store) *Service { return &Service{store: store} }

func (s *Service) Get(ctx context.Context) ([]Group, int64, error) { return s.store.Load(ctx) }

// Patch 合并更新单组设置里的若干字段并立即落库；baseRevision=0 表示不做版本校验。
// 用于音色选择这类“即时生效”的窄写入，避免整组 12 项回传。
func (s *Service) Patch(ctx context.Context, groupID string, updates map[string]any) error {
	groups, rev, err := s.store.Load(ctx)
	if err != nil {
		return err
	}
	found := false
	for i := range groups {
		if groups[i].ID != groupID {
			continue
		}
		found = true
		if groups[i].Values == nil {
			groups[i].Values = map[string]any{}
		}
		for k, v := range updates {
			groups[i].Values[k] = v
		}
	}
	if !found {
		return errors.New("unknown settings group: " + groupID)
	}
	if err := Validate(groups); err != nil {
		return err
	}
	_, err = s.store.Save(ctx, groups, rev)
	return err
}

// Value 读取单组设置中的字段；不存在时返回 nil。
func (s *Service) Value(ctx context.Context, groupID, key string) any {
	groups, _, err := s.store.Load(ctx)
	if err != nil {
		return nil
	}
	for _, g := range groups {
		if g.ID == groupID {
			return g.Values[key]
		}
	}
	return nil
}

func (s *Service) Save(ctx context.Context, groups []Group, base int64) (int64, []string, error) {
	def := DefaultGroups()
	known := map[string]Group{}
	for _, g := range def {
		known[g.ID] = g
	}
	reload := make([]string, 0, len(groups))
	for i := range groups {
		g, ok := known[groups[i].ID]
		if !ok {
			return 0, nil, fmt.Errorf("%w: unknown settings group: %s", ErrValidation, groups[i].ID)
		}
		groups[i].Effective = g.Effective
		groups[i].Title = g.Title
		if g.Effective == "next_session" {
			reload = append(reload, g.ID)
		}
	}
	if len(groups) != len(def) {
		return 0, nil, fmt.Errorf("%w: 必须提交全部 12 组设置", ErrValidation)
	}
	rev, err := s.store.Save(ctx, groups, base)
	return rev, reload, err
}

// Validate 校验 12 组业务约束（目前是 danmaku.mode 取值），供存储实现复用。
func Validate(groups []Group) error {
	for _, g := range groups {
		if g.ID == "danmaku" {
			m, _ := g.Values["mode"].(string)
			if m != "off" && m != "confirm" && m != "auto" {
				return errors.New("danmaku.mode 必须是 off/confirm/auto")
			}
		}
	}
	return nil
}

func clone(in []Group) []Group {
	out := make([]Group, len(in))
	for i, g := range in {
		vals := make(map[string]any, len(g.Values))
		for k, v := range g.Values {
			vals[k] = v
		}
		g.Values = vals
		out[i] = g
	}
	return out
}
