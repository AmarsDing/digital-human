package audio

import (
	"context"
	"runtime"
	"time"
)

// 落后分级阈值来自蓝本 engine.py:1745-1757：
// delay < -300ms 打告警（10s 节流），delay < -50ms 计入“迟到块”统计。
const (
	WarnLag = 300 * time.Millisecond
	LateLag = 50 * time.Millisecond
)

// LagClass 是单帧延迟的分类。
type LagClass int

const (
	LagNormal LagClass = iota // delay >= -50ms
	LagLate                   // -300ms < delay < -50ms
	LagWarn                   // delay <= -300ms
)

func (c LagClass) String() string {
	switch c {
	case LagLate:
		return "late"
	case LagWarn:
		return "warn"
	default:
		return "normal"
	}
}

// Classify 按蓝本阈值分级。注意：只影响“记录/告警”，任何级别都不跳帧。
func Classify(delay time.Duration) LagClass {
	switch {
	case delay <= -WarnLag:
		return LagWarn
	case delay < -LateLag:
		return LagLate
	default:
		return LagNormal
	}
}

// Clock 抽象单调时钟与睡眠，便于 8h/故障注入场景使用虚拟时间做纯函数测试。
type Clock interface {
	Now() time.Time
	Sleep(d time.Duration)
}

// RealClock 是生产实现。Windows 上 time.Sleep 的粒度通常 0.5–15.6ms，
// 因此调用方必须配合 WaitUntil 的混合等待，而不能依赖单次 Sleep 的精度。
type RealClock struct{}

func (RealClock) Now() time.Time { return time.Now() }
func (RealClock) Sleep(d time.Duration) {
	if d > 0 {
		time.Sleep(d)
	}
}

// Pacer 用绝对时基计算每帧 deadline；误差不累积。
//
//	utterance_t0 = 开始喂第一帧前的单调时钟（每轮语音独立）
//	played_ms   += len(frame) / 48
//	deadline     = t0 + played_ms / 1000
//	delay        = deadline - now
//	if delay > 0: 等待；否则立即发送（不跳帧、不补发）
//
// 证据：蓝本 engine.py:1719-1744。
type Pacer struct {
	clock  Clock
	chunk  int // ms
	coarse time.Duration
}

// NewPacer 创建节奏器；chunkMS<=0 用 100，clock 为 nil 用 RealClock。
func NewPacer(clock Clock, chunkMS int) *Pacer {
	if clock == nil {
		clock = RealClock{}
	}
	if chunkMS <= 0 {
		chunkMS = DefaultChunkMS
	}
	return &Pacer{clock: clock, chunk: chunkMS, coarse: DefaultCoarseWindow}
}

// DefaultCoarseWindow 是混合等待的粗睡边界：剩余 <=15ms 时改用 Gosched 忙等。
// 该值对应 Windows 定时器粒度上限（15.6ms），避免“睡过点”。
const DefaultCoarseWindow = 15 * time.Millisecond

func (p *Pacer) WithCoarseWindow(d time.Duration) *Pacer {
	if d > 0 {
		p.coarse = d
	}
	return p
}

func (p *Pacer) Clock() Clock { return p.clock }

func (p *Pacer) ChunkMS() int    { return p.chunk }
func (p *Pacer) ChunkBytes() int { return p.chunk * BytesPerMS }

// Deadline 返回第 playedMS 毫秒音频应发出的墙钟时刻。
func Deadline(t0 time.Time, playedMS int64) time.Time {
	return t0.Add(time.Duration(playedMS) * time.Millisecond)
}

// Delay 返回距离 deadline 的剩余时间；<=0 表示已迟到。
func Delay(now, t0 time.Time, playedMS int64) time.Duration {
	return Deadline(t0, playedMS).Sub(now)
}

// Wait 只在本帧尚未到期时等待，返回等待前的 delay（负数即迟到）。
// 迟到时不 sleep、不丢弃该帧，由调用方立即发送。
func (p *Pacer) Wait(t0 time.Time, playedMS int64) time.Duration {
	now := p.clock.Now()
	delay := Deadline(t0, playedMS).Sub(now)
	if delay > 0 {
		WaitUntil(p.clock, Deadline(t0, playedMS), p.coarse)
	}
	return delay
}

// WaitUntil 混合等待到 deadline：
//  1. 剩余 > coarse 时直接 Sleep(remaining-coarse)，把长等待交给系统；
//  2. 最后 coarse 窗口用 runtime.Gosched 忙等，避免 Windows 睡眠过点。
//
// 约束：只在最后 15ms 忙等，避免长时间占 CPU；默认不调用 timeBeginPeriod(1)。
func WaitUntil(clock Clock, deadline time.Time, coarse time.Duration) {
	if coarse <= 0 {
		coarse = DefaultCoarseWindow
	}
	for {
		remaining := deadline.Sub(clock.Now())
		if remaining <= 0 {
			return
		}
		if remaining > coarse {
			clock.Sleep(remaining - coarse)
			continue
		}
		runtime.Gosched()
	}
}

// LagWarner 对“喂流落后 >300ms”做 10s 节流（蓝本 engine.py:1748-1752）。
type LagWarner struct {
	window time.Duration
	last   time.Time
}

func NewLagWarner(window time.Duration) *LagWarner {
	if window <= 0 {
		window = 10 * time.Second
	}
	return &LagWarner{window: window}
}

// ShouldWarn 判断本次这一帧是否要产生告警；只有 LagWarn 且距上次告警超过窗口才为 true。
func (w *LagWarner) ShouldWarn(now time.Time, delay time.Duration) bool {
	if Classify(delay) != LagWarn {
		return false
	}
	if w.last.IsZero() || now.Sub(w.last) >= w.window {
		w.last = now
		return true
	}
	return false
}

// ContextSleep 便于在取消时立刻退出循环。
func ContextSleep(ctx context.Context, d time.Duration) error {
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
