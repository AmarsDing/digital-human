package live

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/teamwork/digital-human/pkg/eventbus"
)

type counterController struct {
	mu      sync.Mutex
	started int
	stops   int
}

func (c *counterController) Start(context.Context, string) (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.started++
	return "live-idem", nil
}

func (c *counterController) Stop(context.Context, string) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.stops++
	return nil
}

func (c *counterController) Say(context.Context, string, string) (string, error) { return "u", nil }

func (c *counterController) counts() (int, int) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.started, c.stops
}

func TestStartIdempotentSameKeyReturnsSameSession(t *testing.T) {
	ctrl := &counterController{}
	m := NewManagerWithStores(eventbus.New(64), NewMemorySink(64), ctrl,
		nil, NewMemoryIdempotencyStore(time.Hour))
	ctx := context.Background()

	sid1, phase1, live1, err := m.StartIdempotent(ctx, "k-1", "p", "a", "")
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	sid2, phase2, live2, err := m.StartIdempotent(ctx, "k-1", "p", "a", "")
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	if sid1 != sid2 || phase1 != phase2 || live1 != live2 {
		t.Fatalf("same key replay mismatch: %q/%s/%q vs %q/%s/%q",
			sid1, phase1, live1, sid2, phase2, live2)
	}
	if started, _ := ctrl.counts(); started != 1 {
		t.Fatalf("controller start called %d times, want 1", started)
	}
}

func TestStartIdempotentDifferentKeyRestarts(t *testing.T) {
	ctrl := &counterController{}
	m := NewManagerWithStores(eventbus.New(64), NewMemorySink(64), ctrl,
		nil, NewMemoryIdempotencyStore(time.Hour))
	ctx := context.Background()

	sid1, _, _, err := m.StartIdempotent(ctx, "k-1", "p", "a", "")
	if err != nil {
		t.Fatalf("first start: %v", err)
	}
	sid2, _, _, err := m.StartIdempotent(ctx, "k-2", "p", "a", "")
	if err != nil {
		t.Fatalf("second start: %v", err)
	}
	if sid1 == sid2 {
		t.Fatalf("different keys must not reuse session: %s", sid1)
	}
	if started, _ := ctrl.counts(); started != 2 {
		t.Fatalf("controller start called %d times, want 2", started)
	}
}

func TestStartIdempotentEmptyKeyDisablesCache(t *testing.T) {
	ctrl := &counterController{}
	m := NewManagerWithStores(eventbus.New(64), NewMemorySink(64), ctrl,
		nil, NewMemoryIdempotencyStore(time.Hour))
	ctx := context.Background()

	sid1, _, _, _ := m.StartIdempotent(ctx, "", "p", "a", "")
	sid2, _, _, _ := m.StartIdempotent(ctx, "", "p", "a", "")
	if sid1 == sid2 {
		t.Fatalf("empty key should not be cached, got same session %s", sid1)
	}
	if started, _ := ctrl.counts(); started != 2 {
		t.Fatalf("controller start called %d times, want 2", started)
	}
}

func TestStopIdempotentSameKeyStopsOnce(t *testing.T) {
	ctrl := &counterController{}
	m := NewManagerWithStores(eventbus.New(64), NewMemorySink(64), ctrl,
		nil, NewMemoryIdempotencyStore(time.Hour))
	ctx := context.Background()

	if _, _, _, err := m.Start(ctx, "p", "a", ""); err != nil {
		t.Fatalf("start: %v", err)
	}
	ok1, note1, err := m.StopIdempotent(ctx, "stop-1", "manual")
	if err != nil {
		t.Fatalf("first stop: %v", err)
	}
	ok2, note2, err := m.StopIdempotent(ctx, "stop-1", "manual")
	if err != nil {
		t.Fatalf("second stop: %v", err)
	}
	if ok1 != ok2 || note1 != note2 {
		t.Fatalf("stop replay mismatch: %v/%q vs %v/%q", ok1, note1, ok2, note2)
	}
	if _, stops := ctrl.counts(); stops != 1 {
		t.Fatalf("controller stop called %d times, want 1", stops)
	}
}

func TestStartIdempotentConcurrentSameKeyStartsOnce(t *testing.T) {
	ctrl := &counterController{}
	m := NewManagerWithStores(eventbus.New(64), NewMemorySink(64), ctrl,
		nil, NewMemoryIdempotencyStore(time.Hour))
	ctx := context.Background()

	type result struct {
		sid   string
		phase Phase
		live  string
		err   error
	}
	const n = 10
	results := make([]result, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			sid, phase, live, err := m.StartIdempotent(ctx, "k-concurrent", "p", "a", "")
			results[i] = result{sid: sid, phase: phase, live: live, err: err}
		}(i)
	}
	wg.Wait()

	for i, r := range results {
		if r.err != nil {
			t.Fatalf("goroutine %d start: %v", i, r.err)
		}
		if r.sid != results[0].sid || r.phase != results[0].phase || r.live != results[0].live {
			t.Fatalf("goroutine %d mismatch: %q/%s/%q vs %q/%s/%q", i,
				r.sid, r.phase, r.live, results[0].sid, results[0].phase, results[0].live)
		}
	}
	if started, _ := ctrl.counts(); started != 1 {
		t.Fatalf("controller start called %d times, want 1", started)
	}
	// 只允许存在一场：并发同 key 结束后 manager 仍指向同一 session。
	if snap := m.Snapshot(); snap.SessionID != results[0].sid || snap.Phase != PhaseLive {
		t.Fatalf("final snapshot session=%q phase=%s, want %q/live", snap.SessionID, snap.Phase, results[0].sid)
	}
}

func TestStopIdempotentConcurrentSameKeyStopsOnce(t *testing.T) {
	ctrl := &counterController{}
	m := NewManagerWithStores(eventbus.New(64), NewMemorySink(64), ctrl,
		nil, NewMemoryIdempotencyStore(time.Hour))
	ctx := context.Background()
	if _, _, _, err := m.Start(ctx, "p", "a", ""); err != nil {
		t.Fatalf("start: %v", err)
	}

	const n = 16
	oks := make([]bool, n)
	notes := make([]string, n)
	errs := make([]error, n)
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			oks[i], notes[i], errs[i] = m.StopIdempotent(ctx, "stop-concurrent", "manual")
		}(i)
	}
	wg.Wait()

	for i := 0; i < n; i++ {
		if errs[i] != nil {
			t.Fatalf("goroutine %d stop: %v", i, errs[i])
		}
		if oks[i] != oks[0] || notes[i] != notes[0] {
			t.Fatalf("goroutine %d mismatch: %v/%q vs %v/%q", i, oks[i], notes[i], oks[0], notes[0])
		}
	}
	if _, stops := ctrl.counts(); stops != 1 {
		t.Fatalf("controller stop called %d times, want 1", stops)
	}
}

func TestMemoryIdempotencyStoreTTL(t *testing.T) {
	base := time.Now()
	s := NewMemoryIdempotencyStore(time.Hour)
	s.now = func() time.Time { return base }
	if err := s.Put(context.Background(), "k", Result{SessionID: "s1", OK: true}, time.Minute); err != nil {
		t.Fatalf("put: %v", err)
	}
	r, ok, err := s.Get(context.Background(), "k")
	if err != nil || !ok || r.SessionID != "s1" {
		t.Fatalf("get before expiry: %+v ok=%v err=%v", r, ok, err)
	}

	s.now = func() time.Time { return base.Add(2 * time.Minute) }
	if _, ok, _ := s.Get(context.Background(), "k"); ok {
		t.Fatal("entry should expire after ttl")
	}
}
