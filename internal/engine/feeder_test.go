package engine

import (
	"context"
	"errors"
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
