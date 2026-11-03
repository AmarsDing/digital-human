package tts

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/media"
	"github.com/teamwork/digital-human/internal/biz/settings"
)

func newSvc(t *testing.T) (*Service, *FakeSynth) {
	t.Helper()
	lib := media.NewLibrary(t.TempDir())
	synth := NewFakeSynth()
	st := settings.NewService(settings.NewMemoryStore())
	return NewService(lib, synth, st), synth
}

func TestVoicesAndSetVoice(t *testing.T) {
	svc, _ := newSvc(t)
	ctx := context.Background()
	cur, base, list := svc.Voices(ctx)
	if cur == "" || base == "" || len(list) != 3 {
		t.Fatalf("cur=%q base=%q list=%d", cur, base, len(list))
	}
	if err := svc.SetVoice(ctx, "missing"); err == nil {
		t.Fatal("invalid voice should fail")
	}
	if err := svc.SetVoice(ctx, "dh-bright-f"); err != nil {
		t.Fatal(err)
	}
	cur, _, _ = svc.Voices(ctx)
	if cur != "dh-bright-f" {
		t.Fatalf("current=%q", cur)
	}
}

func TestPreviewWavAndQueue(t *testing.T) {
	svc, _ := newSvc(t)
	wav, err := svc.Preview(context.Background(), "   ", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(wav) < 44 || string(wav[:4]) != "RIFF" {
		t.Fatalf("not wav: %d", len(wav))
	}
	q := svc.Queue()
	if q.Depth != 0 || q.Max != 8 || q.Calls != 1 {
		t.Fatalf("queue=%+v", q)
	}
}

func TestPreviewUnknownVoiceFallsBack(t *testing.T) {
	svc, _ := newSvc(t)
	if _, err := svc.Preview(context.Background(), "hi", "nope"); err != nil {
		t.Fatal(err)
	}
}

func TestQueueFull(t *testing.T) {
	svc, synth := newSvc(t)
	synth.Delay = 50 * time.Millisecond
	ctx := context.Background()
	done := make(chan error, 10)
	for i := 0; i < 10; i++ {
		go func() {
			_, err := svc.Preview(ctx, "hello", "")
			done <- err
		}()
	}
	timeout := 0
	for i := 0; i < 10; i++ {
		err := <-done
		if err != nil {
			if !strings.Contains(err.Error(), "队列已满") {
				t.Fatalf("unexpected err: %v", err)
			}
			timeout++
		}
	}
	if timeout == 0 {
		t.Fatal("expected queue-full rejection")
	}
}
