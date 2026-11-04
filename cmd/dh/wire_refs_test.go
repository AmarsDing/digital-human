package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/contentpkg"
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
func TestProvideContentpkgAttachesApplyRoute(t *testing.T) {
	rt := &panelRuntime{}
	svc := provideContentpkg(contentpkg.NewMemoryStore(), rt)
	if rt.deps.Contentpkg != svc {
		t.Fatal("内容包服务必须装配到 live-drafts apply 路由")
	}
}

func TestModePolicyFromConfigDefaultsToPublic(t *testing.T) {
	cfg, err := conf.Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Live.AllowLocalPreview {
		t.Fatal("live.allow_local_preview 默认必须 false")
	}
	if got := modePolicyFromConfig(cfg); got != (live.ModePolicy{EngineMode: "real"}) {
		t.Fatalf("默认策略必须 public-live: %+v", got)
	}

	cfg.Live.AllowLocalPreview = true
	if got := modePolicyFromConfig(cfg); !got.AllowLocalPreview || got.EngineMode != "real" {
		t.Fatalf("allow 不得改写 real 引擎: %+v", got)
	}
	cfg.Engine.Mode = "dev"
	if got := modePolicyFromConfig(cfg); !got.AllowLocalPreview || got.EngineMode != "dev" {
		t.Fatalf("显式 allow + dev 策略: %+v", got)
	}
}

func TestProvideRefsSignalerDevKeepsFake(t *testing.T) {
	cfg := &conf.Config{}
	cfg.Engine.Mode = "dev"
	sig := provideRefsSignaler(cfg, live.NewFakeController(quietLog()))
	if _, ok := sig.(*refs.FakeSignaler); !ok {
		t.Fatalf("dev 必须装配 FakeSignaler: %T", sig)
	}
}

func TestGreetingAudioCapabilityDoesNotFakeUnavailablePlayback(t *testing.T) {
	for _, mode := range []string{"dev", "real"} {
		t.Run(mode, func(t *testing.T) {
			cfg := &conf.Config{}
			cfg.Engine.Mode = mode
			mx := provideCapabilities(cfg, nil, nil, nil, nil)(context.Background())
			if got := mx.Get("greeting_audio_file"); got != "unconfigured" {
				t.Fatalf("无真实文件播放装配时 greeting_audio_file=%s, want unconfigured", got)
			}
		})
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
