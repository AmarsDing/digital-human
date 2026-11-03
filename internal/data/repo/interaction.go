package repo

import (
	"context"
	"sync"

	"github.com/teamwork/digital-human/internal/biz/interaction"
	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/ent/effectoutbox"
	"github.com/teamwork/digital-human/internal/data/ent/interactiondecision"
)

// EntInteractionStore interaction.Store 的 Ent 实现：Decision+EffectOutbox 同事务写入；
// source_event_row_id 非空唯一（§5.2），effect_idempotency_key 唯一（§5.4）。
type EntInteractionStore struct {
	client *ent.Client
}

func NewEntInteractionStore(client *ent.Client) *EntInteractionStore {
	return &EntInteractionStore{client: client}
}

func (s *EntInteractionStore) TxCreateDecisionWithEffects(ctx context.Context, d interaction.Decision, effects []interaction.Effect) (interaction.Decision, []interaction.Effect, bool, error) {
	// 先查后写 + 唯一约束兜底：重复 source 不双建 Decision。
	if d.SourceEventRowID != "" {
		if exist, ok, err := s.GetDecisionBySource(ctx, d.SourceEventRowID); err != nil {
			return interaction.Decision{}, nil, false, err
		} else if ok {
			out, err := s.effectsOfDecision(ctx, exist.ID)
			return exist, out, false, err
		}
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return interaction.Decision{}, nil, false, err
	}
	dc := tx.InteractionDecision.Create().
		SetID(d.ID).
		SetSessionID(d.SessionID).
		SetDecision(d.Decision).
		SetCreatedAtMs(d.CreatedAtMs)
	if d.SourceEventRowID != "" {
		dc.SetSourceEventRowID(d.SourceEventRowID)
	}
	if d.InternalEventID != "" {
		dc.SetInternalEventID(d.InternalEventID)
	}
	if d.RuleVersion != "" {
		dc.SetRuleVersion(d.RuleVersion)
	}
	if d.PolicyRevision != "" {
		dc.SetPolicyRevision(d.PolicyRevision)
	}
	if d.Intent != "" {
		dc.SetIntent(d.Intent)
	}
	if d.TargetID != "" {
		dc.SetTargetID(d.TargetID)
	}
	if d.ReasonCode != "" {
		dc.SetReasonCode(d.ReasonCode)
	}
	if d.PayloadSafe != nil {
		dc.SetPayloadSafeJSON(d.PayloadSafe)
	}
	if _, err := dc.Save(ctx); err != nil {
		_ = tx.Rollback()
		// 并发撞唯一键：返回既有记录（幂等读）。
		if d.SourceEventRowID != "" {
			if exist, ok, qerr := s.GetDecisionBySource(ctx, d.SourceEventRowID); qerr == nil && ok {
				out, qerr := s.effectsOfDecision(ctx, exist.ID)
				return exist, out, false, qerr
			}
		}
		return interaction.Decision{}, nil, false, err
	}
	for _, e := range effects {
		ec := tx.EffectOutbox.Create().
			SetID(e.ID).
			SetDecisionID(e.DecisionID).
			SetSessionID(e.SessionID).
			SetEffectType(e.EffectType).
			SetEffectSeq(e.EffectSeq).
			SetEffectIdempotencyKey(e.EffectIdempotencyKey).
			SetStatus(e.Status).
			SetAttempt(e.Attempt).
			SetMaxAttempts(e.MaxAttempts).
			SetReconcileStatus(e.ReconcileStatus).
			SetCreatedAtMs(e.CreatedAtMs)
		if e.TargetID != "" {
			ec.SetTargetID(e.TargetID)
		}
		if e.PayloadSafe != nil {
			ec.SetPayloadSafeJSON(e.PayloadSafe)
		}
		if _, err := ec.Save(ctx); err != nil {
			_ = tx.Rollback()
			return interaction.Decision{}, nil, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return interaction.Decision{}, nil, false, err
	}
	return d, effects, true, nil
}

func (s *EntInteractionStore) GetDecisionBySource(ctx context.Context, sourceEventRowID string) (interaction.Decision, bool, error) {
	row, err := s.client.InteractionDecision.Query().
		Where(interactiondecision.SourceEventRowID(sourceEventRowID)).
		Only(ctx)
	if err != nil {
		if ent.IsNotFound(err) {
			return interaction.Decision{}, false, nil
		}
		return interaction.Decision{}, false, err
	}
	return decisionFromEnt(row), true, nil
}

func (s *EntInteractionStore) effectsOfDecision(ctx context.Context, decisionID string) ([]interaction.Effect, error) {
	rows, err := s.client.EffectOutbox.Query().
		Where(effectoutbox.DecisionID(decisionID)).
		Order(ent.Asc(effectoutbox.FieldEffectSeq)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]interaction.Effect, 0, len(rows))
	for _, r := range rows {
		out = append(out, effectFromEnt(r))
	}
	return out, nil
}

func (s *EntInteractionStore) ClaimPending(ctx context.Context, owner string, leaseMs, nowMs int64, limit int) ([]interaction.Effect, error) {
	rows, err := s.client.EffectOutbox.Query().
		Where(
			effectoutbox.Status(interaction.EffectPending),
			effectoutbox.Or(
				effectoutbox.NextRetryAtMs(0),
				effectoutbox.NextRetryAtMsLTE(nowMs),
			),
		).
		Order(ent.Asc(effectoutbox.FieldCreatedAtMs)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]interaction.Effect, 0, len(rows))
	for _, r := range rows {
		out = append(out, effectFromEnt(r))
	}
	return out, nil
}

func (s *EntInteractionStore) MarkDispatching(ctx context.Context, id, owner, dispatchID string, leaseUntilMs, nowMs int64) (bool, error) {
	n, err := s.client.EffectOutbox.Update().
		Where(effectoutbox.ID(id), effectoutbox.Status(interaction.EffectPending)).
		SetStatus(interaction.EffectDispatching).
		SetLeaseOwner(owner).
		SetLeaseUntilMs(leaseUntilMs).
		SetDispatchID(dispatchID).
		AddAttempt(1).
		SetDispatchedAtMs(nowMs).
		Save(ctx)
	return n > 0, err
}

func (s *EntInteractionStore) MarkSucceeded(ctx context.Context, id, receiptID string, nowMs int64) error {
	_, err := s.client.EffectOutbox.Update().
		Where(effectoutbox.ID(id)).
		SetStatus(interaction.EffectSucceeded).
		SetDownstreamReceiptID(receiptID).
		SetFinishedAtMs(nowMs).
		Save(ctx)
	return err
}

func (s *EntInteractionStore) MarkFailedRetryable(ctx context.Context, id, lastErr string, nextRetryAtMs, nowMs int64) error {
	// 与内存实现同语义：pending + next_retry_at 表达可重试退避。
	_, err := s.client.EffectOutbox.Update().
		Where(effectoutbox.ID(id)).
		SetStatus(interaction.EffectPending).
		SetLastError(lastErr).
		SetNextRetryAtMs(nextRetryAtMs).
		Save(ctx)
	return err
}

func (s *EntInteractionStore) MarkFailed(ctx context.Context, id, lastErr, reason string, nowMs int64) error {
	_, err := s.client.EffectOutbox.Update().
		Where(effectoutbox.ID(id)).
		SetStatus(interaction.EffectFailed).
		SetLastError(lastErr).
		SetTerminalReason(reason).
		SetFinishedAtMs(nowMs).
		Save(ctx)
	return err
}

func (s *EntInteractionStore) ListLeaseExpired(ctx context.Context, nowMs int64) ([]interaction.Effect, error) {
	rows, err := s.client.EffectOutbox.Query().
		Where(
			effectoutbox.Status(interaction.EffectDispatching),
			effectoutbox.LeaseUntilMsGT(0),
			effectoutbox.LeaseUntilMsLT(nowMs),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]interaction.Effect, 0, len(rows))
	for _, r := range rows {
		out = append(out, effectFromEnt(r))
	}
	return out, nil
}

func (s *EntInteractionStore) ListReceiptTimeout(ctx context.Context, dispatchedBeforeMs int64) ([]interaction.Effect, error) {
	rows, err := s.client.EffectOutbox.Query().
		Where(
			effectoutbox.Status(interaction.EffectDispatching),
			effectoutbox.DispatchedAtMsGT(0),
			effectoutbox.DispatchedAtMsLT(dispatchedBeforeMs),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]interaction.Effect, 0, len(rows))
	for _, r := range rows {
		out = append(out, effectFromEnt(r))
	}
	return out, nil
}

func (s *EntInteractionStore) Requeue(ctx context.Context, id string) error {
	_, err := s.client.EffectOutbox.Update().
		Where(effectoutbox.ID(id), effectoutbox.Status(interaction.EffectDispatching)).
		SetStatus(interaction.EffectPending).
		SetLeaseOwner("").
		SetLeaseUntilMs(0).
		Save(ctx)
	return err
}

func (s *EntInteractionStore) MarkUnknown(ctx context.Context, id, reason string, nowMs int64) error {
	_, err := s.client.EffectOutbox.Update().
		Where(effectoutbox.ID(id), effectoutbox.Status(interaction.EffectDispatching)).
		SetStatus(interaction.EffectUnknown).
		SetReconcileStatus(interaction.ReconcilePending).
		SetLastError(reason).
		SetReconciledAtMs(nowMs).
		Save(ctx)
	return err
}

func (s *EntInteractionStore) CountDecisions(ctx context.Context) (int, error) {
	return s.client.InteractionDecision.Query().Count(ctx)
}

// GetEffect 回执读口（GiftSpeaker.effectOf 依赖）。
func (s *EntInteractionStore) GetEffect(id string) (interaction.Effect, bool) {
	row, err := s.client.EffectOutbox.Query().
		Where(effectoutbox.ID(id)).
		Only(context.Background())
	if err != nil {
		return interaction.Effect{}, false
	}
	return effectFromEnt(row), true
}

func decisionFromEnt(r *ent.InteractionDecision) interaction.Decision {
	return interaction.Decision{
		ID: r.ID, SessionID: r.SessionID, SourceEventRowID: r.SourceEventRowID,
		InternalEventID: r.InternalEventID, RuleVersion: r.RuleVersion,
		PolicyRevision: r.PolicyRevision, Intent: r.Intent, TargetID: r.TargetID,
		Decision: r.Decision, ReasonCode: r.ReasonCode, PayloadSafe: r.PayloadSafeJSON,
		CreatedAtMs: r.CreatedAtMs,
	}
}

func effectFromEnt(r *ent.EffectOutbox) interaction.Effect {
	return interaction.Effect{
		ID: r.ID, DecisionID: r.DecisionID, SessionID: r.SessionID,
		EffectType: r.EffectType, TargetID: r.TargetID, EffectSeq: r.EffectSeq,
		EffectIdempotencyKey: r.EffectIdempotencyKey, PayloadSafe: r.PayloadSafeJSON,
		Status: r.Status, Attempt: r.Attempt, MaxAttempts: r.MaxAttempts,
		LeaseOwner: r.LeaseOwner, LeaseUntilMs: r.LeaseUntilMs, NextRetryAtMs: r.NextRetryAtMs,
		DispatchID: r.DispatchID, DownstreamReceiptID: r.DownstreamReceiptID,
		LastError: r.LastError, ReconcileStatus: r.ReconcileStatus,
		CreatedAtMs: r.CreatedAtMs, DispatchedAtMs: r.DispatchedAtMs,
		FinishedAtMs: r.FinishedAtMs, ReconciledAtMs: r.ReconciledAtMs,
		TerminalReason: r.TerminalReason,
	}
}

// SwitchableInteractionStore 内存启动 → PG 就绪热切（与 SwitchableSessionStore 同先例）。
type SwitchableInteractionStore struct {
	mu  sync.RWMutex
	mem interaction.Store
	db  interaction.Store
}

func NewSwitchableInteractionStore(mem interaction.Store) *SwitchableInteractionStore {
	return &SwitchableInteractionStore{mem: mem}
}

func (s *SwitchableInteractionStore) SetEnt(client *ent.Client) {
	s.mu.Lock()
	s.db = NewEntInteractionStore(client)
	s.mu.Unlock()
}

func (s *SwitchableInteractionStore) current() interaction.Store {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db != nil {
		return s.db
	}
	return s.mem
}

// Persistent §2.2 storage_persistent 探测口径的一部分。
func (s *SwitchableInteractionStore) Persistent() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.db != nil
}

func (s *SwitchableInteractionStore) TxCreateDecisionWithEffects(ctx context.Context, d interaction.Decision, effects []interaction.Effect) (interaction.Decision, []interaction.Effect, bool, error) {
	return s.current().TxCreateDecisionWithEffects(ctx, d, effects)
}
func (s *SwitchableInteractionStore) GetDecisionBySource(ctx context.Context, k string) (interaction.Decision, bool, error) {
	return s.current().GetDecisionBySource(ctx, k)
}
func (s *SwitchableInteractionStore) ClaimPending(ctx context.Context, owner string, leaseMs, nowMs int64, limit int) ([]interaction.Effect, error) {
	return s.current().ClaimPending(ctx, owner, leaseMs, nowMs, limit)
}
func (s *SwitchableInteractionStore) MarkDispatching(ctx context.Context, id, owner, dispatchID string, leaseUntilMs, nowMs int64) (bool, error) {
	return s.current().MarkDispatching(ctx, id, owner, dispatchID, leaseUntilMs, nowMs)
}
func (s *SwitchableInteractionStore) MarkSucceeded(ctx context.Context, id, receiptID string, nowMs int64) error {
	return s.current().MarkSucceeded(ctx, id, receiptID, nowMs)
}
func (s *SwitchableInteractionStore) MarkFailedRetryable(ctx context.Context, id, lastErr string, nextRetryAtMs, nowMs int64) error {
	return s.current().MarkFailedRetryable(ctx, id, lastErr, nextRetryAtMs, nowMs)
}
func (s *SwitchableInteractionStore) MarkFailed(ctx context.Context, id, lastErr, reason string, nowMs int64) error {
	return s.current().MarkFailed(ctx, id, lastErr, reason, nowMs)
}
func (s *SwitchableInteractionStore) ListLeaseExpired(ctx context.Context, nowMs int64) ([]interaction.Effect, error) {
	return s.current().ListLeaseExpired(ctx, nowMs)
}
func (s *SwitchableInteractionStore) ListReceiptTimeout(ctx context.Context, dispatchedBeforeMs int64) ([]interaction.Effect, error) {
	return s.current().ListReceiptTimeout(ctx, dispatchedBeforeMs)
}
func (s *SwitchableInteractionStore) Requeue(ctx context.Context, id string) error {
	return s.current().Requeue(ctx, id)
}
func (s *SwitchableInteractionStore) MarkUnknown(ctx context.Context, id, reason string, nowMs int64) error {
	return s.current().MarkUnknown(ctx, id, reason, nowMs)
}
func (s *SwitchableInteractionStore) CountDecisions(ctx context.Context) (int, error) {
	return s.current().CountDecisions(ctx)
}

// GetEffect 透传回执读口（当前承载实现支持时）。
func (s *SwitchableInteractionStore) GetEffect(id string) (interaction.Effect, bool) {
	type getter interface {
		GetEffect(string) (interaction.Effect, bool)
	}
	if g, ok := s.current().(getter); ok {
		return g.GetEffect(id)
	}
	return interaction.Effect{}, false
}

var _ interaction.Store = (*EntInteractionStore)(nil)
var _ interaction.Store = (*SwitchableInteractionStore)(nil)
