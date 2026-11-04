package interaction

import (
	"context"
	"strings"
	"sync"
	"time"
)

const (
	IntentChatReply   = "chat_reply"
	IntentAction      = "action"
	IntentSongRequest = "song_request"

	EffectTypeChatReply   = "chat_reply"
	EffectTypeAction      = "action"
	EffectTypeSongRequest = "song_request"
)

// ActionMapping 把观众触发词映射到已审核的真实动作指令。
type ActionMapping struct {
	Trigger          string
	Instruction      string
	Revision         string
	Allowed          bool
	CapabilityStatus string
}

// RulePolicy 是场次冻结的确定性互动策略。
type RulePolicy struct {
	RuleVersion          string
	Revision             string
	LLMAvailable         bool
	Songs                map[string]string
	Actions              []ActionMapping
	UserInterval         time.Duration
	UserPerMinute        int
	ActionUserCooldown   time.Duration
	ActionGlobalCooldown time.Duration
}

// Health 只使用真实播放指标；严重卡顿时聊天与动作降级。
type Health struct {
	Known      bool
	AudioLagMs int64
}

// RuleEvent 是原始弹幕提交后进入规则入口的不可变输入。
type RuleEvent struct {
	EventRowID      string
	InternalEventID string
	SessionID       string
	UserKey         string
	Text            string
	ReceivedAtMs    int64
	ExpiresAtMs     int64
}

// RuleEngine 先匹配点歌与白名单动作，再进入普通聊天。
type RuleEngine struct {
	mu         sync.Mutex
	svc        *Service
	policy     RulePolicy
	now        func() time.Time
	health     Health
	users      map[string][]time.Time
	actions    map[string]time.Time
	lastAction time.Time
}

func NewRuleEngine(svc *Service, policy RulePolicy, now func() time.Time) *RuleEngine {
	if now == nil {
		now = time.Now
	}
	if policy.UserInterval <= 0 {
		policy.UserInterval = 2 * time.Second
	}
	if policy.UserPerMinute <= 0 {
		policy.UserPerMinute = 6
	}
	if policy.ActionUserCooldown <= 0 {
		policy.ActionUserCooldown = 60 * time.Second
	}
	if policy.ActionGlobalCooldown <= 0 {
		policy.ActionGlobalCooldown = 8 * time.Second
	}
	return &RuleEngine{
		svc: svc, policy: policy, now: now,
		users: map[string][]time.Time{}, actions: map[string]time.Time{},
	}
}

func (r *RuleEngine) SetHealth(h Health) {
	r.mu.Lock()
	r.health = h
	r.mu.Unlock()
}

// Handle 同源已决策时返回冻结结果；新事件按当前场次策略判定并持久化。
func (r *RuleEngine) Handle(ctx context.Context, ev RuleEvent) (Decision, []Effect, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if ev.EventRowID != "" {
		if existing, ok, err := r.svc.store.GetDecisionBySource(ctx, ev.EventRowID); err != nil {
			return Decision{}, nil, err
		} else if ok {
			_, effects, err := r.svc.Decide(ctx, DecisionInput{SourceEventRowID: ev.EventRowID})
			return existing, effects, err
		}
	}
	return r.svc.Decide(ctx, r.decisionLocked(ev))
}

// DecisionInput 为崩溃扫描生成与在线路径一致的判定，但不提前写库。
func (r *RuleEngine) DecisionInput(ev RuleEvent) DecisionInput {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.decisionLocked(ev)
}

func (r *RuleEngine) decisionLocked(ev RuleEvent) DecisionInput {
	now := r.now()
	in := DecisionInput{
		SessionID: ev.SessionID, SourceEventRowID: ev.EventRowID,
		InternalEventID: ev.InternalEventID, RuleVersion: r.policy.RuleVersion,
		PolicyRevision: r.policy.Revision, TargetID: ev.UserKey,
		Outcome: DecideExecute, PayloadSafe: map[string]any{"text": ev.Text},
	}
	if ev.ExpiresAtMs > 0 && now.UnixMilli() > ev.ExpiresAtMs {
		in.Intent = intentOf(ev.Text)
		in.Outcome = DecideSkipped
		in.ReasonCode = "skipped_expired"
		return in
	}
	if reason := r.rateLimitLocked(ev.SessionID+"\x00"+ev.UserKey, now); reason != "" {
		in.Intent = intentOf(ev.Text)
		in.Outcome = DecideBlocked
		in.ReasonCode = reason
		return in
	}

	text := strings.TrimSpace(ev.Text)
	if title, ok := songTitle(text); ok {
		in.Intent = IntentSongRequest
		in.Outcome = DecideQueued
		in.ReasonCode = "song_rule_matched"
		file, found := r.policy.Songs[title]
		if !found || strings.TrimSpace(file) == "" {
			in.Outcome = DecideBlocked
			in.ReasonCode = "song_not_found"
			return in
		}
		payload := map[string]any{
			"title": title, "file": file,
			"source_event_row_id": ev.EventRowID, "user_key": ev.UserKey,
		}
		in.PayloadSafe = payload
		in.Effects = []EffectInput{{
			EffectType: EffectTypeSongRequest, TargetID: "song", Seq: 1,
			PayloadSafe: payload,
		}}
		return in
	}

	if mapping, ok := r.actionLocked(text); ok {
		in.Intent = IntentAction
		in.ReasonCode = "action_rule_matched"
		if r.severeLagLocked() {
			in.Outcome = DecideDegraded
			in.ReasonCode = "playback_lag"
			return in
		}
		userKey := ev.SessionID + "\x00" + ev.UserKey
		if last, exists := r.actions[userKey]; exists && now.Sub(last) < r.policy.ActionUserCooldown {
			in.Outcome = DecideBlocked
			in.ReasonCode = "action_user_cooldown"
			return in
		}
		if !r.lastAction.IsZero() && now.Sub(r.lastAction) < r.policy.ActionGlobalCooldown {
			in.Outcome = DecideBlocked
			in.ReasonCode = "action_global_cooldown"
			return in
		}
		r.actions[userKey] = now
		r.lastAction = now
		payload := map[string]any{"instruction": mapping.Instruction, "user_key": ev.UserKey}
		in.PayloadSafe = payload
		in.Effects = []EffectInput{{
			EffectType: EffectTypeAction, TargetID: "action", Seq: 1,
			PayloadSafe: payload,
		}}
		return in
	}

	in.Intent = IntentChatReply
	if r.severeLagLocked() {
		in.Outcome = DecideDegraded
		in.ReasonCode = "playback_lag"
		return in
	}
	if !r.policy.LLMAvailable {
		in.Outcome = DecideDegraded
		in.ReasonCode = "llm_unavailable"
		return in
	}
	in.ReasonCode = "chat_fallback"
	in.Effects = []EffectInput{{
		EffectType: EffectTypeChatReply, TargetID: "speech", Seq: 1,
		PayloadSafe: map[string]any{"text": text},
	}}
	return in
}

func (r *RuleEngine) rateLimitLocked(key string, now time.Time) string {
	times := r.users[key]
	cutoff := now.Add(-time.Minute)
	kept := times[:0]
	for _, at := range times {
		if at.After(cutoff) {
			kept = append(kept, at)
		}
	}
	if len(kept) > 0 && now.Sub(kept[len(kept)-1]) < r.policy.UserInterval {
		r.users[key] = kept
		return "rate_limited"
	}
	if len(kept) >= r.policy.UserPerMinute {
		r.users[key] = kept
		return "rate_limited"
	}
	r.users[key] = append(kept, now)
	return ""
}

func (r *RuleEngine) actionLocked(text string) (ActionMapping, bool) {
	for _, mapping := range r.policy.Actions {
		if mapping.Trigger == text && mapping.Allowed && mapping.Revision == r.policy.Revision && mapping.CapabilityStatus == "real" {
			return mapping, true
		}
	}
	return ActionMapping{}, false
}

func (r *RuleEngine) severeLagLocked() bool {
	return r.health.Known && r.health.AudioLagMs > 1500
}

func songTitle(text string) (string, bool) {
	for _, prefix := range []string{"点歌", "我想听"} {
		if strings.HasPrefix(text, prefix) {
			return strings.TrimSpace(strings.TrimPrefix(text, prefix)), true
		}
	}
	return "", false
}

func intentOf(text string) string {
	if _, ok := songTitle(strings.TrimSpace(text)); ok {
		return IntentSongRequest
	}
	return IntentChatReply
}
