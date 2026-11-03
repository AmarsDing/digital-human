package engine

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type vclock struct {
	mu     sync.Mutex
	now    time.Time
	sleeps []time.Duration
}

func newVClock(t0 time.Time) *vclock { return &vclock{now: t0} }

func (c *vclock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *vclock) Sleep(ctx context.Context, d time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	c.mu.Lock()
	if d > 0 {
		c.sleeps = append(c.sleeps, d)
		c.now = c.now.Add(d)
	}
	c.mu.Unlock()
	return nil
}

func (c *vclock) total() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	var total time.Duration
	for _, d := range c.sleeps {
		total += d
	}
	return total
}

func newTask(id string) (Task, func()) {
	done := make(chan struct{})
	var once sync.Once
	closeDone := func() { once.Do(func() { close(done) }) }
	return Task{ID: id, Cancel: closeDone, Done: done}, closeDone
}

func TestAcquireAndReleaseIncrementsGen(t *testing.T) {
	clk := newVClock(time.Unix(0, 0))
	o := NewOwnership(OwnershipConfig{Clock: clk})
	l, err := o.Acquire(context.Background(), Request{Task: Task{ID: "a"}})
	if err != nil {
		t.Fatal(err)
	}
	if !l.Valid() || o.Owner() != "a" || l.Gen() != 1 {
		t.Fatalf("lease valid=%v owner=%s gen=%d", l.Valid(), o.Owner(), l.Gen())
	}
	l.Release()
	if l.Valid() {
		t.Fatal("released lease must be invalid")
	}
	if o.Owner() != "" || o.Gen() < 2 {
		t.Fatalf("owner=%s gen=%d", o.Owner(), o.Gen())
	}
}

func TestPreemptCancelsOldOwnerAndQuiets(t *testing.T) {
	clk := newVClock(time.Unix(0, 0))
	var interrupts atomic.Int32
	speaking := true
	o := NewOwnership(OwnershipConfig{
		Clock: clk,
		Hooks: Hooks{
			Interrupt: func(context.Context) error { interrupts.Add(1); return nil },
			Speaking:  func() bool { return speaking },
		},
	})
	aTask, _ := newTask("a")
	a, _ := o.Acquire(context.Background(), Request{Task: aTask})

	b, err := o.Acquire(context.Background(), Request{Task: Task{ID: "b"}, Preempt: true})
	if err != nil {
		t.Fatal(err)
	}
	if o.Owner() != "b" || !b.Valid() {
		t.Fatalf("owner=%s b.valid=%v", o.Owner(), b.Valid())
	}
	if a.Valid() {
		t.Fatal("old lease must be invalid after preempt")
	}
	if interrupts.Load() != 1 {
		t.Fatalf("interrupt=%d want 1 (type7)", interrupts.Load())
	}
	// 抢占后必须留 250ms 静默窗口，避免 Vidu 缓冲里的旧音频与新帧叠音。
	clk.mu.Lock()
	foundQuiet := false
	for _, d := range clk.sleeps {
		if d == DefaultInterruptQuiet {
			foundQuiet = true
		}
	}
	clk.mu.Unlock()
	if !foundQuiet {
		t.Fatalf("sleeps=%v want 250ms quiet window", clk.sleeps)
	}
}

func TestFourRoundsThenForceTakeover(t *testing.T) {
	clk := newVClock(time.Unix(0, 0))
	var forces []string
	var interrupts atomic.Int32
	// 卡死持有者：Cancel 不关闭 Done，模拟永不结束的任务。
	stuck := Task{ID: "stuck", Done: make(chan struct{})}
	o := NewOwnership(OwnershipConfig{
		Clock: clk,
		Hooks: Hooks{
			Interrupt: func(context.Context) error { interrupts.Add(1); return nil },
			OnForce:   func(reason, victim string) { forces = append(forces, reason+":"+victim) },
		},
	})
	a, err := o.Acquire(context.Background(), Request{Task: stuck})
	if err != nil {
		t.Fatal(err)
	}
	if !a.Valid() {
		t.Fatal("stuck owner should hold")
	}

	b, err := o.Acquire(context.Background(), Request{Task: Task{ID: "b"}, Preempt: true})
	if err != nil {
		t.Fatal(err)
	}
	if o.Owner() != "b" || !b.Valid() {
		t.Fatalf("owner=%s", o.Owner())
	}
	if len(forces) != 1 || forces[0] != "preempt_rounds_exhausted:stuck" {
		t.Fatalf("forces=%v", forces)
	}
	if interrupts.Load() != 1 {
		t.Fatalf("interrupt=%d want 1", interrupts.Load())
	}
	if got := clk.total(); got < 4*DefaultRoundWait+DefaultInterruptQuiet {
		t.Fatalf("waited=%v want >= %v", got, 4*DefaultRoundWait+DefaultInterruptQuiet)
	}
}

// 排队超时强制接管时 type7 必须恰好发一次（收敛到 forceTakeover 单点）。
func TestQueueWaitExceededInterruptsExactlyOnce(t *testing.T) {
	cases := []struct {
		name     string
		speaking bool
		want     int32
	}{
		{"speaking", true, 1},
		{"silent", false, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			clk := newVClock(time.Unix(0, 0))
			var interrupts atomic.Int32
			stuck := Task{ID: "stuck", Done: make(chan struct{}), Cancel: func() {}}
			o := NewOwnership(OwnershipConfig{
				Clock: clk,
				Hooks: Hooks{
					Interrupt: func(context.Context) error { interrupts.Add(1); return nil },
					Speaking:  func() bool { return tc.speaking },
					LastFeed:  func() time.Time { return clk.Now().Add(-10 * time.Second) },
				},
			})
			if _, err := o.Acquire(context.Background(), Request{Task: stuck}); err != nil {
				t.Fatal(err)
			}
			l, err := o.Acquire(context.Background(), Request{
				Task: Task{ID: "dm"}, WaitOnly: true, WaitMax: DefaultQueueWaitMax,
			})
			if err != nil {
				t.Fatal(err)
			}
			if !l.Valid() || o.Owner() != "dm" {
				t.Fatalf("owner=%s valid=%v", o.Owner(), l.Valid())
			}
			if got := interrupts.Load(); got != tc.want {
				t.Fatalf("type7 interrupts=%d want %d", got, tc.want)
			}
		})
	}
}

func TestRespectSongQueuesWithoutCancel(t *testing.T) {
	clk := newVClock(time.Unix(0, 0))
	var cancels atomic.Int32
	done := make(chan struct{})
	song := Task{ID: "song", Song: true, Done: done, Cancel: func() { cancels.Add(1) }}
	o := NewOwnership(OwnershipConfig{Clock: clk})
	a, err := o.Acquire(context.Background(), Request{Task: song})
	if err != nil {
		t.Fatal(err)
	}

	acquired := make(chan *Lease, 1)
	go func() {
		l, err := o.Acquire(context.Background(), Request{Task: Task{ID: "chat"}, RespectSong: true, WaitMax: time.Second})
		if err == nil {
			acquired <- l
		}
	}()

	// 给等待者一点真实时间进入排队路径，然后正常结束歌曲。
	time.Sleep(5 * time.Millisecond)
	close(done)
	a.Release()

	select {
	case l := <-acquired:
		if !l.Valid() || o.Owner() != "chat" {
			t.Fatalf("owner=%s valid=%v", o.Owner(), l.Valid())
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("chat did not acquire after song ended")
	}
	if cancels.Load() != 0 {
		t.Fatalf("respect_song must not cancel song, cancels=%d", cancels.Load())
	}
}

func TestWaitOnlyTakesOverStaleHolder(t *testing.T) {
	clk := newVClock(time.Unix(0, 0))
	var forces []string
	var cancels atomic.Int32
	stuck := Task{
		ID: "stuck", Done: make(chan struct{}),
		Cancel: func() { cancels.Add(1) },
	}
	o := NewOwnership(OwnershipConfig{
		Clock: clk,
		Hooks: Hooks{
			LastFeed: func() time.Time { return clk.Now().Add(-10 * time.Second) },
			OnForce:  func(reason, victim string) { forces = append(forces, reason) },
		},
	})
	if _, err := o.Acquire(context.Background(), Request{Task: stuck}); err != nil {
		t.Fatal(err)
	}
	l, err := o.Acquire(context.Background(), Request{
		Task: Task{ID: "dm"}, WaitOnly: true, WaitMax: DefaultQueueWaitMax,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !l.Valid() || o.Owner() != "dm" {
		t.Fatalf("owner=%s valid=%v", o.Owner(), l.Valid())
	}
	if len(forces) != 1 || forces[0] != "queue_wait_exceeded" {
		t.Fatalf("forces=%v", forces)
	}
	if cancels.Load() != 1 {
		t.Fatalf("cancels=%d want 1", cancels.Load())
	}
}

func TestWaitOnlyKeepsWaitingWhileFeeding(t *testing.T) {
	clk := newVClock(time.Unix(0, 0))
	var cancels atomic.Int32
	stuck := Task{
		ID: "stuck", Done: make(chan struct{}),
		Cancel: func() { cancels.Add(1) },
	}
	o := NewOwnership(OwnershipConfig{
		Clock: clk,
		Hooks: Hooks{LastFeed: func() time.Time { return clk.Now() }},
	})
	if _, err := o.Acquire(context.Background(), Request{Task: stuck}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := o.Acquire(ctx, Request{Task: Task{ID: "dm"}, WaitOnly: true, WaitMax: time.Second})
	if err == nil {
		t.Fatal("expected context deadline while holder keeps feeding")
	}
	if cancels.Load() != 0 {
		t.Fatalf("still-feeding holder must not be cancelled, cancels=%d", cancels.Load())
	}
}

func TestAcquireIsExclusiveUnderConcurrency(t *testing.T) {
	o := NewOwnership(OwnershipConfig{
		Clock:        realClock{},
		QueuePoll:    50 * time.Microsecond,
		QueueWaitMax: time.Second,
	})
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var inUse atomic.Int32
	var maxInUse atomic.Int32
	var wg sync.WaitGroup
	const n = 100
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l, err := o.Acquire(ctx, Request{Task: Task{ID: "t"}, WaitOnly: true})
			if err != nil {
				return
			}
			cur := inUse.Add(1)
			for {
				old := maxInUse.Load()
				if cur <= old || maxInUse.CompareAndSwap(old, cur) {
					break
				}
			}
			time.Sleep(50 * time.Microsecond)
			inUse.Add(-1)
			l.Release()
		}(i)
	}
	wg.Wait()
	if maxInUse.Load() != 1 {
		t.Fatalf("max concurrent owners=%d want 1", maxInUse.Load())
	}
}
