package refs

import (
	"context"
	"errors"
	"testing"
)

func TestSwitchDedupesAndMounts(t *testing.T) {
	sig := NewFakeSignaler()
	svc := NewService(sig, nil)
	st, err := svc.Switch(context.Background(), []Image{
		{ImageID: "a", URL: "u1"},
		{ImageID: "a", URL: "u2"},
		{ImageID: "b", URL: "u3"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Images) != 2 {
		t.Fatalf("images=%+v", st.Images)
	}
	if len(sig.Calls()) != 1 || sig.Calls()[0].Op != "switch" {
		t.Fatalf("calls=%+v", sig.Calls())
	}
	if st.Ack == nil || !st.Ack.Success {
		t.Fatalf("ack=%+v", st.Ack)
	}
}

func TestTooManyImages(t *testing.T) {
	svc := NewService(NewFakeSignaler(), nil)
	_, err := svc.Switch(context.Background(), []Image{
		{ImageID: "1"}, {ImageID: "2"}, {ImageID: "3"}, {ImageID: "4"},
	})
	if !errors.Is(err, ErrTooManyImages) {
		t.Fatalf("err=%v", err)
	}
}

func TestFailureEchoesType14Once(t *testing.T) {
	sig := NewFakeSignaler()
	sig.SetFail(errors.New("prompt rejected"))
	svc := NewService(sig, nil)
	st, err := svc.Switch(context.Background(), []Image{{ImageID: "a"}})
	if err == nil {
		t.Fatal("expected failure")
	}
	// 蓝本 type14 失败只回显，不自动重发：信令调用次数必须仍为 1。
	if len(sig.Calls()) != 1 {
		t.Fatalf("retried: %+v", sig.Calls())
	}
	if st.Ack == nil || st.Ack.Success || st.Ack.Error != "prompt rejected" {
		t.Fatalf("ack=%+v", st.Ack)
	}
	if len(st.Images) != 0 {
		t.Fatalf("images should not mount: %+v", st.Images)
	}
}

func TestNoteType14(t *testing.T) {
	svc := NewService(NewFakeSignaler(), nil)
	svc.NoteType14(context.Background(), "async reject")
	st := svc.State()
	if st.Ack == nil || st.Ack.Success || st.Ack.Error != "async reject" {
		t.Fatalf("ack=%+v", st.Ack)
	}
}

func TestRemove(t *testing.T) {
	sig := NewFakeSignaler()
	svc := NewService(sig, nil)
	if st, err := svc.Switch(context.Background(), []Image{{ImageID: "a"}}); err != nil || len(st.Images) != 1 {
		t.Fatalf("switch: %+v %v", st, err)
	}
	st, err := svc.Remove(context.Background())
	if err != nil || len(st.Images) != 0 {
		t.Fatalf("remove: %+v %v", st, err)
	}
	if got := sig.Calls()[len(sig.Calls())-1].Op; got != "remove" {
		t.Fatalf("last op=%q", got)
	}
}
