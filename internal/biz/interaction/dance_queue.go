package interaction

import (
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode"
)

const (
	DanceQueued              = "queued"
	DanceDispatched          = "dispatched"
	DanceCompletedUnverified = "completed_unverified"
	DanceCancelledUnverified = "cancelled_unverified"
	DanceRejected            = "rejected"
)

// DanceItem 只记录点舞队列事实；dispatched 不表示 Vidu 已执行或执行成功。
type DanceItem struct {
	ID              string `json:"id"`
	SessionID       string `json:"session_id"`
	UserKey         string `json:"user_key"`
	Trigger         string `json:"trigger"`
	Instruction     string `json:"-"`
	State           string `json:"state"`
	Dispatched      bool   `json:"dispatched"`
	DurationMS      int64  `json:"duration_ms"`
	DispatchedAtMs  int64  `json:"dispatched_at_ms,omitempty"`
	RejectionReason string `json:"rejection_reason,omitempty"`
}

// DanceStatus 是队列深拷贝快照；Capacity 按当前项与等待项合计计算。
type DanceStatus struct {
	Current         *DanceItem  `json:"current,omitempty"`
	Waiting         []DanceItem `json:"waiting"`
	QueueLength     int         `json:"queue_length"`
	Capacity        int         `json:"capacity"`
	Rejected        int         `json:"rejected"`
	AdvanceDeferred bool        `json:"advance_deferred"`
	LastCompleted   *DanceItem  `json:"last_completed,omitempty"`
	LastCancelled   *DanceItem  `json:"last_cancelled,omitempty"`
	LastRejected    *DanceItem  `json:"last_rejected,omitempty"`
}

// DanceQueue 是独立有界 FIFO；它只产生既有 action Effect，不直接调用 Sender。
type DanceQueue struct {
	mu              sync.Mutex
	capacity        int
	revision        string
	mappings        []ActionMapping
	now             func() time.Time
	songPlaying     func() bool
	chatActive      func(string) bool
	current         *DanceItem
	waiting         []*DanceItem
	rejected        int
	advanceDeferred bool
	lastCompleted   *DanceItem
	lastCancelled   *DanceItem
	lastRejected    *DanceItem
	nextID          int
}

func NewDanceQueue(capacity int, revision string, mappings []ActionMapping, now func() time.Time) *DanceQueue {
	if capacity <= 0 {
		capacity = 3
	}
	if now == nil {
		now = time.Now
	}
	return &DanceQueue{
		capacity: capacity, revision: revision, now: now,
		mappings: append([]ActionMapping(nil), mappings...),
	}
}

func (q *DanceQueue) SetActivity(songPlaying func() bool, chatActive func(string) bool) {
	q.mu.Lock()
	q.songPlaying, q.chatActive = songPlaying, chatActive
	q.mu.Unlock()
}

// Enqueue 严格解析“点舞 <触发词>”，成功时返回当前项对应的既有动作 Effect。
func (q *DanceQueue) Enqueue(sessionID, userKey, text string) (DanceItem, *EffectInput, string) {
	q.mu.Lock()
	defer q.mu.Unlock()

	target := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(text), "点舞"))
	if target == "" {
		return q.rejectLocked(sessionID, userKey, "", "dance_target_required")
	}
	if strings.IndexFunc(target, unicode.IsSpace) >= 0 {
		return q.rejectLocked(sessionID, userKey, target, "dance_target_invalid")
	}
	mapping, matched, allowed := q.mappingLocked(target)
	if !matched || !allowed {
		return q.rejectLocked(sessionID, userKey, target, "dance_not_allowed")
	}
	if mapping.DurationMS <= 0 {
		return q.rejectLocked(sessionID, userKey, target, "dance_duration_unconfigured")
	}
	occupied := len(q.waiting)
	if q.current != nil {
		occupied++
	}
	if occupied >= q.capacity {
		return q.rejectLocked(sessionID, userKey, target, "dance_queue_full")
	}

	q.nextID++
	item := &DanceItem{
		ID: fmt.Sprintf("dance-%d", q.nextID), SessionID: sessionID, UserKey: userKey,
		Trigger: target, Instruction: mapping.Instruction, State: DanceQueued,
		DurationMS: mapping.DurationMS,
	}
	if q.current == nil {
		q.current = item
	} else {
		q.waiting = append(q.waiting, item)
	}
	var effect *EffectInput
	if q.current == item {
		effect = q.dispatchCurrentLocked()
	}
	return *item, effect, ""
}

func (q *DanceQueue) mappingLocked(target string) (ActionMapping, bool, bool) {
	var candidate ActionMapping
	matched := false
	for _, mapping := range q.mappings {
		if mapping.Trigger != target {
			continue
		}
		if !matched {
			candidate, matched = mapping, true
		}
		if mapping.Allowed && mapping.Revision == q.revision && mapping.CapabilityStatus == "real" {
			return mapping, true, true
		}
	}
	return candidate, matched, false
}

func (q *DanceQueue) rejectLocked(sessionID, userKey, target, reason string) (DanceItem, *EffectInput, string) {
	q.rejected++
	item := &DanceItem{
		SessionID: sessionID, UserKey: userKey, Trigger: target,
		State: DanceRejected, RejectionReason: reason,
	}
	q.lastRejected = item
	return *item, nil, reason
}

// Advance 只在簿记周期到期且歌曲/打字聊天空闲时推进；繁忙时保留当前项。
func (q *DanceQueue) Advance() *EffectInput {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.current == nil {
		q.promoteLocked()
	}
	if q.current == nil {
		return nil
	}
	if q.current.State == DanceQueued {
		return q.dispatchCurrentLocked()
	}
	if q.current.State != DanceDispatched {
		return nil
	}
	elapsed := q.now().Sub(time.UnixMilli(q.current.DispatchedAtMs))
	if elapsed < time.Duration(q.current.DurationMS)*time.Millisecond {
		return nil
	}
	if q.busyLocked(q.current.SessionID) {
		q.advanceDeferred = true
		return nil
	}
	q.current.State = DanceCompletedUnverified
	q.lastCompleted = cloneDanceItem(q.current)
	q.current = nil
	q.advanceDeferred = false
	q.promoteLocked()
	return q.dispatchCurrentLocked()
}

// CancelCurrent 仅簿记取消并推进下一项，不调用动作/歌曲中断。
func (q *DanceQueue) CancelCurrent() (bool, *EffectInput) {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.current == nil {
		return false, nil
	}
	q.current.State = DanceCancelledUnverified
	q.lastCancelled = cloneDanceItem(q.current)
	q.current = nil
	q.advanceDeferred = false
	q.promoteLocked()
	return true, q.dispatchCurrentLocked()
}

// Clear 默认只清等待项；includeCurrent=true 时也只簿记取消当前项。
func (q *DanceQueue) Clear(includeCurrent bool) (int, *EffectInput) {
	q.mu.Lock()
	defer q.mu.Unlock()
	cleared := len(q.waiting)
	q.waiting = nil
	if !includeCurrent || q.current == nil {
		return cleared, nil
	}
	q.current.State = DanceCancelledUnverified
	q.lastCancelled = cloneDanceItem(q.current)
	q.current = nil
	q.advanceDeferred = false
	return cleared, nil
}

func (q *DanceQueue) promoteLocked() {
	if q.current == nil && len(q.waiting) > 0 {
		q.current = q.waiting[0]
		q.waiting = q.waiting[1:]
	}
}

func (q *DanceQueue) dispatchCurrentLocked() *EffectInput {
	if q.current == nil || q.current.State != DanceQueued || q.busyLocked(q.current.SessionID) {
		return nil
	}
	now := q.now()
	q.current.State = DanceDispatched
	q.current.Dispatched = true
	q.current.DispatchedAtMs = now.UnixMilli()
	q.advanceDeferred = false
	return &EffectInput{
		EffectType: EffectTypeAction, TargetID: "action", Seq: 1,
		PayloadSafe: map[string]any{
			"instruction": q.current.Instruction, "user_key": q.current.UserKey,
			"dance_id": q.current.ID,
		},
	}
}

func (q *DanceQueue) busyLocked(sessionID string) bool {
	return (q.songPlaying != nil && q.songPlaying()) ||
		(q.chatActive != nil && q.chatActive(sessionID))
}

func (q *DanceQueue) Revision() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.revision
}

func (q *DanceQueue) Status() DanceStatus {
	q.mu.Lock()
	defer q.mu.Unlock()
	status := DanceStatus{
		Current: cloneDanceItem(q.current), QueueLength: len(q.waiting),
		Capacity: q.capacity, Rejected: q.rejected, AdvanceDeferred: q.advanceDeferred,
		LastCompleted: cloneDanceItem(q.lastCompleted),
		LastCancelled: cloneDanceItem(q.lastCancelled),
		LastRejected:  cloneDanceItem(q.lastRejected),
		Waiting:       make([]DanceItem, 0, len(q.waiting)),
	}
	for _, item := range q.waiting {
		status.Waiting = append(status.Waiting, *cloneDanceItem(item))
	}
	return status
}

func cloneDanceItem(item *DanceItem) *DanceItem {
	if item == nil {
		return nil
	}
	copy := *item
	return &copy
}
