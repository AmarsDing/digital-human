package interaction

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"
)

func danceTestMapping() ActionMapping {
	return ActionMapping{
		Trigger: "霓虹", Instruction: "dance-neon", Revision: "rev-1",
		Allowed: true, CapabilityStatus: "real", DurationMS: 1000,
	}
}

func newDanceTestQueue(capacity int, now *time.Time) *DanceQueue {
	return NewDanceQueue(capacity, "rev-1", []ActionMapping{danceTestMapping()}, func() time.Time { return *now })
}

func TestDanceCommandParsingRejectsReadably(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name string
		text string
		want string
	}{
		{name: "empty", text: "点舞", want: "dance_target_required"},
		{name: "unicode whitespace only", text: "点舞 　", want: "dance_target_required"},
		{name: "inner ascii whitespace", text: "点舞 霓 虹", want: "dance_target_invalid"},
		{name: "inner full width whitespace", text: "点舞 霓　虹", want: "dance_target_invalid"},
		{name: "similar", text: "点舞 霓虹舞", want: "dance_not_allowed"},
		{name: "case sensitive", text: "点舞 霓 虹", want: "dance_target_invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			q := newDanceTestQueue(2, &now)
			_, effect, reason := q.Enqueue("s1", "u1", tt.text)
			if reason != tt.want || effect != nil {
				t.Fatalf("reason=%q effect=%v want=%q", reason, effect, tt.want)
			}
			status := q.Status()
			if status.Rejected != 1 || status.LastRejected == nil || status.LastRejected.State != DanceRejected {
				t.Fatalf("拒绝必须进入状态快照: %+v", status)
			}
		})
	}

	q := NewDanceQueue(2, "rev-1", []ActionMapping{{
		Trigger: "Dance", Instruction: "dance-case", Revision: "rev-1",
		Allowed: true, CapabilityStatus: "real", DurationMS: 1000,
	}}, func() time.Time { return now })
	if _, _, reason := q.Enqueue("s1", "u1", "点舞 dance"); reason != "dance_not_allowed" {
		t.Fatalf("不得忽略大小写或模糊匹配: %q", reason)
	}
	if _, effect, reason := q.Enqueue("s1", "u1", "点舞　 Dance 　"); reason != "" || effect == nil {
		t.Fatalf("Unicode 首尾空白应 trimming 后精确命中: reason=%q effect=%v", reason, effect)
	}
}

func TestDanceQueueRequiresWhitelistAndDuration(t *testing.T) {
	now := time.Now()
	for _, tt := range []struct {
		name   string
		mutate func(*ActionMapping)
		want   string
	}{
		{name: "not selected", mutate: func(m *ActionMapping) { m.Allowed = false }, want: "dance_not_allowed"},
		{name: "stale revision", mutate: func(m *ActionMapping) { m.Revision = "old" }, want: "dance_not_allowed"},
		{name: "capability unavailable", mutate: func(m *ActionMapping) { m.CapabilityStatus = "unknown" }, want: "dance_not_allowed"},
		{name: "duration missing", mutate: func(m *ActionMapping) { m.DurationMS = 0 }, want: "dance_duration_unconfigured"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			mapping := danceTestMapping()
			tt.mutate(&mapping)
			q := NewDanceQueue(2, "rev-1", []ActionMapping{mapping}, func() time.Time { return now })
			_, effect, reason := q.Enqueue("s1", "u1", "点舞 霓虹")
			if reason != tt.want || effect != nil {
				t.Fatalf("reason=%q effect=%v want=%q", reason, effect, tt.want)
			}
		})
	}
}

func TestDanceQueueBoundedFIFO(t *testing.T) {
	now := time.Now()
	q := NewDanceQueue(2, "rev-1", []ActionMapping{
		danceTestMapping(),
		{Trigger: "挥手", Instruction: "wave-01", Revision: "rev-1", Allowed: true, CapabilityStatus: "real", DurationMS: 1000},
	}, func() time.Time { return now })
	first, firstEffect, reason := q.Enqueue("s1", "u1", "点舞 霓虹")
	if reason != "" || firstEffect == nil || first.State != DanceDispatched {
		t.Fatalf("首项应立即进入动作 outbox: item=%+v effect=%+v reason=%q", first, firstEffect, reason)
	}
	second, secondEffect, reason := q.Enqueue("s1", "u2", "点舞 挥手")
	if reason != "" || secondEffect != nil || second.State != DanceQueued {
		t.Fatalf("第二项应等待: item=%+v effect=%+v reason=%q", second, secondEffect, reason)
	}
	if _, effect, reason := q.Enqueue("s1", "u3", "点舞 霓虹"); reason != "dance_queue_full" || effect != nil {
		t.Fatalf("当前项+等待项满员必须明确拒绝: effect=%v reason=%q", effect, reason)
	}
	status := q.Status()
	if status.Capacity != 2 || status.QueueLength != 1 || status.Rejected != 1 || status.Current == nil || status.Current.Trigger != "霓虹" {
		t.Fatalf("有界队列状态: %+v", status)
	}
	t.Logf("dance_queue_full actual: Rejected=%d QueueLength=%d Capacity=%d", status.Rejected, status.QueueLength, status.Capacity)
	now = now.Add(time.Second)
	nextEffect := q.Advance()
	if nextEffect == nil || nextEffect.PayloadSafe["instruction"] != "wave-01" {
		t.Fatalf("到期后必须按 FIFO 派发第二项: %+v", nextEffect)
	}
	status = q.Status()
	if status.Current == nil || status.Current.Trigger != "挥手" || status.LastCompleted == nil || status.LastCompleted.State != DanceCompletedUnverified {
		t.Fatalf("到期只形成 completed_unverified 簿记: %+v", status)
	}
}

func TestDanceAdvanceDefersWhileSongOrChatActive(t *testing.T) {
	for _, kind := range []string{"song", "chat"} {
		t.Run(kind, func(t *testing.T) {
			now := time.Now()
			q := newDanceTestQueue(2, &now)
			songPlaying, chatActive := false, false
			q.SetActivity(func() bool { return songPlaying }, func(string) bool { return chatActive })
			if kind == "song" {
				songPlaying = true
			} else {
				chatActive = true
			}
			first, effect, reason := q.Enqueue("s1", "u1", "点舞 霓虹")
			if reason != "" || effect != nil || first.State != DanceQueued {
				t.Fatalf("繁忙时首项只能排队不得抢占: item=%+v effect=%v reason=%q", first, effect, reason)
			}
			second, effect, reason := q.Enqueue("s1", "u2", "点舞 霓虹")
			if reason != "" || effect != nil || second.State != DanceQueued {
				t.Fatalf("等待项必须保持 FIFO: item=%+v effect=%v reason=%q", second, effect, reason)
			}
			before := q.Status()
			if before.QueueLength != 1 || len(before.Waiting) != 1 || before.Waiting[0].ID != second.ID {
				t.Fatalf("延后前必须有一个确定的等待项: %+v", before)
			}
			if effect := q.Advance(); effect != nil {
				t.Fatalf("繁忙时不得派发: %+v", effect)
			}
			songPlaying, chatActive = false, false
			if effect := q.Advance(); effect == nil {
				t.Fatal("空闲后必须派发保留的当前项")
			}
			if kind == "song" {
				songPlaying = true
			} else {
				chatActive = true
			}
			now = now.Add(time.Second)
			if effect := q.Advance(); effect != nil {
				t.Fatalf("到期遇繁忙不得推进: %+v", effect)
			}
			status := q.Status()
			if !status.AdvanceDeferred || status.Current == nil || status.Current.ID != first.ID || status.LastCompleted != nil {
				t.Fatalf("延后推进不得丢项或伪造完成: %+v", status)
			}
			if status.QueueLength != before.QueueLength || len(status.Waiting) != 1 || status.Waiting[0].ID != second.ID {
				t.Fatalf("延后推进必须保持等待队列长度和顺序: before=%+v after=%+v", before, status)
			}
			songPlaying, chatActive = false, false
			now = now.Add(time.Second)
			effect = q.Advance()
			if effect == nil || effect.PayloadSafe["dance_id"] != second.ID {
				t.Fatalf("空闲后必须推进原来的等待项: effect=%+v want=%s", effect, second.ID)
			}
			status = q.Status()
			if status.AdvanceDeferred || status.Current == nil || status.Current.ID != second.ID || status.QueueLength != 0 || status.LastCompleted == nil || status.LastCompleted.ID != first.ID {
				t.Fatalf("空闲后必须完成被延后的当前项并保留 FIFO: %+v", status)
			}
		})
	}
}

func TestDanceCancelCurrentKeepsDispatchFactAndAdvances(t *testing.T) {
	now := time.Now()
	q := NewDanceQueue(2, "rev-1", []ActionMapping{
		danceTestMapping(),
		{Trigger: "挥手", Instruction: "wave-01", Revision: "rev-1", Allowed: true, CapabilityStatus: "real", DurationMS: 1000},
	}, func() time.Time { return now })
	_, _, _ = q.Enqueue("s1", "u1", "点舞 霓虹")
	_, _, _ = q.Enqueue("s1", "u2", "点舞 挥手")
	cancelled, nextEffect := q.CancelCurrent()
	if !cancelled || nextEffect == nil || nextEffect.PayloadSafe["instruction"] != "wave-01" {
		t.Fatalf("取消只做簿记并推进下一项: cancelled=%v next=%+v", cancelled, nextEffect)
	}
	status := q.Status()
	if status.LastCancelled == nil || status.LastCancelled.State != DanceCancelledUnverified || !status.LastCancelled.Dispatched {
		t.Fatalf("已派发取消必须保留此前 dispatched 事实: %+v", status.LastCancelled)
	}
	if status.Current == nil || status.Current.Trigger != "挥手" {
		t.Fatalf("取消后当前项: %+v", status.Current)
	}
}

func TestDanceCancelUndispatchedAndClearSemantics(t *testing.T) {
	now := time.Now()
	q := newDanceTestQueue(3, &now)
	playing := true
	q.SetActivity(func() bool { return playing }, func(string) bool { return false })
	_, _, _ = q.Enqueue("s1", "u1", "点舞 霓虹")
	_, _, _ = q.Enqueue("s1", "u2", "点舞 霓虹")
	if cleared, next := q.Clear(false); cleared != 1 || next != nil {
		t.Fatalf("Clear 默认只清等待项: cleared=%d next=%v", cleared, next)
	}
	if q.Status().Current == nil || !playing {
		t.Fatal("默认 Clear 不得取消当前项或改变歌曲状态")
	}
	cancelled, next := q.CancelCurrent()
	if !cancelled || next != nil || q.Status().LastCancelled.Dispatched || !playing {
		t.Fatalf("未派发取消不得伪称已派发或影响歌曲: cancelled=%v next=%v status=%+v", cancelled, next, q.Status())
	}
	_, _, _ = q.Enqueue("s1", "u1", "点舞 霓虹")
	cleared, next := q.Clear(true)
	if cleared != 0 || next != nil || q.Status().Current != nil || q.Status().LastCancelled == nil || !playing {
		t.Fatalf("includeCurrent 只能簿记取消当前项: cleared=%d next=%v status=%+v", cleared, next, q.Status())
	}
}

func TestDanceStatusReturnsDeepSnapshot(t *testing.T) {
	now := time.Now()
	q := newDanceTestQueue(3, &now)
	_, _, _ = q.Enqueue("s1", "u1", "点舞 霓虹")
	_, _, _ = q.Enqueue("s1", "u2", "点舞 霓虹")
	snapshot := q.Status()
	snapshot.Current.Trigger = "篡改当前"
	snapshot.Waiting[0].Trigger = "篡改等待"
	status := q.Status()
	if status.Current.Trigger != "霓虹" || status.Waiting[0].Trigger != "霓虹" {
		t.Fatalf("Status 不得暴露内部可变结构: %+v", status)
	}
}

func TestDanceQueueConcurrentEntrypoints(t *testing.T) {
	now := time.Now()
	q := NewDanceQueue(100, "rev-1", []ActionMapping{danceTestMapping()}, func() time.Time { return now })
	var wg sync.WaitGroup
	for worker := 0; worker < 8; worker++ {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				q.Enqueue("s1", fmt.Sprintf("u-%d-%d", worker, i), "点舞 霓虹")
				_ = q.Status()
				q.Advance()
				if i%10 == 0 {
					q.CancelCurrent()
				}
			}
		}(worker)
	}
	wg.Wait()
}

func TestRuleEngineDanceFailuresNeverFallBackToChat(t *testing.T) {
	for _, tt := range []struct {
		text   string
		reason string
	}{
		{text: "点舞", reason: "dance_target_required"},
		{text: "点舞 霓 虹", reason: "dance_target_invalid"},
		{text: "点舞 相似", reason: "dance_not_allowed"},
	} {
		st := NewMemoryStore()
		now := time.Now()
		queue := NewDanceQueue(2, "rev-1", []ActionMapping{danceTestMapping()}, func() time.Time { return now })
		engine := NewRuleEngine(NewService(st), RulePolicy{
			RuleVersion: "rules-v1", Revision: "rev-1", LLMAvailable: true, Dances: queue,
		}, func() time.Time { return now })
		decision, effects, err := engine.Handle(context.Background(), ruleEvent("src-"+tt.reason, "u1", tt.text))
		if err != nil {
			t.Fatal(err)
		}
		if decision.Intent != IntentDance || decision.Decision != DecideBlocked || decision.ReasonCode != tt.reason || len(effects) != 0 {
			t.Fatalf("点舞失败必须可读拒绝且不落普通聊天: d=%+v effects=%+v", decision, effects)
		}
	}
}
