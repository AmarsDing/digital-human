package publish

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func stageTestPublisher(t *testing.T, srv *fakeRTMPServer, dump string) *Publisher {
	t.Helper()
	t.Setenv("GO_WANT_HELPER", "1")
	t.Setenv("HELPER_ARGVDUMP", dump)
	t.Setenv("HELPER_HOLD", "5s")
	p := NewPublisher(Config{
		Source:         SourceStage,
		FFmpegBin:      os.Args[0],
		FFmpegPrepend:  []string{"fake-ffmpeg"},
		ConnectTimeout: 2 * time.Second,
		BackoffInit:    5 * time.Second,
		BackoffMax:     5 * time.Second,
		NoFrameTimeout: 5 * time.Second,
		AudioTimeout:   50 * time.Millisecond,
	}, &MemTargetStore{})
	p.startStageCapture = func(context.Context, CaptureConfig) (*StageCapture, error) {
		frames := make(chan []byte, 1)
		frames <- []byte("jpeg")
		return &StageCapture{frames: frames, cancel: func() {}}, nil
	}
	return p
}

func TestPublisherStageUsesPCMBridgeAsSecondInput(t *testing.T) {
	srv := newFakeRTMPServer(t)
	defer srv.close()
	dump := filepath.Join(t.TempDir(), "argv.txt")
	p := stageTestPublisher(t, srv, dump)
	if _, err := p.SetTarget(Target{RTMPURL: srv.url(), Key: testStreamKey}); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitState(t, p, StatePushing, 3*time.Second)
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	p.Wait()
	argv, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(strings.Fields(string(argv)), " ")
	for _, want := range []string{"-map 0:v -map 1:a", "-f s16le -ar 48000 -ac 1 -i tcp://127.0.0.1:"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("Publisher 未接 PCM bridge，缺少 %q:\n%s", want, joined)
		}
	}
}

func TestPublisherStagePCMTimeoutEntersDegraded(t *testing.T) {
	srv := newFakeRTMPServer(t)
	defer srv.close()
	p := stageTestPublisher(t, srv, filepath.Join(t.TempDir(), "argv.txt"))
	if _, err := p.SetTarget(Target{RTMPURL: srv.url(), Key: testStreamKey}); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Stop(); p.Wait() }()
	waitState(t, p, StatePushing, 3*time.Second)
	collector, err := p.PCMBridge().Attach("s-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer p.PCMBridge().Detach(collector)
	if err := p.PCMBridge().Write(collector, 1, make([]byte, PCMFrameBytes)); err != nil {
		t.Fatal(err)
	}
	st := waitState(t, p, StateDegraded, 3*time.Second)
	if !strings.Contains(st.LastError, "publish_audio_pcm_timeout") {
		t.Fatalf("PCM 超时必须显式降级: %+v", st)
	}
}
