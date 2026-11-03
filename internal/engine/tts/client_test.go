package tts

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeJob struct {
	id        string
	state     string
	cancelReq bool
	dur       time.Duration
	gate      <-chan struct{}
}

// fakeGradio 用 HTTP 实现边车契约，内部 worker=1 模拟 GPU 串行。
type fakeGradio struct {
	mu           sync.Mutex
	jobs         map[string]*fakeJob
	queue        []string
	active       int
	maxActive    int
	cap          int
	jobDur       time.Duration
	gateAll      <-chan struct{}
	healthOK     bool
	submitFail   bool
	cancelCalls  int
	submitCalls  int
	wake         chan struct{}
	wav          []byte
	previewDelay time.Duration
	token        string
}

func newFakeGradio() *fakeGradio {
	f := &fakeGradio{
		jobs:     map[string]*fakeJob{},
		cap:      MaxQueue,
		healthOK: true,
		wake:     make(chan struct{}, 1),
		wav:      []byte("RIFF....WAVEfake"),
	}
	go f.worker()
	return f
}

func (f *fakeGradio) worker() {
	for range f.wake {
		for {
			f.mu.Lock()
			if len(f.queue) == 0 {
				f.mu.Unlock()
				break
			}
			id := f.queue[0]
			f.queue = f.queue[1:]
			j := f.jobs[id]
			j.state = StateRunning
			f.active++
			if f.active > f.maxActive {
				f.maxActive = f.active
			}
			gate := f.gateAll
			dur := f.jobDur
			if j.dur > 0 {
				dur = j.dur
			}
			f.mu.Unlock()

			if gate != nil {
				<-gate
			}
			if dur > 0 {
				time.Sleep(dur)
			}

			f.mu.Lock()
			if j.cancelReq {
				j.state = StateCancelled
			} else {
				j.state = StateDone
			}
			f.active--
			f.mu.Unlock()
		}
	}
}

func (f *fakeGradio) handler() http.Handler {
	mux := http.NewServeMux()
	wrap := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if f.token != "" && r.Header.Get("X-Sidecar-Token") != f.token {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = w.Write([]byte(`{"error_code":"sidecar_auth_failed"}`))
				return
			}
			h(w, r)
		}
	}
	mux.HandleFunc(PathHealth, wrap(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		ok, q := f.healthOK, len(f.queue)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": ok, "model": "omnivoice", "device": "cuda", "queue": q})
	}))
	mux.HandleFunc(PathJobs, wrap(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusMethodNotAllowed)
			return
		}
		f.mu.Lock()
		f.submitCalls++
		if f.submitFail {
			f.mu.Unlock()
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"error_code":"synth_failed"}`))
			return
		}
		var req JobRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Text == "" || len([]rune(req.Text)) > 2000 {
			f.mu.Unlock()
			w.WriteHeader(http.StatusUnprocessableEntity)
			_, _ = w.Write([]byte(`{"error_code":"text_too_long"}`))
			return
		}
		if len(f.queue)+f.active >= f.cap {
			f.mu.Unlock()
			w.Header().Set("Retry-After", "2")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"error_code":"queue_full"}`))
			return
		}
		id := fmt.Sprintf("job-%d", len(f.jobs)+1)
		f.jobs[id] = &fakeJob{id: id, state: StateQueued}
		f.queue = append(f.queue, id)
		pos := len(f.queue)
		f.mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(JobRef{JobID: id, QueuePos: pos})
		select {
		case f.wake <- struct{}{}:
		default:
		}
	}))
	mux.HandleFunc(PathJobs+"/", wrap(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, PathJobs+"/")
		switch {
		case strings.HasSuffix(path, ":cancel"):
			id := strings.TrimSuffix(path, ":cancel")
			f.mu.Lock()
			f.cancelCalls++
			j, ok := f.jobs[id]
			if !ok {
				f.mu.Unlock()
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write([]byte(`{"cancelled":false}`))
				return
			}
			switch j.state {
			case StateQueued:
				for i, q := range f.queue {
					if q == id {
						f.queue = append(f.queue[:i], f.queue[i+1:]...)
						break
					}
				}
				j.state = StateCancelled
			case StateRunning:
				j.cancelReq = true
			}
			cancelled := j.state == StateCancelled || j.cancelReq
			f.mu.Unlock()
			w.WriteHeader(http.StatusOK)
			_ = json.NewEncoder(w).Encode(map[string]any{"cancelled": cancelled})
		case strings.HasSuffix(path, "/audio"):
			id := strings.TrimSuffix(path, "/audio")
			f.mu.Lock()
			j, ok := f.jobs[id]
			f.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusGone)
				return
			}
			f.mu.Lock()
			state := j.state
			f.mu.Unlock()
			if state == StateDone {
				w.Header().Set("Content-Type", "audio/wav")
				_, _ = w.Write(f.wav)
				return
			}
			if state == StateFailed || state == StateCancelled {
				w.WriteHeader(http.StatusGone)
				return
			}
			w.WriteHeader(http.StatusConflict)
		default:
			f.mu.Lock()
			j, ok := f.jobs[path]
			var st JobStatus
			if ok {
				st = JobStatus{State: j.state}
			}
			f.mu.Unlock()
			if !ok {
				w.WriteHeader(http.StatusNotFound)
				return
			}
			_ = json.NewEncoder(w).Encode(st)
		}
	}))
	mux.HandleFunc(PathPreview, wrap(func(w http.ResponseWriter, r *http.Request) {
		if f.previewDelay > 0 {
			time.Sleep(f.previewDelay)
		}
		_, _ = w.Write(f.wav)
	}))
	return mux
}

func startFake(t *testing.T, f *fakeGradio) (*Client, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(f.handler())
	t.Cleanup(srv.Close)
	return NewClient(Options{BaseURL: srv.URL, Token: f.token, PollInterval: 10 * time.Millisecond}), srv
}

func TestJobLifecycle(t *testing.T) {
	f := newFakeGradio()
	c, _ := startFake(t, f)
	c.opt.PollInterval = 10 * time.Millisecond

	ref, err := c.Submit(context.Background(), JobRequest{Text: "你好", VoiceID: "v1"})
	if err != nil {
		t.Fatal(err)
	}
	st, err := c.WaitJob(context.Background(), ref.JobID)
	if err != nil || st.State != StateDone {
		t.Fatalf("state=%+v err=%v", st, err)
	}
	wav, err := c.Audio(context.Background(), ref.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if string(wav) != string(f.wav) {
		t.Fatalf("wav=%q", wav)
	}
}

func TestSidecarWorkerIsSerial(t *testing.T) {
	f := newFakeGradio()
	f.mu.Lock()
	f.cap = 8
	f.jobDur = 10 * time.Millisecond
	f.mu.Unlock()
	c, _ := startFake(t, f)
	c.opt.PollInterval = 5 * time.Millisecond

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := c.Synthesize(context.Background(), JobRequest{Text: "句子", VoiceID: "v"}); err != nil {
				t.Errorf("synth: %v", err)
			}
		}()
	}
	wg.Wait()
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.maxActive != 1 {
		t.Fatalf("maxActive=%d want 1 (worker must be serial)", f.maxActive)
	}
}

func TestJobTimeoutTriggersLogicalCancel(t *testing.T) {
	f := newFakeGradio()
	f.mu.Lock()
	f.cap = 8
	f.jobDur = 400 * time.Millisecond
	f.mu.Unlock()
	c, _ := startFake(t, f)
	c.opt.PollInterval = 10 * time.Millisecond
	c.opt.JobTimeout = 120 * time.Millisecond

	ref, err := c.Submit(context.Background(), JobRequest{Text: "长句", VoiceID: "v"})
	if err != nil {
		t.Fatal(err)
	}

	_, err = c.WaitJob(context.Background(), ref.JobID)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeTimeout {
		t.Fatalf("err=%v want timeout", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.cancelCalls == 0 {
		t.Fatal("Go must logically cancel the sidecar job on timeout")
	}
}

func TestCancelQueuedIsImmediate(t *testing.T) {
	f := newFakeGradio()
	f.mu.Lock()
	f.cap = 8
	gate := make(chan struct{})
	f.gateAll = gate
	f.mu.Unlock()
	c, _ := startFake(t, f)

	ref1, err := c.Submit(context.Background(), JobRequest{Text: "一", VoiceID: "v"})
	if err != nil {
		t.Fatal(err)
	}

	// 等 worker 把 job1 置为 running，再让 job2 排队。
	waitState(t, f, ref1.JobID, StateRunning)
	ref2, err := c.Submit(context.Background(), JobRequest{Text: "二", VoiceID: "v"})
	if err != nil {
		close(gate)
		t.Fatal(err)
	}
	cancelled, err := c.Cancel(context.Background(), ref2.JobID)
	if err != nil {
		close(gate)
		t.Fatal(err)
	}
	if !cancelled {
		close(gate)
		t.Fatal("queued job should cancel immediately")
	}
	st, _ := c.Status(context.Background(), ref2.JobID)
	if st.State != StateCancelled {
		close(gate)
		t.Fatalf("state=%s want cancelled", st.State)
	}
	close(gate)
}

func TestCancelRunningIsLogical(t *testing.T) {
	f := newFakeGradio()
	f.mu.Lock()
	f.cap = 8
	gate := make(chan struct{})
	f.gateAll = gate
	f.mu.Unlock()
	c, _ := startFake(t, f)

	ref, err := c.Submit(context.Background(), JobRequest{Text: "句", VoiceID: "v"})
	if err != nil {
		t.Fatal(err)
	}
	waitState(t, f, ref.JobID, StateRunning)

	cancelled, err := c.Cancel(context.Background(), ref.JobID)
	if err != nil {
		t.Fatal(err)
	}
	if !cancelled {
		close(gate)
		t.Fatal("running cancel should be accepted logically")
	}
	st, _ := c.Status(context.Background(), ref.JobID)
	if st.State != StateRunning {
		close(gate)
		t.Fatalf("state=%s want still running (no GPU preemption)", st.State)
	}
	close(gate)
}

func TestQueueFullBlocksThenFailsWithin30s(t *testing.T) {
	f := newFakeGradio()
	f.mu.Lock()
	f.cap = 1
	gate := make(chan struct{})
	f.gateAll = gate
	f.mu.Unlock()
	c, _ := startFake(t, f)
	c.opt.Retry429Max = 120 * time.Millisecond

	ref, err := c.Submit(context.Background(), JobRequest{Text: "占位", VoiceID: "v"})
	if err != nil {
		close(gate)
		t.Fatal(err)
	}
	waitState(t, f, ref.JobID, StateRunning)

	_, err = c.Submit(context.Background(), JobRequest{Text: "被挡", VoiceID: "v"})
	close(gate)
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeQueueFull {
		t.Fatalf("err=%v want queue_full", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	// 首调 + 至少 1 次重试；不能无限重试。
	if f.submitCalls < 2 {
		t.Fatalf("submitCalls=%d want >=2 (429 must be retried, not dropped)", f.submitCalls)
	}
}

func TestBreakerOpensAfterThreeFailuresThenRecovers(t *testing.T) {
	f := newFakeGradio()
	f.mu.Lock()
	f.submitFail = true
	f.mu.Unlock()
	srv := httptest.NewServer(f.handler())
	defer srv.Close()
	c := NewClient(Options{
		BaseURL: srv.URL, PollInterval: 10 * time.Millisecond,
		BreakerThreshold: 3, BreakerWindow: time.Second, BreakerCooldown: 40 * time.Millisecond,
	})

	var lastErr error
	for i := 0; i < 3; i++ {
		_, lastErr = c.Submit(context.Background(), JobRequest{Text: "x", VoiceID: "v"})
		if lastErr == nil {
			t.Fatalf("attempt %d should fail", i)
		}
	}
	f.mu.Lock()
	callsBefore := f.submitCalls
	f.mu.Unlock()

	if _, err := c.Submit(context.Background(), JobRequest{Text: "x", VoiceID: "v"}); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("err=%v want circuit open", err)
	}
	f.mu.Lock()
	if f.submitCalls != callsBefore {
		f.mu.Unlock()
		t.Fatal("open circuit must not hit HTTP")
	}
	f.submitFail = false
	f.mu.Unlock()

	time.Sleep(60 * time.Millisecond)
	if _, err := c.Submit(context.Background(), JobRequest{Text: "x", VoiceID: "v"}); err != nil {
		t.Fatalf("circuit should close after cooldown: %v", err)
	}
}

func TestWaitHealthyUntilModelLoaded(t *testing.T) {
	f := newFakeGradio()
	f.mu.Lock()
	f.healthOK = false
	f.mu.Unlock()
	c, srv := startFake(t, f)
	_ = srv
	c.opt.PollInterval = 10 * time.Millisecond

	go func() {
		time.Sleep(40 * time.Millisecond)
		f.mu.Lock()
		f.healthOK = true
		f.mu.Unlock()
	}()
	if err := c.WaitHealthy(context.Background(), 300*time.Millisecond); err != nil {
		t.Fatalf("wait healthy: %v", err)
	}
}

func TestPreviewTimeout(t *testing.T) {
	f := newFakeGradio()
	f.previewDelay = 100 * time.Millisecond
	c, _ := startFake(t, f)
	c.opt.PreviewTimeout = 30 * time.Millisecond
	_, err := c.Preview(context.Background(), "短文本", "v")
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeTimeout {
		t.Fatalf("err=%v want preview timeout", err)
	}
}

func TestSidecarAuthHeaderRejected(t *testing.T) {
	f := newFakeGradio()
	f.token = "tok-1"
	c, _ := startFake(t, f) // startFake 用 f.token
	c.opt.Token = "wrong"
	_, err := c.Health(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Code != CodeAuthFailed {
		t.Fatalf("err=%v want auth failed", err)
	}
}

func waitState(t *testing.T, f *fakeGradio, id, want string) {
	t.Helper()
	deadline := time.Now().Add(500 * time.Millisecond)
	for time.Now().Before(deadline) {
		f.mu.Lock()
		j := f.jobs[id]
		state := ""
		if j != nil {
			state = j.state
		}
		f.mu.Unlock()
		if state == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("job %s never reached %s", id, want)
}

// 边车契约的默认参数：120s 总超时、200ms 轮询、worker=1 / 队列 8、429 最多阻塞 30s、熔断 3 次/60s/60s。
func TestClientDefaultsMatchContract(t *testing.T) {
	c := NewClient(Options{})
	if c.opt.JobTimeout != 120*time.Second {
		t.Fatalf("job timeout=%v", c.opt.JobTimeout)
	}
	if c.opt.PollInterval != 200*time.Millisecond {
		t.Fatalf("poll=%v", c.opt.PollInterval)
	}
	if c.opt.PreviewTimeout != 10*time.Second || c.opt.HealthTimeout != 3*time.Second {
		t.Fatalf("preview/health=%v/%v", c.opt.PreviewTimeout, c.opt.HealthTimeout)
	}
	if c.opt.ModelWaitMax != 60*time.Second || c.opt.Retry429Max != 30*time.Second || c.opt.RetryInterval != 2*time.Second {
		t.Fatalf("wait/retry=%v/%v/%v", c.opt.ModelWaitMax, c.opt.Retry429Max, c.opt.RetryInterval)
	}
	if c.opt.BreakerThreshold != 3 || c.opt.BreakerWindow != 60*time.Second || c.opt.BreakerCooldown != 60*time.Second {
		t.Fatalf("breaker=%d/%v/%v", c.opt.BreakerThreshold, c.opt.BreakerWindow, c.opt.BreakerCooldown)
	}
	if MaxQueue != 8 {
		t.Fatalf("max queue=%d", MaxQueue)
	}
}
