package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/biz/refs"
	"github.com/teamwork/digital-human/internal/conf"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// stubPromptCtrl 实现 refs.PromptOperator 的控制器（模拟 ViduController type11 出口）。
type stubPromptCtrl struct {
	live.Controller
	called bool
}

func (s *stubPromptCtrl) PromptOperation(context.Context, string, []map[string]any) error {
	s.called = true
	return nil
}

// TestProvideRefsSignalerDevKeepsFake G3-1：dev 模式保留 FakeSignaler（本地自测）。
func TestProvideRefsSignalerDevKeepsFake(t *testing.T) {
	cfg := &conf.Config{}
	cfg.Engine.Mode = "dev"
	sig := provideRefsSignaler(cfg, live.NewFakeController(quietLog()))
	if _, ok := sig.(*refs.FakeSignaler); !ok {
		t.Fatalf("dev 必须装配 FakeSignaler: %T", sig)
	}
}

// TestProvideRefsSignalerRealBridgesType11 G3-1：real 模式桥到控制器 type11 出口。
func TestProvideRefsSignalerRealBridgesType11(t *testing.T) {
	cfg := &conf.Config{}
	cfg.Engine.Mode = "real"
	ctrl := &stubPromptCtrl{}
	sig := provideRefsSignaler(cfg, ctrl)
	vs, ok := sig.(*refs.ViduSignaler)
	if !ok {
		t.Fatalf("real 必须装配 ViduSignaler: %T", sig)
	}
	if err := vs.SetPrompt(context.Background(), "switch", []refs.Image{{URL: "vidu://r1"}}); err != nil {
		t.Fatal(err)
	}
	if !ctrl.called {
		t.Fatal("SetPrompt 必须桥到控制器 PromptOperation")
	}
}

// TestProvideRefsSignalerRealUnavailableFailsClosed G3-1：real 但控制器无 type11
// 出口（UnavailableController，凭据缺失）→ SetPrompt 如实 ErrNoSignal，绝不静默成功。
func TestProvideRefsSignalerRealUnavailableFailsClosed(t *testing.T) {
	cfg := &conf.Config{}
	cfg.Engine.Mode = "real"
	sig := provideRefsSignaler(cfg, live.NewUnavailableController("missing_credentials"))
	if _, ok := sig.(*refs.ViduSignaler); !ok {
		t.Fatalf("real 无出口也必须装配 ViduSignaler: %T", sig)
	}
	err := sig.SetPrompt(context.Background(), "switch", []refs.Image{{URL: "vidu://r1"}})
	if !errors.Is(err, refs.ErrNoSignal) {
		t.Fatalf("err=%v want ErrNoSignal（fail-closed）", err)
	}
}
