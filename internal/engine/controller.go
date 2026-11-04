// ViduController 是真实引擎适配器：Vidu HTTP 建场 + 信令 WS + 喂流权 + Feeder
// 的组合体，实现 internal/biz/live 的 Controller 与 StatusProvider 接口。
// 硬约束（蓝本 engine.py）：引擎内部重连绝不触发 Manager 层 stop/start（NOT_RESTART）；
// 任何外部依赖失败都如实上抛，绝不静默降级 fake。
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/biz/media"
	"github.com/teamwork/digital-human/internal/engine/tts"
	"github.com/teamwork/digital-human/internal/engine/vidu"
	"github.com/teamwork/digital-human/pkg/audio"
)

// 看门狗与生命周期常量（蓝本 engine.py 节拍）。
const (
	DefaultWatchdogTick    = time.Second
	DefaultStallIdleRecv   = 75 * time.Second
	DefaultStallMinWindow  = 20 * time.Second
	DefaultStallMinSamples = 3
	DefaultStallMaxRate    = 0.55
	DefaultMaxReconnects   = 6
	DefaultReconnectMinGap = 20 * time.Second
	DefaultStallSuppress   = 60 * time.Second
	DefaultIdleTimeout     = 300 * time.Second
	DefaultHardLimit       = 1800 * time.Second
	DefaultGuardInterval   = 5 * time.Second
	DefaultGuardStale      = 30 * time.Second
	DefaultGuardHold       = 30 * time.Second
	defaultJudgeSamples    = 64
)

// Synthesizer 是 TTS 端口的最小面：返回 wav 字节（由适配层保证 24k/s16le/mono）。
type Synthesizer interface {
	Synthesize(ctx context.Context, req tts.JobRequest) ([]byte, error)
}

// FeedSink 按 48B/ms 节奏接收 PCM 分片；实现方是外部播放器（歌曲/口播文件）。
type FeedSink func(ctx context.Context, pcm []byte) error

type ViduConfig struct {
	Client          *vidu.Client
	WS              vidu.WSOptions
	TTS             Synthesizer
	GreetingDecoder media.Decoder
	Voice           string
	Logger          *slog.Logger

	// RecordingEnabled 建场时带 enable_recording=true（装配默认）；
	// 场级 StartOpts.RecordingEnabled 为 true 时同样开启，两者任一成立即带。
	RecordingEnabled bool

	Ownership OwnershipConfig // 可注入以缩短测试等待

	WatchdogTick    time.Duration
	StallIdleRecv   time.Duration
	StallMinWindow  time.Duration
	StallMinSamples int
	StallMaxRate    float64
	MaxReconnects   int
	ReconnectMinGap time.Duration
	StallSuppress   time.Duration
	IdleTimeout     time.Duration
	HardLimit       time.Duration
	GuardInterval   time.Duration
	GuardStale      time.Duration
	GuardHold       time.Duration
}

func (c *ViduConfig) applyDefaults() {
	if c.Logger == nil {
		c.Logger = slog.Default()
	}
	if c.WatchdogTick <= 0 {
		c.WatchdogTick = DefaultWatchdogTick
	}
	if c.StallIdleRecv <= 0 {
		c.StallIdleRecv = DefaultStallIdleRecv
	}
	if c.StallMinWindow <= 0 {
		c.StallMinWindow = DefaultStallMinWindow
	}
	if c.StallMinSamples <= 0 {
		c.StallMinSamples = DefaultStallMinSamples
	}
	if c.StallMaxRate <= 0 {
		c.StallMaxRate = DefaultStallMaxRate
	}
	if c.MaxReconnects <= 0 {
		c.MaxReconnects = DefaultMaxReconnects
	}
	if c.ReconnectMinGap <= 0 {
		c.ReconnectMinGap = DefaultReconnectMinGap
	}
	if c.StallSuppress <= 0 {
		c.StallSuppress = DefaultStallSuppress
	}
	if c.IdleTimeout <= 0 {
		c.IdleTimeout = DefaultIdleTimeout
	}
	if c.HardLimit <= 0 {
		c.HardLimit = DefaultHardLimit
	}
	if c.GuardInterval <= 0 {
		c.GuardInterval = DefaultGuardInterval
	}
	if c.GuardStale <= 0 {
		c.GuardStale = DefaultGuardStale
	}
	if c.GuardHold <= 0 {
		c.GuardHold = DefaultGuardHold
	}
}

// stallJudge 滑动窗口喂流速率判定：窗口≥minWindow 且 采样≥minSamples
// 且 速率≤maxRate（48B/ms 的倍数）时成立；重连后窗口清空并重计抑制期。
type stallJudge struct {
	minWindow  time.Duration
	minSamples int
	maxRate    float64

	ts    []time.Time
	bytes []int64 // 累计已喂字节
}

func (j *stallJudge) observe(t time.Time, fedBytes int64) {
	j.ts = append(j.ts, t)
	j.bytes = append(j.bytes, fedBytes)
	// 窗口只保留最近 4 倍 minWindow，避免陈旧高速样本稀释近期低速。
	cutoff := t.Add(-4 * j.minWindow)
	for len(j.ts) > 0 && j.ts[0].Before(cutoff) {
		j.ts = j.ts[1:]
		j.bytes = j.bytes[1:]
	}
	if len(j.ts) > defaultJudgeSamples {
		j.ts = j.ts[len(j.ts)-defaultJudgeSamples:]
		j.bytes = j.bytes[len(j.bytes)-defaultJudgeSamples:]
	}
}

// stall 返回是否判定为推流 stall。
func (j *stallJudge) stall(idleRecv, minIdle time.Duration) bool {
	if idleRecv < minIdle {
		return false
	}
	if len(j.ts) < j.minSamples {
		return false
	}
	window := j.ts[len(j.ts)-1].Sub(j.ts[0])
	if window < j.minWindow {
		return false
	}
	fed := j.bytes[len(j.bytes)-1] - j.bytes[0]
	expected := float64(window.Milliseconds() * audio.BytesPerMS)
	if expected <= 0 {
		return false
	}
	return float64(fed)/expected <= j.maxRate
}

func (j *stallJudge) reset() { j.ts, j.bytes = nil, nil }

// ViduController 实现 live.Controller（Start/Stop/Say）+ live.StatusProvider。
// 内部断线自动重连（上限 MaxReconnects），对 Manager 透明（NOT_RESTART）。
type ViduController struct {
	cfg  ViduConfig
	log  *slog.Logger
	own  *Ownership
	feed *Feeder

	mu             sync.Mutex
	sig            *vidu.Signaling
	liveID         string
	sessionID      string
	voiceID        string // 当前场音色（v3 §3.4）：Start 时落入，Say 合成读场值
	actionDegraded bool   // G3-2：本场建场发生动作库 403 降级（去 action 重建成功）
	phase          string // idle | live | ended | error
	errMsg         string
	startedAt      time.Time
	lastActivity   time.Time
	reconnects     int32
	lastReconnect  time.Time
	reconnecting   bool
	wsState        string
	stopCh         chan struct{}
	stopped        bool
	wg             sync.WaitGroup

	judge stallJudge

	lastRecvMs atomic.Int64
	fedBytes   atomic.Int64
	lastFeedMs atomic.Int64
	lastPongMs atomic.Int64
	lastLagMs  atomic.Int64
}

var (
	_ live.Controller     = (*ViduController)(nil)
	_ live.StatusProvider = (*ViduController)(nil)
)

// NewViduController 校验必要依赖；缺依赖即失败（装配层据此 fail-closed）。
func NewViduController(cfg ViduConfig) (*ViduController, error) {
	if cfg.Client == nil {
		return nil, errors.New("engine: vidu client is required")
	}
	if cfg.TTS == nil {
		return nil, errors.New("engine: tts synthesizer is required")
	}
	cfg.applyDefaults()
	c := &ViduController{cfg: cfg, log: cfg.Logger, phase: "idle", wsState: "disconnected"}
	c.judge = stallJudge{minWindow: cfg.StallMinWindow, minSamples: cfg.StallMinSamples, maxRate: cfg.StallMaxRate}
	ownCfg := cfg.Ownership
	ownCfg.Hooks.Interrupt = c.interruptOnce
	ownCfg.Hooks.Speaking = c.speaking
	ownCfg.Hooks.LastFeed = func() time.Time { return msToTime(c.lastFeedMs.Load()) }
	c.own = NewOwnership(ownCfg)
	c.feed = NewFeeder(FeedConfig{
		ChunkMS: audio.DefaultChunkMS,
		OnLate: func(delay time.Duration) {
			if -delay > 0 {
				c.lastLagMs.Store((-delay).Milliseconds())
			}
		},
	})
	return c, nil
}

func msToTime(ms int64) time.Time {
	if ms <= 0 {
		return time.Time{}
	}
	return time.UnixMilli(ms)
}

// interruptOnce 是喂流权 type7 的唯一出口（经 Ownership 单点调用）。
func (c *ViduController) interruptOnce(ctx context.Context) error {
	sig := c.currentSig()
	if sig == nil {
		return nil
	}
	return sig.Interrupt(ctx)
}

// speaking 以最近 300ms 内确有 PCM 喂出为准。
func (c *ViduController) speaking() bool {
	last := msToTime(c.lastFeedMs.Load())
	return !last.IsZero() && time.Since(last) < 300*time.Millisecond
}

func (c *ViduController) currentSig() *vidu.Signaling {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sig
}

// Start 建场 → 连信令 → conn_init → 起看门狗；返回真实 liveID。
// 建场 body 由 opts 构造（v3 §3.1/§4）：model + image_uri|avatar_id（URI 优先，
// 蓝本 engine.py:258-270）+ 可选 action 段；RefImageURIs 不进建场 body
// （会话运行中经 prompt_operation 推送，属 M5）；opts.VoiceID 落入当前场状态。
func (c *ViduController) Start(ctx context.Context, opts live.StartOpts) (string, error) {
	c.mu.Lock()
	if c.phase == "live" {
		c.mu.Unlock()
		return "", errors.New("engine: already live")
	}
	c.mu.Unlock()

	model := opts.Model
	if model == "" {
		model = "vidu-s2"
	}
	body := map[string]any{"session_id": opts.SessionID, "model": model}
	if opts.ViduImageURI != "" {
		body["image_uri"] = opts.ViduImageURI
	} else if opts.ViduAvatarID != "" {
		body["avatar_id"] = opts.ViduAvatarID
	}
	if opts.ActionLibraryID != "" {
		// action 段格式对齐蓝本动作库建场参数（字段名待真机核验，H 系假设）。
		body["action"] = map[string]any{"library_id": opts.ActionLibraryID}
	}
	if c.cfg.RecordingEnabled || opts.RecordingEnabled {
		body["enable_recording"] = true
	}
	resp, err := c.cfg.Client.CreateSession(ctx, body)
	degraded := false
	if err != nil {
		var apiErr *vidu.APIError
		// 动作库 403 降级（v3 §3.3 时序，G3-2）：配置了动作库且平台拒绝 action 段
		// → 去 action 重建一次；成功记降级标记（Manager 发 action_library_degraded
		// 事件），仍失败才如实上抛。
		if errors.As(err, &apiErr) && apiErr.ActionForbidden && opts.ActionLibraryID != "" {
			delete(body, "action")
			resp, err = c.cfg.Client.CreateSession(ctx, body)
			if err == nil {
				degraded = true
				c.log.Warn("vidu 动作库被 403 拒绝，已去 action 重建成功",
					"action_library_id", opts.ActionLibraryID)
			}
		}
		if err != nil {
			return "", err
		}
	}
	liveID, _ := resp["live_id"].(string)
	if liveID == "" {
		liveID, _ = resp["id"].(string)
	}
	if liveID == "" {
		return "", fmt.Errorf("engine: create session 返回缺少 live_id: %v", resp)
	}

	sig, err := c.dial(ctx, liveID)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.sig = sig
	c.liveID = liveID
	c.sessionID = opts.SessionID
	c.voiceID = opts.VoiceID
	c.actionDegraded = degraded
	c.phase = "live"
	c.errMsg = ""
	c.startedAt = time.Now()
	c.lastActivity = c.startedAt
	c.reconnects = 0
	c.lastReconnect = time.Time{}
	c.wsState = "connected"
	c.stopped = false
	c.stopCh = make(chan struct{})
	c.mu.Unlock()
	c.judge.reset()
	c.fedBytes.Store(0)
	c.lastRecvMs.Store(time.Now().UnixMilli())

	c.wg.Add(2)
	go c.eventLoop(sig)
	go c.watchdog()
	c.log.Info("vidu live started", "live_id", liveID, "session_id", opts.SessionID)
	return liveID, nil
}

// dial 建立一条信令连接并完成 conn_init。
func (c *ViduController) dial(ctx context.Context, liveID string) (*vidu.Signaling, error) {
	opt := c.cfg.WS
	opt.OnPong = func(t time.Time) { c.lastPongMs.Store(t.UnixMilli()) }
	sig := vidu.NewSignaling(liveID, "", opt)
	if err := sig.Connect(ctx); err != nil {
		return nil, err
	}
	if err := sig.ConnInit(ctx); err != nil {
		_ = sig.Close()
		return nil, err
	}
	return sig, nil
}

// Stop 主动停场：hangup + 关信令 + 停看门狗。幂等。
func (c *ViduController) Stop(ctx context.Context, reason string) error {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		c.wg.Wait()
		return nil
	}
	c.stopped = true
	c.phase = "ended"
	sig := c.sig
	stopCh := c.stopCh
	c.mu.Unlock()
	if stopCh != nil {
		close(stopCh)
	}
	if sig != nil {
		_ = sig.Hangup(ctx, reason)
		_ = sig.Close()
	}
	c.wg.Wait()
	return nil
}

// Say 口播一句：取喂流权（可抢占）→ type10 文本 → TTS wav → 48B/ms 喂流。
// TODO(待真机)：type10 文本帧与 TTS 音频帧在 Vidu 侧的严格时序（先文后声/并行）
// 需真网联调确认；当前实现为 先 type10 文本、后 PCM 喂流。
func (c *ViduController) Say(ctx context.Context, text, mode string) (string, error) {
	if strings.TrimSpace(text) == "" {
		return "", errors.New("engine: empty text")
	}
	c.mu.Lock()
	liveOK := c.phase == "live"
	c.mu.Unlock()
	if !liveOK {
		return "", errors.New("engine: not live")
	}
	id := uuid.NewString()
	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	task := Task{
		ID:     "say-" + id,
		Chat:   mode == "chat",
		Cancel: cancel,
		Done:   taskCtx.Done(),
	}
	lease, err := c.own.Acquire(ctx, Request{Task: task, Preempt: true})
	if err != nil {
		return "", err
	}
	defer lease.Release()

	sig := c.currentSig()
	if sig == nil {
		return "", errors.New("engine: signaling unavailable")
	}
	// type10 口播文本先行。
	if err := sig.SendOutputText(ctx, text); err != nil && !errors.Is(err, vidu.ErrNotReady) {
		return "", err
	}

	// voice_id 时序（v3 §3.4）：Say 合成读当前场值，空回落装配默认 cfg.Voice。
	c.mu.Lock()
	voice := c.voiceID
	c.mu.Unlock()
	if voice == "" {
		voice = c.cfg.Voice
	}
	wav, err := c.cfg.TTS.Synthesize(ctx, tts.JobRequest{Text: text, VoiceID: voice, Format: "wav"})
	if err != nil {
		return "", err
	}
	pcm, err := audio.PCMFromWav(wav)
	if err != nil {
		return "", fmt.Errorf("engine: tts wav 不合规: %w", err)
	}
	sender := &leaseSender{sig: sig, lease: lease, onSent: c.noteFed}
	_, err = c.feed.Feed(ctx, sender, pcm, func() bool {
		return !lease.Valid() || c.isStopped()
	})
	if err != nil && !errors.Is(err, ErrFeedInterrupted) && !errors.Is(err, ErrFeedStopped) {
		return "", err
	}
	c.mu.Lock()
	c.lastActivity = time.Now()
	c.mu.Unlock()
	return id, nil
}

// PlayGreeting 解码配置的音频文件并直接喂 PCM；不发送 type10，也不经过 TTS。
// 文件缺失、损坏、格式不支持或解码器未配置均显式报错，由 Manager 决定降级事件。
func (c *ViduController) PlayGreeting(ctx context.Context, file string) (string, error) {
	file = strings.TrimSpace(file)
	if file == "" {
		return "", errors.New("engine: empty greeting audio file")
	}
	c.mu.Lock()
	liveOK := c.phase == "live"
	c.mu.Unlock()
	if !liveOK {
		return "", errors.New("engine: not live")
	}
	if c.cfg.GreetingDecoder == nil {
		return "", errors.New("engine: greeting audio decoder not configured")
	}
	pcm, err := c.cfg.GreetingDecoder.Decode(ctx, file)
	if err != nil {
		return "", fmt.Errorf("engine: greeting audio decode: %w", err)
	}
	if len(pcm) == 0 {
		return "", errors.New("engine: greeting audio decode returned empty PCM")
	}

	id := uuid.NewString()
	taskCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	lease, err := c.own.Acquire(ctx, Request{Task: Task{
		ID: "greeting-" + id, Cancel: cancel, Done: taskCtx.Done(),
	}, Preempt: true})
	if err != nil {
		return "", err
	}
	defer lease.Release()
	sig := c.currentSig()
	if sig == nil {
		return "", errors.New("engine: signaling unavailable")
	}
	sender := &leaseSender{sig: sig, lease: lease, onSent: c.noteFed}
	_, err = c.feed.Feed(ctx, sender, pcm, func() bool {
		return !lease.Valid() || c.isStopped()
	})
	if err != nil && !errors.Is(err, ErrFeedInterrupted) && !errors.Is(err, ErrFeedStopped) {
		return "", err
	}
	return id, nil
}

// NewFeedSink 供外部播放器（歌曲/口播文件）喂流：首次调用自动取喂流权，
// 该 ctx 结束时自动释放。song=true 标记歌曲持有者（守卫豁免、抢占受限）。
func (c *ViduController) NewFeedSink(kind string, song bool) FeedSink {
	var mu sync.Mutex
	var lease *Lease
	return func(ctx context.Context, pcm []byte) error {
		mu.Lock()
		if lease == nil {
			taskCtx, cancel := context.WithCancel(context.Background())
			l, err := c.own.Acquire(ctx, Request{
				Task:    Task{ID: kind + "-" + uuid.NewString(), Song: song, Cancel: cancel, Done: taskCtx.Done()},
				Preempt: !song,
			})
			if err != nil {
				mu.Unlock()
				cancel()
				return err
			}
			lease = l
			go func() {
				<-ctx.Done()
				mu.Lock()
				if lease != nil {
					lease.Release()
					lease = nil
				}
				cancel()
				mu.Unlock()
			}()
		}
		l := lease
		mu.Unlock()
		if !l.Valid() {
			return ErrFeedInterrupted
		}
		sig := c.currentSig()
		if sig == nil {
			return errors.New("engine: not live")
		}
		if err := sig.SendPCM(ctx, pcm); err != nil {
			return err
		}
		c.noteFed(int64(len(pcm)))
		return nil
	}
}

// leaseSender 写 WS 前强制校验喂流权；旧 gen 的帧直接拒绝。
type leaseSender struct {
	sig    *vidu.Signaling
	lease  *Lease
	onSent func(int64)
}

func (s *leaseSender) SendPCM(ctx context.Context, pcm []byte) error {
	if !s.lease.Valid() {
		return ErrFeedInterrupted
	}
	if err := s.sig.SendPCM(ctx, pcm); err != nil {
		return err
	}
	if s.onSent != nil {
		s.onSent(int64(len(pcm)))
	}
	return nil
}

func (c *ViduController) noteFed(n int64) {
	c.fedBytes.Add(n)
	now := time.Now()
	c.lastFeedMs.Store(now.UnixMilli())
	c.mu.Lock()
	c.lastActivity = now
	c.mu.Unlock()
}

func (c *ViduController) isStopped() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.stopped
}

// eventLoop 消费信令下行事件；WS 关闭时在限额内自动重连（NOT_RESTART）。
// 只有当前在用的 sig 关闭才触发重连；被重连替换掉的旧连接静默退出。
func (c *ViduController) eventLoop(sig *vidu.Signaling) {
	defer c.wg.Done()
	for ev := range sig.Events() {
		c.lastRecvMs.Store(time.Now().UnixMilli())
		if ev.Type == vidu.TypeWSClosed {
			break
		}
	}
	c.mu.Lock()
	stale := c.sig != sig
	stopped := c.stopped
	c.mu.Unlock()
	if stopped || stale {
		return
	}
	c.setWSState("disconnected")
	c.requestReconnect("ws_closed")
}

// watchdog 1s 节拍：stall 判定 / 闲置 / 硬上限；音频守卫走独立 5s 节拍。
func (c *ViduController) watchdog() {
	defer c.wg.Done()
	tick := c.cfg.WatchdogTick
	guardEvery := int(c.cfg.GuardInterval / tick)
	if guardEvery < 1 {
		guardEvery = 1
	}
	n := 0
	t := time.NewTicker(tick)
	defer t.Stop()
	for {
		select {
		case <-c.stopCh:
			return
		case now := <-t.C:
			if c.tickOnce(now) {
				return
			}
			n++
			if n%guardEvery == 0 {
				if _, err := c.own.RevokeStale(context.Background(), c.cfg.GuardStale, c.cfg.GuardHold); err != nil {
					c.log.Warn("audio guard revoke failed", "err", err)
				}
			}
		}
	}
}

// tickOnce 返回 true 表示看门狗应退出（场次已终结）。
func (c *ViduController) tickOnce(now time.Time) bool {
	c.mu.Lock()
	started, lastAct := c.startedAt, c.lastActivity
	lastRec := c.lastReconnect
	wsDown := c.wsState != "connected" && c.phase == "live"
	c.mu.Unlock()

	// WS 断线重连兜底：requestReconnect 被最小间隔丢弃时，由看门狗节拍补试。
	if wsDown {
		c.requestReconnect("ws_retry")
	}

	// 硬上限 1800s：无条件终结。
	if now.Sub(started) >= c.cfg.HardLimit {
		c.terminateLocal("hard_limit_1800s")
		return true
	}
	// 闲置 300s：无 Say/喂流活动即收场。
	if now.Sub(lastAct) >= c.cfg.IdleTimeout {
		c.terminateLocal("idle_timeout_300s")
		return true
	}

	c.judge.observe(now, c.fedBytes.Load())
	idleRecv := time.Duration(now.UnixMilli()-c.lastRecvMs.Load()) * time.Millisecond
	// 距上次重连 <60s 不判 stall（给链路回暖时间）。
	if !lastRec.IsZero() && now.Sub(lastRec) < c.cfg.StallSuppress {
		return false
	}
	if c.judge.stall(idleRecv, c.cfg.StallIdleRecv) {
		c.log.Warn("feed stall detected, reconnecting", "idle_recv", idleRecv)
		c.requestReconnect("feed_stall")
		c.judge.reset()
	}
	return false
}

// terminateLocal 本地终结场次：不触碰 Manager，只关闭引擎侧资源并落 error 态。
func (c *ViduController) terminateLocal(reason string) {
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		return
	}
	c.stopped = true
	c.phase = "error"
	c.errMsg = reason
	sig := c.sig
	stopCh := c.stopCh
	c.mu.Unlock()
	if stopCh != nil {
		close(stopCh)
	}
	if sig != nil {
		_ = sig.Hangup(context.Background(), reason)
		_ = sig.Close()
	}
	c.log.Warn("vidu live terminated locally", "reason", reason, "live_id", c.liveID)
}

// requestReconnect 受限额与最小间隔约束地重连信令；并发调用只会放行一次。
func (c *ViduController) requestReconnect(reason string) {
	c.mu.Lock()
	if c.stopped || c.phase != "live" || c.reconnecting {
		c.mu.Unlock()
		return
	}
	if int(c.reconnects) >= c.cfg.MaxReconnects {
		c.mu.Unlock()
		c.log.Error("vidu reconnect budget exhausted", "live_id", c.liveID, "max", c.cfg.MaxReconnects)
		c.terminateLocal("reconnect_exhausted")
		return
	}
	now := time.Now()
	if !c.lastReconnect.IsZero() && now.Sub(c.lastReconnect) < c.cfg.ReconnectMinGap {
		c.mu.Unlock()
		return // 距上次重连 <20s：下一拍再试
	}
	c.reconnects++
	c.lastReconnect = now
	c.reconnecting = true
	c.wsState = "reconnecting"
	old := c.sig
	liveID := c.liveID
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.reconnecting = false
		c.mu.Unlock()
	}()

	if old != nil {
		_ = old.Close()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	sig, err := c.dial(ctx, liveID)
	cancel()
	if err != nil {
		c.log.Warn("vidu reconnect failed", "reason", reason, "err", err)
		c.setWSState("disconnected")
		return
	}
	c.mu.Lock()
	if c.stopped {
		c.mu.Unlock()
		_ = sig.Close()
		return
	}
	c.sig = sig
	c.wsState = "connected"
	c.mu.Unlock()
	c.judge.reset()
	c.lastRecvMs.Store(time.Now().UnixMilli())
	c.wg.Add(1)
	go c.eventLoop(sig)
	c.log.Info("vidu signaling reconnected", "reason", reason, "live_id", liveID, "reconnects", c.reconnects)
}

func (c *ViduController) setWSState(s string) {
	c.mu.Lock()
	c.wsState = s
	c.mu.Unlock()
}

// RuntimeStatus 实现 live.StatusProvider：feed 与 ws 状态全部来自真实运行值。
func (c *ViduController) RuntimeStatus(context.Context) live.RuntimeStatus {
	_, kind, _, owned := c.own.OwnerInfo()
	c.mu.Lock()
	st := live.RuntimeStatus{
		FeedKnown:        true,
		FeedOwner:        owned,
		FeedKind:         kind,
		FeedEpoch:        c.own.Gen(),
		FeedLagMs:        c.lastLagMs.Load(),
		WSKnown:          true,
		WSState:          c.wsState,
		WSReconnectTotal: c.reconnects,
		WSPongMs:         c.lastPongMs.Load(),
	}
	c.mu.Unlock()
	return st
}

// LiveID 返回当前 Vidu 场次 ID（未开播为空）。
func (c *ViduController) LiveID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.liveID
}

// PromptOperation 是当前场 type11 参考图操作出口（v3 §3.3 时序，G3-1 生产链路）；
// 未开播/信令未就绪返回 vidu.ErrNotReady（refs 侧据此如实降级 refs_push_failed）。
func (c *ViduController) PromptOperation(ctx context.Context, opType string, images []map[string]any) error {
	sig := c.currentSig()
	if sig == nil {
		return vidu.ErrNotReady
	}
	return sig.PromptOperation(ctx, opType, images)
}

// ActionDegraded 报告本场建场是否发生动作库 403 降级（去 action 重建成功，G3-2）；
// Manager 据此发布 action_library_degraded 事件。
func (c *ViduController) ActionDegraded() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.actionDegraded
}

// phaseString 返回内部阶段（idle/live/ended/error），供诊断与测试断言。
func (c *ViduController) phaseString() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.phase
}
