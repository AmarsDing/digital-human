package song

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/media"
)

type gatePlayer struct {
	started chan string
	release chan struct{}
}

func newGatePlayer() *gatePlayer {
	return &gatePlayer{started: make(chan string, 8), release: make(chan struct{}, 8)}
}

func (p *gatePlayer) Play(ctx context.Context, filePath, _ string) error {
	p.started <- filepath.Base(filePath)
	select {
	case <-p.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (p *gatePlayer) Stop(context.Context) error { return nil }
func (p *gatePlayer) Name() string               { return "gate" }

type emptyClaimObserver struct {
	*MemoryRequestStore
	emptyClaim chan struct{}
}

func (o *emptyClaimObserver) ClaimNext(ctx context.Context, playID string) (Request, bool, error) {
	req, ok, err := o.MemoryRequestStore.ClaimNext(ctx, playID)
	if err == nil && !ok {
		select {
		case o.emptyClaim <- struct{}{}:
		default:
		}
	}
	return req, ok, err
}

func newQueueService(t *testing.T, player media.Player, names ...string) *Service {
	t.Helper()
	dir := t.TempDir()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return NewService(media.NewLibrary(dir), media.FakeDecoder{}, player, nil, func() bool { return true })
}

func waitStarted(t *testing.T, player *gatePlayer) string {
	t.Helper()
	select {
	case name := <-player.started:
		return name
	case <-time.After(2 * time.Second):
		t.Fatal("等待播放超时")
		return ""
	}
}

func waitRequestStatus(t *testing.T, store *MemoryRequestStore, id, status string) {
	t.Helper()
	ctx := context.Background()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		req, ok, err := store.Get(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		if ok && req.Status == status {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	req, _, _ := store.Get(ctx, id)
	t.Fatalf("请求 %s 未进入 %s: %+v", id, status, req)
}

func TestMemoryRequestStoreEnqueuePendingConfirm(t *testing.T) {
	store := NewMemoryRequestStore(QueuePolicy{MaxQueue: 10}, time.Now)
	req, duplicate, err := store.Enqueue(context.Background(), Request{
		SessionID: "s1", SourceEventRowID: "src-pending", UserKey: "u1", File: "a.wav", Status: RequestPendingConfirm,
	})
	if err != nil || duplicate {
		t.Fatalf("入队待确认请求: duplicate=%v err=%v", duplicate, err)
	}
	if req.Status != RequestPendingConfirm {
		t.Fatalf("待确认状态被改写: got=%s want=%s", req.Status, RequestPendingConfirm)
	}
}

func TestSongRequestQueueIdempotentBoundedAndFIFO(t *testing.T) {
	player := newGatePlayer()
	svc := newQueueService(t, player, "a.wav", "b.wav")
	store := NewMemoryRequestStore(QueuePolicy{MaxQueue: 2, TTL: 2 * time.Minute, UserCooldown: 2 * time.Minute}, time.Now)
	ctx := context.Background()
	first, duplicate, err := store.Enqueue(ctx, Request{SessionID: "s1", SourceEventRowID: "src-a", UserKey: "u1", File: "a.wav"})
	if err != nil || duplicate || first.QueueSeq != 1 || first.Status != RequestQueued {
		t.Fatalf("首次入队: %+v duplicate=%v err=%v", first, duplicate, err)
	}
	again, duplicate, err := store.Enqueue(ctx, Request{SessionID: "s1", SourceEventRowID: "src-a", UserKey: "u1", File: "a.wav"})
	if err != nil || !duplicate || again.ID != first.ID || again.QueueSeq != first.QueueSeq {
		t.Fatalf("同源幂等: %+v duplicate=%v err=%v", again, duplicate, err)
	}
	if _, _, err := store.Enqueue(ctx, Request{SessionID: "s1", SourceEventRowID: "src-u1-other", UserKey: "u1", File: "b.wav"}); !errors.Is(err, ErrUserActive) {
		t.Fatalf("每人只允许一首活动请求: %v", err)
	}
	second, _, err := store.Enqueue(ctx, Request{SessionID: "s1", SourceEventRowID: "src-b", UserKey: "u2", File: "b.wav"})
	if err != nil || second.QueueSeq != 2 {
		t.Fatalf("第二首入队: %+v err=%v", second, err)
	}
	if _, _, err := store.Enqueue(ctx, Request{SessionID: "s1", SourceEventRowID: "src-full", UserKey: "u3", File: "a.wav"}); !errors.Is(err, ErrQueueFull) {
		t.Fatalf("队列必须有界: %v", err)
	}

	scheduler := NewScheduler(store, svc, time.Now)
	if ok, err := scheduler.DispatchNext(ctx); err != nil || !ok {
		t.Fatalf("调度队首: ok=%v err=%v", ok, err)
	}
	if got := waitStarted(t, player); got != "a.wav" {
		t.Fatalf("FIFO 首曲=%s", got)
	}
	if ok, err := scheduler.DispatchNext(ctx); err != nil || ok {
		t.Fatalf("播放中不得并行调度: ok=%v err=%v", ok, err)
	}
	player.release <- struct{}{}
	if got := waitStarted(t, player); got != "b.wav" {
		t.Fatalf("完成后应自动播放下一首: %s", got)
	}
	waitRequestStatus(t, store, first.ID, RequestDone)
	player.release <- struct{}{}
	waitRequestStatus(t, store, second.ID, RequestDone)
}

func TestSongRequestExpirySkippedBeforePlayback(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	player := newGatePlayer()
	svc := newQueueService(t, player, "a.wav", "b.wav")
	store := NewMemoryRequestStore(QueuePolicy{MaxQueue: 10}, clock)
	ctx := context.Background()
	expired, _, _ := store.Enqueue(ctx, Request{SessionID: "s1", SourceEventRowID: "src-expired", UserKey: "u1", File: "a.wav", ExpiresAtMs: now.Add(time.Second).UnixMilli()})
	valid, _, _ := store.Enqueue(ctx, Request{SessionID: "s1", SourceEventRowID: "src-valid", UserKey: "u2", File: "b.wav"})
	now = now.Add(2 * time.Second)
	scheduler := NewScheduler(store, svc, clock)
	if ok, err := scheduler.DispatchNext(ctx); err != nil || !ok {
		t.Fatalf("调度未过期请求: ok=%v err=%v", ok, err)
	}
	if got := waitStarted(t, player); got != "b.wav" {
		t.Fatalf("过期歌曲不得播放，实际=%s", got)
	}
	if req, _, err := store.Get(ctx, expired.ID); err != nil || req.Status != RequestExpired || req.TerminalAtMs == 0 {
		t.Fatalf("过期请求状态: %+v err=%v", req, err)
	}
	player.release <- struct{}{}
	waitRequestStatus(t, store, valid.ID, RequestDone)
}

func TestSongRecoveryPlayingUnknownAndManualRequeue(t *testing.T) {
	player := newGatePlayer()
	svc := newQueueService(t, player, "a.wav", "b.wav")
	store := NewMemoryRequestStore(QueuePolicy{MaxQueue: 10, UserCooldown: time.Nanosecond}, time.Now)
	ctx := context.Background()
	playing, _, _ := store.Enqueue(ctx, Request{SessionID: "s1", SourceEventRowID: "src-playing", UserKey: "u1", File: "a.wav"})
	queued, _, _ := store.Enqueue(ctx, Request{SessionID: "s1", SourceEventRowID: "src-queued", UserKey: "u2", File: "b.wav"})
	claimed, ok, err := store.ClaimNext(ctx, "play-crashed")
	if err != nil || !ok || claimed.ID != playing.ID {
		t.Fatalf("模拟崩溃前 playing: %+v ok=%v err=%v", claimed, ok, err)
	}
	observer := &emptyClaimObserver{MemoryRequestStore: store, emptyClaim: make(chan struct{}, 1)}
	scheduler := NewScheduler(observer, svc, time.Now)
	if err := scheduler.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	if req, _, err := store.Get(ctx, playing.ID); err != nil || req.Status != RequestUnknown || req.ReconcileStatus != "pending" {
		t.Fatalf("崩溃中的 playing 必须转 unknown: %+v err=%v", req, err)
	}
	select {
	case name := <-player.started:
		t.Fatalf("Recover 不得自动重放: %s", name)
	default:
	}
	if ok, err := store.Complete(ctx, playing.ID, "play-crashed", nil); err != nil || ok {
		t.Fatalf("旧 play_id 的迟到回执不得绕过对账: ok=%v err=%v", ok, err)
	}
	if ok, err := scheduler.DispatchNext(ctx); err != nil || !ok {
		t.Fatalf("恢复 queued: ok=%v err=%v", ok, err)
	}
	if got := waitStarted(t, player); got != "b.wav" {
		t.Fatalf("恢复时应按原顺序播放 queued: %s", got)
	}
	player.release <- struct{}{}
	waitRequestStatus(t, store, queued.ID, RequestDone)
	select {
	case <-observer.emptyClaim:
	case <-time.After(2 * time.Second):
		t.Fatal("等待完成后的自动调度超时")
	}
	scheduler.mu.Lock()
	scheduler.mu.Unlock()
	if err := store.ResolveUnknown(ctx, playing.ID, RequestReconcileRequeue); err != nil {
		t.Fatal(err)
	}
	if ok, err := scheduler.DispatchNext(ctx); err != nil || !ok {
		t.Fatalf("人工重排队后调度: ok=%v err=%v", ok, err)
	}
	if got := waitStarted(t, player); got != "a.wav" {
		t.Fatalf("只有人工 requeue 才能重播 unknown: %s", got)
	}
	player.release <- struct{}{}
	waitRequestStatus(t, store, playing.ID, RequestDone)
	if ok, err := scheduler.DispatchNext(ctx); err != nil || ok {
		t.Fatalf("done 不得重播: ok=%v err=%v", ok, err)
	}
}

func TestSongUserCooldownAfterCompletion(t *testing.T) {
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	store := NewMemoryRequestStore(QueuePolicy{MaxQueue: 10, UserCooldown: 2 * time.Minute}, func() time.Time { return now })
	ctx := context.Background()
	first, _, _ := store.Enqueue(ctx, Request{SessionID: "s1", SourceEventRowID: "src-cooldown-1", UserKey: "u1", File: "a.wav"})
	claimed, ok, err := store.ClaimNext(ctx, "play-done")
	if err != nil || !ok {
		t.Fatalf("模拟 claim: ok=%v err=%v", ok, err)
	}
	completed, err := store.Complete(ctx, first.ID, claimed.PlayID, nil)
	if err != nil || !completed {
		t.Fatalf("模拟完成失败: ok=%v err=%v", completed, err)
	}
	now = now.Add(time.Second)
	if _, _, err := store.Enqueue(ctx, Request{SessionID: "s1", SourceEventRowID: "src-cooldown-2", UserKey: "u1", File: "a.wav"}); !errors.Is(err, ErrUserCooldown) {
		t.Fatalf("完成后仍需用户点歌冷却: %v", err)
	}
	now = now.Add(2 * time.Minute)
	if _, _, err := store.Enqueue(ctx, Request{SessionID: "s1", SourceEventRowID: "src-cooldown-3", UserKey: "u1", File: "a.wav"}); err != nil {
		t.Fatalf("冷却过后应允许点歌: %v", err)
	}
}

func TestSchedulersContendWithoutParallelPlayback(t *testing.T) {
	player := newGatePlayer()
	svc := newQueueService(t, player, "a.wav", "b.wav")
	store := NewMemoryRequestStore(QueuePolicy{MaxQueue: 10, UserCooldown: time.Nanosecond}, time.Now)
	ctx := context.Background()
	first, _, _ := store.Enqueue(ctx, Request{SessionID: "s1", SourceEventRowID: "src-race-a", UserKey: "u1", File: "a.wav"})
	second, _, _ := store.Enqueue(ctx, Request{SessionID: "s1", SourceEventRowID: "src-race-b", UserKey: "u2", File: "b.wav"})
	schedulers := []*Scheduler{NewScheduler(store, svc, time.Now), NewScheduler(store, svc, time.Now)}
	type result struct {
		ok  bool
		err error
	}
	results := make(chan result, len(schedulers))
	for _, scheduler := range schedulers {
		go func(s *Scheduler) {
			ok, err := s.DispatchNext(ctx)
			results <- result{ok: ok, err: err}
		}(scheduler)
	}
	succeeded := 0
	for range schedulers {
		got := <-results
		if got.ok {
			succeeded++
		} else if got.err != nil && !errors.Is(got.err, ErrBusy) {
			t.Fatalf("竞争调度出现意外错误: %v", got.err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("并发竞争只能一个 Scheduler 接受播放: %d", succeeded)
	}
	if got := waitStarted(t, player); got != "a.wav" {
		t.Fatalf("竞争时仍需 FIFO: %s", got)
	}
	select {
	case got := <-player.started:
		t.Fatalf("不得并行播放第二首: %s", got)
	default:
	}
	player.release <- struct{}{}
	if got := waitStarted(t, player); got != "b.wav" {
		t.Fatalf("当前曲完成后应继续 queued: %s", got)
	}
	waitRequestStatus(t, store, first.ID, RequestDone)
	player.release <- struct{}{}
	waitRequestStatus(t, store, second.ID, RequestDone)
}
