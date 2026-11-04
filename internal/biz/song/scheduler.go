package song

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	RequestPendingConfirm = "pending_confirm"
	RequestQueued         = "queued"
	RequestPlaying        = "playing"
	RequestDone           = "done"
	RequestFailed         = "failed"
	RequestUnknown        = "unknown"
	RequestExpired        = "expired"
	RequestSkipped        = "skipped"
	RequestRejected       = "rejected"
	RequestCancelled      = "cancelled"
)

const (
	RequestActionApprove = "approve"
	RequestActionReject  = "reject"
	RequestActionCancel  = "cancel"
)

var (
	ErrQueueFull                = errors.New("点歌队列已满")
	ErrUserActive               = errors.New("每位观众只能有一首活动点歌")
	ErrUserCooldown             = errors.New("观众点歌冷却中")
	ErrUnknownState             = errors.New("点歌请求不是 unknown 状态")
	ErrRequestNotFound          = errors.New("点歌请求不存在")
	ErrInvalidRequestTransition = errors.New("非法点歌请求状态迁移")
)

// QueuePolicy 默认队列 10、请求 TTL 120 秒、每人 120 秒冷却。
type QueuePolicy struct {
	MaxQueue     int
	TTL          time.Duration
	UserCooldown time.Duration
}

// Request 是点歌持久队列记录；Version 与 PlayID 防止旧回执覆盖新状态。
type Request struct {
	ID                   string `json:"id"`
	SessionID            string `json:"session_id"`
	SourceEventRowID     string `json:"source_event_row_id"`
	DecisionID           string `json:"decision_id"`
	EffectIdempotencyKey string `json:"effect_idempotency_key"`
	UserKey              string `json:"user_key"`
	File                 string `json:"file"`
	Script               string `json:"script"`
	Status               string `json:"status"`
	QueueSeq             int64  `json:"queue_seq"`
	Position             int    `json:"position"`
	Version              int64  `json:"version"`
	PlayID               string `json:"play_id"`
	Attempt              int    `json:"attempt"`
	LastError            string `json:"last_error"`
	ReconcileStatus      string `json:"reconcile_status"`
	CreatedAtMs          int64  `json:"created_at_ms"`
	ExpiresAtMs          int64  `json:"expires_at_ms"`
	StartedAtMs          int64  `json:"started_at_ms"`
	TerminalAtMs         int64  `json:"terminal_at_ms"`
	TerminalReason       string `json:"terminal_reason"`
}

// RequestEnqueuer 是 interaction effect 所需的最小写端口。
type RequestEnqueuer interface {
	Enqueue(ctx context.Context, in Request) (Request, bool, error)
}

// PlaybackRequestStore 是 Scheduler 所需的播放窄端口。
type PlaybackRequestStore interface {
	ClaimNext(ctx context.Context, playID string) (Request, bool, error)
	Complete(ctx context.Context, id, playID string, playErr error) (bool, error)
	RequeueNotStarted(ctx context.Context, id, playID string, cause error) (bool, error)
	FailNotStarted(ctx context.Context, id, playID string, cause error) (bool, error)
	RecoverPlaying(ctx context.Context) error
}

// RequestReconcileAction 是人工核对播放事实后的选择。
type RequestReconcileAction string

const (
	RequestReconcileDone    RequestReconcileAction = "done"
	RequestReconcileFailed  RequestReconcileAction = "failed"
	RequestReconcileRequeue RequestReconcileAction = "requeue"
)

// RequestStore 是点歌请求持久化端口；Memory 与 Ent 实现遵循同一状态机。
type RequestStore interface {
	RequestEnqueuer
	PlaybackRequestStore
	Get(ctx context.Context, id string) (Request, bool, error)
	List(ctx context.Context) ([]Request, error)
	ResolveUnknown(ctx context.Context, id string, action RequestReconcileAction) error
	ChangeStatus(ctx context.Context, id, action string) error
}

// MemoryRequestStore 提供同源幂等、单调 queue_seq 和播放状态 CAS。
type MemoryRequestStore struct {
	mu       sync.Mutex
	policy   QueuePolicy
	now      func() time.Time
	requests map[string]Request
	bySource map[string]string
	nextSeq  int64
}

func NewMemoryRequestStore(policy QueuePolicy, now func() time.Time) *MemoryRequestStore {
	if now == nil {
		now = time.Now
	}
	if policy.MaxQueue <= 0 {
		policy.MaxQueue = 10
	}
	if policy.TTL <= 0 {
		policy.TTL = 120 * time.Second
	}
	if policy.UserCooldown <= 0 {
		policy.UserCooldown = 120 * time.Second
	}
	return &MemoryRequestStore{
		policy: policy, now: now, requests: map[string]Request{}, bySource: map[string]string{},
	}
}

// Enqueue 返回已保存的请求；重复来源返回原请求。
func (m *MemoryRequestStore) Enqueue(_ context.Context, in Request) (Request, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if in.SourceEventRowID == "" {
		in.SourceEventRowID = uuid.NewString()
	}
	source := in.SessionID + "\x00" + in.SourceEventRowID
	if id, ok := m.bySource[source]; ok {
		return m.requests[id], true, nil
	}
	if in.Status == "" {
		in.Status = RequestQueued
	}
	if in.Status != RequestQueued && in.Status != RequestPendingConfirm {
		return Request{}, false, ErrInvalidRequestTransition
	}
	now := m.now()
	active := 0
	var latest int64
	for _, req := range m.requests {
		if req.SessionID != in.SessionID {
			continue
		}
		if req.Status == RequestQueued || req.Status == RequestPlaying {
			active++
		}
		if req.UserKey == in.UserKey {
			if req.Status == RequestQueued || req.Status == RequestPlaying || req.Status == RequestUnknown {
				return Request{}, false, ErrUserActive
			}
			if req.CreatedAtMs > latest {
				latest = req.CreatedAtMs
			}
		}
	}
	if active >= m.policy.MaxQueue {
		return Request{}, false, ErrQueueFull
	}
	if latest > 0 && now.Sub(time.UnixMilli(latest)) < m.policy.UserCooldown {
		return Request{}, false, ErrUserCooldown
	}
	m.nextSeq++
	in.ID = uuid.NewString()
	in.QueueSeq = m.nextSeq
	in.Position = 0
	if in.Status == RequestQueued {
		in.Position = active + 1
	}
	in.Version = 1
	in.ReconcileStatus = "none"
	in.CreatedAtMs = now.UnixMilli()
	if in.ExpiresAtMs <= 0 {
		in.ExpiresAtMs = now.Add(m.policy.TTL).UnixMilli()
	}
	m.requests[in.ID] = in
	m.bySource[source] = in.ID
	return in, false, nil
}

func (m *MemoryRequestStore) Get(_ context.Context, id string) (Request, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	req, ok := m.requests[id]
	return req, ok, nil
}

func (m *MemoryRequestStore) List(_ context.Context) ([]Request, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := m.sortedLocked()
	position := 0
	for i := range out {
		if out[i].Status == RequestQueued {
			position++
			out[i].Position = position
		} else {
			out[i].Position = 0
		}
	}
	return out, nil
}

func (m *MemoryRequestStore) sortedLocked() []Request {
	out := make([]Request, 0, len(m.requests))
	for _, req := range m.requests {
		out = append(out, req)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].QueueSeq == out[j].QueueSeq {
			return out[i].ID < out[j].ID
		}
		return out[i].QueueSeq < out[j].QueueSeq
	})
	return out
}

// ClaimNext 原子过期队首并把下一首 queued 转为 playing。
func (m *MemoryRequestStore) ClaimNext(_ context.Context, playID string) (Request, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.expireLocked(now)
	for _, req := range m.sortedLocked() {
		if req.Status != RequestQueued {
			continue
		}
		req.Status = RequestPlaying
		req.Position = 0
		req.Version++
		req.PlayID = playID
		req.Attempt++
		req.StartedAtMs = now.UnixMilli()
		m.requests[req.ID] = req
		return req, true, nil
	}
	return Request{}, false, nil
}

func (m *MemoryRequestStore) expireLocked(now time.Time) {
	for id, req := range m.requests {
		if req.Status == RequestQueued && req.ExpiresAtMs > 0 && now.UnixMilli() >= req.ExpiresAtMs {
			req.Status = RequestExpired
			req.Version++
			req.Position = 0
			req.TerminalAtMs = now.UnixMilli()
			req.TerminalReason = "expired"
			m.requests[id] = req
		}
	}
}

// Complete 只接受当前 play_id 的回执。
func (m *MemoryRequestStore) Complete(_ context.Context, id, playID string, playErr error) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	req, ok := m.requests[id]
	if !ok || req.Status != RequestPlaying || req.PlayID != playID {
		return false, nil
	}
	req.Version++
	req.TerminalAtMs = m.now().UnixMilli()
	if playErr == nil {
		req.Status = RequestDone
		req.TerminalReason = "completed"
	} else {
		if errors.Is(playErr, ErrStopped) {
			req.Status = RequestSkipped
			req.TerminalReason = "stopped"
		} else {
			req.Status = RequestFailed
			req.TerminalReason = "play_failed"
		}
		req.LastError = playErr.Error()
	}
	m.requests[id] = req
	return true, nil
}

func (m *MemoryRequestStore) RequeueNotStarted(_ context.Context, id, playID string, cause error) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	req, ok := m.requests[id]
	if !ok || req.Status != RequestPlaying || req.PlayID != playID {
		return false, nil
	}
	req.Status = RequestQueued
	req.Version++
	req.PlayID = ""
	req.LastError = cause.Error()
	m.requests[id] = req
	return true, nil
}

func (m *MemoryRequestStore) FailNotStarted(_ context.Context, id, playID string, cause error) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	req, ok := m.requests[id]
	if !ok || req.Status != RequestPlaying || req.PlayID != playID {
		return false, nil
	}
	req.Status = RequestFailed
	req.Version++
	req.LastError = cause.Error()
	req.TerminalReason = "start_failed"
	req.TerminalAtMs = m.now().UnixMilli()
	m.requests[id] = req
	return true, nil
}

// RecoverPlaying 崩溃恢复只把 playing 转 unknown，绝不自动重放。
func (m *MemoryRequestStore) RecoverPlaying(_ context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, req := range m.requests {
		if req.Status != RequestPlaying {
			continue
		}
		req.Status = RequestUnknown
		req.Version++
		req.ReconcileStatus = "pending"
		req.LastError = "playing_recovery_unknown"
		m.requests[id] = req
	}
	return nil
}

func (m *MemoryRequestStore) ResolveUnknown(_ context.Context, id string, action RequestReconcileAction) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	req, ok := m.requests[id]
	if !ok || req.Status != RequestUnknown {
		return ErrUnknownState
	}
	req.Version++
	req.ReconcileStatus = "resolved"
	switch action {
	case RequestReconcileDone:
		req.Status = RequestDone
		req.TerminalReason = "manual_completed"
	case RequestReconcileFailed:
		req.Status = RequestFailed
		req.TerminalReason = "manual_failed"
	case RequestReconcileRequeue:
		req.Status = RequestQueued
		req.PlayID = ""
		req.LastError = ""
		req.TerminalAtMs = 0
		req.TerminalReason = "manual_requeued"
		m.requests[id] = req
		return nil
	default:
		return ErrUnknownState
	}
	req.TerminalAtMs = m.now().UnixMilli()
	m.requests[id] = req
	return nil
}

// ChangeStatus 执行管理动作，拒绝未定义迁移与 playing/terminal 取消。
func (m *MemoryRequestStore) ChangeStatus(_ context.Context, id, action string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	req, ok := m.requests[id]
	if !ok {
		return ErrRequestNotFound
	}
	now := m.now().UnixMilli()
	switch action {
	case RequestActionApprove:
		if req.Status != RequestPendingConfirm {
			return ErrInvalidRequestTransition
		}
		req.Status = RequestQueued
	case RequestActionReject:
		if req.Status != RequestPendingConfirm {
			return ErrInvalidRequestTransition
		}
		req.Status = RequestRejected
		req.Position = 0
		req.TerminalAtMs = now
		req.TerminalReason = "rejected"
	case RequestActionCancel:
		if req.Status != RequestPendingConfirm && req.Status != RequestQueued {
			return ErrInvalidRequestTransition
		}
		req.Status = RequestCancelled
		req.Position = 0
		req.TerminalAtMs = now
		req.TerminalReason = "cancelled"
	default:
		return ErrInvalidRequestTransition
	}
	req.Version++
	m.requests[id] = req
	return nil
}

// Scheduler 是唯一点歌自动 Play 出口；完成回执触发下一首。
type Scheduler struct {
	mu    sync.Mutex
	store PlaybackRequestStore
	songs *Service
	now   func() time.Time
}

func NewScheduler(store PlaybackRequestStore, songs *Service, now func() time.Time) *Scheduler {
	if now == nil {
		now = time.Now
	}
	return &Scheduler{store: store, songs: songs, now: now}
}

func (s *Scheduler) Recover(ctx context.Context) error {
	if s.store == nil {
		return errors.New("点歌调度器未完整装配")
	}
	return s.store.RecoverPlaying(ctx)
}

func (s *Scheduler) DispatchNext(ctx context.Context) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.store == nil || s.songs == nil {
		return false, errors.New("点歌调度器未完整装配")
	}
	s.songs.dispatchMu.Lock()
	defer s.songs.dispatchMu.Unlock()
	if s.songs.Status().Playing {
		return false, nil
	}
	req, ok, err := s.store.ClaimNext(ctx, "play-"+uuid.NewString())
	if err != nil || !ok {
		return false, err
	}
	_, err = s.songs.start(context.WithoutCancel(ctx), req.File, req.Script, func(playErr error) {
		_, _ = s.store.Complete(context.Background(), req.ID, req.PlayID, playErr)
		go func() { _, _ = s.DispatchNext(context.Background()) }()
	})
	if err != nil {
		var storeErr error
		if errors.Is(err, ErrBusy) || errors.Is(err, ErrNotLive) {
			_, storeErr = s.store.RequeueNotStarted(ctx, req.ID, req.PlayID, err)
		} else {
			_, storeErr = s.store.FailNotStarted(ctx, req.ID, req.PlayID, err)
		}
		if storeErr != nil {
			return false, storeErr
		}
		return false, err
	}
	return true, nil
}

var _ RequestStore = (*MemoryRequestStore)(nil)
