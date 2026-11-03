package recording

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/engine/vidu"
)

// fake Vidu server：/live/v1/lives/live-1/recording 依次返回 processing → processing → ready。
func TestSupervisorPollUntilReady(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		if n < 3 {
			_, _ = w.Write([]byte(`{"state":"processing"}`))
			return
		}
		_, _ = w.Write([]byte(`{"state":"ready","url":"https://cdn.example.com/rec/live-1.mp4"}`))
	}))
	t.Cleanup(srv.Close)

	vc := vidu.NewClient(vidu.Options{APIKey: "k", BaseURL: srv.URL})
	sup := New(true, ViduClient{C: vc}, nil)
	sup.PollSteps = []time.Duration{time.Millisecond, time.Millisecond}
	sup.Steady = time.Millisecond
	sup.MaxWait = 5 * time.Second

	if err := sup.OnSessionStart(context.Background(), "live-1"); err != nil {
		t.Fatalf("on start: %v", err)
	}
	if got := sup.State(); got != StateRecording {
		t.Fatalf("state=%s want recording", got)
	}
	state, err := sup.OnSessionStop(context.Background(), "live-1")
	if err != nil {
		t.Fatalf("on stop: %v", err)
	}
	if state != StateReady {
		t.Fatalf("state=%s want ready", state)
	}
	if sup.URL() == "" {
		t.Fatal("ready 后必须有出片 URL")
	}
	if calls.Load() < 3 {
		t.Fatalf("poll calls=%d want >=3（轮询递增节奏）", calls.Load())
	}
}

// Vidu 返回 404（不支持录制）→ Unsupported → recording_not_enabled。
func TestSupervisorUnsupportedMapsNotEnabled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		_, _ = w.Write([]byte(`{"code":"not_found","message":"recording not supported"}`))
	}))
	t.Cleanup(srv.Close)

	vc := vidu.NewClient(vidu.Options{APIKey: "k", BaseURL: srv.URL})
	sup := New(true, ViduClient{C: vc}, nil)
	sup.PollSteps = []time.Duration{time.Millisecond}
	sup.Steady = time.Millisecond
	_ = sup.OnSessionStart(context.Background(), "live-1")
	state, err := sup.OnSessionStop(context.Background(), "live-1")
	if !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("err=%v want ErrNotEnabled", err)
	}
	if state != StateUnavailable {
		t.Fatalf("state=%s want unavailable", state)
	}
}

// 配置未开通：一切操作明确 recording_not_enabled，绝不假装在录。
func TestSupervisorDisabledFailClosed(t *testing.T) {
	sup := New(false, nil, nil)
	if sup.Enabled() {
		t.Fatal("disabled supervisor must not report enabled")
	}
	if got := sup.State(); got != StateUnavailable {
		t.Fatalf("state=%s want unavailable", got)
	}
	if err := sup.OnSessionStart(context.Background(), "live-1"); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("err=%v want recording_not_enabled", err)
	}
	if _, err := sup.OnSessionStop(context.Background(), "live-1"); !errors.Is(err, ErrNotEnabled) {
		t.Fatalf("err=%v want recording_not_enabled", err)
	}
}

// 出片超时上限：永远不 ready → failed（上限可注入缩短）。
func TestSupervisorPollTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"state":"processing"}`))
	}))
	t.Cleanup(srv.Close)

	vc := vidu.NewClient(vidu.Options{APIKey: "k", BaseURL: srv.URL})
	sup := New(true, ViduClient{C: vc}, nil)
	sup.PollSteps = []time.Duration{time.Millisecond}
	sup.Steady = time.Millisecond
	sup.MaxWait = 3 * time.Millisecond
	state, err := sup.OnSessionStop(context.Background(), "live-1")
	if err == nil {
		t.Fatal("timeout must fail")
	}
	if state != StateFailed {
		t.Fatalf("state=%s want failed", state)
	}
}

func TestMapViduState(t *testing.T) {
	if got := MapViduState(map[string]any{"status": "ready", "video_url": "u"}); got.State != StateReady || got.URL != "u" {
		t.Fatalf("ready mapping: %+v", got)
	}
	if got := MapViduState(map[string]any{"status": "rendering"}); got.State != StateProcessing {
		t.Fatalf("processing mapping: %+v", got)
	}
	if got := MapViduState(map[string]any{"status": "error"}); got.State != StateFailed {
		t.Fatalf("failed mapping: %+v", got)
	}
	// 只有 URL 没有状态时视为 ready。
	if got := MapViduState(map[string]any{"record_url": "u2"}); got.State != StateReady {
		t.Fatalf("url-only mapping: %+v", got)
	}
}
