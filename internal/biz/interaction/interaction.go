// Package interaction 实现深化方案 v2 §5.4/§5.5 互动一致性最小闭环骨架：
// Decision（决策层，每个原始事件仅一条）与 EffectOutbox（副作用 outbox）同事务写入；
// dispatcher lease 派发 + 回执写状态；崩溃扫描补建缺失 Decision、回收过期 lease、
// 超回执期转 unknown+reconcile_pending。
//
// 礼物答谢经 gift 合并触发进入同一闭环；规则侧 chat_reply/action 也有现有出口适配，
// song_request 的队列与播放适配位于 song 包，统一复用此 dispatcher。
package interaction

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"
)

// Decision 决策层记录（§5.4 逐字段）。
type Decision struct {
	ID               string         // uuid
	SessionID        string         // §5.2 第三层
	SourceEventRowID string         // §5.2 第一层平台源事件落库行；空 = 无外部源（恢复补建）
	InternalEventID  string         // §5.2 第二层 eventbus 内部 event_id
	RuleVersion      string         // 冻结规则版本；策略变化不得借新 revision 重放旧事件
	PolicyRevision   string         // 冻结策略 revision
	Intent           string         // gift_thanks / chat_reply / song_request ...
	TargetID         string         // 房间/观众等目标
	Decision         string         // execute/confirm/queued/skipped/blocked/degraded
	ReasonCode       string         // 判定码（礼物金额档/AI 档/频率压制/能力降级等）
	PayloadSafe      map[string]any // 脱敏安全载荷
	CreatedAtMs      int64
}

// Effect 副作用 outbox 记录（§5.4 逐字段）。
type Effect struct {
	ID                   string // uuid
	DecisionID           string
	SessionID            string
	EffectType           string // gift_thanks / say / action / song_play ...
	TargetID             string
	EffectSeq            int
	EffectIdempotencyKey string         // UNIQUE：session_id:decision_id:effect_type:target_id:effect_seq
	PayloadSafe          map[string]any // 脱敏载荷
	Status               string         // 见 EffectStatus* 常量（§5.4 状态机）
	Attempt              int
	MaxAttempts          int
	LeaseOwner           string
	LeaseUntilMs         int64
	NextRetryAtMs        int64
	DispatchID           string
	DownstreamReceiptID  string // 回执：下游返回的 receipt id
	LastError            string
	ReconcileStatus      string // 见 Reconcile* 常量（§5.5）
	CreatedAtMs          int64
	DispatchedAtMs       int64
	FinishedAtMs         int64
	ReconciledAtMs       int64
	TerminalReason       string
}

// §5.4 Effect 状态机（含 §5.5 unknown/reconcile）。
const (
	EffectPending            = "pending"
	EffectDispatching        = "dispatching"
	EffectSucceeded          = "succeeded"
	EffectFailedRetryable    = "failed_retryable"
	EffectFailed             = "failed"
	EffectSkipped            = "skipped"
	EffectExpired            = "expired"
	EffectUnknown            = "unknown" // 超回执期无法证实，绝不默认 succeeded
	EffectReconcileSucceeded = "reconciled_succeeded"
	EffectReconcileFailed    = "reconciled_failed"
)

// §5.5 reconcile 状态机。
const (
	ReconcileNone           = "none"
	ReconcilePending        = "pending"
	ReconcileInProgress     = "in_progress"
	ReconcileResolved       = "resolved"
	ReconcileManualRequired = "manual_required"
)

// ReconcileAction 是操作员核对下游事实后的显式选择。
type ReconcileAction string

const (
	ReconcileActionSucceeded ReconcileAction = "succeeded"
	ReconcileActionFailed    ReconcileAction = "failed"
	ReconcileActionRequeue   ReconcileAction = "requeue"
)

var ErrInvalidReconcile = errors.New("interaction: effect is not unknown or action is invalid")

// 决策结果（§5.4 decision 枚举）。
const (
	DecideExecute  = "execute"
	DecideConfirm  = "confirm"
	DecideQueued   = "queued"
	DecideSkipped  = "skipped"
	DecideBlocked  = "blocked"
	DecideDegraded = "degraded"
)

// ErrDuplicateSource 重复 source_event_row_id：返回既有 Decision（同事务唯一性，§5.2）。
var ErrDuplicateSource = errors.New("interaction: duplicate source_event_row_id")

// IdempotencyKey §5.4 公式：session_id:decision_id:effect_type:target_id:effect_seq。
func IdempotencyKey(sessionID, decisionID, effectType, targetID string, seq int) string {
	return fmt.Sprintf("%s:%s:%s:%s:%d", sessionID, decisionID, effectType, targetID, seq)
}

// SourceKey 无外部源事件行时的确定性兜底键（如 gift 合并批：内容指纹）。
func SourceKey(parts ...string) string {
	h := sha256.New()
	for _, p := range parts {
		h.Write([]byte(p))
		h.Write([]byte{0})
	}
	return "local:" + hex.EncodeToString(h.Sum(nil))[:24]
}

// Store 是 Decision/EffectOutbox 持久化端口（Ent 实现见 internal/data/repo）。
type Store interface {
	// TxCreateDecisionWithEffects 同事务写入 Decision+Effects；
	// source_event_row_id 已存在 → 返回既有记录与 created=false（不双建）。
	TxCreateDecisionWithEffects(ctx context.Context, d Decision, effects []Effect) (Decision, []Effect, bool, error)
	GetDecisionBySource(ctx context.Context, sourceEventRowID string) (Decision, bool, error)
	// ClaimPending 取 pending 且到期的 Effect 并置 lease（dispatcher lease 骨架）。
	ClaimPending(ctx context.Context, owner string, leaseMs, nowMs int64, limit int) ([]Effect, error)
	// MarkDispatching pending→dispatching（写 dispatch_id/lease/attempt+1/dispatched_at）。
	MarkDispatching(ctx context.Context, id, owner, dispatchID string, leaseUntilMs, nowMs int64) (bool, error)
	// MarkSucceeded 写回执：succeeded + downstream_receipt_id + finished_at。
	MarkSucceeded(ctx context.Context, id, receiptID string, nowMs int64) error
	// MarkFailedRetryable / MarkFailed 终态/退避。
	MarkFailedRetryable(ctx context.Context, id, lastErr string, nextRetryAtMs, nowMs int64) error
	MarkFailed(ctx context.Context, id, lastErr, reason string, nowMs int64) error
	// 崩溃扫描（§5.5）：
	// ListLeaseExpired 取 dispatching 且 lease_until < now 的记录（回收为 pending）。
	ListLeaseExpired(ctx context.Context, nowMs int64) ([]Effect, error)
	// ListReceiptTimeout 取 dispatching 且 dispatched_at < cutoff 的记录（转 unknown）。
	ListReceiptTimeout(ctx context.Context, dispatchedBeforeMs int64) ([]Effect, error)
	// Requeue 回收：dispatching→pending 清 lease。
	Requeue(ctx context.Context, id string) error
	// MarkUnknown 超回执期：dispatching→unknown + reconcile_status=pending。
	MarkUnknown(ctx context.Context, id, reason string, nowMs int64) error
	// Reconcile 只允许人工核对 unknown 后写终态或显式重排队。
	Reconcile(ctx context.Context, id string, action ReconcileAction, receiptID, reason string, nowMs int64) error
	// ListEffects 按状态读取副作用，供 unknown 人工对账；空状态表示全部。
	ListEffects(ctx context.Context, status string) ([]Effect, error)
	// 崩溃扫描补建缺失 Decision 的审计计数（观测用）。
	CountDecisions(ctx context.Context) (int, error)
}

// DecisionInput Decide 入参。
type DecisionInput struct {
	SessionID        string
	SourceEventRowID string // 必填；空时由调用方用 SourceKey 生成确定性兜底键
	InternalEventID  string
	RuleVersion      string
	PolicyRevision   string
	Intent           string
	TargetID         string
	Outcome          string
	ReasonCode       string
	PayloadSafe      map[string]any
	Effects          []EffectInput
}

// EffectInput 副作用声明。
type EffectInput struct {
	EffectType  string
	TargetID    string
	Seq         int
	PayloadSafe map[string]any
	MaxAttempts int
}

// Service 决策+副作用同事务写入的唯一入口。
type Service struct {
	store Store
	now   func() time.Time
	newID func() string
}

// NewService store 为 nil 时 Decide 直接报错（fail-closed，不静默丢决策）。
func NewService(store Store) *Service {
	return &Service{store: store, now: time.Now, newID: newUUID}
}

// Store 返回底层存储（GiftSpeaker 回执读口等使用）。
func (s *Service) Store() Store { return s.store }

// Decide 写 Decision+EffectOutbox（同事务）；重复 source 返回既有记录（不双 Decision）。
func (s *Service) Decide(ctx context.Context, in DecisionInput) (Decision, []Effect, error) {
	if s.store == nil {
		return Decision{}, nil, errors.New("interaction: store not wired")
	}
	if in.Outcome == "" {
		in.Outcome = DecideExecute
	}
	d := Decision{
		ID: s.newID(), SessionID: in.SessionID, SourceEventRowID: in.SourceEventRowID,
		InternalEventID: in.InternalEventID, RuleVersion: in.RuleVersion,
		PolicyRevision: in.PolicyRevision, Intent: in.Intent, TargetID: in.TargetID,
		Decision: in.Outcome, ReasonCode: in.ReasonCode, PayloadSafe: in.PayloadSafe,
		CreatedAtMs: s.now().UnixMilli(),
	}
	effects := make([]Effect, 0, len(in.Effects))
	for _, e := range in.Effects {
		maxA := e.MaxAttempts
		if maxA <= 0 {
			maxA = 3
		}
		effects = append(effects, Effect{
			ID: s.newID(), DecisionID: d.ID, SessionID: in.SessionID,
			EffectType: e.EffectType, TargetID: e.TargetID, EffectSeq: e.Seq,
			EffectIdempotencyKey: IdempotencyKey(in.SessionID, d.ID, e.EffectType, e.TargetID, e.Seq),
			PayloadSafe:          e.PayloadSafe, Status: EffectPending, MaxAttempts: maxA,
			ReconcileStatus: ReconcileNone, CreatedAtMs: d.CreatedAtMs,
		})
	}
	// created 标志对调用方透明：重复 source 返回既有记录即可（不双 Decision）。
	out, outEffects, _, err := s.store.TxCreateDecisionWithEffects(ctx, d, effects)
	return out, outEffects, err
}
