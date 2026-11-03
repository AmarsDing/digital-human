package action

import (
	"context"
	"testing"
	"time"
)

func newSched() (*Scheduler, *FakeSender) {
	snd := NewFakeSender()
	return NewScheduler(snd, nil, DefaultConfig()), snd
}

func TestLibrariesCRUD(t *testing.T) {
	r := NewMemoryRemote()
	lib, err := r.CreateLibrary(context.Background(), "打招呼", "开场动作")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.CreateLibrary(context.Background(), "打招呼", ""); err == nil {
		t.Fatal("duplicate library should fail")
	}
	act, err := r.CreateAction(context.Background(), lib.ID, "挥手", "右手挥动")
	if err != nil {
		t.Fatal(err)
	}
	list, err := r.Actions(context.Background(), lib.ID)
	if err != nil || len(list) != 1 || list[0].Instruction != "右手挥动" {
		t.Fatalf("actions=%+v err=%v", list, err)
	}
	if err := r.DeleteAction(context.Background(), act.ID); err != nil {
		t.Fatal(err)
	}
	if err := r.DeleteLibrary(context.Background(), lib.ID); err != nil {
		t.Fatal(err)
	}
}

func TestManualSameCommandIsDeferred(t *testing.T) {
	s, snd := newSched()
	defer s.Close()
	ctx := context.Background()
	if _, err := s.Command(ctx, "挥手", "manual", ""); err != nil {
		t.Fatal(err)
	}
	res, err := s.Command(ctx, "挥手", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	if !res.Deferred || res.GapS != 2 || res.Queued != 1 {
		t.Fatalf("res=%+v", res)
	}
	if got := len(snd.Sent()); got != 1 {
		t.Fatalf("sent=%d", got)
	}
	if q := s.Status().Queue; len(q) != 1 || q[0] != "挥手" {
		t.Fatalf("queue=%v", q)
	}
}

func TestQueueDropsLowestAndManualJumpsPriority(t *testing.T) {
	s, _ := newSched()
	defer s.Close()
	ctx := context.Background()
	if err := s.SendDirect(ctx, "a", "auto"); err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"b", "c", "d"} {
		if _, err := s.Command(ctx, text, "auto", ""); err != nil {
			t.Fatal(err)
		}
	}
	res, err := s.Command(ctx, "e", "auto", "")
	if err != nil {
		t.Fatal(err)
	}
	if res.Dropped != "d" || res.Queued != 3 {
		t.Fatalf("res=%+v", res)
	}
	res, err = s.Command(ctx, "m", "manual", "")
	if err != nil {
		t.Fatal(err)
	}
	// 手动不同文本 gap=0，直接发送（蓝本 manual 非同动作不排队）。
	if res.Deferred {
		t.Fatalf("manual policy should not defer: %+v", res)
	}
	if q := s.Status().Queue; len(q) != 3 || q[0] != "b" {
		t.Fatalf("queue=%v", q)
	}
}

func TestVIPBypassesAndPausesChoreo(t *testing.T) {
	s, snd := newSched()
	defer s.Close()
	var paused time.Duration
	s.SetVIP(func(u string) bool { return u == "vip1" })
	s.SetPauseChoreo(func(d time.Duration) { paused = d })
	res, err := s.Command(context.Background(), "跳舞", "auto", "vip1")
	if err != nil {
		t.Fatal(err)
	}
	if !res.VIP || res.Deferred || res.Priority != 3 {
		t.Fatalf("res=%+v", res)
	}
	if paused != 4500*time.Millisecond {
		t.Fatalf("paused=%v", paused)
	}
	if len(snd.Sent()) != 1 {
		t.Fatal("vip should send immediately")
	}
}

func TestInterruptClearsQueue(t *testing.T) {
	s, _ := newSched()
	defer s.Close()
	ctx := context.Background()
	_ = s.SendDirect(ctx, "a", "auto")
	if _, err := s.Command(ctx, "b", "auto", ""); err != nil {
		t.Fatal(err)
	}
	if n := s.Interrupt(ctx); n != 1 {
		t.Fatalf("cleared=%d", n)
	}
	if q := s.Status().Queue; len(q) != 0 {
		t.Fatalf("queue=%v", q)
	}
}

func TestPriorityOrder(t *testing.T) {
	cases := map[string]int{"vip": 3, "manual": 2, "choreo": 1, "auto": 0, "": 0}
	for src, want := range cases {
		if got := priorityOf(src); got != want {
			t.Fatalf("%q got %d want %d", src, got, want)
		}
	}
}
