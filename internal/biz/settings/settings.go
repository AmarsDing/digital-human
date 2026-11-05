package settings

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
)

// 哨兵错误：供服务层把校验失败/revision 冲突映射为 409。
var (
	ErrRevisionConflict = errors.New("settings_revision_conflict")
	ErrValidation       = errors.New("settings_validation_failed")
)

// 13 组设置，顺序与 stage 设置抽屉一致；effective 决定保存后何时生效。
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
			"idle_timeout_s": 300, "max_duration_s": 1800, "greeting_audio_file": "",
		}},
		{ID: "voice", Title: "声音", Effective: "immediate", Values: map[string]any{
			"voice_id": "", "speed": 1.0,
		}},
		{ID: "member", Title: "专属会员", Effective: "immediate", Values: map[string]any{
			"enabled": true, "threshold_diamond": 99, "cooldown_s": 30, "once_per_session": true,
			// F28 贵宾欢迎模板：{nick}/{item} 占位（蓝本 gift_vip.template 等价物）；
			// forward_joins：贵宾进场欢迎（G24）；welcome_template：{nick}/{note} 占位（蓝本 patrons 欢迎）。
			"template":      "{nick}，欢迎光临！",
			"forward_joins": true, "welcome_template": "诶，{nick}来啦，欢迎欢迎。",
		}},
		{ID: "persona", Title: "人设", Effective: "next_session", Values: map[string]any{
			"active_id": "",
		}},
		// F27 礼物合并播报：键与默认值全量对齐蓝本 config.example.json:99-115 gift_batch。
		{ID: "gift", Title: "礼物合并播报", Effective: "immediate", Values: map[string]any{
			"enabled": true, "window_s": 6, "flush_people": 4, "flush_diamond": 50,
			"max_names": 4, "min_interval_s": 25, "urgent_min_interval_s": 10,
			"max_per_minute": 2, "max_items": 12,
			"tier_medium_diamond": 50, "tier_large_diamond": 200,
			"tier_small_template": "{thanks}", "tier_medium_template": "{thanks}", "tier_large_template": "{thanks}",
			"tier_small_action": "", "tier_medium_action": "", "tier_large_action": "",
		}},
		{ID: "llm", Title: "LLM", Effective: "next_session", Values: map[string]any{
			"base_url": "https://api.deepseek.com", "api_key": "", "model": "", "temperature": 0.7,
			"fallback_models": []any{}, "system": "",
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
			// read_interval_ms 默认对齐蓝本 engine.py _danmaku_worker 的 20000（原为 3000）；
			// vip_interval_ms 对齐蓝本 vip 快通道（蓝本 vip_min_interval_ms 默认 5000，本端默认 2000 沿用前端 schema）。
			"mode": "off", "read_interval_ms": 20000, "vip_interval_ms": 2000, "block_words": "",
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

// Service 负责 13 组校验与生效时机矩阵。
type Service struct {
	store Store
}

func NewService(store Store) *Service { return &Service{store: store} }

// Get 返回设置组；llm.api_key 按 vidu_key active_tail8 同款脱敏：
// 明文不回显，仅派生 api_key_tail8（尾 8 位）。两个 Store 实现的 Load 均返回
// 全新副本，脱敏原地进行不会污染存储态。内部取明文走 Service.Value("llm","api_key")。
func (s *Service) Get(ctx context.Context) ([]Group, int64, error) {
	groups, rev, err := s.store.Load(ctx)
	if err != nil {
		return nil, 0, err
	}
	scrubLLMKey(groups)
	return groups, rev, nil
}

func scrubLLMKey(groups []Group) {
	for i := range groups {
		if groups[i].ID != "llm" {
			continue
		}
		key, _ := groups[i].Values["api_key"].(string)
		groups[i].Values["api_key"] = ""
		tail := ""
		if key != "" {
			if r := []rune(key); len(r) > 8 {
				tail = string(r[len(r)-8:])
			} else {
				tail = key
			}
		}
		groups[i].Values["api_key_tail8"] = tail
	}
}

// preserveLLMKey 空 api_key 视为未修改：回种当前存储值，防止前端拿着脱显结果
// 原样回存把 key 冲掉；api_key_tail8 是派生字段，不持久化。
func preserveLLMKey(current, incoming []Group) {
	stored := ""
	for _, g := range current {
		if g.ID == "llm" {
			stored, _ = g.Values["api_key"].(string)
		}
	}
	for i := range incoming {
		if incoming[i].ID != "llm" {
			continue
		}
		delete(incoming[i].Values, "api_key_tail8")
		if v, _ := incoming[i].Values["api_key"].(string); v == "" && stored != "" {
			incoming[i].Values["api_key"] = stored
		}
	}
}

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
			if groupID == "llm" {
				if k == "api_key_tail8" {
					continue // 派生字段不持久化
				}
				if k == "api_key" {
					if s, _ := v.(string); s == "" {
						continue // 空 key 视为未修改
					}
				}
			}
			if isDeadKey(groupID, k) {
				slog.Default().Warn("settings: 忽略无消费方死键", "group", groupID, "key", k)
				continue
			}
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
		return 0, nil, fmt.Errorf("%w: 必须提交全部 13 组设置", ErrValidation)
	}
	cur, _, err := s.store.Load(ctx)
	if err != nil {
		return 0, nil, err
	}
	stripDeadKeys(groups)
	preserveLLMKey(cur, groups)
	rev, err := s.store.Save(ctx, groups, base)
	return rev, reload, err
}

// deadKeys：persona.content 后端零消费方，PUT/Patch 写入一律剥离并告警。
// llm.system 是 G05 的第二 prompt 来源，不在此列。
var deadKeys = map[string][]string{
	"persona": {"content"},
}

func stripDeadKeys(groups []Group) {
	for i := range groups {
		keys, ok := deadKeys[groups[i].ID]
		if !ok {
			continue
		}
		for _, k := range keys {
			if _, has := groups[i].Values[k]; has {
				delete(groups[i].Values, k)
				slog.Default().Warn("settings: 剥离无消费方死键", "group", groups[i].ID, "key", k)
			}
		}
	}
}

func isDeadKey(groupID, key string) bool {
	for _, k := range deadKeys[groupID] {
		if k == key {
			return true
		}
	}
	return false
}

// Validate 校验 13 组业务约束（目前是 danmaku.mode 取值），供存储实现复用。
func Validate(groups []Group) error {
	for _, g := range groups {
		if g.ID == "danmaku" {
			m, _ := g.Values["mode"].(string)
			switch m {
			case "off", "confirm", "auto", "read_all", "dm_anchor":
			default:
				return errors.New("danmaku.mode 必须是 off/confirm/auto/read_all/dm_anchor")
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
