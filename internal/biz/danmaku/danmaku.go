package danmaku

import (
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/google/uuid"
)

type Mode string

const (
	ModeOff     Mode = "off"
	ModeConfirm Mode = "confirm"
	ModeAuto    Mode = "auto"
)

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
)

// Service 保留蓝本语义：Ingest 永远成功返回 {ok, reason}；缓冲最近 8 条且不按文本去重。
type Service struct {
	mu      sync.Mutex
	mode    Mode
	recent  []string
	pend    []Pending
	dropped int64
	t2skip  int64
	// sessionID 记录当前场次；状态接口按 session_id 过滤，跨场次不复用计数。
	sessionID string
	// counts 复刻蓝本 dm_router: 按“当天”分桶计数（不是滚动 24h），每项截断 24 字符，Top10。
	counts map[string]map[string]int
	// persist 为可选的模式持久化回调（由 service 层注入 settings patch）。
	persist func(Mode)
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
	return &Service{mode: mode, recent: make([]string, 0, 8), counts: map[string]map[string]int{}}
}

func (s *Service) Mode() Mode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mode
}

func (s *Service) SetMode(m Mode) bool {
	if m != ModeOff && m != ModeConfirm && m != ModeAuto {
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

// Ingest 返回 ok=false 时也是 200；reason 供采集器识别。
func (s *Service) Ingest(kind, userID, userName, text string, diamond int64, source string) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if kind != "chat" && kind != "gift" && kind != "join" {
		return false, "unknown_kind"
	}
	if s.mode == ModeOff {
		s.dropped++
		return false, "off"
	}
	// 结构过滤：剔除控制符/零宽字符、折叠超长重复字符、按 rune 截断。
	text = sanitizeText(text)
	// deque(maxlen=8)，不按文本去重。
	if len(s.recent) == 8 {
		s.recent = s.recent[1:]
	}
	s.recent = append(s.recent, text)
	if s.mode == ModeConfirm && kind == "chat" {
		// pending 上限：超出丢最旧并计入 dropped。
		if len(s.pend) >= maxPending {
			s.pend = s.pend[1:]
			s.dropped++
		}
		s.pend = append(s.pend, Pending{ID: uuid.NewString(), UserName: userName, Text: text, CreatedAtMs: time.Now().UnixMilli()})
	}
	return true, ""
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
	s.pend = nil
	s.dropped = 0
	s.t2skip = 0
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
	out := State{
		Mode: s.mode, Enabled: s.mode != ModeOff,
		Pending: append([]Pending(nil), s.pend...),
		Recent:  append([]string(nil), s.recent...),
	}
	s.mu.Unlock()
	out.Top = s.Top(now, 10)
	return out
}
