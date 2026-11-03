// Package gift 实现 F27 礼物合并播报：弹幕链路 kind=gift 不直接念，
// 按 (nick,item) 时间窗聚合后合成「谢谢 xx 的 N 个 yy」交给 Speaker（TTS/Say）。
//
// 蓝本对齐（cyber-xiaoya-live-main src/engine.py:2641-2960）：
//   - 窗口 6s 内同一观众的同名礼物合并为一条（num/value 累加）；
//   - ≥4 人或累计 ≥50 钻立即触发（urgent），否则窗口到期触发；
//   - 上榜最多 4 名，溢出计入 extra_people，播报追加「另有 N 位」；
//   - 节流：min_interval 25s（紧急 10s）+ 每分钟最多 2 次（取两者更晚的 floor）。
//
// 参数来自 settings gift 组（默认值对齐蓝本 config.example.json:99-115）。
// G26：礼物名命中 danmaku.block_words 的词直接丢弃（同蓝本弹幕过滤）。
package gift

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// Speaker 是 live.Manager.Say 的窄接口（入 TTS）。
type Speaker interface {
	Say(ctx context.Context, text string, mode string) error
}

// SettingsProvider 读 gift / danmaku 组键。
type SettingsProvider interface {
	Value(ctx context.Context, group, key string) any
}

// EventSink 事件总线（live.Manager EmitEvent）。
type EventSink func(kind string, payload map[string]any)

// Clock 可注入测试。
type Clock func() time.Time

// Event 一条入站礼物（danmaku.Ingest kind=gift）。
type Event struct {
	UserID   string
	UserName string
	Item     string // 礼物名
	Num      int    // 数量
	Unit     int    // 单件钻石价（relay diamondCount）
	Value    int    // 本次总钻石价值（Unit*Num）
}

// Config 从 settings 组解析（默认值对齐蓝本）。
type Config struct {
	Enabled            bool
	WindowS            int // 聚合窗口秒，默认 6
	FlushPeople        int // 达到 N 人立即触发，默认 4
	FlushDiamond       int // 累计钻石达到 N 立即触发，默认 50
	MaxNames           int // 播报最多上榜人数，默认 4
	MinIntervalS       int // 两次播报最小间隔秒，默认 25
	UrgentMinIntervalS int // 紧急（达标）最小间隔秒，默认 10
	MaxPerMinute       int // 每分钟最多播报次数，默认 2
	MaxItems           int // 合并窗口内最多追踪的 (nick,item) 组合，默认 12
}

// Merger 礼物合并器（F27）。会话级：Start/Stop 由 live.Manager 钩子驱动。
type Merger struct {
	sp      SettingsProvider
	speaker Speaker
	log     *slog.Logger
	clock   Clock
	emit    EventSink

	mu       sync.Mutex
	cfg      Config
	cfgAt    time.Time
	items    map[string]*aggItem // key = nick + "\x00" + item
	order    []string            // 首次出现顺序（上榜按此序）
	extraVal int                 // max_items 溢出礼物的钻石价值（并入 urgent 判定，对齐蓝本 _total）
	lastSay  time.Time
	recent   []time.Time // 最近 60s 内的播报时刻（max_per_minute）
	running  bool
	stop     chan struct{}
	deadline time.Time
	// F28 贵宾欢迎：会话内已欢迎集合（G25 once_per_session）+ per-nick 冷却。
	vipWelcomed map[string]bool
	vipCooldown map[string]time.Time
	stats       Stats
}

type aggItem struct {
	nick  string
	item  string
	num   int
	value int
}

// Stats 会话级计数（对齐蓝本 gift.state + gift_vip.state）。
type Stats struct {
	Received  int `json:"received"`   // 入库的礼物事件数
	Batches   int `json:"batches"`    // 播报批次数
	Blocked   int `json:"blocked"`    // G26 词表丢弃数
	SaidItems int `json:"said_items"` // 播报覆盖的组合数
	Extra     int `json:"extra"`      // 溢出人数累计
	VipFired  int `json:"vip_fired"`  // F28 贵宾欢迎触发次数
	VipCooled int `json:"vip_cooled"` // F28 冷却/已欢迎拦截次数
}

func NewMerger(sp SettingsProvider, speaker Speaker, emit EventSink, logger *slog.Logger) *Merger {
	if logger == nil {
		logger = slog.Default()
	}
	return &Merger{
		sp:          sp,
		speaker:     speaker,
		emit:        emit,
		log:         logger.With("module", "biz/gift"),
		clock:       time.Now,
		items:       map[string]*aggItem{},
		vipWelcomed: map[string]bool{},
		vipCooldown: map[string]time.Time{},
	}
}

// SetClock 测试注入。
func (m *Merger) SetClock(c Clock) { m.clock = c }

// OnSessionStart 会话开始：重置聚合与会话态（G25：已欢迎集合随场次清空）。
// 实现 live.DanmakuWorker 生命周期接口（经 Manager.AddSessionHook 挂载）。
func (m *Merger) OnSessionStart(ctx context.Context, sessionID string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items = map[string]*aggItem{}
	m.order = nil
	m.extraVal = 0
	m.recent = nil
	m.lastSay = time.Time{}
	m.deadline = time.Time{}
	m.vipWelcomed = map[string]bool{}
	m.vipCooldown = map[string]time.Time{}
	m.stats = Stats{}
	if m.running {
		return
	}
	m.running = true
	m.stop = make(chan struct{})
	go m.loop(ctx, m.stop)
}

// OnSessionStop 会话结束：丢弃缓冲（对齐蓝本会话结束不补播报），停循环。
func (m *Merger) OnSessionStop(_ context.Context, _ string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.running {
		return
	}
	m.running = false
	close(m.stop)
}

func (m *Merger) cfgNow(ctx context.Context) Config {
	m.mu.Lock()
	if time.Since(m.cfgAt) < time.Second {
		c := m.cfg
		m.mu.Unlock()
		return c
	}
	m.mu.Unlock()
	c := Config{
		Enabled:            getBool(ctx, m.sp, "gift", "enabled", true),
		WindowS:            getInt(ctx, m.sp, "gift", "window_s", 6),
		FlushPeople:        getInt(ctx, m.sp, "gift", "flush_people", 4),
		FlushDiamond:       getInt(ctx, m.sp, "gift", "flush_diamond", 50),
		MaxNames:           getInt(ctx, m.sp, "gift", "max_names", 4),
		MinIntervalS:       getInt(ctx, m.sp, "gift", "min_interval_s", 25),
		UrgentMinIntervalS: getInt(ctx, m.sp, "gift", "urgent_min_interval_s", 10),
		MaxPerMinute:       getInt(ctx, m.sp, "gift", "max_per_minute", 2),
		MaxItems:           getInt(ctx, m.sp, "gift", "max_items", 12),
	}
	m.mu.Lock()
	m.cfg = c
	m.cfgAt = time.Now()
	m.mu.Unlock()
	return c
}

// Ingest 收一条礼物事件（来自 danmaku sink）。不进缓冲的情况：开关关、词表命中、
// 空礼物名、num<=0。满足 urgent 阈值时立即调度播报。
func (m *Merger) Ingest(ctx context.Context, ev Event) {
	cfg := m.cfgNow(ctx)
	if !cfg.Enabled {
		return
	}
	ev.Item = strings.TrimSpace(ev.Item)
	ev.UserName = strings.TrimSpace(ev.UserName)
	if ev.Item == "" || ev.UserName == "" || ev.Num <= 0 {
		return
	}
	if blockedByWords(ctx, m.sp, ev.Item) || blockedByWords(ctx, m.sp, ev.UserName) {
		m.mu.Lock()
		m.stats.Blocked++
		m.mu.Unlock()
		return
	}
	// F28 贵宾欢迎：按**单件钻石价**判定（蓝本 engine.py:2763 unit=val/num，连击按单价），
	// 严格大于阈值（蓝本 `unit > threshold`）；once_per_session（G25）+ cooldown_s 双闸。
	m.vipCheck(ctx, ev)
	m.mu.Lock()
	m.stats.Received++
	key := ev.UserName + "\x00" + ev.Item
	it, ok := m.items[key]
	if !ok {
		if len(m.order) >= cfg.MaxItems {
			// 窗口内组合超上限：蓝本行为是直接计入 extra（不逐条播报），价值并入总计。
			m.stats.Extra++
			m.extraVal += ev.Value
			m.mu.Unlock()
			return
		}
		it = &aggItem{nick: ev.UserName, item: ev.Item}
		m.items[key] = it
		m.order = append(m.order, key)
	}
	it.num += ev.Num
	it.value += ev.Value

	people := len(m.order)
	totalValue := m.extraVal
	for _, k := range m.order {
		totalValue += m.items[k].value
	}
	urgent := people >= cfg.FlushPeople || totalValue >= cfg.FlushDiamond
	now := m.clock()
	if urgent {
		m.deadline = now
	} else if m.deadline.IsZero() {
		m.deadline = now.Add(time.Duration(cfg.WindowS) * time.Second)
	}
	m.mu.Unlock()
}

// loop 周期检查 deadline；节流判定在 flush 时做。
func (m *Merger) loop(ctx context.Context, stop chan struct{}) {
	tick := time.NewTicker(200 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ctx.Done():
			return
		case <-tick.C:
			m.mu.Lock()
			due := !m.deadline.IsZero() && !m.clock().Before(m.deadline) && len(m.order) > 0
			m.mu.Unlock()
			if due {
				m.flush(ctx)
			}
		}
	}
}

// flush 出队当前批次并播报；节流未到期则顺延。
func (m *Merger) flush(ctx context.Context) {
	cfg := m.cfgNow(ctx)
	m.mu.Lock()
	if len(m.order) == 0 {
		m.deadline = time.Time{}
		m.mu.Unlock()
		return
	}
	now := m.clock()
	// 节流：min_interval（紧急用 urgent 档）+ max_per_minute。
	gap := cfg.MinIntervalS
	people := len(m.order)
	totalValue := m.extraVal
	for _, k := range m.order {
		totalValue += m.items[k].value
	}
	if people >= cfg.FlushPeople || totalValue >= cfg.FlushDiamond {
		gap = cfg.UrgentMinIntervalS
	}
	floor := m.lastSay.Add(time.Duration(gap) * time.Second)
	if cfg.MaxPerMinute > 0 {
		cutoff := now.Add(-time.Minute)
		kept := m.recent[:0]
		for _, t := range m.recent {
			if t.After(cutoff) {
				kept = append(kept, t)
			}
		}
		m.recent = kept
		if len(m.recent) >= cfg.MaxPerMinute {
			if t := m.recent[0].Add(time.Minute); t.After(floor) {
				floor = t
			}
		}
	}
	if now.Before(floor) {
		m.deadline = floor
		m.mu.Unlock()
		return
	}
	// 取批次：上榜 max_names，其余并入 extra。
	shown := m.order
	if len(shown) > cfg.MaxNames {
		shown = shown[:cfg.MaxNames]
	}
	extra := len(m.order) - len(shown)
	batch := make([]aggItem, 0, len(shown))
	for _, k := range shown {
		batch = append(batch, *m.items[k])
	}
	text := composeThanks(batch, extra)
	m.items = map[string]*aggItem{}
	m.order = nil
	m.extraVal = 0
	m.deadline = time.Time{}
	m.lastSay = now
	m.recent = append(m.recent, now)
	m.stats.Batches++
	m.stats.SaidItems += len(batch)
	m.stats.Extra += extra
	m.mu.Unlock()

	if text == "" {
		return
	}
	if err := m.speaker.Say(ctx, text, "gift"); err != nil {
		m.log.Warn("gift say failed", "err", err)
		return
	}
	if m.emit != nil {
		m.emit("gift_batch", map[string]any{
			"text": text, "items": len(batch), "extra": extra, "diamond": totalValue,
		})
	}
}

// composeThanks 合成「谢谢 xx 的 N 个 yy、zz 的 …」（蓝本 engine.py:_gift_say 文案对齐）。
func composeThanks(batch []aggItem, extra int) string {
	parts := make([]string, 0, len(batch))
	for _, it := range batch {
		parts = append(parts, fmt.Sprintf("%s 的 %s", it.nick, formatNum(it.num, it.item)))
	}
	if len(parts) == 0 {
		return ""
	}
	text := "谢谢 " + strings.Join(parts, "、")
	if extra > 0 {
		text += fmt.Sprintf("，以及另外 %d 位朋友", extra)
	}
	return text
}

// formatNum 数量短语：1 个直接报名字，>1 带数量。
func formatNum(num int, item string) string {
	if num <= 1 {
		return item
	}
	return fmt.Sprintf("%d 个 %s", num, item)
}

// blockedByWords G26：danmaku.block_words 命中即丢弃。
func blockedByWords(ctx context.Context, sp SettingsProvider, s string) bool {
	raw, _ := sp.Value(ctx, "danmaku", "block_words").(string)
	if raw == "" {
		return false
	}
	ls := strings.ToLower(s)
	for _, w := range strings.Split(raw, "\n") {
		w = strings.TrimSpace(strings.ToLower(w))
		if w != "" && strings.Contains(ls, w) {
			return true
		}
	}
	return false
}

// vipCheck F28：单件钻石价 > threshold_diamond 触发贵宾欢迎播报；
// once_per_session（G25，会话内每人一次）与 cooldown_s（per-nick 冷却）双闸，
// 模板 member.template（{nick}/{item} 占位，蓝本 gift_vip.template 等价物）。
func (m *Merger) vipCheck(ctx context.Context, ev Event) {
	if !getBool(ctx, m.sp, "member", "enabled", true) {
		return
	}
	unit := ev.Unit
	if unit <= 0 && ev.Num > 0 { // 兼容只给总价的调用方：unit = value/num
		unit = ev.Value / ev.Num
	}
	threshold := getInt(ctx, m.sp, "member", "threshold_diamond", 99)
	if unit <= threshold {
		return
	}
	once := getBool(ctx, m.sp, "member", "once_per_session", true)
	cooldown := time.Duration(getInt(ctx, m.sp, "member", "cooldown_s", 30)) * time.Second
	now := m.clock()

	m.mu.Lock()
	if once && m.vipWelcomed[ev.UserName] {
		m.stats.VipCooled++
		m.mu.Unlock()
		return
	}
	if until, ok := m.vipCooldown[ev.UserName]; ok && now.Before(until) {
		m.stats.VipCooled++
		m.mu.Unlock()
		return
	}
	m.vipWelcomed[ev.UserName] = true
	m.vipCooldown[ev.UserName] = now.Add(cooldown)
	m.stats.VipFired++
	m.mu.Unlock()

	tpl, _ := m.sp.Value(ctx, "member", "template").(string)
	if tpl == "" {
		tpl = "{nick}，欢迎光临！"
	}
	line := strings.NewReplacer("{nick}", ev.UserName, "{item}", ev.Item).Replace(tpl)
	if err := m.speaker.Say(ctx, line, "patron"); err != nil {
		m.log.Warn("vip welcome say failed", "err", err)
		return
	}
	if m.emit != nil {
		m.emit("gift_vip", map[string]any{
			"nick": ev.UserName, "item": ev.Item, "unit": unit, "line": line,
		})
	}
}

// State 观测。
func (m *Merger) State() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	return map[string]any{
		"running":    m.running,
		"pending":    len(m.order),
		"received":   m.stats.Received,
		"batches":    m.stats.Batches,
		"blocked":    m.stats.Blocked,
		"said_items": m.stats.SaidItems,
		"extra":      m.stats.Extra,
		"vip_fired":  m.stats.VipFired,
		"vip_cooled": m.stats.VipCooled,
	}
}

// Reset 会话级重置（等价 Stop 不发播报）。
func (m *Merger) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.items = map[string]*aggItem{}
	m.order = nil
	m.extraVal = 0
	m.deadline = time.Time{}
	m.recent = nil
	m.vipWelcomed = map[string]bool{}
	m.vipCooldown = map[string]time.Time{}
}

func getBool(ctx context.Context, sp SettingsProvider, group, key string, def bool) bool {
	v, ok := sp.Value(ctx, group, key).(bool)
	if !ok {
		return def
	}
	return v
}

func getInt(ctx context.Context, sp SettingsProvider, group, key string, def int) int {
	switch v := sp.Value(ctx, group, key).(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return def
}
