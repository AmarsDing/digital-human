package repo

import (
	"context"
	"sync"
	"time"

	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

type sink interface {
	Append(context.Context, eventbus.Event) error
	List(context.Context, string, int64, int) ([]eventbus.Event, error)
}

// SwitchableSink 启动时用内存实现；PG 在运行期接上后原子切换为 Ent。
//
// 事件边界（最小 outbox）：
//   - 切换前写入内存的事件会在切换时一次性补写 Ent（按 seq 顺序，幂等：唯一约束冲突视为已写）。
//   - 切换后所有新事件只写 Ent；补写失败则停止并保留内存数据，重启后仍以 DB 为准。
//   - 内存实现有容量上限（默认 4096），超出上限的旧事件无法补写，这是降级期的已知丢失边界。
type SwitchableSink struct {
	mu  sync.RWMutex
	mem sink
	db  sink
}

func NewSwitchableSink(mem sink) *SwitchableSink {
	return &SwitchableSink{mem: mem}
}

func (s *SwitchableSink) SetEnt(client *ent.Client) {
	s.switchTo(NewEntEventSink(client))
}

// switchTo 原子切换并把降级期间的内存事件补写进新 sink（最小 outbox）。
// db 为 nil 时仅用于测试跳过补写。
func (s *SwitchableSink) switchTo(db sink) {
	s.mu.Lock()
	s.db = db
	s.mu.Unlock()
	if db == nil {
		return
	}
	s.backfill(db)
}

// backfill 一次性补写内存快照；失败的记录留在内存，不阻塞服务启动。
func (s *SwitchableSink) backfill(db sink) {
	all, ok := s.mem.(interface{ All() []eventbus.Event })
	if !ok {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	for _, e := range all.All() {
		if err := db.Append(ctx, e); err != nil {
			// (session_id, seq) / event_id 唯一约束说明该事件已写入，继续下一条。
			if isUniqueViolation(err) {
				continue
			}
			return
		}
	}
}

func (s *SwitchableSink) current() sink {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db != nil {
		return s.db
	}
	return s.mem
}

func (s *SwitchableSink) Append(ctx context.Context, e eventbus.Event) error {
	return s.current().Append(ctx, e)
}

func (s *SwitchableSink) List(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]eventbus.Event, error) {
	return s.current().List(ctx, sessionID, afterSeq, limit)
}
