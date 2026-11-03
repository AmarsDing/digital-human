package publish

import (
	"errors"
	"testing"
)

func TestStateMachineTransitions(t *testing.T) {
	m := newMachine("douyin", nil)
	seq := []struct {
		to     State
		reason string
		ok     bool
	}{
		{StatePushing, "", false}, // idle→pushing 非法
		{StateStarting, "", true},
		{StatePushing, "", true},
		{StateDegraded, "net", true},
		{StateStarting, "", true},
		{StateRejected, "key", true},
		{StatePushing, "", false}, // rejected→pushing 非法
		{StateStarting, "", true}, // 人工改 key 后允许重来
		{StateStopped, "", true},
		{StateStarting, "", true},
	}
	for i, s := range seq {
		err := m.transit(s.to, s.reason)
		if s.ok && err != nil {
			t.Fatalf("step %d: want ok, got %v", i, err)
		}
		if !s.ok && !errors.Is(err, ErrInvalidTransition) {
			t.Fatalf("step %d: want ErrInvalidTransition, got %v", i, err)
		}
	}
}

func TestStateMachineEvents(t *testing.T) {
	var got []State
	m := newMachine("douyin", func(e Event) { got = append(got, e.State) })
	_ = m.transit(StateStarting, "")
	_ = m.transit(StatePushing, "")
	if len(got) != 2 || got[0] != StateStarting || got[1] != StatePushing {
		t.Fatalf("events: %v", got)
	}
	if m.snapshot().LastError != "" {
		t.Fatal("pushing 后 LastError 应清空")
	}
}
