package live

import (
	"context"
	"log/slog"

	"github.com/google/uuid"
)

// FakeController 只用于 M0/M1 的无外部依赖自测；真实引擎在 M1 接入。
// 启动时写一条显著日志，避免把 fake 行为误当真实能力。
type FakeController struct {
	logger *slog.Logger
}

func NewFakeController(logger *slog.Logger) *FakeController {
	logger.Warn("using fake live controller: Vidu/OmniVoice 未接入，所有开播均为本地模拟")
	return &FakeController{logger: logger}
}

func (f *FakeController) Start(_ context.Context, sessionID string) (string, error) {
	f.logger.Info("fake start", "session_id", sessionID)
	// 短 ID 保护：空 sessionID 与超长 ID 都安全截断，不产生 "fake-live-" 空尾或超长结果。
	short := sessionID
	if short == "" {
		short = "anon"
	}
	if len(short) > 8 {
		short = short[:8]
	}
	return "fake-live-" + short, nil
}

func (f *FakeController) Stop(_ context.Context, reason string) error {
	f.logger.Info("fake stop", "reason", reason)
	return nil
}

func (f *FakeController) Say(_ context.Context, text, mode string) (string, error) {
	return uuid.NewString(), nil
}
