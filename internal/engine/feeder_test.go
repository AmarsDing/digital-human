package engine

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teamwork/digital-human/pkg/audio"
)

type senderFunc func(ctx context.Context, pcm []byte) error

func (f senderFunc) SendPCM(ctx context.Context, pcm []byte) error { return f(ctx, pcm) }

func TestFeedSendsEveryChunkIncludingTail(t *testing.T) {
	var frames, bytes int32
	s := senderFunc(func(_ context.Context, pcm []byte) error {
		atomic.AddInt32(&frames, 1)
		atomic.AddInt32(&bytes, int32(len(pcm)))
		return nil
	})
	pcm := make([]byte, audio.DefaultChunkBytes+200) // 4800 + 200
	f := NewFeeder(FeedConfig{})
	res, err := f.Feed(context.Background(), s, pcm, nil)
	if err != nil {
		t.Fatal(err)
	}
	if res.Frames != 2 || frames != 2 {
		t.Fatalf("frames=%d/%d want 2", res.Frames, frames)
	}
	if bytes != int32(len(pcm)) || res.Bytes != len(pcm) {
		t.Fatalf("bytes=%d/%d want %d (tail must not be dropped)", bytes, res.Bytes, len(pcm))
	}
	if res.Drift > 100*time.Millisecond {
		t.Fatalf("drift=%v want <=100ms", res.Drift)
	}
}

func TestFeedStopsAtWriteTimeoutWithoutRetry(t *testing.T) {
	var calls atomic.Int32
	s := senderFunc(func(ctx context.Context, _ []byte) error {
		calls.Add(1)
		<-ctx.Done()
		return ctx.Err()
	})
	f := NewFeeder(FeedConfig{WriteTimeout: 30 * time.Millisecond})
	start := time.Now()
	_, err := f.Feed(context.Background(), s, make([]byte, 4800), nil)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Fatalf("elapsed=%v, timeout not honored", elapsed)
	}
	if calls.Load() != 1 {
		t.Fatalf("send calls=%d want 1 (no retry)", calls.Load())
	}
}

func TestFeedHonorsStopPredicate(t *testing.T) {
	var frames int32
	s := senderFunc(func(context.Context, []byte) error {
		atomic.AddInt32(&frames, 1)
		return nil
	})
	f := NewFeeder(FeedConfig{})
	stop := func() bool { return atomic.LoadInt32(&frames) > 0 }
	_, err := f.Feed(context.Background(), s, make([]byte, 3*audio.DefaultChunkBytes), stop)
	if !errors.Is(err, ErrFeedInterrupted) {
		t.Fatalf("err=%v want ErrFeedInterrupted", err)
	}
	if frames != 1 {
		t.Fatalf("frames=%d want 1", frames)
	}
}

func TestFeedCancelledContext(t *testing.T) {
	f := NewFeeder(FeedConfig{})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := f.Feed(ctx, senderFunc(func(context.Context, []byte) error { return nil }), make([]byte, 4800), nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v want context.Canceled", err)
	}
}

func TestFeedSessionSendsFullChunksAndTailAcrossWrites(t *testing.T) {
	var mu sync.Mutex
	var sizes []int
	sender := senderFunc(func(_ context.Context, pcm []byte) error {
		mu.Lock()
		sizes = append(sizes, len(pcm))
		mu.Unlock()
		return nil
	})
	f := NewFeeder(FeedConfig{})
	session := f.newFeedSession(context.Background(), sender, nil)
	for _, n := range []int{1000, 3800, 4800, 200} {
		if err := session.Write(make([]byte, n)); err != nil {
			t.Fatal(err)
		}
	}
	if len(sizes) != 2 || sizes[0] != 4800 || sizes[1] != 4800 {
		t.Fatalf("frames before Finish = %v, want two immediate 4800-byte chunks", sizes)
	}
	if len(session.pending) != 200 {
		t.Fatalf("pending = %d, want only 200-byte tail", len(session.pending))
	}
	res, err := session.Finish()
	if err != nil {
		t.Fatal(err)
	}
	if len(sizes) != 3 || sizes[2] != 200 || res.Bytes != 9800 {
		t.Fatalf("frames after Finish = %v, result = %+v; tail must be sent", sizes, res)
	}
}

type feedSessionClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *feedSessionClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(time.Millisecond)
	return c.now
}

func (c *feedSessionClock) Sleep(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *feedSessionClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}

func (c *feedSessionClock) Time() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func TestFeedSessionKeepsSingleTimeOriginAcrossWrites(t *testing.T) {
	start := time.Date(2026, 11, 4, 0, 0, 0, 0, time.UTC)
	clock := &feedSessionClock{now: start}
	f := NewFeeder(FeedConfig{Clock: clock})
	session := f.newFeedSession(context.Background(), senderFunc(func(context.Context, []byte) error { return nil }), nil)
	if err := session.Write(make([]byte, 4800)); err != nil {
		t.Fatal(err)
	}
	clock.Advance(50 * time.Millisecond)
	if err := session.Write(make([]byte, 4800)); err != nil {
		t.Fatal(err)
	}
	if _, err := session.Finish(); err != nil {
		t.Fatal(err)
	}
	if got := clock.Time(); got.After(start.Add(230 * time.Millisecond)) {
		t.Fatalf("session finished at %v, start %v; per-write t0 reset added extra delay", got, start)
	}
}

func TestFeedSessionCancelDropsTailAndStopsFeeding(t *testing.T) {
	var calls atomic.Int32
	sender := senderFunc(func(context.Context, []byte) error {
		calls.Add(1)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	session := NewFeeder(FeedConfig{}).newFeedSession(ctx, sender, nil)
	if err := session.Write(make([]byte, 4800)); err != nil {
		t.Fatal(err)
	}
	if err := session.Write(make([]byte, 200)); err != nil {
		t.Fatal(err)
	}
	cancel()
	session.Discard()
	if err := session.Write(make([]byte, 4800)); !errors.Is(err, context.Canceled) {
		t.Fatalf("Write after cancel = %v, want context.Canceled", err)
	}
	if _, err := session.Finish(); !errors.Is(err, context.Canceled) {
		t.Fatalf("Finish after cancel = %v, want context.Canceled", err)
	}
	if calls.Load() != 1 || len(session.pending) != 0 {
		t.Fatalf("calls after cancel = %d, pending = %d; want no further feed and no tail", calls.Load(), len(session.pending))
	}
}

func TestFeedSessionBackpressureStopsAtDefaultThreeSecondTimeout(t *testing.T) {
	var calls atomic.Int32
	sender := senderFunc(func(ctx context.Context, _ []byte) error {
		calls.Add(1)
		<-ctx.Done()
		return ctx.Err()
	})
	session := NewFeeder(FeedConfig{}).newFeedSession(context.Background(), sender, nil)
	start := time.Now()
	err := session.Write(make([]byte, 4800))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Write error = %v, want deadline exceeded", err)
	}
	if elapsed := time.Since(start); elapsed < 2500*time.Millisecond || elapsed > 5*time.Second {
		t.Fatalf("elapsed = %v, want default 3-second write deadline", elapsed)
	}
	if calls.Load() != 1 || len(session.pending) != 0 {
		t.Fatalf("send calls = %d, pending = %d; want one blocked write, no retry and no tail", calls.Load(), len(session.pending))
	}
}
