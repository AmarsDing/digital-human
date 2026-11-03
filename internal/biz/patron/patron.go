// Package patron 实现 F29 贵宾名单 + G24 进场欢迎（once_per_session 见 gift 包 F28 侧与本侧 welcomed 集合）。
//
// 蓝本对齐（cyber-xiaoya-live-main）：
//   - data/patrons.json 名单（nick/uid/alias/note/since），bridge.py:878-940 五路由；
//   - engine.py:1092-1179 welcome_patron：贵宾进场播欢迎语（模板可配，{nick}/{note} 占位）；
//   - once_per_session：同一场次同一贵宾只欢迎一次，换场次（OnSessionStart）清空 welcomed 集合。
//
// 持久化走 Ent（Store 接口 + repo 可切换实现），内存实现供测试与无 PG 场景。
package patron

import (
	"context"
	"strings"
	"sync"
	"time"
)

// Entry 一条贵宾名单（蓝本 patrons.json 行）。
type Entry struct {
	Nick  string `json:"nick"`
	UID   string `json:"uid,omitempty"`
	Alias string `json:"alias,omitempty"`
	Note  string `json:"note,omitempty"`
	Since string `json:"since,omitempty"` // YYYY-MM-DD
}

// Store 名单持久化端口（Ent / 内存可切换，同 persona 先例）。
type Store interface {
	List(ctx context.Context) ([]Entry, error)
	Replace(ctx context.Context, items []Entry) error
}

// MemoryStore 内存降级（无 PG 的 dev / 测试）。
type MemoryStore struct {
	mu    sync.Mutex
	items []Entry
}

func NewMemoryStore() *MemoryStore { return &MemoryStore{} }

func (s *MemoryStore) List(_ context.Context) ([]Entry, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Entry(nil), s.items...), nil
}

func (s *MemoryStore) Replace(_ context.Context, items []Entry) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = append([]Entry(nil), items...)
	return nil
}

// Speaker 是 live.Manager.Say 的窄接口（mode 由装配层固定为 "patron"）。
type Speaker interface {
	Say(ctx context.Context, text string) error
}

// SettingsProvider 读 member / danmaku 组键。
type SettingsProvider interface {
	Value(ctx context.Context, group, key string) any
}

// EventSink 事件总线。
type EventSink func(kind string, payload map[string]any)

// Service 贵宾名单与欢迎（F29/G24/F28/G25）。
type Service struct {
	store   Store
	sp      SettingsProvider
	speaker Speaker
	emit    EventSink

	mu        sync.Mutex
	cache     []Entry
	cacheAt   time.Time
	welcomed  map[string]bool // 本场已欢迎（once_per_session）
	sessionID string
	live      bool
	stats     Stats
}

// Stats 会话级计数（对齐蓝本 patrons.state {welcomed,count}）。
type Stats struct {
	Welcomed  int `json:"welcomed"`   // 本场进场欢迎次数
	SavedCnt  int `json:"saved_cnt"`  // 名单保存次数
	MemberCnt int `json:"member_cnt"` // 当前名单人数
}

func NewService(store Store, sp SettingsProvider, speaker Speaker, emit EventSink) *Service {
	if store == nil {
		store = NewMemoryStore()
	}
	return &Service{
		store:    store,
		sp:       sp,
		speaker:  speaker,
		emit:     emit,
		welcomed: map[string]bool{},
	}
}

// ---- 名单 CRUD（bridge.py:878-905）----

// List 全量名单（1s 缓存，保存后失效）。
func (s *Service) List(ctx context.Context) ([]Entry, error) {
	s.mu.Lock()
	if time.Since(s.cacheAt) < time.Second && s.cache != nil {
		out := append([]Entry(nil), s.cache...)
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()
	items, err := s.store.List(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cache = append([]Entry(nil), items...)
	s.cacheAt = time.Now()
	s.stats.MemberCnt = len(items)
	s.mu.Unlock()
	return items, nil
}

// IDs 返回 nicks + uids（bridge.py:884 /api/patrons/ids，阅读端本地匹配用）。
func (s *Service) IDs(ctx context.Context) (nicks, uids []string, err error) {
	items, err := s.List(ctx)
	if err != nil {
		return nil, nil, err
	}
	for _, e := range items {
		if e.Nick != "" {
			nicks = append(nicks, e.Nick)
		}
		if e.UID != "" {
			uids = append(uids, e.UID)
		}
	}
	return nicks, uids, nil
}

// Save 全量替换（bridge.py:891 /api/patrons/save）：nick 必填、去重（先出现优先）、trim。
func (s *Service) Save(ctx context.Context, items []Entry) (int, error) {
	seen := map[string]bool{}
	clean := make([]Entry, 0, len(items))
	for _, e := range items {
		e.Nick = strings.TrimSpace(e.Nick)
		e.UID = strings.TrimSpace(e.UID)
		e.Alias = strings.TrimSpace(e.Alias)
		e.Note = strings.TrimSpace(e.Note)
		e.Since = strings.TrimSpace(e.Since)
		if e.Nick == "" || seen[e.Nick] {
			continue
		}
		seen[e.Nick] = true
		clean = append(clean, e)
	}
	if err := s.store.Replace(ctx, clean); err != nil {
		return 0, err
	}
	s.mu.Lock()
	s.cache = nil
	s.stats.SavedCnt++
	s.stats.MemberCnt = len(clean)
	s.mu.Unlock()
	return len(clean), nil
}

// IsPatron 名单命中判定（nick 或 uid），同时是 danmaku.IsVIP 的贵宾判定来源之一（F29 任务）。
func (s *Service) IsPatron(ctx context.Context, user string) bool {
	u := strings.TrimSpace(user)
	if u == "" {
		return false
	}
	items, err := s.List(ctx)
	if err != nil {
		return false
	}
	for _, e := range items {
		if e.Nick == u || (e.UID != "" && e.UID == u) {
			return true
		}
	}
	return false
}

// ---- 会话生命周期（live.Manager 钩子）----

// OnSessionStart 开播：清本场 welcomed（once_per_session 每会话语义）。
func (s *Service) OnSessionStart(_ context.Context, sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessionID = sessionID
	s.live = true
	s.welcomed = map[string]bool{}
	s.stats.Welcomed = 0
}

// OnSessionStop 停播：清空会话态，未欢迎的不再补播。
func (s *Service) OnSessionStop(_ context.Context, _ string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.live = false
	s.sessionID = ""
	s.welcomed = map[string]bool{}
}

// ---- G24 进场欢迎 ----

// OnJoin 进场事件（danmaku kind=join 钩子或 /api/v1/patron/join）。
// nick/uid 任一命中名单即欢迎（蓝本 bridge.py:912 join 语义）；uid 命中时以名单 nick 为
// 欢迎称呼与去重键（同人按 nick 与 uid 进场只会欢迎一次）。
// 返回 (line, welcomed)：welcomed=false 表示非贵宾/未开播/已欢迎/开关关。
func (s *Service) OnJoin(ctx context.Context, nick, uid, note string) (string, bool) {
	if !boolCfg(ctx, s.sp, "member", "enabled", true) ||
		!boolCfg(ctx, s.sp, "member", "forward_joins", true) {
		return "", false
	}
	nick = strings.TrimSpace(nick)
	uid = strings.TrimSpace(uid)
	if nick == "" && uid == "" {
		return "", false
	}
	s.mu.Lock()
	if !s.live {
		s.mu.Unlock()
		return "", false
	}
	s.mu.Unlock()
	e, hit := s.match(ctx, nick, uid)
	if !hit {
		return "", false
	}
	return s.welcome(ctx, e.Nick, note, "join")
}

// match 名单命中（nick 或 uid），返回命中条目（用于欢迎称呼与去重）。
func (s *Service) match(ctx context.Context, nick, uid string) (Entry, bool) {
	items, err := s.List(ctx)
	if err != nil {
		return Entry{}, false
	}
	for _, e := range items {
		if (nick != "" && e.Nick == nick) || (uid != "" && e.UID != "" && e.UID == uid) {
			return e, true
		}
	}
	return Entry{}, false
}

// Welcome /api/v1/patron/welcome 强制欢迎（不查名单，once_per_session 仍生效，对齐蓝本）。
func (s *Service) Welcome(ctx context.Context, nick, note string) (string, bool) {
	nick = strings.TrimSpace(nick)
	if nick == "" {
		return "", false
	}
	s.mu.Lock()
	live := s.live
	s.mu.Unlock()
	if !live {
		return "", false
	}
	return s.welcome(ctx, nick, note, "manual")
}

// IsLive 供路由层区分 409 not_live 与静默 skipped。
func (s *Service) IsLive() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.live
}

func (s *Service) welcome(ctx context.Context, nick, note, source string) (string, bool) {
	s.mu.Lock()
	if s.welcomed[nick] && boolCfg(ctx, s.sp, "member", "once_per_session", true) {
		s.mu.Unlock()
		return "", false
	}
	s.welcomed[nick] = true
	s.stats.Welcomed++
	sid := s.sessionID
	s.mu.Unlock()

	line := renderTemplate(strCfg(ctx, s.sp, "member", "welcome_template", "诶，{nick}来啦，欢迎欢迎。"),
		map[string]string{"nick": nick, "note": note})
	if !strings.Contains(line, nick) {
		line = "诶，" + nick + "来啦，欢迎欢迎。" // 蓝本兜底：模板不含名字时回退
	}
	if s.speaker != nil {
		if err := s.speaker.Say(ctx, line); err != nil {
			return "", false
		}
	}
	if s.emit != nil {
		s.emit("patron_welcome", map[string]any{"nick": nick, "line": line, "source": source, "session_id": sid})
	}
	return line, true
}

// State 观测（对齐蓝本 patrons.state {welcomed,count}）。
func (s *Service) State(ctx context.Context) map[string]any {
	items, _ := s.List(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	return map[string]any{
		"live":       s.live,
		"session_id": s.sessionID,
		"count":      len(items),
		"welcomed":   s.stats.Welcomed,
		"saved_cnt":  s.stats.SavedCnt,
	}
}

func renderTemplate(tpl string, vars map[string]string) string {
	out := tpl
	for k, v := range vars {
		out = strings.ReplaceAll(out, "{"+k+"}", v)
	}
	return out
}

func boolCfg(ctx context.Context, sp SettingsProvider, group, key string, def bool) bool {
	if sp == nil {
		return def
	}
	v, ok := sp.Value(ctx, group, key).(bool)
	if !ok {
		return def
	}
	return v
}

func strCfg(ctx context.Context, sp SettingsProvider, group, key, def string) string {
	if sp == nil {
		return def
	}
	if v, ok := sp.Value(ctx, group, key).(string); ok && v != "" {
		return v
	}
	return def
}
