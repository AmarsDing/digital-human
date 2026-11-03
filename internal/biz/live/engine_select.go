package live

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
)

// EngineOptions 决定装配真实引擎还是显式 dev fake。
// Mode 默认 real；只有 Mode=dev 才会启用 FakeController。
type EngineOptions struct {
	Mode          string
	ViduKey       string
	OmniVoiceAddr string
	RealFactory   RealFactory
}

// RealFactory 由后续真实 Vidu/OmniVoice 适配器注入；当前未装配时返回 nil。
type RealFactory func(ctx context.Context) (Controller, error)

// UnavailableController 是 fail-closed 实现：所有启动都返回 ErrEngineUnavailable，
// 绝不生成 fake-live-* 会话。
type UnavailableController struct {
	reason string
}

func NewUnavailableController(reason string) *UnavailableController {
	return &UnavailableController{reason: reason}
}

func (u *UnavailableController) Start(context.Context, string) (string, error) {
	return "", fmt.Errorf("%w: %s", ErrEngineUnavailable, u.reason)
}

func (u *UnavailableController) Stop(context.Context, string) error { return nil }

func (u *UnavailableController) Say(context.Context, string, string) (string, error) {
	return "", fmt.Errorf("%w: %s", ErrEngineUnavailable, u.reason)
}

// NewConfiguredController 按配置选择引擎，默认 fail-closed：
//   - mode=dev：显式开启 fake（仅本地自测）；
//   - mode=real（默认）：要求 ViduKey + OmniVoiceAddr；缺失或适配器未装配都返回 unavailable。
func NewConfiguredController(ctx context.Context, opts EngineOptions, logger *slog.Logger) Controller {
	mode := strings.ToLower(strings.TrimSpace(opts.Mode))
	if mode == "dev" {
		if logger != nil {
			logger.Warn("engine mode=dev：使用 fake live controller，仅限本地自测")
		}
		return NewFakeController(logger)
	}
	var missing []string
	if strings.TrimSpace(opts.ViduKey) == "" {
		missing = append(missing, "DH_VIDU_KEY")
	}
	if strings.TrimSpace(opts.OmniVoiceAddr) == "" {
		missing = append(missing, "DH_OMNIVOICE_ADDR")
	}
	if len(missing) > 0 {
		return NewUnavailableController("missing_credentials: " + strings.Join(missing, ","))
	}
	if opts.RealFactory == nil {
		return NewUnavailableController("real_engine_adapter_not_wired")
	}
	ctrl, err := opts.RealFactory(ctx)
	if err != nil {
		return NewUnavailableController("real_engine_factory_failed: " + err.Error())
	}
	return ctrl
}
