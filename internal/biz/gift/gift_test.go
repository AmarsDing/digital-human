package gift

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/contentgate"
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

type batchCaptureSpeaker struct {
	fakeSpeaker
	batchIDs []string
}

func (s *batchCaptureSpeaker) SayGiftBatch(_ context.Context, text, batchID string) error {
	s.batchIDs = append(s.batchIDs, batchID)
	return s.fakeSpeaker.Say(context.Background(), text, "gift")
}

func TestMergerSameTextDifferentBatchesHaveDifferentIDs(t *testing.T) {
	sp := &batchCaptureSpeaker{}
	m := NewMerger(fakeSettings{groups: map[string]map[string]any{
		"gift": {"enabled": true, "window_s": 6, "flush_people": 4, "flush_diamond": 50,
			"max_names": 4, "min_interval_s": 0, "urgent_min_interval_s": 0, "max_per_minute": 99, "max_items": 12},
		"member": {"enabled": false},
	}}, sp, nil, nil)
	m.OnSessionStart(context.Background(), "s-gift-batches")
	defer m.OnSessionStop(context.Background(), "s-gift-batches")
	for i := 0; i < 2; i++ {
		m.Ingest(context.Background(), Event{UserName: "甲", Item: "玫瑰", Num: 1, Unit: 1, Value: 1})
		m.flush(context.Background())
	}
	if got := sp.lines(); len(got) != 2 || got[0] != got[1] {
		t.Fatalf("测试需要两个同文案批次: %v", got)
	}
	if len(sp.batchIDs) != 2 || sp.batchIDs[0] == "" || sp.batchIDs[0] == sp.batchIDs[1] {
		t.Fatalf("同文案不同批次必须传不同 batch ID: lines=%v ids=%v", sp.lines(), sp.batchIDs)
	}
}

func TestMergerTierBoundaries(t *testing.T) {
	for _, tt := range []struct {
		value int
		want  string
	}{
		{value: 49, want: "small:谢谢 甲 的 玫瑰:1:49"},
		{value: 50, want: "medium:谢谢 甲 的 玫瑰:1:50"},
		{value: 199, want: "medium:谢谢 甲 的 玫瑰:1:199"},
		{value: 200, want: "large:谢谢 甲 的 玫瑰:1:200"},
	} {
		t.Run(tt.want[:strings.Index(tt.want, ":")], func(t *testing.T) {
			sp := &batchCaptureSpeaker{}
			m := NewMerger(fakeSettings{groups: map[string]map[string]any{
				"gift": {
					"enabled": true, "window_s": 6, "flush_people": 4, "flush_diamond": 50,
					"max_names": 4, "min_interval_s": 0, "urgent_min_interval_s": 0, "max_per_minute": 99, "max_items": 12,
					"tier_medium_diamond": 50, "tier_large_diamond": 200,
					"tier_small_template":  "small:{thanks}:{num}:{diamond}",
					"tier_medium_template": "medium:{thanks}:{num}:{diamond}",
					"tier_large_template":  "large:{thanks}:{num}:{diamond}",
				},
				"member": {"enabled": false},
			}}, sp, nil, nil)
			m.OnSessionStart(context.Background(), "s-tier")
			defer m.OnSessionStop(context.Background(), "s-tier")
			m.Ingest(context.Background(), Event{UserName: "甲", Item: "玫瑰", Num: 1, Unit: tt.value, Value: tt.value})
			m.flush(context.Background())
			if got := sp.lines(); len(got) != 1 || got[0] != tt.want {
				t.Fatalf("tiered line=%v want %q", got, tt.want)
			}
		})
	}
}

func TestMergerSameUserWindowProducesOneTieredReaction(t *testing.T) {
	sp := &batchCaptureSpeaker{}
	m := NewMerger(fakeSettings{groups: map[string]map[string]any{
		"gift": {
			"enabled": true, "window_s": 6, "flush_people": 4, "flush_diamond": 50,
			"max_names": 4, "min_interval_s": 0, "urgent_min_interval_s": 0, "max_per_minute": 99, "max_items": 12,
			"tier_medium_diamond": 50, "tier_large_diamond": 200,
			"tier_medium_template": "medium:{thanks}:{num}:{diamond}",
		},
		"member": {"enabled": false},
	}}, sp, nil, nil)
	m.OnSessionStart(context.Background(), "s-merge")
	defer m.OnSessionStop(context.Background(), "s-merge")
	m.Ingest(context.Background(), Event{UserName: "甲", Item: "小心心", Num: 3, Unit: 10, Value: 30})
	m.Ingest(context.Background(), Event{UserName: "甲", Item: "玫瑰", Num: 2, Unit: 10, Value: 20})
	m.flush(context.Background())
	lines := sp.lines()
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "medium:") || !strings.HasSuffix(lines[0], ":5:50") {
		t.Fatalf("同用户窗口内必须合并成一次 medium 反应: %v", lines)
	}
}

type giftAuditRecorder struct{ records []contentgate.AuditRecord }

func (r *giftAuditRecorder) RecordContentDecision(_ context.Context, record contentgate.AuditRecord) error {
	r.records = append(r.records, record)
	return nil
}

type reactionCaptureSpeaker struct {
	batchCaptureSpeaker
	triggers []string
}

func (s *reactionCaptureSpeaker) SayGiftReaction(ctx context.Context, text, batchID, actionTrigger string) error {
	s.triggers = append(s.triggers, actionTrigger)
	return s.SayGiftBatch(ctx, text, batchID)
}

func TestMergerSelectsConfiguredTierAction(t *testing.T) {
	for _, tt := range []struct {
		value       int
		wantTrigger string
	}{
		{value: 49, wantTrigger: "点头"},
		{value: 50, wantTrigger: "鞠躬"},
		{value: 200, wantTrigger: "跳舞"},
	} {
		t.Run(tt.wantTrigger, func(t *testing.T) {
			sp := &reactionCaptureSpeaker{}
			m := NewMerger(fakeSettings{groups: map[string]map[string]any{
				"gift": {
					"enabled": true, "window_s": 6, "flush_people": 4, "flush_diamond": 50,
					"max_names": 4, "min_interval_s": 0, "urgent_min_interval_s": 0, "max_per_minute": 99, "max_items": 12,
					"tier_medium_diamond": 50, "tier_large_diamond": 200,
					"tier_small_action": "点头", "tier_medium_action": "鞠躬", "tier_large_action": "跳舞",
				},
				"member": {"enabled": false},
			}}, sp, nil, nil)
			m.OnSessionStart(context.Background(), "s-action")
			defer m.OnSessionStop(context.Background(), "s-action")
			m.Ingest(context.Background(), Event{UserName: "甲", Item: "玫瑰", Num: 1, Unit: tt.value, Value: tt.value})
			m.flush(context.Background())
			if len(sp.triggers) != 1 || sp.triggers[0] != tt.wantTrigger {
				t.Fatalf("分档动作触发词=%v want %q", sp.triggers, tt.wantTrigger)
			}
		})
	}
}

func TestMergerSuppressionIsObservableAndContainsNoSensitiveAudit(t *testing.T) {
	for _, tt := range []struct {
		name     string
		song     bool
		chat     bool
		category string
	}{
		{name: "song", song: true, category: "song_playing"},
		{name: "text chat", chat: true, category: "text_chat_active"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			sp := &batchCaptureSpeaker{}
			recorder := &giftAuditRecorder{}
			m := NewMerger(fakeSettings{groups: map[string]map[string]any{
				"gift": {"enabled": true, "window_s": 6, "flush_people": 4, "flush_diamond": 50,
					"max_names": 4, "min_interval_s": 0, "urgent_min_interval_s": 0, "max_per_minute": 99, "max_items": 12},
				"member": {"enabled": false},
			}}, sp, nil, nil)
			m.SetActivity(func() bool { return tt.song }, func(string) bool { return tt.chat }, recorder)
			m.OnSessionStart(context.Background(), "s-suppressed")
			defer m.OnSessionStop(context.Background(), "s-suppressed")
			m.Ingest(context.Background(), Event{UserName: "敏感昵称", Item: "敏感礼物", Num: 3, Unit: 100, Value: 300})
			m.flush(context.Background())
			if got := sp.lines(); len(got) != 0 {
				t.Fatalf("高优先级活动中不得口播或转闲聊: %v", got)
			}
			if got := m.State()["skipped"]; got != 1 {
				t.Fatalf("skipped=%v want 1", got)
			}
			if len(recorder.records) != 1 {
				t.Fatalf("抑制必须有一条审计: %+v", recorder.records)
			}
			record := recorder.records[0]
			if record.Outlet != "gift" || record.Category != tt.category || record.Disposition != contentgate.DispositionSilent {
				t.Fatalf("抑制审计元数据错误: %+v", record)
			}
			raw, err := json.Marshal(record)
			if err != nil {
				t.Fatal(err)
			}
			for _, sensitive := range []string{"敏感昵称", "敏感礼物", "300"} {
				if strings.Contains(string(raw), sensitive) {
					t.Fatalf("审计不得包含敏感字段 %q: %s", sensitive, raw)
				}
			}
		})
	}
}
