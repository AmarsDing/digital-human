package danmaku

import (
	"testing"
	"time"
)

func TestTop10CalendarDay(t *testing.T) {
	svc := NewService(ModeAuto)
	now := time.Date(2026, 11, 3, 23, 59, 0, 0, time.Local)
	for i := 1; i <= 12; i++ {
		for j := 0; j < i; j++ {
			svc.NoteCommand("cmd-"+string(rune('a'+i-1)), now)
		}
	}
	top := svc.Top(now, 10)
	if len(top) != 10 {
		t.Fatalf("top len=%d", len(top))
	}
	if top[0].Text != "cmd-l" || top[0].Count != 12 {
		t.Fatalf("top[0]=%+v", top[0])
	}
	// 跨天清零（不是滚动 24h）。
	next := now.Add(2 * time.Minute)
	if got := svc.Top(next, 10); len(got) != 0 {
		t.Fatalf("next day top=%+v", got)
	}
}

func TestTopTrimsTo24Runes(t *testing.T) {
	svc := NewService(ModeOff)
	long := ""
	for i := 0; i < 30; i++ {
		long += "字"
	}
	now := time.Now()
	svc.NoteCommand(long, now)
	top := svc.Top(now, 10)
	if len([]rune(top[0].Text)) != 24 {
		t.Fatalf("trimmed len=%d", len([]rune(top[0].Text)))
	}
}

func TestState(t *testing.T) {
	svc := NewService(ModeOff)
	st := svc.State(time.Now())
	if st.Enabled || st.Mode != ModeOff {
		t.Fatalf("state=%+v", st)
	}
}
