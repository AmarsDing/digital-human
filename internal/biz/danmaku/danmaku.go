package danmaku

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
)

type Mode string

const (
	ModeOff     Mode = "off"
	ModeConfirm Mode = "confirm"
	ModeAuto    Mode = "auto"
	// ModeReadAll 批读模式：按到达顺序逐条复读（F24，无 LLM 回答）。
	ModeReadAll Mode = "read_all"
	// ModeAnchor 主播模式：每拍取最新一条复读 + LLM 完整回答
	// （蓝本 engine.py _danmaku_worker 非合并路径：取最新→复读→完整回答）。
	ModeAnchor Mode = "dm_anchor"
)

// ConsumeItem 是批读 worker 取出的弹幕。VIP 标记来自采集侧（蓝本 priority=vip）。
type ConsumeItem struct {
	UserName string
	Text     string
	Kind     string
	VIP      bool
}

type Pending struct {
	ID          string `json:"id"`
	UserName    string `json:"user_name"`
	Text        string `json:"text"`
	CreatedAtMs int64  `json:"created_at_ms"`
}

// 结构过滤与队列上限参数。
const (
	maxTextRunes = 100 // 弹幕文本按 rune 截断上限
	maxSameRunes = 20  // 连续同一字符超过该值折叠
	maxPending   = 200 // 待确认队列上限，超出丢最旧
	// MaxRecentDanmaku 普通弹幕批读缓冲上限（蓝本 deque(maxlen=8)，不按文本去重）。
	MaxRecentDanmaku = 8
	// maxVIPQueue VIP 优先队列上限（蓝本 _dan_vipq 满 5 丢最旧：送礼观众也防刷屏）。
	maxVIPQueue = 5
)

// Service 保留蓝本语义：Ingest 永远成功返回 {ok, reason}；缓冲最近 8 条且不按文本去重。
// VIP（采集侧 vip=true）弹幕进独立优先队列，不进 recent 环（蓝本 _dan_vipq 语义）。
type Service struct {
	mu      sync.Mutex
	mode    Mode
	recent  []ConsumeItem
	vipq    []ConsumeItem
	pend    []Pending
	dropped int64
	t2skip  int64
	// blocked 批读前词表过滤命中计数（F22）；skipped 批读被顶掉/非 chat 跳过计数。
	blocked int64
	skipped int64
	// vipUsers 本场贵宾名单（F26，userName/userID → 最近到达 ms），BeginSession 清空；
	// 供 action.Scheduler.SetVIP 生产接线（蓝本 _vip_users/_is_vip）。
	vipUsers map[string]int64
	// sessionID 记录当前场次；状态接口按 session_id 过滤，跨场次不复用计数。
	sessionID string
	// counts 复刻蓝本 dm_router: 按“当天”分桶计数（不是滚动 24h），每项截断 24 字符，Top10。
	counts map[string]map[string]int
	// persist 为可选的模式持久化回调（由 service 层注入 settings patch）。
	persist func(Mode)
	// sink 为可选的弹幕事件落库端口；nil 表示不持久化（纯内存运行）。
	sink EventSink
	// kindTap 为可选的 gift/join 分流钩子（F27 礼物合并器 / G24 贵宾欢迎的入口）；
	// 事件被接受后在锁外同步调用，批读 worker 对 gift/join 仍保持不念（计 skipped）。
	kindTap KindTap
	ruleTap RuleTap
	seen    map[string]Event
}

// TapEvent 是 kindTap 分发的原始事件（kind 取 gift/join）。
type TapEvent struct {
	EventRowID    string
	SourceEventID string
	UserID        string
	UserName      string
	Text          string
	Diamond       int64
	VIP           bool
	OccurredAtMs  int64
	ReceivedAtMs  int64
	ExpiresAtMs   int64
}

// KindTap gift/join 事件分流端口；装配层注入 gift.Merger / patron.Service 的适配器。
type KindTap func(kind string, ev TapEvent)

// Event 是落库用弹幕事件（danmakuevent 表口径）；decision 取值见 schema 注释。
type Event struct {
	ID            string
	SessionID     string
	Kind          string
	UserID        string
	UserName      string
	Text          string
	Diamond       int64
	Source        string
	SourceEventID string
	Decision      string
	OccurredAtMs  int64
	ReceivedAtMs  int64
	ExpiresAtMs   int64
	CreatedAtMs   int64
}

// EventSink 弹幕事件写路径端口；Ent 实现落 PG，内存实现只留最近若干条供排查。
type EventSink interface {
	Record(ctx context.Context, e Event) error
}

// IdempotentEventSink 由持久存储实现，用于进程重启后仍能识别同源重放。
type IdempotentEventSink interface {
	RecordEvent(ctx context.Context, e Event) (duplicate bool, err error)
}

// MemoryEventSink 内存降级：有界环形缓冲（默认 512 条），重启即丢，不提供复盘查询。
type MemoryEventSink struct {
	mu   sync.Mutex
	cap  int
	rows []Event
}

func NewMemoryEventSink(capacity int) *MemoryEventSink {
	if capacity <= 0 {
		capacity = 512
	}
	return &MemoryEventSink{cap: capacity}
}

func (s *MemoryEventSink) Record(_ context.Context, e Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.rows) >= s.cap {
		s.rows = s.rows[1:]
	}
	s.rows = append(s.rows, e)
	return nil
}

// TopItem 对齐蓝本 /api/dmcmd/state 的 top 项。
type TopItem struct {
	Text  string `json:"text"`
	Count int    `json:"count"`
}

type State struct {
	Mode    Mode      `json:"mode"`
	Enabled bool      `json:"enabled"`
	Pending []Pending `json:"pending,omitempty"`
	Top     []TopItem `json:"top"`
	Recent  []string  `json:"recent,omitempty"`
}

func NewService(mode Mode) *Service {
	return &Service{
		mode:     mode,
		recent:   make([]ConsumeItem, 0, MaxRecentDanmaku),
		counts:   map[string]map[string]int{},
		vipUsers: map[string]int64{},
		seen:     map[string]Event{},
	}
}

func (s *Service) Mode() Mode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

func (s *Service) SetMode(m Mode) bool {
	switch m {
	case ModeOff, ModeConfirm, ModeAuto, ModeReadAll, ModeAnchor:
	default:
		return false
	}
	s.mu.Lock()
	s.mode = m
	persist := s.persist
	s.mu.Unlock()
	if persist != nil {
		persist(m)
	}
	return true
}

// SetModePersist 注入模式持久化回调（G15：模式变更落库），在锁外调用。
func (s *Service) SetModePersist(fn func(Mode)) {
	s.mu.Lock()
	s.persist = fn
	s.mu.Unlock()
}

// SetEventSink 注入弹幕事件落库端口（PG 可用时由装配层给 Ent 实现）。
// 落库失败不回馈到 Ingest 的 ok/reason（写路径 best-effort，与场次 persist 一致）。
func (s *Service) SetEventSink(sink EventSink) {
	s.mu.Lock()
	s.sink = sink
	s.mu.Unlock()
}

// SetKindTap 注入 gift/join 分流钩子（F27/G24）；nil 表示不分流。
func (s *Service) SetKindTap(fn KindTap) {
	s.mu.Lock()
	s.kindTap = fn
	s.mu.Unlock()
}

// Ingest 保留旧 API；新采集入口通过 Envelope 携带平台来源事件 ID。
func (s *Service) Ingest(ctx context.Context, kind, userID, userName, text string, diamond int64, source string, vip bool) (bool, string) {
	result := s.IngestEvent(ctx, Envelope{
		Kind: kind, UserID: userID, UserName: userName, Text: text,
		Diamond: diamond, Source: source, VIP: vip,
	})
	return result.OK, result.Reason
}

// PendingReads 返回待播报深度（vip 队列 / 普通缓冲），供 worker 决策节奏与空转退出。
func (s *Service) PendingReads() (vip, normal int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.vipq), len(s.recent)
}

// NextForRead 批读取件（F24/F25/F22）：VIP 队列优先插队；普通缓冲 readAll=true 取最旧
// （顺序读），false 取最新并把更旧的计 skipped 丢弃（蓝本合并关闭时“只留最新一条”语义）。
// blockWords 命中即跳过并计 blocked；gift/join 不念（F27/G24 后续切片），计 skipped。
// 无可消费时返回 ok=false。
func (s *Service) NextForRead(readAll bool, blockWords []string) (ConsumeItem, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		var it ConsumeItem
		if len(s.vipq) > 0 {
			it = s.vipq[0]
			s.vipq = s.vipq[1:]
		} else {
			if len(s.recent) == 0 {
				return ConsumeItem{}, false
			}
			if readAll {
				it = s.recent[0]
				s.recent = s.recent[1:]
			} else {
				it = s.recent[len(s.recent)-1]
				s.skipped += int64(len(s.recent) - 1)
				s.recent = s.recent[:0]
			}
		}
		if it.Kind != "chat" {
			s.skipped++
			continue
		}
		if blockedByWord(it.Text, blockWords) {
			s.blocked++
			continue
		}
		return it, true
	}
}

// blockedByWord 词表命中判定（蓝本 enqueue_danmaku drop_words：子串包含即拦截）。
func blockedByWord(text string, words []string) bool {
	for _, w := range words {
		if w != "" && strings.Contains(text, w) {
			return true
		}
	}
	return false
}

// IsVIP 报告 user 是否本场贵宾（F26 透传 action.Scheduler.SetVIP 的判定源）。
func (s *Service) IsVIP(user string) bool {
	u := strings.TrimSpace(user)
	if u == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.vipUsers[u]
	return ok
}

// ReadStats 返回批读链路计数：blocked=F22 词表过滤命中，skipped=被顶掉/非 chat 跳过。
func (s *Service) ReadStats() (blocked, skipped int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.blocked, s.skipped
}

// sanitizeText 弹幕文本结构过滤：
// 1) 剔除控制符与零宽字符（零宽空格/连词符/BOM）；
// 2) 连续同一字符超过 maxSameRunes 折叠；
// 3) 整体按 rune 截断到 maxTextRunes。
func sanitizeText(text string) string {
	out := make([]rune, 0, len(text))
	last := rune(-1)
	repeat := 0
	for _, r := range text {
		if unicode.IsControl(r) || isZeroWidth(r) {
			continue
		}
		if r == last {
			repeat++
			if repeat > maxSameRunes {
				continue
			}
		} else {
			last = r
			repeat = 1
		}
		out = append(out, r)
		if len(out) >= maxTextRunes {
			break
		}
	}
	return string(out)
}

func isZeroWidth(r rune) bool {
	switch r {
	case 0x200B, 0x200C, 0x200D, 0xFEFF: // ZWSP / ZWNJ / ZWJ / BOM
		return true
	}
	return false
}

func (s *Service) ListPending() []Pending {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]Pending(nil), s.pend...)
}

func (s *Service) Confirm(id string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, p := range s.pend {
		if p.ID == id {
			s.pend = append(s.pend[:i], s.pend[i+1:]...)
			return true
		}
	}
	return false
}

func (s *Service) Dismiss(id string) bool { return s.Confirm(id) }

func (s *Service) Stats() (pending int, dropped, t2Skipped int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.pend), s.dropped, s.t2skip
}

// BeginSession 在开播成功时绑定当前场次；换场次时清空缓冲/丢弃计数，避免跨场次串数据。
func (s *Service) BeginSession(sessionID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.sessionID == sessionID {
		return
	}
	s.sessionID = sessionID
	s.recent = s.recent[:0]
	s.vipq = nil
	s.pend = nil
	s.dropped = 0
	s.t2skip = 0
	s.blocked = 0
	s.skipped = 0
	s.vipUsers = map[string]int64{}
}

// StatsFor 只返回 session_id 对应场次的计数；场次不匹配时明确返回 ok=false。
func (s *Service) StatsFor(sessionID string) (pending int, dropped, t2Skipped int64, ok bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if sessionID != "" && s.sessionID != sessionID {
		return 0, 0, 0, false
	}
	return len(s.pend), s.dropped, s.t2skip, true
}

// NoteCommand 计入手动 /api/cmd 的触发次数（蓝本 dm_router.count(text)）。
// 只统计成功进入动作通道的手动指令；静音/自动触发不计入（与蓝本一致）。
func (s *Service) NoteCommand(text string, now time.Time) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	r := []rune(text)
	if len(r) > 24 {
		text = string(r[:24])
	}
	day := now.Format("2006-01-02")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.counts[day] == nil {
		s.counts[day] = map[string]int{}
	}
	s.counts[day][text]++
	s.pruneDays(now, 3)
}

func (s *Service) pruneDays(now time.Time, keep int) {
	cutoff := now.AddDate(0, 0, -keep).Format("2006-01-02")
	for day := range s.counts {
		if day < cutoff {
			delete(s.counts, day)
		}
	}
}

// Top 返回当天 Top10；与蓝本 `counts[today]` 口径一致（跨天清零）。
func (s *Service) Top(now time.Time, n int) []TopItem {
	if n <= 0 {
		n = 10
	}
	day := now.Format("2006-01-02")
	s.mu.Lock()
	dayCounts := s.counts[day]
	out := make([]TopItem, 0, len(dayCounts))
	for text, count := range dayCounts {
		out = append(out, TopItem{Text: text, Count: count})
	}
	s.mu.Unlock()
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Count == out[j].Count {
			return out[i].Text < out[j].Text
		}
		return out[i].Count > out[j].Count
	})
	if len(out) > n {
		out = out[:n]
	}
	return out
}

// State 返回命令面板状态（mode/enabled/pending/top/recent）。
func (s *Service) State(now time.Time) State {
	s.mu.Lock()
	recent := make([]string, 0, len(s.recent))
	for _, it := range s.recent {
		recent = append(recent, it.Text)
	}
	out := State{
		Mode: s.mode, Enabled: s.mode != ModeOff,
		Pending: append([]Pending(nil), s.pend...),
		Recent:  recent,
	}
	s.mu.Unlock()
	out.Top = s.Top(now, 10)
	return out
}
