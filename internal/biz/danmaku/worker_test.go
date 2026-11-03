package danmaku

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/chat"
)

// ---- fakes（不碰网络）----

type fakeSpeaker struct {
	mu    sync.Mutex
	texts []string
}

func (f *fakeSpeaker) Say(_ context.Context, text string) error {
	f.mu.Lock()
	f.texts = append(f.texts, text)
	f.mu.Unlock()
	return nil
}

func (f *fakeSpeaker) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.texts...)
}

type fakeChatter struct {
	mu      sync.Mutex
	calls   int
	key     string
	prompts []string
	reply   []string
}

func (f *fakeChatter) Chat(ctx context.Context, key, userText string, sink chat.SentenceSink) (string, error) {
	f.mu.Lock()
	f.calls++
	f.key = key
	f.prompts = append(f.prompts, userText)
	reply := append([]string(nil), f.reply...)
	f.mu.Unlock()
	for _, s := range reply {
		if err := sink(ctx, s); err != nil {
			return "", err
		}
	}
	return strings.Join(reply, ""), nil
}

func (f *fakeChatter) called() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// fakeSettings 实现 SettingsProvider：group -> key -> value。
type fakeSettings map[string]map[string]any

func (f fakeSettings) Value(_ context.Context, groupID, key string) any {
	if g, ok := f[groupID]; ok {
		return g[key]
	}
	return nil
}

type fakeEmitter struct {
	mu    sync.Mutex
	types []string
	last  map[string]any
}

func (f *fakeEmitter) emit(typ string, payload map[string]any) {
	f.mu.Lock()
	f.types = append(f.types, typ)
	f.last = payload
	f.mu.Unlock()
}

func (f *fakeEmitter) got() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.types...)
}

func waitCond(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("wait %s timeout after %s", what, d)
}

func newTestWorker(svc *Service, sp *fakeSpeaker, ch *fakeChatter, st fakeSettings) (*Worker, *fakeEmitter) {
	em := &fakeEmitter{}
	w := NewWorker(svc, sp, ch, st, WorkerOptions{Tick: 5 * time.Millisecond, EmitEvent: em.emit})
	return w, em
}

// ---- mode 三分支消费行为 ----

func TestWorkerOffModeNoConsume(t *testing.T) {
	svc := NewService(ModeOff)
	sp := &fakeSpeaker{}
	w, _ := newTestWorker(svc, sp, nil, fakeSettings{})
	w.OnSessionStart(context.Background(), "s-off")
	defer w.OnSessionStop(context.Background(), "s-off")
	ok, reason := svc.Ingest(context.Background(), "chat", "u1", "甲", "hello", 0, "", false)
	if ok || reason != "off" {
		t.Fatalf("off ingest: ok=%v reason=%s", ok, reason)
	}
	time.Sleep(50 * time.Millisecond)
	if got := sp.got(); len(got) != 0 {
		t.Fatalf("off mode should stay silent, got %v", got)
	}
}

func TestWorkerReadAllInOrder(t *testing.T) {
	svc := NewService(ModeReadAll)
	sp := &fakeSpeaker{}
	st := fakeSettings{"danmaku": {"read_interval_ms": 15}}
	w, _ := newTestWorker(svc, sp, nil, st)
	w.OnSessionStart(context.Background(), "s-ra")
	defer w.OnSessionStop(context.Background(), "s-ra")
	for _, text := range []string{"第一条", "第二条", "第三条"} {
		if ok, _ := svc.Ingest(context.Background(), "chat", "u1", "甲", text, 0, "", false); !ok {
			t.Fatalf("ingest %s failed", text)
		}
	}
	waitCond(t, 2*time.Second, "read_all 顺序读三条", func() bool { return len(sp.got()) == 3 })
	got := sp.got()
	if got[0] != "第一条" || got[1] != "第二条" || got[2] != "第三条" {
		t.Fatalf("read_all order = %v", got)
	}
}

func TestWorkerConfirmModeNotConsumed(t *testing.T) {
	svc := NewService(ModeConfirm)
	sp := &fakeSpeaker{}
	st := fakeSettings{"danmaku": {"read_interval_ms": 10}}
	w, _ := newTestWorker(svc, sp, nil, st)
	w.OnSessionStart(context.Background(), "s-cf")
	defer w.OnSessionStop(context.Background(), "s-cf")
	if ok, _ := svc.Ingest(context.Background(), "chat", "u1", "甲", "待确认弹幕", 0, "", false); !ok {
		t.Fatal("ingest failed")
	}
	time.Sleep(60 * time.Millisecond)
	if got := sp.got(); len(got) != 0 {
		t.Fatalf("confirm 模式不应批读消费, got %v", got)
	}
	if _, normal := svc.PendingReads(); normal != 1 {
		t.Fatalf("confirm 模式弹幕应留在缓冲, normal=%d", normal)
	}
}

// ---- dm_anchor：复读 + 经 chat.Manager 的 LLM 完整回答 ----

func TestWorkerAnchorRepeatThenLLM(t *testing.T) {
	svc := NewService(ModeAnchor)
	sp := &fakeSpeaker{}
	ch := &fakeChatter{reply: []string{"答一句。", "再补一句。"}}
	st := fakeSettings{
		"danmaku": {"read_interval_ms": 15},
		"llm":     {"api_key": "k"},
	}
	w, _ := newTestWorker(svc, sp, ch, st)
	w.OnSessionStart(context.Background(), "s-anchor")
	defer w.OnSessionStop(context.Background(), "s-anchor")
	if ok, _ := svc.Ingest(context.Background(), "chat", "u7", "小明", "主播好", 0, "", false); !ok {
		t.Fatal("ingest failed")
	}
	waitCond(t, 2*time.Second, "复读+两句回答入 Speaker", func() bool { return len(sp.got()) == 3 })
	got := sp.got()
	if got[0] != "主播好" || got[1] != "答一句。" || got[2] != "再补一句。" {
		t.Fatalf("anchor speak sequence = %v", got)
	}
	if ch.called() != 1 {
		t.Fatalf("chatter calls=%d", ch.called())
	}
	if ch.key != "s-anchor" {
		t.Fatalf("chat key 应为场次 ID, got %q", ch.key)
	}
	if !strings.Contains(ch.prompts[0], "观众「小明」说：主播好") {
		t.Fatalf("anchor prompt = %q", ch.prompts[0])
	}
	if w.LLMDegraded() != 0 {
		t.Fatalf("llm 已配置不应降级, degraded=%d", w.LLMDegraded())
	}
}

func TestWorkerAnchorLLMNotConfiguredDegrades(t *testing.T) {
	svc := NewService(ModeAnchor)
	sp := &fakeSpeaker{}
	ch := &fakeChatter{reply: []string{"不应出现"}}
	st := fakeSettings{
		"danmaku": {"read_interval_ms": 15},
		"llm":     {"api_key": ""},
	}
	w, em := newTestWorker(svc, sp, ch, st)
	w.OnSessionStart(context.Background(), "s-deg")
	defer w.OnSessionStop(context.Background(), "s-deg")
	if ok, _ := svc.Ingest(context.Background(), "chat", "u1", "甲", "弹幕一条", 0, "", false); !ok {
		t.Fatal("ingest failed")
	}
	// 等显式降级事件（speak 内 复读→计数→日志→事件 顺序执行，事件是最后一步）。
	waitCond(t, 2*time.Second, "降级事件落账", func() bool { return len(em.got()) == 1 })
	if got := sp.got(); len(got) != 1 || got[0] != "弹幕一条" {
		t.Fatalf("degraded repeat = %v", got)
	}
	if ch.called() != 0 {
		t.Fatalf("未配置 LLM 不应调 chatter, calls=%d", ch.called())
	}
	if w.LLMDegraded() != 1 {
		t.Fatalf("degraded=%d, want 1", w.LLMDegraded())
	}
	// 显式降级事件（不许静默）。
	types := em.got()
	if types[0] != "danmaku.anchor_degraded" {
		t.Fatalf("events = %v", types)
	}
	if em.last["reason"] != "llm_not_configured" || em.last["session_id"] != "s-deg" {
		t.Fatalf("event payload = %v", em.last)
	}
}

// ---- block_words 过滤 + 计数（F22）----

func TestWorkerBlockWordsFilter(t *testing.T) {
	svc := NewService(ModeReadAll)
	sp := &fakeSpeaker{}
	st := fakeSettings{"danmaku": {
		"read_interval_ms": 15,
		"block_words":      "违禁,推广\ngamble",
	}}
	w, _ := newTestWorker(svc, sp, nil, st)
	w.OnSessionStart(context.Background(), "s-bw")
	defer w.OnSessionStop(context.Background(), "s-bw")
	svc.Ingest(context.Background(), "chat", "u1", "甲", "这是违禁内容", 0, "", false)
	svc.Ingest(context.Background(), "chat", "u2", "乙", "正常弹幕", 0, "", false)
	svc.Ingest(context.Background(), "chat", "u3", "丙", "来gamble一下", 0, "", false)
	waitCond(t, 2*time.Second, "干净弹幕播出", func() bool { return len(sp.got()) == 1 })
	if got := sp.got(); got[0] != "正常弹幕" {
		t.Fatalf("spoken = %v", got)
	}
	// 第三条（gamble）在下一拍才被过滤，等计数到位。
	waitCond(t, 2*time.Second, "blocked 计数到位", func() bool {
		blocked, _ := svc.ReadStats()
		return blocked == 2
	})
	// 命中弹幕被丢弃，缓冲应排空。
	if _, normal := svc.PendingReads(); normal != 0 {
		t.Fatalf("buffer should drain, normal=%d", normal)
	}
}

// ---- VIP 优先与节奏（F25/F26）----

// manualClock 手动时钟：注入 WorkerOptions.Now，配合直接驱动 beat 消除定时竞态。
type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

func TestWorkerVIPFirstWithNickPrefix(t *testing.T) {
	svc := NewService(ModeReadAll)
	svc.BeginSession("s-vip")
	sp := &fakeSpeaker{}
	clk := &manualClock{t: time.Now()}
	st := fakeSettings{"danmaku": {"read_interval_ms": 15, "vip_interval_ms": 15}}
	// 不启动 goroutine，逐拍手动驱动（注入时钟）：消除 ingest 与 tick 的时序竞态。
	w := NewWorker(svc, sp, nil, st, WorkerOptions{Now: clk.Now})
	svc.Ingest(context.Background(), "chat", "u1", "甲", "普通一", 0, "", false)
	svc.Ingest(context.Background(), "chat", "u2", "乙", "普通二", 0, "", false)
	svc.Ingest(context.Background(), "chat", "u9", "土豪", "贵宾弹幕", 0, "", true)
	for i := 0; i < 3; i++ {
		w.beat(context.Background())
		clk.advance(20 * time.Millisecond)
	}
	got := sp.got()
	if len(got) != 3 {
		t.Fatalf("spoken = %v", got)
	}
	if got[0] != "土豪说贵宾弹幕" {
		t.Fatalf("VIP 应插队且保留“XX说”前缀, got %v", got)
	}
	if got[1] != "普通一" || got[2] != "普通二" {
		t.Fatalf("VIP 后应按原顺序读普通弹幕, got %v", got)
	}
}

func TestWorkerVIPIntervalFaster(t *testing.T) {
	svc := NewService(ModeReadAll)
	sp := &fakeSpeaker{}
	st := fakeSettings{"danmaku": {"read_interval_ms": 5000, "vip_interval_ms": 20}}
	w, _ := newTestWorker(svc, sp, nil, st)
	w.OnSessionStart(context.Background(), "s-vipms")
	defer w.OnSessionStop(context.Background(), "s-vipms")
	// 首条普通弹幕：lastRead 零值 → 立即读；此后普通节奏 5s。
	svc.Ingest(context.Background(), "chat", "u1", "甲", "第一条", 0, "", false)
	waitCond(t, 2*time.Second, "首条播出", func() bool { return len(sp.got()) == 1 })
	t0 := time.Now()
	svc.Ingest(context.Background(), "chat", "u9", "土豪", "贵宾快读", 0, "", true)
	waitCond(t, 2*time.Second, "VIP 播出", func() bool { return len(sp.got()) == 2 })
	if elapsed := time.Since(t0); elapsed > time.Second {
		t.Fatalf("VIP 未走快通道: elapsed=%s（普通节奏 5s 下不可能读出）", elapsed)
	}
	if got := sp.got(); got[1] != "土豪说贵宾快读" {
		t.Fatalf("spoken = %v", got)
	}
}

func TestVIPRegistryAndQueue(t *testing.T) {
	svc := NewService(ModeReadAll)
	svc.BeginSession("s1")
	if svc.IsVIP("土豪") {
		t.Fatal("入账前不应是贵宾")
	}
	svc.Ingest(context.Background(), "chat", "uid9", "土豪", "来啦", 0, "", true)
	if !svc.IsVIP("土豪") || !svc.IsVIP("uid9") {
		t.Fatal("vip 入账后应按 userName/userID 双键可查（F26 供 SetVIP 透传）")
	}
	if svc.IsVIP("路人") {
		t.Fatal("路人不应是贵宾")
	}
	// VIP 进独立队列，不进普通缓冲。
	if vip, normal := svc.PendingReads(); vip != 1 || normal != 0 {
		t.Fatalf("pending: vip=%d normal=%d", vip, normal)
	}
	// 贵宾名单跨场清空。
	svc.BeginSession("s2")
	if svc.IsVIP("土豪") {
		t.Fatal("BeginSession 应清空贵宾名单")
	}
}

func TestVIPQueueCapsAt5(t *testing.T) {
	svc := NewService(ModeReadAll)
	svc.BeginSession("s")
	for i := 0; i < 6; i++ {
		svc.Ingest(context.Background(), "chat", "u", "土豪", string(rune('a'+i)), 0, "", true)
	}
	if vip, _ := svc.PendingReads(); vip != 5 {
		t.Fatalf("vip queue should cap at 5, got %d", vip)
	}
	if _, dropped, _ := svc.Stats(); dropped != 1 {
		t.Fatalf("vip 满 5 丢最旧应计 dropped, got %d", dropped)
	}
	// 最旧的 "a" 被丢，队首应为 "b"。
	it, ok := svc.NextForRead(true, nil)
	if !ok || !it.VIP || it.Text != "b" {
		t.Fatalf("vip head = %+v ok=%v", it, ok)
	}
}

// ---- Service 消费语义（确定性单测，不走时间） ----

func TestNextForReadAnchorTakesLatestDropsOlder(t *testing.T) {
	svc := NewService(ModeAnchor)
	svc.BeginSession("s")
	svc.Ingest(context.Background(), "chat", "u1", "甲", "q1", 0, "", false)
	svc.Ingest(context.Background(), "chat", "u2", "乙", "q2", 0, "", false)
	svc.Ingest(context.Background(), "chat", "u3", "丙", "q3", 0, "", false)
	it, ok := svc.NextForRead(false, nil)
	if !ok || it.Text != "q3" || it.UserName != "丙" {
		t.Fatalf("anchor 应取最新 q3, got %+v ok=%v", it, ok)
	}
	if _, skipped := svc.ReadStats(); skipped != 2 {
		t.Fatalf("更旧两条应计 skipped, got %d", skipped)
	}
	if _, ok := svc.NextForRead(false, nil); ok {
		t.Fatal("缓冲应排空")
	}
}

func TestNextForReadRingCapsAt8NoDedup(t *testing.T) {
	svc := NewService(ModeReadAll)
	svc.BeginSession("s")
	// 同一文本重复 10 次：不去重（蓝本 deque(maxlen=8)），环上限 8。
	for i := 0; i < 10; i++ {
		svc.Ingest(context.Background(), "chat", "u1", "甲", "同一句话", 0, "", false)
	}
	if _, normal := svc.PendingReads(); normal != MaxRecentDanmaku {
		t.Fatalf("ring should cap at %d, got %d", MaxRecentDanmaku, normal)
	}
	n := 0
	for {
		if _, ok := svc.NextForRead(true, nil); !ok {
			break
		}
		n++
	}
	if n != MaxRecentDanmaku {
		t.Fatalf("应消费 %d 条（无去重）, got %d", MaxRecentDanmaku, n)
	}
}

func TestNextForReadSkipsNonChat(t *testing.T) {
	svc := NewService(ModeReadAll)
	svc.BeginSession("s")
	svc.Ingest(context.Background(), "gift", "u1", "甲", "送出礼物", 10, "", false)
	svc.Ingest(context.Background(), "chat", "u2", "乙", "真弹幕", 0, "", false)
	it, ok := svc.NextForRead(true, nil)
	if !ok || it.Kind != "chat" || it.Text != "真弹幕" {
		t.Fatalf("gift/join 不念（F27/G24 后续切片）, got %+v ok=%v", it, ok)
	}
	if _, skipped := svc.ReadStats(); skipped != 1 {
		t.Fatalf("gift 应计 skipped, got %d", skipped)
	}
}

// ---- 生命周期 ----

func TestWorkerStopsWithSession(t *testing.T) {
	svc := NewService(ModeReadAll)
	sp := &fakeSpeaker{}
	st := fakeSettings{"danmaku": {"read_interval_ms": 10}}
	w, _ := newTestWorker(svc, sp, nil, st)
	w.OnSessionStart(context.Background(), "s-life")
	svc.Ingest(context.Background(), "chat", "u1", "甲", "停播前", 0, "", false)
	waitCond(t, 2*time.Second, "停播前播出", func() bool { return len(sp.got()) == 1 })
	w.OnSessionStop(context.Background(), "s-life")
	// 停播后不再消费；新场次开始才清缓冲并恢复。
	svc.Ingest(context.Background(), "chat", "u1", "甲", "停播后", 0, "", false)
	time.Sleep(60 * time.Millisecond)
	if got := sp.got(); len(got) != 1 {
		t.Fatalf("停播后不应再读, got %v", got)
	}
	// 重复 stop 幂等。
	w.OnSessionStop(context.Background(), "s-life")
}

func TestWorkerRestartClearsBuffers(t *testing.T) {
	svc := NewService(ModeReadAll)
	sp := &fakeSpeaker{}
	st := fakeSettings{"danmaku": {"read_interval_ms": 10}}
	w, _ := newTestWorker(svc, sp, nil, st)
	w.OnSessionStart(context.Background(), "s1")
	svc.Ingest(context.Background(), "chat", "u1", "甲", "第一场", 0, "", false)
	waitCond(t, 2*time.Second, "第一场播出", func() bool { return len(sp.got()) == 1 })
	w.OnSessionStop(context.Background(), "s1")
	svc.Ingest(context.Background(), "chat", "u1", "甲", "残留弹幕", 0, "", false)
	// 新场次启动：BeginSession 清掉“残留弹幕”，不应被播出。
	w.OnSessionStart(context.Background(), "s2")
	defer w.OnSessionStop(context.Background(), "s2")
	time.Sleep(60 * time.Millisecond)
	for _, text := range sp.got() {
		if text == "残留弹幕" {
			t.Fatalf("跨场次残留不应播出, got %v", sp.got())
		}
	}
}
