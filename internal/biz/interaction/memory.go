package interaction

import (
	"context"
	"sort"
	"sync"
)

// MemoryStore 内存实现：dev/测试用；语义与 Ent 实现对齐（同事务=同临界区，
// source_event_row_id 非空唯一、effect_idempotency_key 唯一）。
type MemoryStore struct {
	mu        sync.Mutex
	decisions map[string]Decision
	bySource  map[string]string // source_event_row_id -> decision_id
	effects   map[string]Effect
	byIdem    map[string]string // effect_idempotency_key -> effect_id
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		decisions: map[string]Decision{}, bySource: map[string]string{},
		effects: map[string]Effect{}, byIdem: map[string]string{},
	}
}

func (m *MemoryStore) TxCreateDecisionWithEffects(_ context.Context, d Decision, effects []Effect) (Decision, []Effect, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d.SourceEventRowID != "" {
		if id, ok := m.bySource[d.SourceEventRowID]; ok {
			exist := m.decisions[id]
			out := make([]Effect, 0)
			for _, e := range m.effects {
				if e.DecisionID == id {
					out = append(out, e)
				}
			}
			sort.Slice(out, func(i, j int) bool { return out[i].EffectSeq < out[j].EffectSeq })
			return exist, out, false, nil
		}
	}
	keys := make(map[string]struct{}, len(effects))
	for _, e := range effects {
		if _, dup := m.byIdem[e.EffectIdempotencyKey]; dup {
			return Decision{}, nil, false, ErrDuplicateSource
		}
		if _, dup := keys[e.EffectIdempotencyKey]; dup {
			return Decision{}, nil, false, ErrDuplicateSource
		}
		keys[e.EffectIdempotencyKey] = struct{}{}
	}
	m.decisions[d.ID] = d
	if d.SourceEventRowID != "" {
		m.bySource[d.SourceEventRowID] = d.ID
	}
	for _, e := range effects {
		m.effects[e.ID] = e
		m.byIdem[e.EffectIdempotencyKey] = e.ID
	}
	return d, effects, true, nil
}

func (m *MemoryStore) GetDecisionBySource(_ context.Context, sourceEventRowID string) (Decision, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.bySource[sourceEventRowID]
	if !ok {
		return Decision{}, false, nil
	}
	return m.decisions[id], true, nil
}

func (m *MemoryStore) ClaimPending(_ context.Context, owner string, leaseMs, nowMs int64, limit int) ([]Effect, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Effect
	for _, e := range m.effects {
		if len(out) >= limit {
			break
		}
		if e.Status != EffectPending {
			continue
		}
		if e.NextRetryAtMs > 0 && e.NextRetryAtMs > nowMs {
			continue
		}
		out = append(out, e)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAtMs < out[j].CreatedAtMs })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (m *MemoryStore) MarkDispatching(_ context.Context, id, owner, dispatchID string, leaseUntilMs, nowMs int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.effects[id]
	if !ok || e.Status != EffectPending {
		return false, nil
	}
	e.Status = EffectDispatching
	e.LeaseOwner = owner
	e.LeaseUntilMs = leaseUntilMs
	e.DispatchID = dispatchID
	e.Attempt++
	e.DispatchedAtMs = nowMs
	m.effects[id] = e
	return true, nil
}

func (m *MemoryStore) MarkSucceeded(_ context.Context, id, receiptID string, nowMs int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.effects[id]
	if !ok {
		return nil
	}
	e.Status = EffectSucceeded
	e.DownstreamReceiptID = receiptID
	e.FinishedAtMs = nowMs
	m.effects[id] = e
	return nil
}

func (m *MemoryStore) MarkFailedRetryable(_ context.Context, id, lastErr string, nextRetryAtMs, nowMs int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.effects[id]
	if !ok {
		return nil
	}
	// failed_retryable 退避到期后可再领：用 pending + next_retry_at 表达
	//（ClaimPending 按 next_retry_at 过滤；终态由 MarkFailed 写 failed）。
	e.Status = EffectPending
	e.LastError = lastErr
	e.NextRetryAtMs = nextRetryAtMs
	m.effects[id] = e
	return nil
}

func (m *MemoryStore) MarkFailed(_ context.Context, id, lastErr, reason string, nowMs int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.effects[id]
	if !ok {
		return nil
	}
	e.Status = EffectFailed
	e.LastError = lastErr
	e.TerminalReason = reason
	e.FinishedAtMs = nowMs
	m.effects[id] = e
	return nil
}

func (m *MemoryStore) ListLeaseExpired(_ context.Context, nowMs int64) ([]Effect, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Effect
	for _, e := range m.effects {
		if e.Status == EffectDispatching && e.LeaseUntilMs > 0 && e.LeaseUntilMs < nowMs {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *MemoryStore) ListReceiptTimeout(_ context.Context, dispatchedBeforeMs int64) ([]Effect, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []Effect
	for _, e := range m.effects {
		if e.Status == EffectDispatching && e.DispatchedAtMs > 0 && e.DispatchedAtMs < dispatchedBeforeMs {
			out = append(out, e)
		}
	}
	return out, nil
}

func (m *MemoryStore) Requeue(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.effects[id]
	if !ok {
		return nil
	}
	if e.Status == EffectDispatching {
		e.Status = EffectPending
		e.LeaseOwner = ""
		e.LeaseUntilMs = 0
		m.effects[id] = e
	}
	return nil
}

func (m *MemoryStore) MarkUnknown(_ context.Context, id, reason string, nowMs int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.effects[id]
	if !ok {
		return nil
	}
	if e.Status != EffectDispatching {
		return nil
	}
	e.Status = EffectUnknown
	e.ReconcileStatus = ReconcilePending
	e.LastError = reason
	m.effects[id] = e
	return nil
}

func (m *MemoryStore) Reconcile(_ context.Context, id string, action ReconcileAction, receiptID, reason string, nowMs int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.effects[id]
	if !ok || e.Status != EffectUnknown {
		return ErrInvalidReconcile
	}
	e.ReconcileStatus = ReconcileResolved
	e.ReconciledAtMs = nowMs
	e.LeaseOwner = ""
	e.LeaseUntilMs = 0
	switch action {
	case ReconcileActionSucceeded:
		e.Status = EffectReconcileSucceeded
		e.DownstreamReceiptID = receiptID
		e.TerminalReason = reason
		e.FinishedAtMs = nowMs
	case ReconcileActionFailed:
		e.Status = EffectReconcileFailed
		e.TerminalReason = reason
		e.FinishedAtMs = nowMs
	case ReconcileActionRequeue:
		e.Status = EffectPending
		e.NextRetryAtMs = 0
		e.DispatchID = ""
		e.LastError = reason
	default:
		return ErrInvalidReconcile
	}
	m.effects[id] = e
	return nil
}

func (m *MemoryStore) ListEffects(_ context.Context, status string) ([]Effect, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Effect, 0)
	for _, e := range m.effects {
		if status == "" || e.Status == status {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAtMs == out[j].CreatedAtMs {
			return out[i].ID < out[j].ID
		}
		return out[i].CreatedAtMs < out[j].CreatedAtMs
	})
	return out, nil
}

func (m *MemoryStore) CountDecisions(_ context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.decisions), nil
}

// GetEffect 测试/调试用读口。
func (m *MemoryStore) GetEffect(id string) (Effect, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	e, ok := m.effects[id]
	return e, ok
}
