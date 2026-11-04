package repo

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/song"
	"github.com/teamwork/digital-human/internal/data/ent"
)

func newEntSongStore(client *ent.Client, now func() time.Time) *EntSongRequestStore {
	return NewEntSongRequestStore(client, song.QueuePolicy{MaxQueue: 10, UserCooldown: time.Nanosecond}, now)
}

func enqueueEntSongRequest(t *testing.T, ctx context.Context, store *EntSongRequestStore, source, status string) song.Request {
	t.Helper()
	req, duplicate, err := store.Enqueue(ctx, song.Request{
		SessionID: "s1", SourceEventRowID: source, DecisionID: "decision-" + source,
		EffectIdempotencyKey: "effect-" + source, UserKey: "user-" + source,
		File: source + ".wav", Script: "script-" + source, Status: status,
	})
	if err != nil || duplicate {
		t.Fatalf("enqueue %s: duplicate=%v err=%v", source, duplicate, err)
	}
	return req
}

func TestEntSongRequestStoreReloadsAndRecoversPlaying(t *testing.T) {
	ctx := context.Background()
	client := newEntTestClient(t)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	store := newEntSongStore(client, clock)
	playing := enqueueEntSongRequest(t, ctx, store, "recover-playing", song.RequestQueued)
	queued := enqueueEntSongRequest(t, ctx, store, "recover-queued", song.RequestQueued)
	claimed, ok, err := store.ClaimNext(ctx, "play-crashed")
	if err != nil || !ok || claimed.ID != playing.ID {
		t.Fatalf("claim playing: req=%+v ok=%v err=%v", claimed, ok, err)
	}
	before, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}

	restarted := newEntSongStore(client, clock)
	after, err := restarted.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("新 store 实例必须完整读回请求:\nbefore=%+v\nafter=%+v", before, after)
	}
	if err := restarted.RecoverPlaying(ctx); err != nil {
		t.Fatal(err)
	}
	got, ok, err := restarted.Get(ctx, playing.ID)
	if err != nil || !ok || got.Status != song.RequestUnknown || got.ReconcileStatus != "pending" {
		t.Fatalf("重启恢复 playing: got=%+v ok=%v err=%v", got, ok, err)
	}
	other, _, err := restarted.Get(ctx, queued.ID)
	if err != nil || other.Status != song.RequestQueued {
		t.Fatalf("恢复不得改写 queued: got=%+v err=%v", other, err)
	}
	if ok, err := restarted.Complete(ctx, playing.ID, "play-crashed", nil); err != nil || ok {
		t.Fatalf("崩溃前 play_id 回执必须无效: ok=%v err=%v", ok, err)
	}
	next, ok, err := restarted.ClaimNext(ctx, "play-after-recovery")
	if err != nil || !ok || next.ID != queued.ID {
		t.Fatalf("恢复后只能调度原 queued: next=%+v ok=%v err=%v", next, ok, err)
	}
}

func TestEntSongRequestStoreSameSourceIsIdempotentAcrossInstances(t *testing.T) {
	ctx := context.Background()
	client := newEntTestClient(t)
	firstStore := newEntSongStore(client, time.Now)
	first := enqueueEntSongRequest(t, ctx, firstStore, "same-source", song.RequestQueued)

	secondStore := newEntSongStore(client, time.Now)
	again, duplicate, err := secondStore.Enqueue(ctx, song.Request{
		SessionID: first.SessionID, SourceEventRowID: first.SourceEventRowID,
		UserKey: "different-user", File: "different.wav",
	})
	if err != nil || !duplicate || again.ID != first.ID || again.File != first.File {
		t.Fatalf("同源重放必须返回原请求: again=%+v duplicate=%v err=%v", again, duplicate, err)
	}
	list, err := secondStore.List(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("同源请求只能持久化一行: list=%+v err=%v", list, err)
	}
}

func TestEntSongRequestStoreClaimNextIsFIFO(t *testing.T) {
	ctx := context.Background()
	store := newEntSongStore(newEntTestClient(t), time.Now)
	first := enqueueEntSongRequest(t, ctx, store, "fifo-a", song.RequestQueued)
	second := enqueueEntSongRequest(t, ctx, store, "fifo-b", song.RequestQueued)
	if first.QueueSeq >= second.QueueSeq {
		t.Fatalf("queue_seq 必须递增: first=%d second=%d", first.QueueSeq, second.QueueSeq)
	}
	claimed, ok, err := store.ClaimNext(ctx, "play-fifo-a")
	if err != nil || !ok || claimed.ID != first.ID || claimed.Attempt != 1 {
		t.Fatalf("FIFO 首曲: claimed=%+v ok=%v err=%v", claimed, ok, err)
	}
	if ok, err := store.Complete(ctx, first.ID, claimed.PlayID, nil); err != nil || !ok {
		t.Fatalf("完成首曲: ok=%v err=%v", ok, err)
	}
	claimed, ok, err = store.ClaimNext(ctx, "play-fifo-b")
	if err != nil || !ok || claimed.ID != second.ID {
		t.Fatalf("FIFO 次曲: claimed=%+v ok=%v err=%v", claimed, ok, err)
	}
}

func TestEntSongRequestStoreChangeStatusLegalTransitions(t *testing.T) {
	tests := []struct {
		name    string
		initial string
		action  string
		want    string
	}{
		{name: "approve pending", initial: song.RequestPendingConfirm, action: song.RequestActionApprove, want: song.RequestQueued},
		{name: "reject pending", initial: song.RequestPendingConfirm, action: song.RequestActionReject, want: song.RequestRejected},
		{name: "cancel pending", initial: song.RequestPendingConfirm, action: song.RequestActionCancel, want: song.RequestCancelled},
		{name: "cancel queued", initial: song.RequestQueued, action: song.RequestActionCancel, want: song.RequestCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
			store := newEntSongStore(newEntTestClient(t), func() time.Time { return now })
			req := enqueueEntSongRequest(t, ctx, store, "legal-"+tt.name, tt.initial)
			if err := store.ChangeStatus(ctx, req.ID, tt.action); err != nil {
				t.Fatalf("合法迁移失败: %v", err)
			}
			got, ok, err := store.Get(ctx, req.ID)
			if err != nil || !ok || got.Status != tt.want || got.Version != req.Version+1 {
				t.Fatalf("迁移结果: got=%+v ok=%v err=%v", got, ok, err)
			}
			if (tt.want == song.RequestRejected || tt.want == song.RequestCancelled) && got.TerminalAtMs != now.UnixMilli() {
				t.Fatalf("终态时间未持久化: %+v", got)
			}
		})
	}
}

func TestEntSongRequestStoreChangeStatusRejectsInvalidTransitions(t *testing.T) {
	tests := []struct {
		name    string
		initial string
		action  string
	}{
		{name: "approve queued", initial: song.RequestQueued, action: song.RequestActionApprove},
		{name: "reject queued", initial: song.RequestQueued, action: song.RequestActionReject},
		{name: "cancel playing", initial: song.RequestPlaying, action: song.RequestActionCancel},
		{name: "cancel done", initial: song.RequestDone, action: song.RequestActionCancel},
		{name: "cancel rejected", initial: song.RequestRejected, action: song.RequestActionCancel},
		{name: "unknown action", initial: song.RequestPendingConfirm, action: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store := newEntSongStore(newEntTestClient(t), time.Now)
			initial := tt.initial
			if initial == song.RequestPlaying || initial == song.RequestDone {
				initial = song.RequestQueued
			} else if initial == song.RequestRejected {
				initial = song.RequestPendingConfirm
			}
			req := enqueueEntSongRequest(t, ctx, store, "invalid-"+tt.name, initial)
			if tt.initial == song.RequestPlaying || tt.initial == song.RequestDone {
				claimed, ok, err := store.ClaimNext(ctx, "play-admin")
				if err != nil || !ok {
					t.Fatalf("准备 playing: ok=%v err=%v", ok, err)
				}
				req = claimed
				if tt.initial == song.RequestDone {
					if ok, err := store.Complete(ctx, req.ID, req.PlayID, nil); err != nil || !ok {
						t.Fatalf("准备 done: ok=%v err=%v", ok, err)
					}
				}
			}
			if tt.initial == song.RequestRejected {
				if err := store.ChangeStatus(ctx, req.ID, song.RequestActionReject); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.ChangeStatus(ctx, req.ID, tt.action); !errors.Is(err, song.ErrInvalidRequestTransition) {
				t.Fatalf("非法迁移必须返回显式错误: %v", err)
			}
			got, ok, err := store.Get(ctx, req.ID)
			if err != nil || !ok || got.Status != tt.initial {
				t.Fatalf("非法迁移不得改状态: got=%+v ok=%v err=%v", got, ok, err)
			}
		})
	}
	for _, action := range []string{song.RequestActionApprove, song.RequestActionReject, song.RequestActionCancel, "unknown"} {
		store := newEntSongStore(newEntTestClient(t), time.Now)
		if err := store.ChangeStatus(context.Background(), "missing", action); !errors.Is(err, song.ErrRequestNotFound) {
			t.Fatalf("action=%s not found err=%v", action, err)
		}
	}
}

func TestSwitchableSongRequestStoreUsesEntAfterCutover(t *testing.T) {
	ctx := context.Background()
	mem := song.NewMemoryRequestStore(song.QueuePolicy{}, time.Now)
	sw := NewSwitchableSongRequestStore(mem, song.QueuePolicy{UserCooldown: time.Nanosecond}, time.Now)
	client := newEntTestClient(t)
	sw.SetEnt(client)
	req, duplicate, err := sw.Enqueue(ctx, song.Request{
		SessionID: "s1", SourceEventRowID: "switchable-source", UserKey: "u1", File: "a.wav",
	})
	if err != nil || duplicate {
		t.Fatalf("热切后入队: duplicate=%v err=%v", duplicate, err)
	}
	persisted, ok, err := NewEntSongRequestStore(client, song.QueuePolicy{}, time.Now).Get(ctx, req.ID)
	if err != nil || !ok || persisted.SourceEventRowID != "switchable-source" {
		t.Fatalf("热切后请求必须写入 Ent: req=%+v ok=%v err=%v", persisted, ok, err)
	}
	memoryRows, err := mem.List(ctx)
	if err != nil || len(memoryRows) != 0 {
		t.Fatalf("热切后不得继续写内存: rows=%+v err=%v", memoryRows, err)
	}
}

func TestEntSongRequestStorePlaybackTransitionsCASPlayID(t *testing.T) {
	ctx := context.Background()
	store := newEntSongStore(newEntTestClient(t), time.Now)
	req := enqueueEntSongRequest(t, ctx, store, "cas-play-id", song.RequestQueued)
	oldPlay, ok, err := store.ClaimNext(ctx, "play-old")
	if err != nil || !ok {
		t.Fatalf("claim old: ok=%v err=%v", ok, err)
	}
	if ok, err := store.RequeueNotStarted(ctx, req.ID, oldPlay.PlayID, song.ErrNotLive); err != nil || !ok {
		t.Fatalf("未开始播放应可重排队: ok=%v err=%v", ok, err)
	}
	newPlay, ok, err := store.ClaimNext(ctx, "play-new")
	if err != nil || !ok {
		t.Fatalf("claim new: ok=%v err=%v", ok, err)
	}
	if ok, err := store.Complete(ctx, req.ID, oldPlay.PlayID, nil); err != nil || ok {
		t.Fatalf("旧 play_id 回执必须无效: ok=%v err=%v", ok, err)
	}
	if ok, err := store.FailNotStarted(ctx, req.ID, oldPlay.PlayID, errors.New("stale")); err != nil || ok {
		t.Fatalf("旧 play_id 启动失败回执必须无效: ok=%v err=%v", ok, err)
	}
	got, _, err := store.Get(ctx, req.ID)
	if err != nil || got.Status != song.RequestPlaying || got.PlayID != newPlay.PlayID {
		t.Fatalf("旧回执破坏新播放: got=%+v err=%v", got, err)
	}
	if ok, err := store.Complete(ctx, req.ID, newPlay.PlayID, nil); err != nil || !ok {
		t.Fatalf("当前 play_id 回执应成功: ok=%v err=%v", ok, err)
	}
}
