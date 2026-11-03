package live

import (
	"context"
	"sync"

	"github.com/teamwork/digital-human/pkg/eventbus"
)

// MemorySink 用于无 PG 环境；保留最近 cap 条，供 WS 补齐与 ListEvents。
type MemorySink struct {
	mu   sync.Mutex
	cap  int
	rows []eventbus.Event
}

func NewMemorySink(capacity int) *MemorySink {
	if capacity <= 0 {
		capacity = 4096
	}
	return &MemorySink{cap: capacity}
}

func (s *MemorySink) Append(_ context.Context, e eventbus.Event) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rows = append(s.rows, e)
	if len(s.rows) > s.cap {
		s.rows = s.rows[len(s.rows)-s.cap:]
	}
	return nil
}

// All 返回当前内存快照，用于切换到持久化实现时的一次性补写（最小 outbox）。
func (s *MemorySink) All() []eventbus.Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]eventbus.Event, len(s.rows))
	copy(out, s.rows)
	return out
}

func (s *MemorySink) List(_ context.Context, sessionID string, afterSeq int64, limit int) ([]eventbus.Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]eventbus.Event, 0, limit)
	for _, e := range s.rows {
		if sessionID != "" && e.SessionID != sessionID {
			continue
		}
		if e.Seq <= afterSeq {
			continue
		}
		out = append(out, e)
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}
