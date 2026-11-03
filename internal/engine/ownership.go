package engine

import (
	"context"
	"errors"
	"sync"
	"time"
)

// 喂流权参数来自蓝本 engine.py:831-938：
// 非强制路径最多 4 轮、每轮等旧持有者 ≤1.5s；排队上限 25s；
// 25s 后若持有者最近 5s 仍在喂流则继续等；强制收回前发 type7 并静默 250ms。
const (
	DefaultPreemptRounds   = 4
	DefaultRoundWait       = 1500 * time.Millisecond
	DefaultQueueWaitMax    = 25 * time.Second
	DefaultStillFeedWindow = 5 * time.Second
	DefaultInterruptQuiet  = 250 * time.Millisecond
	DefaultQueuePoll       = 100 * time.Millisecond
	DefaultRoundPoll       = 50 * time.Millisecond
	// 歌曲/聊天持有者不强制取消；每轮等待延长的值。
	DefaultProtectedWaitMax = 30 * time.Second
)

// Clock 抽象时钟，测试可注入虚拟时间。
type Clock interface {
	Now() time.Time
	Sleep(ctx context.Context, d time.Duration) error
}

type realClock struct{}

func (realClock) Now() time.Time { return time.Now() }
func (realClock) Sleep(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Task 描述一个竞争者（一次发言/一首歌/一条弹幕播报）。
type Task struct {
	ID     string
	Song   bool
	Chat   bool
	Cancel func()          // 抢占时由喂流权调用，用于精确取消持有者
	Done   <-chan struct{} // 持有者真正退出时关闭；nil 表示只能靠 Release 判定
}

// Request 复刻蓝本的三个布尔开关。
type Request struct {
	Task        Task
	Preempt     bool
	RespectSong bool
	WaitOnly    bool
	WaitMax     time.Duration
}

// Hooks 是引擎注入的副作用端口。
type Hooks struct {
	Interrupt func(ctx context.Context) error // 发 type7 audio_interrupted
	Speaking  func() bool                     // 当前是否真的有音频在发
	LastFeed  func() time.Time                // 最后一次真正喂出 PCM 的时刻
	OnForce   func(reason, victimID string)   // feed_owner_force_cancel 指标/事件
}

type OwnershipConfig struct {
	Clock        Clock
	Hooks        Hooks
	Rounds       int
	RoundWait    time.Duration
	QueueWaitMax time.Duration
	StillFeed    time.Duration
	Quiet        time.Duration
	QueuePoll    time.Duration
	RoundPoll    time.Duration
}

func (c *OwnershipConfig) applyDefaults() {
	if c.Clock == nil {
		c.Clock = realClock{}
	}
	if c.Rounds <= 0 {
		c.Rounds = DefaultPreemptRounds
	}
	if c.RoundWait <= 0 {
		c.RoundWait = DefaultRoundWait
	}
	if c.QueueWaitMax <= 0 {
		c.QueueWaitMax = DefaultQueueWaitMax
	}
	if c.StillFeed <= 0 {
		c.StillFeed = DefaultStillFeedWindow
	}
	if c.Quiet <= 0 {
		c.Quiet = DefaultInterruptQuiet
	}
	if c.QueuePoll <= 0 {
		c.QueuePoll = DefaultQueuePoll
	}
	if c.RoundPoll <= 0 {
		c.RoundPoll = DefaultRoundPoll
	}
}

type ownerState struct {
	lease    *Lease
	task     Task
	gen      int64
	acquired time.Time
}

// Ownership 保证任意时刻最多一个喂流者（蓝本硬约束）。
type Ownership struct {
	mu    sync.Mutex
	cfg   OwnershipConfig
	owner *ownerState
	gen   int64
}

func NewOwnership(cfg OwnershipConfig) *Ownership {
	cfg.applyDefaults()
	return &Ownership{cfg: cfg}
}

// Lease 是当前喂流权凭证；写入 WS 前必须检查 Valid()，旧 gen 的帧会被拒绝。
type Lease struct {
	o    *Ownership
	gen  int64
	task Task
}

func (l *Lease) Gen() int64 { return l.gen }
func (l *Lease) ID() string { return l.task.ID }
func (l *Lease) Song() bool { return l.task.Song }

// Valid 表示该 lease 仍是当前持有者且 gen 未被抢占/释放递增。
func (l *Lease) Valid() bool {
	if l == nil || l.o == nil {
		return false
	}
	l.o.mu.Lock()
	defer l.o.mu.Unlock()
	return l.o.owner != nil && l.o.owner.lease == l && l.o.gen == l.gen
}

// Release 幂等；释放时 gen++ 让任何在途帧失效。
func (l *Lease) Release() {
	if l == nil || l.o == nil {
		return
	}
	l.o.mu.Lock()
	defer l.o.mu.Unlock()
	if l.o.owner != nil && l.o.owner.lease == l {
		l.o.gen++
		l.o.owner = nil
	}
}

// Gen 返回当前代号（诊断/事件用）。
func (o *Ownership) Gen() int64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.gen
}

// Owner 返回当前持有者 ID；空闲时为空字符串。
func (o *Ownership) Owner() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.owner == nil {
		return ""
	}
	return o.owner.task.ID
}

var ErrEmptyTaskID = errors.New("engine: acquire without task id")

// Acquire 复刻 _own_audio：
//   - 空闲直接拿权；
//   - preempt 且非 wait_only 且非“respect_song 遇到歌曲”时，取消旧持有者并等 ≤1.5s；
//   - 4 轮未退出则强制收回（type7 + 250ms + gen++）；
//   - 否则排队等待；25s 后持有者仍在喂流则继续等，否则（非歌曲/聊天）强制收回。
func (o *Ownership) Acquire(ctx context.Context, req Request) (*Lease, error) {
	if req.Task.ID == "" {
		return nil, ErrEmptyTaskID
	}
	waitMax := req.WaitMax
	if waitMax <= 0 {
		waitMax = o.cfg.QueueWaitMax
	}
	attempts := 0

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		o.mu.Lock()
		cur := o.owner
		if cur == nil || o.doneLocked(cur) {
			if cur != nil {
				o.gen++
			}
			lease := o.installLocked(req.Task)
			o.mu.Unlock()
			return lease, nil
		}
		victim := cur
		o.mu.Unlock()

		preempt := req.Preempt && !req.WaitOnly && !(req.RespectSong && victim.task.Song)
		if preempt {
			attempts++
			if attempts > o.cfg.Rounds {
				return o.forceTakeover(ctx, req.Task, "preempt_rounds_exhausted", victim.task.ID)
			}
			// 蓝本在 cancel 之前记录 was_speaking（engine.py:866），
			// 否则 cancel 之后可能误判“没在播”而不发 type7，留下残余音频叠音。
			wasSpeaking := o.speaking()
			invokeCancel(victim.task)
			if err := o.waitDone(ctx, victim.task.Done, o.cfg.RoundWait); err != nil {
				return nil, err
			}
			o.clearIf(victim)
			if wasSpeaking && o.cfg.Hooks.Interrupt != nil {
				if err := o.cfg.Hooks.Interrupt(ctx); err != nil {
					return nil, err
				}
				if err := o.cfg.Clock.Sleep(ctx, o.cfg.Quiet); err != nil {
					return nil, err
				}
			}
			continue
		}

		// 排队路径：不取消任何人，除非超过 25s 且持有者已停止喂流。
		queueStart := o.cfg.Clock.Now()
		deadline := queueStart.Add(waitMax)
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			if !o.stillOwner(victim) {
				break // 持有者已自然结束或权已变化，回到外层重新判定
			}
			now := o.cfg.Clock.Now()
			if !now.Before(deadline) {
				if o.recentlyFeeding(now) {
					// 还在出音频 = 不是卡死，继续等（蓝本 2026-10-29 修复）。
					deadline = now.Add(waitMax)
				} else if !victim.task.Song && !victim.task.Chat {
					invokeCancel(victim.task)
					if err := o.waitDone(ctx, victim.task.Done, o.cfg.RoundWait); err != nil {
						return nil, err
					}
					o.clearIf(victim)
					// type7 只由 forceTakeover 按 speaking 判定发一次，此处不再直发。
					return o.forceTakeover(ctx, req.Task, "queue_wait_exceeded", victim.task.ID)
				} else {
					// 歌曲/聊天持有者不强制取消：继续等，但每轮重置计时。
					deadline = now.Add(DefaultProtectedWaitMax)
				}
			}
			if err := o.cfg.Clock.Sleep(ctx, o.cfg.QueuePoll); err != nil {
				return nil, err
			}
		}
	}
}

func (o *Ownership) installLocked(task Task) *Lease {
	o.gen++
	l := &Lease{o: o, gen: o.gen, task: task}
	o.owner = &ownerState{lease: l, task: task, gen: o.gen, acquired: o.cfg.Clock.Now()}
	return l
}

func (o *Ownership) forceTakeover(ctx context.Context, task Task, reason, victimID string) (*Lease, error) {
	// 第一次 gen++：让旧持有者在途的帧立刻失效。
	o.mu.Lock()
	o.gen++
	o.mu.Unlock()

	// type7 + 静默窗口的唯一发送点：仅当引擎确实还在播时发一次，
	// 避免排队超时路径“先 Interrupt+Quiet、接管时又 Interrupt+Quiet”双发。
	// Speaking 未注入时保守视为在播（对齐蓝本强制收回前必发 type7）。
	if (o.cfg.Hooks.Speaking == nil || o.speaking()) && o.cfg.Hooks.Interrupt != nil {
		if err := o.cfg.Hooks.Interrupt(ctx); err != nil {
			return nil, err
		}
		if err := o.cfg.Clock.Sleep(ctx, o.cfg.Quiet); err != nil {
			return nil, err
		}
	}

	o.mu.Lock()
	lease := o.installLocked(task) // 再次 gen++ 后接管
	o.mu.Unlock()

	if o.cfg.Hooks.OnForce != nil {
		o.cfg.Hooks.OnForce(reason, victimID)
	}
	return lease, nil
}

func (o *Ownership) doneLocked(cur *ownerState) bool {
	if cur.task.Done != nil {
		select {
		case <-cur.task.Done:
			return true
		default:
		}
	}
	return false
}

func (o *Ownership) stillOwner(victim *ownerState) bool {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.owner == victim && !o.doneLocked(victim)
}

func (o *Ownership) clearIf(victim *ownerState) {
	o.mu.Lock()
	defer o.mu.Unlock()
	// 只有旧持有者真正退出后才清空；否则下一轮还要继续尝试抢占/等待，
	// 不能仅因为“等超时”就把 owner 置空（那会绕过 type7 静默窗口造成叠音）。
	if o.owner == victim && o.doneLocked(victim) {
		o.gen++
		o.owner = nil
	}
}

func (o *Ownership) speaking() bool {
	if o.cfg.Hooks.Speaking == nil {
		return false
	}
	return o.cfg.Hooks.Speaking()
}

func (o *Ownership) recentlyFeeding(now time.Time) bool {
	if o.cfg.Hooks.LastFeed == nil {
		return false
	}
	last := o.cfg.Hooks.LastFeed()
	if last.IsZero() {
		return false
	}
	return now.Sub(last) < o.cfg.StillFeed
}

// waitDone 轮询等待持有者退出，最多 d；Done 为 nil 时退化为等待 d。
func (o *Ownership) waitDone(ctx context.Context, done <-chan struct{}, d time.Duration) error {
	if done == nil {
		return o.cfg.Clock.Sleep(ctx, d)
	}
	deadline := o.cfg.Clock.Now().Add(d)
	for {
		select {
		case <-done:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		now := o.cfg.Clock.Now()
		if !now.Before(deadline) {
			return nil
		}
		step := o.cfg.RoundPoll
		if rem := deadline.Sub(now); rem < step {
			step = rem
		}
		if err := o.cfg.Clock.Sleep(ctx, step); err != nil {
			return err
		}
	}
}

func invokeCancel(t Task) {
	if t.Cancel != nil {
		t.Cancel()
	}
}
