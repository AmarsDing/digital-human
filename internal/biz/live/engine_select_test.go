package live

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestConfiguredControllerFailClosedByDefault(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name string
		opts EngineOptions
		want bool // 是否允许启动
	}{
		{"默认 real 且无凭据", EngineOptions{}, false},
		{"real 缺 OmniVoice", EngineOptions{Mode: "real", ViduKey: "vk"}, false},
		{"real 缺 Vidu", EngineOptions{Mode: "real", OmniVoiceAddr: "http://omni"}, false},
		{"real 凭据齐全但适配器未装配", EngineOptions{Mode: "real", ViduKey: "vk", OmniVoiceAddr: "http://omni"}, false},
		{"错误 mode", EngineOptions{Mode: "fake", ViduKey: "vk", OmniVoiceAddr: "http://omni"}, false},
		{"显式 dev", EngineOptions{Mode: "dev"}, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctrl := NewConfiguredController(ctx, tc.opts, slog.Default())
			_, err := ctrl.Start(ctx, "s-1")
			if tc.want {
				if err != nil {
					t.Fatalf("dev 模式应可启动: %v", err)
				}
				return
			}
			if !errors.Is(err, ErrEngineUnavailable) {
				t.Fatalf("必须 fail-closed 返回 ErrEngineUnavailable，got %v", err)
			}
			if strings.Contains(err.Error(), "fake-live-") {
				t.Fatalf("不得生成 fake-live 会话: %v", err)
			}
		})
	}
}

type stubController struct{}

func (s *stubController) Start(context.Context, string) (string, error) { return "real-live-x", nil }
func (s *stubController) Stop(context.Context, string) error            { return nil }
func (s *stubController) Say(context.Context, string, string) (string, error) {
	return "u", nil
}

func TestConfiguredControllerUsesRealFactoryWhenWired(t *testing.T) {
	ctrl := NewConfiguredController(context.Background(), EngineOptions{
		Mode: "real", ViduKey: "vk", OmniVoiceAddr: "http://omni",
		RealFactory: func(context.Context) (Controller, error) {
			return &stubController{}, nil
		},
	}, slog.Default())
	if _, ok := ctrl.(*UnavailableController); ok {
		t.Fatal("配置齐全且注入 RealFactory 时不应落到 unavailable")
	}
	if _, err := ctrl.Start(context.Background(), "s-1"); err != nil {
		t.Fatalf("真实工厂注入后应能启动: %v", err)
	}
}

// 工厂失败也必须 fail-closed，不能回退到 fake。
func TestConfiguredControllerFactoryErrorFailsClosed(t *testing.T) {
	ctrl := NewConfiguredController(context.Background(), EngineOptions{
		Mode: "real", ViduKey: "vk", OmniVoiceAddr: "http://omni",
		RealFactory: func(context.Context) (Controller, error) {
			return nil, errors.New("dial failed")
		},
	}, slog.Default())
	if _, err := ctrl.Start(context.Background(), "s-1"); !errors.Is(err, ErrEngineUnavailable) {
		t.Fatalf("工厂失败必须返回 ErrEngineUnavailable，got %v", err)
	}
}

// 只有 dev 模式才允许出现 fake-live-*；默认/real 模式任何路径都不得生成。
func TestFakeLiveIDOnlyInDevMode(t *testing.T) {
	dev := NewConfiguredController(context.Background(), EngineOptions{Mode: "dev"}, slog.Default())
	id, err := dev.Start(context.Background(), "abcdefgh")
	if err != nil {
		t.Fatalf("dev start: %v", err)
	}
	if !strings.HasPrefix(id, "fake-live-") {
		t.Fatalf("dev 模式应使用 fake 控制器，got %q", id)
	}

	for _, mode := range []string{"", "real", "prod"} {
		ctrl := NewConfiguredController(context.Background(), EngineOptions{Mode: mode}, slog.Default())
		if _, err := ctrl.Start(context.Background(), "abcdefgh"); !errors.Is(err, ErrEngineUnavailable) {
			t.Fatalf("mode=%q 必须 fail-closed，got %v", mode, err)
		}
	}
}
