package billing

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// fakeCredits 可控额度源：测试随时改当前余额/错误，避免脚本时序竞态。
type fakeCredits struct {
	mu     sync.Mutex
	remain int64
	err    error
}

func (f *fakeCredits) set(remain int64, err error) {
	f.mu.Lock()
	f.remain, f.err = remain, err
	f.mu.Unlock()
}

func (f *fakeCredits) Credits(context.Context) (int64, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.remain, f.err
}

func waitLedger(t *testing.T, svc *Service, sessionID string, want func(Ledger) bool) Ledger {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		l, ok, err := svc.Get(context.Background(), sessionID)
		if err != nil {
			t.Fatal(err)
		}
		if ok && want(l) {
			return l
		}
		time.Sleep(5 * time.Millisecond)
	}
	l, ok, _ := svc.Get(context.Background(), sessionID)
	t.Fatalf("等待账本满足条件超时: ok=%v ledger=%+v", ok, l)
	return Ledger{}
}

// 开播记基线 → 轮询增量落账 → 下播停止并落最终一笔。
func TestPollerBaselineDeltaAndFinal(t *testing.T) {
	src := &fakeCredits{remain: 1000}
	svc := NewService(NewMemoryStore())
	p := NewPoller(svc, src, PollerOptions{Interval: 5 * time.Millisecond})

	p.OnSessionStart(context.Background(), "s1")
	l := waitLedger(t, svc, "s1", func(l Ledger) bool { return l.CreditsStart == 1000 })
	if l.Source != "vidu_poll" || l.CostSource != "vidu_credits_api" {
		t.Fatalf("来源标注错误: %+v", l)
	}
	src.set(850, nil)
	l = waitLedger(t, svc, "s1", func(l Ledger) bool { return l.CreditsEnd == 850 })
	if l.CreditsSpent != 150 {
		t.Fatalf("credits_spent=%d want 150", l.CreditsSpent)
	}
	if l.EndedAtMs != 0 {
		t.Fatalf("直播中不应有 ended_at: %+v", l)
	}

	src.set(700, nil)
	p.OnSessionStop(context.Background(), "s1")
	l = waitLedger(t, svc, "s1", func(l Ledger) bool { return l.EndedAtMs != 0 })
	if l.CreditsEnd != 700 || l.CreditsSpent != 300 {
		t.Fatalf("最终落账错误: %+v", l)
	}

	// 下播后轮询必须停止：再等几个周期，余额不得变化。
	src.set(400, nil)
	time.Sleep(30 * time.Millisecond)
	l2, _, _ := svc.Get(context.Background(), "s1")
	if l2.CreditsEnd != 700 {
		t.Fatalf("下播后仍在轮询: %+v", l2)
	}
}

// 基线查询失败不阻塞开播：首个成功 tick 补基线；连续失败触发事件，不中断。
func TestPollerFailureTolerance(t *testing.T) {
	src := &fakeCredits{err: errors.New("vidu timeout")}
	svc := NewService(NewMemoryStore())
	events := make(chan string, 8)
	p := NewPoller(svc, src, PollerOptions{
		Interval: 5 * time.Millisecond,
		EmitEvent: func(typ string, payload map[string]any) {
			events <- typ
		},
	})

	p.OnSessionStart(context.Background(), "s2")
	select {
	case typ := <-events:
		if typ != "billing.credits_poll_failed" {
			t.Fatalf("事件类型=%s", typ)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("连续失败应触发 billing.credits_poll_failed 事件")
	}
	// 恢复后补基线 500，再落到 480。
	src.set(500, nil)
	waitLedger(t, svc, "s2", func(l Ledger) bool { return l.CreditsStart == 500 })
	src.set(480, nil)
	l := waitLedger(t, svc, "s2", func(l Ledger) bool { return l.CreditsEnd == 480 })
	if l.CreditsSpent != 20 {
		t.Fatalf("credits_spent=%d want 20", l.CreditsSpent)
	}
	p.OnSessionStop(context.Background(), "s2")
}

// 换场次重开：基线必须重置（不得沿用上一场余额）。
func TestPollerBaselineResetAcrossSessions(t *testing.T) {
	src := &fakeCredits{remain: 1000}
	svc := NewService(NewMemoryStore())
	p := NewPoller(svc, src, PollerOptions{Interval: 5 * time.Millisecond})

	p.OnSessionStart(context.Background(), "s1")
	waitLedger(t, svc, "s1", func(l Ledger) bool { return l.CreditsStart == 1000 })
	src.set(900, nil)
	waitLedger(t, svc, "s1", func(l Ledger) bool { return l.CreditsEnd == 900 })
	p.OnSessionStop(context.Background(), "s1")

	src.set(2000, nil)
	p.OnSessionStart(context.Background(), "s2")
	l := waitLedger(t, svc, "s2", func(l Ledger) bool { return l.CreditsStart == 2000 })
	if l.CreditsSpent != 0 {
		t.Fatalf("新场次基线未重置: %+v", l)
	}
	p.OnSessionStop(context.Background(), "s2")
}
