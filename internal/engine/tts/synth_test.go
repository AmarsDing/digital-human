package tts

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teamwork/digital-human/pkg/audio"
)

// jobSidecar 模拟 OmniVoice 边车 job 协议：submit → poll → audio。
type jobSidecar struct {
	srv *httptest.Server

	wav     []byte
	running bool // true=状态永远 running（用于取消传播测试）
	cancels atomic.Int32
	submits atomic.Int32
	statusQ atomic.Int32
}

func newJobSidecar(t *testing.T, wav []byte) *jobSidecar {
	t.Helper()
	j := &jobSidecar{wav: wav}
	j.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == PathJobs:
			j.submits.Add(1)
			_, _ = w.Write([]byte(`{"job_id":"job-1","queue_pos":0}`))
		case r.Method == http.MethodGet && r.URL.Path == PathJobs+"/job-1":
			j.statusQ.Add(1)
			if j.running {
				_, _ = w.Write([]byte(`{"state":"running"}`))
				return
			}
			_, _ = w.Write([]byte(`{"state":"done","duration_ms":120}`))
		case r.Method == http.MethodGet && r.URL.Path == PathJobs+"/job-1/audio":
			w.Header().Set("Content-Type", "audio/wav")
			_, _ = w.Write(j.wav)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, ":cancel"):
			j.cancels.Add(1)
			_, _ = w.Write([]byte(`{"cancelled":true}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(j.srv.Close)
	return j
}

// 契约：SynthAdapter 把边车 wav 解成 24k/s16le/mono PCM。
func TestSynthAdapterWavToPCM(t *testing.T) {
	pcm := audio.TonePCM(120)
	sidecar := newJobSidecar(t, audio.WavBytes(pcm))

	c := NewClient(Options{BaseURL: sidecar.srv.URL, PollInterval: time.Millisecond})
	a := NewSynthAdapter(c, "v1")
	out, err := a.Synth(context.Background(), "你好", "")
	if err != nil {
		t.Fatalf("synth: %v", err)
	}
	if len(out) != len(pcm) {
		t.Fatalf("pcm len=%d want %d", len(out), len(pcm))
	}
	if a.Name() != "omnivoice" {
		t.Fatalf("name=%s", a.Name())
	}
}

// 契约：边车返回损坏 wav 时 fail-closed 报错，不静默重采样。
func TestSynthAdapterRejectsBadWav(t *testing.T) {
	sidecar := newJobSidecar(t, []byte("RIFF\x00\x00\x00\x00WAVEfmt "))
	c := NewClient(Options{BaseURL: sidecar.srv.URL, PollInterval: time.Millisecond})
	a := NewSynthAdapter(c, "")
	if _, err := a.Synth(context.Background(), "hi", ""); err == nil {
		t.Fatal("bad wav must fail")
	}
}

// 契约补全：取消传播——ctx 取消后 Synthesize 立刻返回，并向边车发逻辑取消。
func TestContextCancelPropagatesToSidecar(t *testing.T) {
	sidecar := newJobSidecar(t, nil)
	sidecar.running = true

	c := NewClient(Options{BaseURL: sidecar.srv.URL, PollInterval: 5 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.Synthesize(ctx, JobRequest{Text: "hi"})
		done <- err
	}()
	// 等客户端进入 WaitJob 轮询（statusQ>0）再取消，而不是只等服务端收到 submit：
	// submits 在服务端 handler 入口即计数，若此时客户端尚未读完 Submit 响应，
	// 取消会让 Submit 直接失败，Synthesize 从未进入 WaitJob，逻辑取消不会发出
	// （全量并行 -race 下该窗口被放大，导致 cancels==0 偶发失败）。
	deadline := time.Now().Add(3 * time.Second)
	for sidecar.statusQ.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("sidecar 未收到状态轮询")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v want context.Canceled", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancel did not abort synthesize")
	}
	if sidecar.cancels.Load() == 0 {
		t.Fatal("sidecar 必须收到逻辑取消请求")
	}
}
