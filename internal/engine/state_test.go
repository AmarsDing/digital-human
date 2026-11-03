package engine

import (
	"errors"
	"testing"
)

func TestHappyPathTransitions(t *testing.T) {
	s := NewStateMachine()
	steps := []Phase{PhaseStarting, PhaseLive, PhaseStopping, PhaseEnded, PhaseStarting, PhaseLive}
	for _, to := range steps {
		if err := s.Transition(to); err != nil {
			t.Fatalf("transition to %s: %v", to, err)
		}
	}
	if s.Phase() != PhaseLive {
		t.Fatalf("phase=%s want live", s.Phase())
	}
}

func TestIllegalTransitionsRejected(t *testing.T) {
	cases := []struct{ from, to Phase }{
		{PhaseIdle, PhaseLive},
		{PhaseIdle, PhaseEnded},
		{PhaseLive, PhaseStarting},
		{PhaseEnded, PhaseLive},
	}
	for _, c := range cases {
		s := NewStateMachine()
		// 用合法路径把状态带到 from。
		switch c.from {
		case PhaseLive:
			_ = s.Transition(PhaseStarting)
			_ = s.Transition(PhaseLive)
		case PhaseEnded:
			_ = s.Transition(PhaseStarting)
			_ = s.Transition(PhaseLive)
			_ = s.Transition(PhaseStopping)
			_ = s.Transition(PhaseEnded)
		}
		err := s.Transition(c.to)
		var inv *InvalidTransitionError
		if !errors.As(err, &inv) {
			t.Fatalf("%s->%s: want InvalidTransitionError, got %v", c.from, c.to, err)
		}
	}
}

func TestFailClearsOnRecovery(t *testing.T) {
	s := NewStateMachine()
	_ = s.Transition(PhaseStarting)
	if err := s.Fail("vidu_create_timeout", "create timed out"); err != nil {
		t.Fatal(err)
	}
	code, msg := s.ErrorInfo()
	if code != "vidu_create_timeout" || msg == "" {
		t.Fatalf("error info=%q,%q", code, msg)
	}
	// 蓝本语义：error 可被下一次 Start 覆盖。
	if err := s.Transition(PhaseStarting); err != nil {
		t.Fatalf("recover from error: %v", err)
	}
	code, msg = s.ErrorInfo()
	if code != "" || msg != "" {
		t.Fatalf("errors should be cleared, got %q,%q", code, msg)
	}
}

func TestStopWhileStartingAllowed(t *testing.T) {
	s := NewStateMachine()
	_ = s.Transition(PhaseStarting)
	if err := s.Transition(PhaseStopping); err != nil {
		t.Fatalf("stop while starting (reconnect) must be allowed: %v", err)
	}
}
