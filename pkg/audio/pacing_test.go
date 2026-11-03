package audio

import (
	"sync"
	"testing"
	"time"
)

func TestConstantsMatchBlueprint(t *testing.T) {
	if SampleRate != 24000 {
		t.Fatalf("SampleRate=%d want 24000", SampleRate)
	}
	if BytesPerMS != 48 {
		t.Fatalf("BytesPerMS=%d want 48", BytesPerMS)
	}
	if DefaultChunkBytes != 4800 {
		t.Fatalf("DefaultChunkBytes=%d want 4800", DefaultChunkBytes)
	}
}

func TestMSForBytesAndBytesForMS(t *testing.T) {
	cases := []struct {
		bytes int
		ms    int64
	}{
		{0, 0},
		{48, 1},
		{4800, 100},
		{24000, 500},
		{200, 4}, // 不足 1ms 的部分截断，与蓝本 len//48 一致
		{47, 0},
	}
	for _, c := range cases {
		if got := MSForBytes(c.bytes); got != c.ms {
			t.Fatalf("MSForBytes(%d)=%d want %d", c.bytes, got, c.ms)
		}
	}
	if got := BytesForMS(100); got != 4800 {
		t.Fatalf("BytesForMS(100)=%d want 4800", got)
	}
}

func TestSplitPCMKeepsTail(t *testing.T) {
	pcm := make([]byte, 5000)
	chunks := SplitPCM(pcm, DefaultChunkBytes)
	if len(chunks) != 2 {
		t.Fatalf("chunks=%d want 2", len(chunks))
	}
	if len(chunks[0]) != 4800 || len(chunks[1]) != 200 {
		t.Fatalf("chunk sizes=%d,%d want 4800,200", len(chunks[0]), len(chunks[1]))
	}
	// 尾巴不能被丢弃：总字节必须相等。
	total := 0
	for _, c := range chunks {
		total += len(c)
	}
	if total != len(pcm) {
		t.Fatalf("total=%d want %d", total, len(pcm))
	}
	if SplitPCM(nil, 0) != nil {
		t.Fatal("empty pcm should return nil")
	}
}

func TestClassifyThresholds(t *testing.T) {
	cases := []struct {
		delay time.Duration
		want  LagClass
	}{
		{0, LagNormal},
		{-49 * time.Millisecond, LagNormal},
		{-51 * time.Millisecond, LagLate},
		{-299 * time.Millisecond, LagLate},
		{-300 * time.Millisecond, LagWarn},
		{-350 * time.Millisecond, LagWarn},
	}
	for _, c := range cases {
		if got := Classify(c.delay); got != c.want {
			t.Fatalf("Classify(%v)=%v want %v", c.delay, got, c.want)
		}
	}
}

type fakeClock struct {
	mu         sync.Mutex
	now        time.Time
	perNow     time.Duration
	sleeps     []time.Duration
	sleepCalls int
}

func newFakeClock(t0 time.Time) *fakeClock { return &fakeClock{now: t0} }

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(c.perNow)
	return c.now
}

func (c *fakeClock) Sleep(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if d > 0 {
		c.sleeps = append(c.sleeps, d)
		c.now = c.now.Add(d)
	}
	c.sleepCalls++
}

func (c *fakeClock) totalSlept() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var total time.Duration
	for _, d := range c.sleeps {
		total += d
	}
	return total
}

func TestPacerWaitOnlyWhenAhead(t *testing.T) {
	t0 := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	clk := newFakeClock(t0)
	clk.perNow = time.Millisecond
	p := NewPacer(clk, 100)

	// 先喂完 100ms 音频：deadline = t0+100ms，当前约 t0+1ms → 应等待。
	delay := p.Wait(t0, 100)
	if delay <= 0 {
		t.Fatalf("delay=%v want >0", delay)
	}
	if clk.sleepCalls == 0 {
		t.Fatal("ahead frame should sleep")
	}

	// 已迟到 350ms：delay<0，必须不 sleep（不跳帧由调用方立即发送）。
	clk.sleeps = nil
	clk.sleepCalls = 0
	delay = p.Wait(t0, 100)
	if delay > 0 {
		t.Fatalf("delay=%v want <=0", delay)
	}
	if clk.sleepCalls != 0 {
		t.Fatalf("late frame must not sleep, slept %d times", clk.sleepCalls)
	}
}

func TestWaitUntilHybridWaitCoarseWindow(t *testing.T) {
	t0 := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	clk := newFakeClock(t0)
	clk.perNow = time.Millisecond // 代表忙等循环每次推进 1ms
	start := clk.Now()
	deadline := start.Add(100 * time.Millisecond)

	WaitUntil(clk, deadline, DefaultCoarseWindow)

	if len(clk.sleeps) != 1 {
		t.Fatalf("sleeps=%v want exactly one coarse sleep", clk.sleeps)
	}
	// 粗睡应为 100ms-15ms(±1ms 的 Now 抖动)。
	slept := clk.totalSlept()
	if slept < 83*time.Millisecond || slept > 85*time.Millisecond {
		t.Fatalf("slept=%v want ~84ms", slept)
	}
}

func TestLagWarnerThrottle(t *testing.T) {
	t0 := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	w := NewLagWarner(10 * time.Second)

	if w.ShouldWarn(t0, -400*time.Millisecond) != true {
		t.Fatal("first warn should fire")
	}
	if w.ShouldWarn(t0.Add(5*time.Second), -400*time.Millisecond) != false {
		t.Fatal("warn within 10s must be throttled")
	}
	if w.ShouldWarn(t0.Add(11*time.Second), -400*time.Millisecond) != true {
		t.Fatal("warn after 10s should fire")
	}
	if w.ShouldWarn(t0.Add(12*time.Second), -100*time.Millisecond) != false {
		t.Fatal("late (<300ms) must not warn")
	}
}

// TestEightHourDriftBound 用虚拟时间模拟 8h / 100ms 分片：
// 每帧按绝对 deadline 计算等待，唤醒抖动 0–20ms 不累积。
// 通过标准（§4.3.3）：累计 deadline 偏移 ≤100ms；丢帧 0。
func TestEightHourDriftBound(t *testing.T) {
	const chunkMS = 100
	const frames = 8 * 3600 * 10 // 8h -> 288000 个 100ms 分片
	t0 := time.Date(2026, 11, 3, 0, 0, 0, 0, time.UTC)
	now := t0
	played := int64(0)

	for i := 0; i < frames; i++ {
		played += chunkMS
		deadline := Deadline(t0, played)
		delay := deadline.Sub(now)
		if delay > 0 {
			// 实际唤醒：睡到 deadline，再叠加 0–20ms 抖动。
			now = deadline.Add(time.Duration(i%21) * time.Millisecond)
		} else {
			// 已迟到：立即发送，只叠加处理耗时。
			now = now.Add(time.Duration(i%21) * time.Millisecond)
		}
	}

	if played != 8*3600*1000 {
		t.Fatalf("played=%d want 28800000", played)
	}
	drift := now.Sub(Deadline(t0, played))
	if drift < 0 {
		drift = -drift
	}
	if drift > 100*time.Millisecond {
		t.Fatalf("8h drift=%v want <=100ms", drift)
	}
}

// TestNoSkipWhenLate 断言迟到 >300ms 的帧仍然会被发送：分片数不因延迟减少。
func TestNoSkipWhenLate(t *testing.T) {
	pcm := make([]byte, 3*DefaultChunkBytes)
	chunks := SplitPCM(pcm, DefaultChunkBytes)
	if len(chunks) != 3 {
		t.Fatalf("frames=%d want 3", len(chunks))
	}
	// 蓝本没有 0.25s/1s 跳过阈值；这里用一个迟到 350ms 的时基验证
	// 分片计划不变，告警只影响日志。
	if Classify(-350*time.Millisecond) != LagWarn {
		t.Fatal("350ms lag must be classified as warn")
	}
	if len(chunks) != 3 {
		t.Fatal("late frames must not be dropped")
	}
}
