package song

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestMemoryRequestStoreChangeStatusLegalTransitions(t *testing.T) {
	tests := []struct {
		name    string
		initial string
		action  string
		want    string
	}{
		{name: "approve pending", initial: RequestPendingConfirm, action: RequestActionApprove, want: RequestQueued},
		{name: "reject pending", initial: RequestPendingConfirm, action: RequestActionReject, want: RequestRejected},
		{name: "cancel pending", initial: RequestPendingConfirm, action: RequestActionCancel, want: RequestCancelled},
		{name: "cancel queued", initial: RequestQueued, action: RequestActionCancel, want: RequestCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
			store := NewMemoryRequestStore(QueuePolicy{MaxQueue: 10}, func() time.Time { return now })
			req, _, err := store.Enqueue(ctx, Request{
				SessionID: "s1", SourceEventRowID: "src-" + tt.action + "-" + tt.initial,
				UserKey: "u1", File: "a.wav", Status: tt.initial,
			})
			if err != nil {
				t.Fatal(err)
			}
			if err := store.ChangeStatus(ctx, req.ID, tt.action); err != nil {
				t.Fatalf("合法迁移失败: %v", err)
			}
			got, ok, err := store.Get(ctx, req.ID)
			if err != nil || !ok {
				t.Fatalf("读取迁移结果: ok=%v err=%v", ok, err)
			}
			if got.Status != tt.want || got.Version != req.Version+1 {
				t.Fatalf("迁移结果: got=%+v want status=%s version=%d", got, tt.want, req.Version+1)
			}
			if (tt.want == RequestRejected || tt.want == RequestCancelled) && got.TerminalAtMs != now.UnixMilli() {
				t.Fatalf("终态时间未持久化: %+v", got)
			}
		})
	}
}

func TestMemoryRequestStoreChangeStatusRejectsInvalidTransitions(t *testing.T) {
	tests := []struct {
		name    string
		initial string
		action  string
	}{
		{name: "approve queued", initial: RequestQueued, action: RequestActionApprove},
		{name: "reject queued", initial: RequestQueued, action: RequestActionReject},
		{name: "cancel playing", initial: RequestPlaying, action: RequestActionCancel},
		{name: "cancel done", initial: RequestDone, action: RequestActionCancel},
		{name: "approve rejected", initial: RequestRejected, action: RequestActionApprove},
		{name: "unknown action", initial: RequestPendingConfirm, action: "unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			store := NewMemoryRequestStore(QueuePolicy{MaxQueue: 10}, time.Now)
			initial := tt.initial
			if initial == RequestPlaying || initial == RequestDone {
				initial = RequestQueued
			} else if initial == RequestRejected {
				initial = RequestPendingConfirm
			}
			req, _, err := store.Enqueue(ctx, Request{
				SessionID: "s1", SourceEventRowID: "src-invalid-" + tt.name,
				UserKey: "u1", File: "a.wav", Status: initial,
			})
			if err != nil {
				t.Fatal(err)
			}
			if tt.initial == RequestPlaying || tt.initial == RequestDone {
				claimed, ok, err := store.ClaimNext(ctx, "play-admin")
				if err != nil || !ok {
					t.Fatalf("准备 playing: ok=%v err=%v", ok, err)
				}
				req = claimed
				if tt.initial == RequestDone {
					if ok, err := store.Complete(ctx, req.ID, req.PlayID, nil); err != nil || !ok {
						t.Fatalf("准备 done: ok=%v err=%v", ok, err)
					}
				}
			}
			if tt.initial == RequestRejected {
				if err := store.ChangeStatus(ctx, req.ID, RequestActionReject); err != nil {
					t.Fatal(err)
				}
			}
			if err := store.ChangeStatus(ctx, req.ID, tt.action); !errors.Is(err, ErrInvalidRequestTransition) {
				t.Fatalf("非法迁移必须返回显式错误: %v", err)
			}
			got, ok, err := store.Get(ctx, req.ID)
			if err != nil || !ok || got.Status != tt.initial {
				t.Fatalf("非法迁移不得改状态: got=%+v ok=%v err=%v", got, ok, err)
			}
		})
	}
}

func TestMemoryRequestStoreChangeStatusNotFound(t *testing.T) {
	store := NewMemoryRequestStore(QueuePolicy{}, time.Now)
	for _, action := range []string{RequestActionApprove, RequestActionReject, RequestActionCancel, "unknown"} {
		if err := store.ChangeStatus(context.Background(), "missing", action); !errors.Is(err, ErrRequestNotFound) {
			t.Fatalf("action=%s not found err=%v", action, err)
		}
	}
}

func TestMemoryRequestStorePlaybackTransitionsCASPlayID(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryRequestStore(QueuePolicy{MaxQueue: 10}, time.Now)
	req, _, err := store.Enqueue(ctx, Request{SessionID: "s1", SourceEventRowID: "src-cas", UserKey: "u1", File: "a.wav"})
	if err != nil {
		t.Fatal(err)
	}
	oldPlay, ok, err := store.ClaimNext(ctx, "play-old")
	if err != nil || !ok {
		t.Fatalf("claim old: ok=%v err=%v", ok, err)
	}
	if ok, err := store.RequeueNotStarted(ctx, req.ID, oldPlay.PlayID, ErrNotLive); err != nil || !ok {
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
	if err != nil || got.Status != RequestPlaying || got.PlayID != newPlay.PlayID {
		t.Fatalf("旧回执破坏新播放: got=%+v err=%v", got, err)
	}
	if ok, err := store.Complete(ctx, req.ID, newPlay.PlayID, nil); err != nil || !ok {
		t.Fatalf("当前 play_id 回执应成功: ok=%v err=%v", ok, err)
	}
}
