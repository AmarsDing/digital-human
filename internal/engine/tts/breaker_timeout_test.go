package tts

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// 故障注入：边车挂起（受理请求但永不响应）→ Submit 必须按超时失败而非悬挂；
// 连续失败触发熔断开路（不再打到边车）；边车恢复且冷却期过后熔断半开回正。
func TestSidecarHangTimeoutTripsBreaker(t *testing.T) {
	var hang atomic.Bool
	var hits atomic.Int32
	hang.Store(true)
	// release 兜底释放挂起的 handler：客户端超时后服务端是否及时察觉连接断开
	// 属于传输层细节，不作为被测语义；保证 httptest.Close 不因此卡住。
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == PathJobs {
			hits.Add(1)
			if hang.Load() {
				select {
				case <-r.Context().Done():
				case <-release:
				}
				return
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"job_id":"j-ok","queue_pos":0}`))
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	// LIFO：srv.Close 先注册后执行；close(release) 先执行，保证挂起 handler 退出后 Close 才不卡。
	t.Cleanup(srv.Close)
	t.Cleanup(func() { close(release) })

	c := NewClient(Options{
		BaseURL: srv.URL, PollInterval: 5 * time.Millisecond,
		BreakerThreshold: 3, BreakerWindow: 5 * time.Second, BreakerCooldown: 50 * time.Millisecond,
	})

	// 连续 3 次：每次都按 ctx 超时失败（含超时语义），且迅速返回不悬挂。
	for i := 0; i < 3; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
		start := time.Now()
		_, err := c.Submit(ctx, JobRequest{Text: "x", VoiceID: "v"})
		cancel()
		var apiErr *APIError
		if !errors.As(err, &apiErr) || apiErr.Code != CodeTimeout {
			t.Fatalf("attempt %d err=%v want timeout", i, err)
		}
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("attempt %d err=%v want deadline exceeded", i, err)
		}
		if d := time.Since(start); d > 2*time.Second {
			t.Fatalf("attempt %d 悬挂 %v，未按超时失败", i, d)
		}
	}

	// 熔断开路：第 4 次直接 ErrCircuitOpen，且不再打到边车。
	if _, err := c.Submit(context.Background(), JobRequest{Text: "x", VoiceID: "v"}); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("err=%v want circuit open", err)
	}
	if got := hits.Load(); got != 3 {
		t.Fatalf("开路期间打到边车 %d 次, want 3", got)
	}

	// 边车恢复 + 冷却期过后：熔断半开，请求成功并回正。
	hang.Store(false)
	time.Sleep(80 * time.Millisecond)
	ref, err := c.Submit(context.Background(), JobRequest{Text: "x", VoiceID: "v"})
	if err != nil || ref.JobID != "j-ok" {
		t.Fatalf("恢复后 submit: ref=%+v err=%v", ref, err)
	}
	// 回正后持续可用（失败计数已被 success 清零）。
	if _, err := c.Submit(context.Background(), JobRequest{Text: "x", VoiceID: "v"}); err != nil {
		t.Fatalf("熔断回正后再次 submit: %v", err)
	}
}
