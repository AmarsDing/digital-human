// worker.go 弹幕批读消费循环（F24/F22/F25/F26）。
// 蓝本 engine.py _danmaku_worker：节奏闸门 read_interval_ms（默认 20000ms）每拍取件播报；
// 送礼观众（vip）进优先队列插队、走更短的 vip 间隔（蓝本 vip_min_interval_ms 默认 5000，
// 本端 settings 默认 vip_interval_ms=2000）；dm_anchor 模式逐条“复读 + LLM 完整回答”。
// 本切片不实现蓝本冷场收集窗（1.2s）与 idle_chat 空闲搭话：那是 _idle_chat_loop 的职责，
// 需另立切片（依赖 session.idle_chat_seconds 配置与 last_activity 追踪，本清单未列）。
package danmaku

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/teamwork/digital-human/internal/biz/chat"
)

// Speaker 播报出口：文本入 TTS/口播链路；装配层接 live.Manager.Say。
type Speaker interface {
	Say(ctx context.Context, text string) error
}

// Chatter LLM 对话端口（dm_anchor 模式的完整回答）；由 chat.Manager 实现。
type Chatter interface {
	Chat(ctx context.Context, key, userText string, sink chat.SentenceSink) (string, error)
}

// SettingsProvider 设置只读端口（settings.Service 实现）：
// 读 danmaku 组节奏/词表与 llm.api_key（判 LLM 是否可用）。
type SettingsProvider interface {
	Value(ctx context.Context, groupID, key string) any
}

const (
	// DefaultTick 主循环拍子；节奏闸门由 settings 控制，tick 只是醒来看一眼的粒度。
	DefaultTick = 100 * time.Millisecond
	// DefaultReadIntervalMs 普通弹幕节奏默认值：对齐蓝本 read_interval_ms 默认 20000。
	DefaultReadIntervalMs = 20000
	// DefaultVIPIntervalMs VIP 快通道节奏默认值：settings 默认 2000（蓝本 vip_min_interval_ms 默认 5000，取值沿用前端 schema 既有默认）。
	DefaultVIPIntervalMs = 2000
	// settingsCacheTTL 设置热读缓存：对齐蓝本 _dan_conf 的 1 秒缓存（避免每拍全量 Load）。
	settingsCacheTTL = time.Second
)

// WorkerOptions worker 参数；EmitEvent 可选，用于把 LLM 降级等状态以事件透出（复盘可见）。
type WorkerOptions struct {
	Tick      time.Duration
	Logger    *slog.Logger
	EmitEvent func(typ string, payload map[string]any)
	Now       func() time.Time // 测试注入时钟；nil 用 time.Now
}

// danmakuCfg 是 worker 热读的设置快照。
type danmakuCfg struct {
	readMs   int
	vipMs    int
	block    []string
	llmReady bool
}

// Worker 弹幕批读消费循环：随开播启动、停播停止（live.Manager 生命周期钩子，
// 参照 billing.Poller 的 OnSessionStart/OnSessionStop 模式）。
type Worker struct {
	svc *Service
	sp  Speaker
	ch  Chatter
	st  SettingsProvider
	opt WorkerOptions

	mu          sync.Mutex
	running     bool
	cancel      context.CancelFunc
	done        chan struct{}
	sessionID   string
	lastRead    time.Time
	cfgAt       time.Time
	cfg         danmakuCfg
	llmDegraded int64
}

func NewWorker(svc *Service, sp Speaker, ch Chatter, st SettingsProvider, opt WorkerOptions) *Worker {
	if opt.Tick <= 0 {
		opt.Tick = DefaultTick
	}
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	if opt.Now == nil {
		opt.Now = time.Now
	}
	return &Worker{svc: svc, sp: sp, ch: ch, st: st, opt: opt}
}

// OnSessionStart 开播成功后启动消费循环。重复调用幂等（运行中直接返回）。
// ctx 只用于本次启动动作本身，worker 生命周期由内部 cancel 控制（与 billing.Poller 一致）。
func (w *Worker) OnSessionStart(_ context.Context, sessionID string) {
	w.mu.Lock()
	if w.running {
		w.mu.Unlock()
		return
	}
	w.running = true
	w.sessionID = sessionID
	w.lastRead = time.Time{} // 零值 → 开播后首条弹幕到即读（对齐蓝本 _dan_last_read 初始 0）
	ctx, cancel := context.WithCancel(context.Background())
	w.cancel = cancel
	w.done = make(chan struct{})
	done := w.done
	w.mu.Unlock()

	// worker 自洽绑定场次（幂等）：清空上一场缓冲/计数/贵宾名单，避免跨场次串数据。
	w.svc.BeginSession(sessionID)
	go w.loop(ctx, done)
}

// OnSessionStop 停播后停止消费循环并等其退出。
func (w *Worker) OnSessionStop(_ context.Context, sessionID string) {
	w.mu.Lock()
	if !w.running || (sessionID != "" && w.sessionID != sessionID) {
		w.mu.Unlock()
		return
	}
	cancel, done := w.cancel, w.done
	w.running = false
	w.cancel = nil
	w.done = nil
	w.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
}

// LLMDegraded 返回 dm_anchor 因 LLM 未配置降级为只复读的次数（显式降级证据，不许静默）。
func (w *Worker) LLMDegraded() int64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.llmDegraded
}

func (w *Worker) loop(ctx context.Context, done chan struct{}) {
	defer close(done)
	tick := time.NewTicker(w.opt.Tick)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			w.beat(ctx)
		}
	}
}

// beat 单拍：mode 非批读模式不消费；节奏闸门未到不消费；空队列不消费。
func (w *Worker) beat(ctx context.Context) {
	mode := w.svc.Mode()
	if mode != ModeReadAll && mode != ModeAnchor {
		return
	}
	cfg := w.config(ctx)
	vipPending, normal := w.svc.PendingReads()
	if vipPending+normal == 0 {
		return
	}
	// 节奏闸门（蓝本：VIP 走自己的更短最小间隔，但同样要等，不连播刷屏）。
	intervalMs := cfg.readMs
	if vipPending > 0 {
		intervalMs = cfg.vipMs
	}
	w.mu.Lock()
	last := w.lastRead
	w.mu.Unlock()
	if !last.IsZero() && w.opt.Now().Sub(last) < time.Duration(intervalMs)*time.Millisecond {
		return
	}
	it, ok := w.svc.NextForRead(mode == ModeReadAll, cfg.block)
	if !ok {
		return
	}
	w.mu.Lock()
	w.lastRead = w.opt.Now()
	sessionID := w.sessionID
	w.mu.Unlock()
	w.speak(ctx, mode, sessionID, it, cfg)
}

// speak 先复读（VIP 保留“XX说”前缀，对齐蓝本 _dan_read_text 的 vip_nick 默认 true），
// dm_anchor 模式再经 chat.Manager 给 LLM 完整回答（逐句入 Speaker）。
func (w *Worker) speak(ctx context.Context, mode Mode, sessionID string, it ConsumeItem, cfg danmakuCfg) {
	text := readText(it)
	if text == "" || w.sp == nil {
		return
	}
	if err := w.sp.Say(ctx, text); err != nil {
		w.opt.Logger.Warn("danmaku read-aloud failed", "text", text, "err", err)
		return
	}
	if mode != ModeAnchor {
		return
	}
	if w.ch == nil || !cfg.llmReady {
		// 未配置 LLM（无 api_key）：明确降级为只复读——记日志、计数、发事件，绝不静默。
		w.mu.Lock()
		w.llmDegraded++
		w.mu.Unlock()
		w.opt.Logger.Warn("dm_anchor degraded to repeat-only: llm not configured")
		w.emit("danmaku.anchor_degraded", map[string]any{"reason": "llm_not_configured"})
		return
	}
	prompt := anchorPrompt(it)
	if prompt == "" {
		return
	}
	// chat key 用场次 ID：与 live.Manager.ResetLlm → chat.ResetHistory(sessionID) 口径一致。
	_, err := w.ch.Chat(ctx, sessionID, prompt, func(ctx context.Context, s string) error {
		return w.sp.Say(ctx, s)
	})
	if err != nil {
		// 复读已播出；回答失败显式记日志（不许静默），不中断 worker。
		w.opt.Logger.Warn("dm_anchor llm answer failed", "err", err)
	}
}

func (w *Worker) emit(typ string, payload map[string]any) {
	if w.opt.EmitEvent == nil {
		return
	}
	if payload == nil {
		payload = map[string]any{}
	}
	w.mu.Lock()
	payload["session_id"] = w.sessionID
	w.mu.Unlock()
	w.opt.EmitEvent(typ, payload)
}

// config 读设置快照（1 秒缓存，对齐蓝本 _dan_conf 热读）；settings 缺失时回落默认。
func (w *Worker) config(ctx context.Context) danmakuCfg {
	now := w.opt.Now()
	w.mu.Lock()
	if !w.cfgAt.IsZero() && now.Sub(w.cfgAt) < settingsCacheTTL {
		c := w.cfg
		w.mu.Unlock()
		return c
	}
	w.mu.Unlock()

	c := danmakuCfg{readMs: DefaultReadIntervalMs, vipMs: DefaultVIPIntervalMs}
	if w.st != nil {
		c.readMs = settingsInt(w.st.Value(ctx, "danmaku", "read_interval_ms"), DefaultReadIntervalMs)
		c.vipMs = settingsInt(w.st.Value(ctx, "danmaku", "vip_interval_ms"), DefaultVIPIntervalMs)
		c.block = splitBlockWords(settingsStr(w.st.Value(ctx, "danmaku", "block_words")))
		c.llmReady = strings.TrimSpace(settingsStr(w.st.Value(ctx, "llm", "api_key"))) != ""
	}
	// 蓝本 max(1.0, interval_ms)：下限 1ms，防止 0/负值变成忙循环。
	if c.readMs < 1 {
		c.readMs = 1
	}
	if c.vipMs < 1 {
		c.vipMs = 1
	}
	w.mu.Lock()
	w.cfg, w.cfgAt = c, now
	w.mu.Unlock()
	return c
}

// readText 复读文本（蓝本 _dan_read_text）：普通弹幕只念内容；VIP 送礼观众保留“XX说”。
func readText(it ConsumeItem) string {
	content := strings.TrimSpace(it.Text)
	who := strings.TrimSpace(it.UserName)
	if it.VIP && who != "" {
		return who + "说" + content
	}
	return content
}

// anchorPrompt 弹幕主播提示词（蓝本 _danmaku_prompt）：打上“直播间弹幕 + 观众名”标签，
// 指示 LLM 别复述原话。
func anchorPrompt(it ConsumeItem) string {
	who := strings.TrimSpace(it.UserName)
	content := strings.TrimSpace(it.Text)
	if who == "" || content == "" {
		return content
	}
	return fmt.Sprintf("【直播间弹幕】观众「%s」说：%s\n（用主播的口吻自然接话：可以叫他的名字，一到两句就够，不要复述他说的话）", who, content)
}

// splitBlockWords 解析 settings danmaku.block_words：逗号（中英文）/换行分隔，去空白空项。
func splitBlockWords(raw string) []string {
	var out []string
	for _, piece := range strings.FieldsFunc(raw, func(r rune) bool {
		return r == ',' || r == '，' || r == '\n' || r == '\r'
	}) {
		if w := strings.TrimSpace(piece); w != "" {
			out = append(out, w)
		}
	}
	return out
}

func settingsStr(v any) string {
	s, _ := v.(string)
	return s
}

func settingsInt(v any, def int) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case string:
		var i int
		if _, err := fmt.Sscanf(strings.TrimSpace(n), "%d", &i); err == nil {
			return i
		}
	}
	return def
}
