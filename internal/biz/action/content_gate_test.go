package action

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/contentgate"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

func newActionContentGate(t *testing.T) *contentgate.Gate {
	t.Helper()
	path := filepath.Join(t.TempDir(), "content_gate.yaml")
	if err := os.WriteFile(path, []byte("layers:\n  vulgar:\n    decision: block\n    words: [低俗词]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return contentgate.New(contentgate.Options{Enabled: true, RulesPath: path, CircuitBreakerThreshold: 3})
}

func TestContentGateBlockedActionIsSuppressedWithoutFakeSentEvent(t *testing.T) {
	sender := NewFakeSender()
	bus := eventbus.New(16)
	scheduler := NewScheduler(sender, bus, DefaultConfig())
	defer scheduler.Close()
	scheduler.SetContentGate(newActionContentGate(t), func() string { return "s1" })
	events, cancel := bus.Subscribe(0)
	defer cancel()

	if err := scheduler.SendDirect(context.Background(), "低俗词", "manual"); err != nil {
		t.Fatal(err)
	}
	if got := sender.Sent(); len(got) != 0 {
		t.Fatalf("命中动作必须直接抑制，不得发送替代动作: %v", got)
	}
	select {
	case event := <-events:
		if event.Type == "action.sent" {
			t.Fatalf("不得伪造安全动作已发送事件: %+v", event)
		}
	case <-time.After(20 * time.Millisecond):
	}
	if got := scheduler.Status().Fired; got != 0 {
		t.Fatalf("被抑制动作不得计入已发送: %d", got)
	}
}

func TestContentGateAllowedActionStillSends(t *testing.T) {
	sender := NewFakeSender()
	bus := eventbus.New(16)
	scheduler := NewScheduler(sender, bus, DefaultConfig())
	defer scheduler.Close()
	scheduler.SetContentGate(newActionContentGate(t), func() string { return "s1" })
	events, cancel := bus.Subscribe(0)
	defer cancel()

	if err := scheduler.SendDirect(context.Background(), "挥手", "manual"); err != nil {
		t.Fatal(err)
	}
	if got := sender.Sent(); len(got) != 1 || got[0] != "挥手" {
		t.Fatalf("正常动作必须保持不变: %v", got)
	}
	select {
	case event := <-events:
		if event.Type != "action.sent" {
			t.Fatalf("正常动作应发布真实 sent 事件: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("正常动作缺少 action.sent")
	}
}
