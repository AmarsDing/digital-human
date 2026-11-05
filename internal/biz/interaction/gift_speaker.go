package interaction

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	"github.com/teamwork/digital-human/internal/biz/contentgate"
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

	mu             sync.RWMutex
	gate           *contentgate.Gate
	actionMappings []ActionMapping
	actionRevision string
}

// NewGiftSpeaker svc/disp/inner 任一为空时 Say(gift) 显式报错（不静默丢失答谢）。
func NewGiftSpeaker(svc *Service, disp *Dispatcher, inner Sayer, sessionFn func() (string, bool)) *GiftSpeaker {
	return &GiftSpeaker{svc: svc, disp: disp, inner: inner, session: sessionFn}
}

// Say 实现 gift.Speaker；旧调用保留内容幂等，新 Merger 走 SayGiftBatch。
func (g *GiftSpeaker) Say(ctx context.Context, text string, mode string) error {
	if mode != "gift" {
		return g.inner.Say(ctx, text, mode)
	}
	sessionID, ok := g.currentSession()
	if !ok {
		return errors.New("interaction: gift thanks without live session (refused, fail-closed)")
	}
	return g.sayGift(ctx, text, SourceKey("gift_thanks", sessionID, text), "")
}

// SayGiftBatch 以 Merger 批次为幂等边界，同文案不同批次不会互相去重。
func (g *GiftSpeaker) SayGiftBatch(ctx context.Context, text, batchID string) error {
	if batchID == "" {
		return errors.New("interaction: gift batch ID is required")
	}
	sessionID, ok := g.currentSession()
	if !ok {
		return errors.New("interaction: gift thanks without live session (refused, fail-closed)")
	}
	return g.sayGift(ctx, text, SourceKey("gift_thanks", sessionID, batchID), "")
}

// SayGiftReaction 在同一 Decision 下持久化文案与可选白名单动作。
func (g *GiftSpeaker) SayGiftReaction(ctx context.Context, text, batchID, actionTrigger string) error {
	if batchID == "" {
		return errors.New("interaction: gift batch ID is required")
	}
	sessionID, ok := g.currentSession()
	if !ok {
		return errors.New("interaction: gift thanks without live session (refused, fail-closed)")
	}
	return g.sayGift(ctx, text, SourceKey("gift_thanks", sessionID, batchID), actionTrigger)
}

// SetGate 配置礼物文本的统一终检。
func (g *GiftSpeaker) SetGate(gate *contentgate.Gate) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.gate = gate
}

// SetActionMappings 冻结当前选中动作库的白名单与 revision。
func (g *GiftSpeaker) SetActionMappings(mappings []ActionMapping, revision string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.actionMappings = append([]ActionMapping(nil), mappings...)
	g.actionRevision = revision
}

func (g *GiftSpeaker) currentSession() (string, bool) {
	if g.session == nil {
		return "", false
	}
	sessionID, ok := g.session()
	return sessionID, ok && sessionID != ""
}

func (g *GiftSpeaker) sayGift(ctx context.Context, text, source, actionTrigger string) error {
	if g.svc == nil || g.disp == nil || g.inner == nil {
		return errors.New("interaction: gift speaker not fully wired")
	}
	sessionID, _ := g.currentSession()
	g.mu.RLock()
	gate := g.gate
	mappings := append([]ActionMapping(nil), g.actionMappings...)
	revision := g.actionRevision
	g.mu.RUnlock()
	if gate != nil {
		decision := gate.Check(ctx, contentgate.Input{Outlet: "gift", SessionID: sessionID, Text: text})
		switch decision.Disposition {
		case contentgate.DispositionSilent:
			return errors.New("interaction: gift content suppressed by gate")
		case contentgate.DispositionSpeakTemplate:
			text = decision.Text
			actionTrigger = ""
		}
	}
	effectInputs := []EffectInput{{
		EffectType: EffectTypeGiftThanks, TargetID: "tts", Seq: 1,
		PayloadSafe: map[string]any{"text": text},
	}}
	if mapping, ok := allowedGiftAction(mappings, revision, actionTrigger); ok {
		effectInputs = append(effectInputs, EffectInput{
			EffectType: EffectTypeAction, TargetID: "action", Seq: 2,
			PayloadSafe: map[string]any{"instruction": mapping.Instruction, "user_key": "gift"},
		})
	}
	_, effects, err := g.svc.Decide(ctx, DecisionInput{
		SessionID:        sessionID,
		SourceEventRowID: source,
		Intent:           EffectTypeGiftThanks,
		Outcome:          DecideExecute,
		ReasonCode:       "gift_merged",
		PayloadSafe:      map[string]any{"text": text, "mode": "gift"},
		Effects:          effectInputs,
	})
	if err != nil {
		return fmt.Errorf("interaction: decide gift_thanks: %w", err)
	}
	if len(effects) == 0 {
		return errors.New("interaction: decide gift_thanks: no effect persisted")
	}
	// 同步派发本批次的文案与可选动作；回执仍只证明出口调用，不证明 Vidu 执行完成。
	for {
		ok, err := g.disp.DispatchOnce(ctx)
		if err != nil {
			return fmt.Errorf("interaction: dispatch gift_thanks: %w", err)
		}
		if !ok {
			break
		}
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

func allowedGiftAction(mappings []ActionMapping, revision, trigger string) (ActionMapping, bool) {
	if trigger == "" {
		return ActionMapping{}, false
	}
	for _, mapping := range mappings {
		if mapping.Trigger == trigger && mapping.Allowed && mapping.Revision == revision && mapping.CapabilityStatus == "real" {
			return mapping, true
		}
	}
	return ActionMapping{}, false
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
