package billing

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// DefaultPollInterval 蓝本口径：credits 轮询间隔 max(30, credits_poll_s)，默认 60s。
const (
	DefaultPollInterval = 60 * time.Second
	MinPollInterval     = 30 * time.Second
)

// CreditsSource 返回当前剩余额度（credits.remain）；Vidu 实现由装配层适配。
type CreditsSource interface {
	Credits(ctx context.Context) (remain int64, err error)
}

// PollerOptions 轮询参数；EmitEvent 可选，用于把连续失败以事件形式透出（复盘可见）。
type PollerOptions struct {
	// Interval 轮询间隔；<=0 用 DefaultPollInterval。配置入口在装配层做 max(30s,.) 截断，
	// 本结构不做下限检查以便契约测试用毫秒级间隔。
	Interval time.Duration
	Logger   *slog.Logger
	// EmitEvent 连续失败事件回调（sessionID 由 Poller 填充）；nil 时只记日志。
	EmitEvent func(typ string, payload map[string]any)
}

// Poller F36 额度轮询落账：开播记基线并周期查询 Vidu credits，
// 差值（累计消耗）随余额写入 CostLedger；下播停止并落最终一笔。
// 查询失败重试不中断直播；连续失败只记日志+事件，永不回传错误到直播主流程。
type Poller struct {
	svc *Service
	src CreditsSource
	opt PollerOptions

	mu       sync.Mutex
	cancel   context.CancelFunc
	session  string
	running  bool
	baseline int64
	baseOK   bool
	last     int64
	failSeq  int
	startMs  int64
}

func NewPoller(svc *Service, src CreditsSource, opt PollerOptions) *Poller {
	if opt.Interval <= 0 {
		opt.Interval = DefaultPollInterval
	}
	if opt.Logger == nil {
		opt.Logger = slog.Default()
	}
	return &Poller{svc: svc, src: src, opt: opt}
}

// OnSessionStart 开播成功后调用：立即查一次作基线，随后启动周期轮询。
// 重复调用会先停掉上一场轮询（单场次语义与 Manager 一致）。
func (p *Poller) OnSessionStart(ctx context.Context, sessionID string) {
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
	}
	p.session = sessionID
	p.baseOK = false
	p.failSeq = 0
	p.startMs = time.Now().UnixMilli()
	p.running = true
	ctx, cancel := context.WithCancel(context.Background())
	p.cancel = cancel
	p.mu.Unlock()

	// 基线查询失败不阻塞开播：首个成功 tick 补基线。
	if remain, err := p.src.Credits(ctx); err == nil {
		p.mu.Lock()
		p.baseline, p.last, p.baseOK = remain, remain, true
		p.mu.Unlock()
		p.save(ctx, remain, false)
	} else {
		p.opt.Logger.Warn("credits baseline query failed; will retry on ticks", "session", sessionID, "err", err)
	}
	go p.loop(ctx, sessionID)
}

// OnSessionStop 下播后调用：停止轮询并落最终一笔（EndedAtMs 非零）。
func (p *Poller) OnSessionStop(ctx context.Context, sessionID string) {
	p.mu.Lock()
	if p.cancel != nil {
		p.cancel()
		p.cancel = nil
	}
	running := p.running && p.session == sessionID
	p.running = false
	p.mu.Unlock()
	if !running {
		return
	}
	if remain, err := p.src.Credits(ctx); err == nil {
		p.save(ctx, remain, true)
	} else {
		p.opt.Logger.Warn("credits final query failed; ledger keeps last known balance", "session", sessionID, "err", err)
	}
}

func (p *Poller) loop(ctx context.Context, sessionID string) {
	t := time.NewTicker(p.opt.Interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			remain, err := p.src.Credits(ctx)
			if err != nil {
				p.mu.Lock()
				p.failSeq++
				fails := p.failSeq
				p.mu.Unlock()
				p.opt.Logger.Warn("credits poll failed", "session", sessionID, "consecutive", fails, "err", err)
				if p.opt.EmitEvent != nil && fails >= 3 {
					p.opt.EmitEvent("billing.credits_poll_failed", map[string]any{
						"session_id": sessionID, "consecutive": fails, "error": err.Error(),
					})
				}
				continue
			}
			p.mu.Lock()
			p.failSeq = 0
			if !p.baseOK {
				p.baseline, p.baseOK = remain, true
			}
			p.last = remain
			p.mu.Unlock()
			p.save(ctx, remain, false)
		}
	}
}

// save 把当前余额落账；credits_spent 由 Service.Upsert 按 start-end 计算。
func (p *Poller) save(ctx context.Context, remain int64, final bool) {
	p.mu.Lock()
	l := Ledger{
		SessionID:    p.session,
		CreditsStart: p.baseline,
		CreditsEnd:   remain,
		StartedAtMs:  p.startMs,
		Source:       "vidu_poll",
		CostSource:   "vidu_credits_api",
	}
	if final {
		l.EndedAtMs = time.Now().UnixMilli()
	}
	p.mu.Unlock()
	if _, err := p.svc.Upsert(ctx, l); err != nil {
		p.opt.Logger.Warn("credits ledger save failed", "session", l.SessionID, "err", err)
	}
}
