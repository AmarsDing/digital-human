package gift

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSettings 按组返回键值（缺省回落到代码默认值）。
type fakeSettings struct{ groups map[string]map[string]any }

func (f fakeSettings) Value(_ context.Context, group, key string) any {
	if g, ok := f.groups[group]; ok {
		return g[key]
	}
	return nil
}

type fakeSpeaker struct {
	mu   sync.Mutex
	said []string
	mode []string
}

func (s *fakeSpeaker) Say(_ context.Context, text, mode string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.said = append(s.said, text)
	s.mode = append(s.mode, mode)
	return nil
}

func (s *fakeSpeaker) lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.said...)
}

type emitted struct {
	kind string
	m    map[string]any
}

type emitLog struct {
	mu sync.Mutex
	ev []emitted
}

func (e *emitLog) fn() EventSink {
	return func(kind string, m map[string]any) {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.ev = append(e.ev, emitted{kind, m})
	}
}

func newTestMerger(sp SettingsProvider) (*Merger, *fakeSpeaker, *emitLog) {
	speaker := &fakeSpeaker{}
	el := &emitLog{}
	m := NewMerger(sp, speaker, el.fn(), slog.New(slog.NewTextHandler(discard{}, nil)))
	return m, speaker, el
}

type discard struct{}

func (discard) Write(p []byte) (int, error) { return len(p), nil }

// F27 核心：同人同礼窗口内合并，触发后合成「谢谢 xx 的 N 个 yy」。
func TestMerger_MergesSameUserSameItem(t *testing.T) {
	m, speaker, _ := newTestMerger(fakeSettings{groups: map[string]map[string]any{
		"gift": {"enabled": true, "window_s": 6, "flush_people": 4, "flush_diamond": 50,
			"max_names": 4, "min_interval_s": 0, "urgent_min_interval_s": 0, "max_per_minute": 99, "max_items": 12},
		"member": {"enabled": false},
	}})
	m.OnSessionStart(context.Background(), "s1")
	defer m.OnSessionStop(context.Background(), "s1")
	m.Ingest(context.Background(), Event{UserName: "甲", Item: "小心心", Num: 3, Unit: 1, Value: 3})
	m.Ingest(context.Background(), Event{UserName: "甲", Item: "小心心", Num: 2, Unit: 1, Value: 2})
	m.flush(context.Background())
	lines := speaker.lines()
	if len(lines) != 1 {
		t.Fatalf("said=%v want 1 line", lines)
	}
	if !strings.Contains(lines[0], "甲 的 5 个 小心心") {
		t.Fatalf("line=%q want 合并为 5 个小心心", lines[0])
	}
	if got := m.State()["said_items"]; got != 1 {
		t.Fatalf("said_items=%v want 1", got)
	}
}

// F27：≥flush_diamond 钻石紧急触发（urgent），min_interval 用 urgent 档。
func TestMerger_UrgentFlushByDiamond(t *testing.T) {
	m, speaker, el := newTestMerger(fakeSettings{groups: map[string]map[string]any{
		"gift": {"enabled": true, "window_s": 6, "flush_people": 4, "flush_diamond": 50,
			"max_names": 4, "min_interval_s": 25, "urgent_min_interval_s": 10, "max_per_minute": 2, "max_items": 12},
		"member": {"enabled": false},
	}})
	m.OnSessionStart(context.Background(), "s1")
	defer m.OnSessionStop(context.Background(), "s1")
	m.Ingest(context.Background(), Event{UserName: "土豪", Item: "嘉年华", Num: 1, Unit: 60, Value: 60})
	// urgent：deadline=now，flush 立即播报（lastSay 零值不受 min_interval 限制）。
	m.flush(context.Background())
	if len(speaker.lines()) != 1 {
		t.Fatalf("urgent 未立即播报: %v", speaker.lines())
	}
	// 节流验证：窗口内再来小额礼物，25s 间隔未到 → 不播；10s urgent 档不适用（非 urgent）。
	m.Ingest(context.Background(), Event{UserName: "乙", Item: "小花", Num: 1, Unit: 1, Value: 1})
	m.flush(context.Background())
	if len(speaker.lines()) != 1 {
		t.Fatalf("min_interval 内不应再播: %v", speaker.lines())
	}
	_ = el
}

// F27：上榜 max_names，溢出计入 extra 并体现在文案「另外 N 位朋友」。
func TestMerger_MaxNamesAndExtra(t *testing.T) {
	m, speaker, _ := newTestMerger(fakeSettings{groups: map[string]map[string]any{
		"gift": {"enabled": true, "window_s": 6, "flush_people": 4, "flush_diamond": 50,
			"max_names": 2, "min_interval_s": 0, "urgent_min_interval_s": 0, "max_per_minute": 99, "max_items": 12},
		"member": {"enabled": false},
	}})
	m.OnSessionStart(context.Background(), "s1")
	defer m.OnSessionStop(context.Background(), "s1")
	for _, u := range []string{"A", "B", "C", "D"} {
		m.Ingest(context.Background(), Event{UserName: u, Item: "玫瑰", Num: 1, Unit: 1, Value: 1})
	}
	m.flush(context.Background())
	lines := speaker.lines()
	if len(lines) != 1 {
		t.Fatalf("said=%v want 1", lines)
	}
	if !strings.Contains(lines[0], "A 的 玫瑰") || !strings.Contains(lines[0], "B 的 玫瑰") {
		t.Fatalf("上榜前 2 名: %q", lines[0])
	}
	if !strings.Contains(lines[0], "另外 2 位朋友") {
		t.Fatalf("溢出文案: %q", lines[0])
	}
}

// G26：礼物名命中 danmaku.block_words 直接丢弃（不进缓冲、不触发 F28）。
func TestMerger_BlockWordsFilter(t *testing.T) {
	m, _, _ := newTestMerger(fakeSettings{groups: map[string]map[string]any{
		"gift":    {"enabled": true, "window_s": 6, "flush_people": 4, "flush_diamond": 50, "max_names": 4, "min_interval_s": 0, "urgent_min_interval_s": 0, "max_per_minute": 99, "max_items": 12},
		"danmaku": {"block_words": "嘉年华\n火箭"},
		"member":  {"enabled": true, "threshold_diamond": 99, "cooldown_s": 30, "once_per_session": true},
	}})
	m.OnSessionStart(context.Background(), "s1")
	defer m.OnSessionStop(context.Background(), "s1")
	m.Ingest(context.Background(), Event{UserName: "甲", Item: "豪华嘉年华", Num: 1, Unit: 999, Value: 999})
	if got := m.State()["blocked"]; got != 1 {
		t.Fatalf("blocked=%v want 1", got)
	}
	if got := m.State()["received"]; got != 0 {
		t.Fatalf("received=%v want 0（词表先于入库与 F28）", got)
	}
	if got := m.State()["vip_fired"]; got != 0 {
		t.Fatalf("vip_fired=%v want 0", got)
	}
}

// F28：单件钻石价严格 > threshold 触发贵宾欢迎；once_per_session（G25）+ cooldown 双闸；
// 模板 member.template 渲染 {nick}/{item}；Stop 清空（G25）。
func TestMerger_VipWelcome(t *testing.T) {
	m, speaker, el := newTestMerger(fakeSettings{groups: map[string]map[string]any{
		"gift": {"enabled": true, "window_s": 6, "flush_people": 4, "flush_diamond": 50,
			"max_names": 4, "min_interval_s": 0, "urgent_min_interval_s": 0, "max_per_minute": 99, "max_items": 12},
		"member": {"enabled": true, "threshold_diamond": 99, "cooldown_s": 30,
			"once_per_session": true, "template": "感谢 {nick} 的 {item}！"},
	}})
	ctx := context.Background()
	m.OnSessionStart(ctx, "s1")
	// 单价 100 > 99 触发。
	m.Ingest(ctx, Event{UserName: "大哥", Item: "火箭", Num: 3, Unit: 100, Value: 300})
	lines := speaker.lines()
	if len(lines) != 1 || lines[0] != "感谢 大哥 的 火箭！" {
		t.Fatalf("vip 欢迎: %v", lines)
	}
	if got := m.State()["vip_fired"]; got != 1 {
		t.Fatalf("vip_fired=%v want 1", got)
	}
	// 单价 ≤ 阈值不触发（蓝本严格大于）。
	m.Ingest(ctx, Event{UserName: "二哥", Item: "火箭", Num: 1, Unit: 99, Value: 99})
	if len(speaker.lines()) != 1 {
		t.Fatalf("99 钻不应触发（严格大于）: %v", speaker.lines())
	}
	// G25 once_per_session：同人本场第二次不重复欢迎。
	m.Ingest(ctx, Event{UserName: "大哥", Item: "火箭", Num: 1, Unit: 200, Value: 200})
	if len(speaker.lines()) != 1 {
		t.Fatalf("once_per_session 应拦截: %v", speaker.lines())
	}
	if got := m.State()["vip_cooled"]; got != 1 {
		t.Fatalf("vip_cooled=%v want 1", got)
	}
	// G25：Stop 清空已欢迎集合，新场次可再次欢迎。
	m.OnSessionStop(ctx, "s1")
	m.OnSessionStart(ctx, "s2")
	m.Ingest(ctx, Event{UserName: "大哥", Item: "火箭", Num: 1, Unit: 200, Value: 200})
	if len(speaker.lines()) != 2 {
		t.Fatalf("新场次应可再次欢迎: %v", speaker.lines())
	}
	m.OnSessionStop(ctx, "s2")
	el.mu.Lock()
	defer el.mu.Unlock()
	var vipEvents int
	for _, e := range el.ev {
		if e.kind == "gift_vip" {
			vipEvents++
		}
	}
	if vipEvents != 2 {
		t.Fatalf("gift_vip 事件=%d want 2", vipEvents)
	}
}

// F28 cooldown：once_per_session 关闭时，cooldown_s 内不重复触发。
func TestMerger_VipCooldown(t *testing.T) {
	base := time.Now()
	cur := base
	m, speaker, _ := newTestMerger(fakeSettings{groups: map[string]map[string]any{
		"gift": {"enabled": true, "window_s": 6, "flush_people": 4, "flush_diamond": 50,
			"max_names": 4, "min_interval_s": 0, "urgent_min_interval_s": 0, "max_per_minute": 99, "max_items": 12},
		"member": {"enabled": true, "threshold_diamond": 99, "cooldown_s": 30, "once_per_session": false},
	}})
	m.SetClock(func() time.Time { return cur })
	ctx := context.Background()
	m.OnSessionStart(ctx, "s1")
	defer m.OnSessionStop(ctx, "s1")
	m.Ingest(ctx, Event{UserName: "大哥", Item: "火箭", Num: 1, Unit: 200, Value: 200})
	cur = base.Add(10 * time.Second)
	m.Ingest(ctx, Event{UserName: "大哥", Item: "火箭", Num: 1, Unit: 200, Value: 200})
	if len(speaker.lines()) != 1 {
		t.Fatalf("cooldown 内不应重复: %v", speaker.lines())
	}
	cur = base.Add(31 * time.Second)
	m.Ingest(ctx, Event{UserName: "大哥", Item: "火箭", Num: 1, Unit: 200, Value: 200})
	if len(speaker.lines()) != 2 {
		t.Fatalf("cooldown 过后应再触发: %v", speaker.lines())
	}
}

// F27：开关关闭时完全不入库；session 未开始（Stop 后）状态重置。
func TestMerger_DisabledNoop(t *testing.T) {
	m, speaker, _ := newTestMerger(fakeSettings{groups: map[string]map[string]any{
		"gift":   {"enabled": false},
		"member": {"enabled": false},
	}})
	m.OnSessionStart(context.Background(), "s1")
	defer m.OnSessionStop(context.Background(), "s1")
	m.Ingest(context.Background(), Event{UserName: "甲", Item: "玫瑰", Num: 1, Unit: 1, Value: 1})
	m.flush(context.Background())
	if len(speaker.lines()) != 0 {
		t.Fatalf("关闭时不应播报: %v", speaker.lines())
	}
	if got := m.State()["received"]; got != 0 {
		t.Fatalf("received=%v want 0", got)
	}
}

// F27 文案：单数量不带数字（「甲 的 玫瑰」），多人顿号拼接。
func TestComposeThanks(t *testing.T) {
	got := composeThanks([]aggItem{{nick: "甲", item: "玫瑰", num: 1}, {nick: "乙", item: "小心心", num: 3}}, 0)
	want := "谢谢 甲 的 玫瑰、乙 的 3 个 小心心"
	if got != want {
		t.Fatalf("compose=%q want %q", got, want)
	}
	if composeThanks(nil, 3) != "" {
		t.Fatal("空批次应空串")
	}
}
