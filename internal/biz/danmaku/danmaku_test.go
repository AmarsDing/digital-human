package danmaku

import (
	"context"
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

// F27/G24：SetKindTap 把 gift/join 事件分流到合并器/贵宾欢迎，批读 worker 不念语义不变。
func TestKindTap_GiftJoin(t *testing.T) {
	svc := NewService(ModeAuto)
	var got []string
	svc.SetKindTap(func(kind string, ev TapEvent) {
		got = append(got, kind+":"+ev.UserName)
	})
	svc.BeginSession("s1")
	defer svc.BeginSession("")
	ok, _ := svc.Ingest(context.Background(), "gift", "u1", "甲", "玫瑰 x3", 3, "", false)
	if !ok {
		t.Fatal("gift ingest 应接受")
	}
	ok, _ = svc.Ingest(context.Background(), "join", "u2", "乙", "", 0, "", false)
	if !ok {
		t.Fatal("join ingest 应接受")
	}
	ok, _ = svc.Ingest(context.Background(), "chat", "u3", "丙", "你好", 0, "", false)
	if !ok {
		t.Fatal("chat ingest 应接受")
	}
	if len(got) != 2 || got[0] != "gift:甲" || got[1] != "join:乙" {
		t.Fatalf("tap=%v want [gift:甲 join:乙]", got)
	}
}
