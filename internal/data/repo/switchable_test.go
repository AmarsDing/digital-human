package repo

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

type fakeEventSink struct {
	mu   sync.Mutex
	rows []eventbus.Event
	per  error
}

func (f *fakeEventSink) Append(_ context.Context, e eventbus.Event) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.per != nil {
		return f.per
	}
	f.rows = append(f.rows, e)
	return nil
}

func (f *fakeEventSink) List(context.Context, string, int64, int) ([]eventbus.Event, error) {
	return nil, nil
}

func (f *fakeEventSink) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.rows)
}

func ev(seq int64) eventbus.Event {
	return eventbus.Event{Seq: seq, SessionID: "s1", EventID: "e", Type: "song.playing"}
}

// 降级期写内存的事件，在切换 Ent 时必须补写一次（最小 outbox）。
func TestSwitchableSinkBackfillsMemoryOnSwitch(t *testing.T) {
	mem := live.NewMemorySink(8)
	for i := int64(1); i <= 3; i++ {
		if err := mem.Append(context.Background(), ev(i)); err != nil {
			t.Fatal(err)
		}
	}
	sw := NewSwitchableSink(mem)
	db := &fakeEventSink{}
	sw.switchTo(db)
	if got := db.count(); got != 3 {
		t.Fatalf("补写条数=%d want 3", got)
	}
	// 切换后的新事件只进 db，不重复补写旧事件。
	if err := sw.Append(context.Background(), ev(4)); err != nil {
		t.Fatal(err)
	}
	if db.count() != 4 {
		t.Fatalf("切换后写入条数=%d want 4", db.count())
	}
}

// 补写中途失败必须停止且不 panic，剩余事件留在内存（重启后以 DB 为准）。
func TestSwitchableSinkBackfillStopsOnError(t *testing.T) {
	mem := live.NewMemorySink(8)
	for i := int64(1); i <= 3; i++ {
		_ = mem.Append(context.Background(), ev(i))
	}
	sw := NewSwitchableSink(mem)
	db := &fakeEventSink{per: errors.New("db_down")}
	sw.switchTo(db)
	if db.count() != 0 {
		t.Fatalf("失败时不应写入任何事件: %d", db.count())
	}
	// 服务仍可用：切换后写入走 db（由上层决定是否降级）。
	if err := sw.Append(context.Background(), ev(4)); err == nil {
		t.Fatal("db 失败应透出错误")
	}
}
