package live

import (
	"context"
	"sort"
	"sync"
	"time"
)

// SessionRecord 对齐 LiveSession 表，仅保存非敏感字段；绝不存密钥。
type SessionRecord struct {
	ID             string `json:"id"`
	LiveID         string `json:"live_id,omitempty"`
	Phase          Phase  `json:"phase"`
	Mode           string `json:"mode"`
	PersonaID      string `json:"persona_id,omitempty"`
	AvatarID       string `json:"avatar_id,omitempty"`
	StartedAtMs    int64  `json:"started_at_ms"`
	EndedAtMs      int64  `json:"ended_at_ms"`
	DurationS      int64  `json:"duration_s"`
	RecordingState string `json:"recording_state"`
	ErrorCode      string `json:"error_code,omitempty"`
	UpdatedAtMs    int64  `json:"updated_at_ms"`
}

// SessionStore 场次生命周期持久化端口；内存实现用于无 PG 环境。
type SessionStore interface {
	Save(ctx context.Context, r SessionRecord) error
	// ListStale 返回 phase in starting/live/stopping 且 updated_at_ms < cutoff 的记录。
	ListStale(ctx context.Context, cutoffMs int64) ([]SessionRecord, error)
	// MarkCrashed 把场次标记为 error(crash_recovered)。
	MarkCrashed(ctx context.Context, id string) error
	// List 复盘用：按开始时间倒序分页，phase 为空表示不过滤。
	List(ctx context.Context, limit, offset int, phase string) ([]SessionRecord, error)
	// Count 复盘用：返回符合 phase 过滤的总条数（分页 total）。
	Count(ctx context.Context, phase string) (int, error)
	// GetRecord 复盘用：按 id 取单场。
	GetRecord(ctx context.Context, id string) (SessionRecord, bool, error)
}

// MemorySessionStore 进程内场次存储；重启即丢失，仅用于降级与测试。
type MemorySessionStore struct {
	mu   sync.Mutex
	rows map[string]SessionRecord
}

func NewMemorySessionStore() *MemorySessionStore {
	return &MemorySessionStore{rows: map[string]SessionRecord{}}
}

func (s *MemorySessionStore) Save(_ context.Context, r SessionRecord) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows[r.ID] = r
	return nil
}

func (s *MemorySessionStore) ListStale(_ context.Context, cutoffMs int64) ([]SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SessionRecord, 0)
	for _, r := range s.rows {
		if !stalePhase(r.Phase) || r.UpdatedAtMs >= cutoffMs {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

func (s *MemorySessionStore) MarkCrashed(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	if !ok {
		return nil
	}
	r.Phase = PhaseError
	r.ErrorCode = "crash_recovered"
	r.UpdatedAtMs = time.Now().UnixMilli()
	if r.EndedAtMs == 0 {
		r.EndedAtMs = r.UpdatedAtMs
	}
	s.rows[id] = r
	return nil
}

func (s *MemorySessionStore) List(_ context.Context, limit, offset int, phase string) ([]SessionRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]SessionRecord, 0, len(s.rows))
	for _, r := range s.rows {
		if phase != "" && string(r.Phase) != phase {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartedAtMs == out[j].StartedAtMs {
			return out[i].ID > out[j].ID
		}
		return out[i].StartedAtMs > out[j].StartedAtMs
	})
	if offset >= len(out) {
		return []SessionRecord{}, nil
	}
	out = out[offset:]
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func (s *MemorySessionStore) Count(_ context.Context, phase string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, r := range s.rows {
		if phase != "" && string(r.Phase) != phase {
			continue
		}
		n++
	}
	return n, nil
}

func (s *MemorySessionStore) GetRecord(_ context.Context, id string) (SessionRecord, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	return r, ok, nil
}

func stalePhase(p Phase) bool {
	return p == PhaseStarting || p == PhaseLive || p == PhaseStopping
}

// Get 仅供测试与诊断使用；不属于 SessionStore 端口契约。
func (s *MemorySessionStore) Get(id string) (SessionRecord, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.rows[id]
	return r, ok
}
