package live

import (
	"context"
	"testing"

	"github.com/teamwork/digital-human/pkg/eventbus"
)

type recController struct {
	started int
	stops   []string
	said    int
}

func (c *recController) Start(context.Context, StartOpts) (string, error) {
	c.started++
	return "live-x", nil
}
func (c *recController) Stop(_ context.Context, reason string) error {
	c.stops = append(c.stops, reason)
	return nil
}
func (c *recController) Say(context.Context, string, string) (string, error) {
	c.said++
	return "u", nil
}

func TestRestartStopsWithReconnectFirst(t *testing.T) {
	ctrl := &recController{}
	m := NewManager(eventbus.New(64), NewMemorySink(64), ctrl)

	if _, phase, _, err := m.Start(context.Background(), "p", "a", ""); err != nil || phase != PhaseLive {
		t.Fatalf("first start: phase=%s err=%v", phase, err)
	}
	if _, phase, _, err := m.Start(context.Background(), "p", "a", ""); err != nil || phase != PhaseLive {
		t.Fatalf("restart: phase=%s err=%v", phase, err)
	}
	if ctrl.started != 2 {
		t.Fatalf("want 2 starts, got %d", ctrl.started)
	}
	if len(ctrl.stops) != 1 || ctrl.stops[0] != "reconnect" {
		t.Fatalf("want stop(reconnect) before restart, got %v", ctrl.stops)
	}
}

func TestStopIsIdempotent(t *testing.T) {
	m := NewManager(eventbus.New(64), NewMemorySink(64), &recController{})
	ok, note, err := m.Stop(context.Background(), "manual")
	if err != nil || !ok || note == "" {
		t.Fatalf("idle stop should be idempotent ok, got ok=%v note=%q err=%v", ok, note, err)
	}
	if snap := m.Snapshot(); snap.Phase != PhaseIdle {
		t.Fatalf("phase changed on idle stop: %s", snap.Phase)
	}
}

func TestSayRequiresLive(t *testing.T) {
	m := NewManager(eventbus.New(64), NewMemorySink(64), &recController{})
	if _, err := m.Say(context.Background(), "hi", "sentence"); err == nil {
		t.Fatal("say before live should fail")
	}
}
