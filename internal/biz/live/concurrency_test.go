package live

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/teamwork/digital-human/pkg/eventbus"
)

// concController 并发安全的控制器，Start 带 1ms 延迟放大竞态窗口。
type concController struct {
	mu      sync.Mutex
	started int
	stopped int
}

func (c *concController) Start(context.Context, StartOpts) (string, error) {
	time.Sleep(time.Millisecond)
	c.mu.Lock()
	c.started++
	c.mu.Unlock()
	return fmt.Sprintf("live-%d", c.started), nil
}

func (c *concController) Stop(context.Context, string) error {
	c.mu.Lock()
	c.stopped++
	c.mu.Unlock()
	return nil
}

func (c *concController) Say(context.Context, string, string) (string, error) { return "u", nil }

// TestConcurrentStartStopConsistency 并发 50 路 Start/Stop 混合轰炸：
// 不追求某一路的输赢，只断言不变量——最终全场至多一个 live 场次，
// 且若存在 live 场次，它必须等于 Manager 当前在场 session。
func TestConcurrentStartStopConsistency(t *testing.T) {
	bus := eventbus.New(256)
	store := NewMemorySessionStore()
	ctrl := &concController{}
	m := NewManagerWithStores(bus, NewMemorySink(256), ctrl, store,
		NewMemoryIdempotencyStore(time.Hour), nil)

	var wg sync.WaitGroup
	sidCh := make(chan string, 50)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				sid, phase, _, err := m.Start(context.Background(), "", fmt.Sprintf("req-%d", i), "")
				if err == nil && phase == PhaseLive {
					sidCh <- sid
				}
			} else {
				_, _, _ = m.Stop(context.Background(), "concurrent-test")
			}
		}(i)
	}
	wg.Wait()
	close(sidCh)

	var started []string
	for sid := range sidCh {
		started = append(started, sid)
	}
	liveCount := 0
	var liveSID string
	for _, sid := range started {
		r, ok := store.Get(sid)
		if !ok {
			continue
		}
		if r.Phase == PhaseLive || r.Phase == PhaseStarting {
			liveCount++
			liveSID = sid
		}
	}
	if liveCount > 1 {
		t.Fatalf("并发后存在 %d 个 live/starting 场次，双建场未收敛", liveCount)
	}
	snap := m.Snapshot()
	if liveCount == 1 {
		if snap.Phase != PhaseLive || snap.SessionID != liveSID {
			t.Fatalf("store 中 live=%s，但 Manager 在场为 %s(%s)", liveSID, snap.SessionID, snap.Phase)
		}
	} else if snap.Phase == PhaseLive {
		t.Fatalf("Manager 认为 live=%s，但 store 中无 live 场次", snap.SessionID)
	}
	t.Logf("50 路并发: Start 成功 %d 次，Stop 调用 %d 次，最终 live=%d", len(started), ctrl.stopped, liveCount)
}

// TestConcurrentSameIdempotencyKey 同一幂等键并发 20 次 Start：
// 只允许创建一场，且所有成功者拿到同一 session_id。
func TestConcurrentSameIdempotencyKey(t *testing.T) {
	bus := eventbus.New(256)
	ctrl := &concController{}
	m := NewManagerWithStores(bus, NewMemorySink(256), ctrl, nil,
		NewMemoryIdempotencyStore(time.Hour), nil)

	var wg sync.WaitGroup
	sids := make(chan string, 20)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sid, phase, _, err := m.StartIdempotent(context.Background(), "same-key", "", "", "")
			if err == nil && phase == PhaseLive {
				sids <- sid
			}
		}()
	}
	wg.Wait()
	close(sids)

	var first string
	n := 0
	for sid := range sids {
		if n == 0 {
			first = sid
		}
		if sid != first {
			t.Fatalf("同幂等键返回了不同 session: %s vs %s", sid, first)
		}
		n++
	}
	if n == 0 {
		t.Fatal("同幂等键并发全部失败")
	}
	if got := ctrl.started; got != 1 {
		t.Fatalf("同幂等键并发应只建一场，实际 %d", got)
	}
}
