package interaction

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// EffectTypeGiftThanks 礼物答谢副作用（§5.4；本期唯一打通链路）。
const EffectTypeGiftThanks = "gift_thanks"

// Sayer 与 gift.Speaker 同构（Say 入 TTS）；避免反向依赖 gift 包。
type Sayer interface {
	Say(ctx context.Context, text string, mode string) error
}

// GiftSpeaker 礼物答谢链路：gift.Merger 合并触发 → Decision+EffectOutbox 同事务落库
// → dispatcher 调内层 Say → 回执写状态。其他 mode（如 patron 欢迎）本期透传内层，
// 仅留骨架（后续切片同样改走 outbox）。
type GiftSpeaker struct {
	svc     *Service
	disp    *Dispatcher
	inner   Sayer
	session func() (sessionID string, ok bool) // 当前场次；无场次时拒绝落库（fail-closed）
}

// NewGiftSpeaker svc/disp/inner 任一为空时 Say(gift) 显式报错（不静默丢失答谢）。
func NewGiftSpeaker(svc *Service, disp *Dispatcher, inner Sayer, sessionFn func() (string, bool)) *GiftSpeaker {
	return &GiftSpeaker{svc: svc, disp: disp, inner: inner, session: sessionFn}
}

// Say 实现 gift.Speaker。
func (g *GiftSpeaker) Say(ctx context.Context, text string, mode string) error {
	if mode != "gift" {
		// 骨架：非礼物链路（patron 欢迎等）暂透传，接入点保留。
		return g.inner.Say(ctx, text, mode)
	}
	if g.svc == nil || g.disp == nil || g.inner == nil || g.session == nil {
		return errors.New("interaction: gift speaker not fully wired")
	}
	sessionID, ok := g.session()
	if !ok || sessionID == "" {
		return errors.New("interaction: gift thanks without live session (refused, fail-closed)")
	}
	// 无外部源事件行时的确定性兜底键：同场次同内容不重复建 Decision（§5.2 唯一性）。
	source := SourceKey("gift_thanks", sessionID, text)
	_, effects, err := g.svc.Decide(ctx, DecisionInput{
		SessionID:        sessionID,
		SourceEventRowID: source,
		Intent:           EffectTypeGiftThanks,
		Outcome:          DecideExecute,
		ReasonCode:       "gift_merged",
		PayloadSafe:      map[string]any{"text": text, "mode": mode},
		Effects: []EffectInput{{
			EffectType: EffectTypeGiftThanks, TargetID: "tts", Seq: 1,
			PayloadSafe: map[string]any{"text": text},
		}},
	})
	if err != nil {
		return fmt.Errorf("interaction: decide gift_thanks: %w", err)
	}
	if len(effects) == 0 {
		return errors.New("interaction: decide gift_thanks: no effect persisted")
	}
	// 同步派发一次（skeleton：先打通闭环；异步批量派发由 Run 周期/后续切片接管）。
	if _, err := g.disp.DispatchOnce(ctx); err != nil {
		return fmt.Errorf("interaction: dispatch gift_thanks: %w", err)
	}
	// 回执已写（dispatcher 内 MarkSucceeded/MarkFailed*）；handler 失败以 err 形式
	// 经由 store 状态呈现，这里按状态决定是否向上报错。
	eff, ok := g.effectOf(effects[0].ID)
	if !ok {
		return errors.New("interaction: effect vanished after dispatch")
	}
	switch eff.Status {
	case EffectSucceeded:
		return nil
	case EffectPending: // 失败退避中
		return fmt.Errorf("interaction: gift_thanks retryable: %s", strings.TrimSpace(eff.LastError))
	default:
		return fmt.Errorf("interaction: gift_thanks %s: %s", eff.Status, strings.TrimSpace(eff.LastError))
	}
}

// effectOf 从 dispatcher store 读回执后状态（MemoryStore 提供 GetEffect；
// Ent 实现提供同等读口——见 repo.InteractionStore.GetEffect）。
func (g *GiftSpeaker) effectOf(id string) (Effect, bool) {
	type getter interface {
		GetEffect(id string) (Effect, bool)
	}
	if g.svc == nil {
		return Effect{}, false
	}
	if st, ok := g.svc.Store().(getter); ok {
		return st.GetEffect(id)
	}
	return Effect{}, false
}

// RegisterGiftHandler 把「执行器=内层 Say」注册进 dispatcher（wire 装配点）。
func (g *GiftSpeaker) RegisterGiftHandler() {
	g.disp.RegisterHandler(EffectTypeGiftThanks, func(ctx context.Context, eff Effect) (string, error) {
		text, _ := eff.PayloadSafe["text"].(string)
		if text == "" {
			return "", errors.New("interaction: gift_thanks payload missing text")
		}
		if err := g.inner.Say(ctx, text, "gift"); err != nil {
			return "", err
		}
		// 回执 id：dispatch 维度可追踪（无外部回执时用 dispatch_id 充数并显式标注）。
		return "say:" + eff.DispatchID, nil
	})
}
