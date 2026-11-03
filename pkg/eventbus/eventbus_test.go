package eventbus

import (
	"context"
	"sync"
	"testing"

	"github.com/teamwork/digital-human/pkg/logx"
)

func TestSeqMonotonicAndGapFill(t *testing.T) {
	b := New(16)
	for i := 0; i < 3; i++ {
		b.Publish(context.Background(), "s1", "t", 1, map[string]any{"i": i})
	}
	if b.SeqFor("s1") != 3 {
		t.Fatalf("session seq=%d, want 3", b.SeqFor("s1"))
	}
	ch, cancel := b.Subscribe(1)
	defer cancel()
	if e := <-ch; e.Seq != 2 {
		t.Fatalf("first backlog seq=%d, want 2", e.Seq)
	}
	if e := <-ch; e.Seq != 3 {
		t.Fatalf("second backlog seq=%d, want 3", e.Seq)
	}
	b.Publish(context.Background(), "s1", "t", 1, nil)
	if e := <-ch; e.Seq != 4 {
		t.Fatalf("live seq=%d, want 4", e.Seq)
	}
}

func TestEventIDUnique(t *testing.T) {
	b := New(8)
	seen := map[string]bool{}
	for i := 0; i < 8; i++ {
		e := b.Publish(context.Background(), "s", "t", 0, nil)
		if seen[e.EventID] {
			t.Fatalf("duplicate event_id %s", e.EventID)
		}
		seen[e.EventID] = true
	}
}

func TestPublishCarriesRequestID(t *testing.T) {
	b := New(8)
	ctx := logx.WithRequestID(context.Background(), "rid-123")
	e := b.Publish(ctx, "s", "t", 0, nil)
	if e.RequestID != "rid-123" {
		t.Fatalf("request_id=%q, want rid-123", e.RequestID)
	}

	echo := b.Publish(context.Background(), "s", "t", 0, nil)
	if echo.RequestID != "" {
		t.Fatalf("without request id, want empty, got %q", echo.RequestID)
	}
}

// 换场次后 seq 从头开始，跨场次不串号。
func TestSeqRestartsPerSession(t *testing.T) {
	b := New(16)
	for i := 0; i < 3; i++ {
		b.Publish(context.Background(), "s1", "t", 1, nil)
	}
	for i := 0; i < 2; i++ {
		b.Publish(context.Background(), "s2", "t", 2, nil)
	}
	if got := b.SeqFor("s1"); got != 3 {
		t.Fatalf("s1 seq=%d want 3", got)
	}
	if got := b.SeqFor("s2"); got != 2 {
		t.Fatalf("s2 seq=%d want 2", got)
	}

	ch, cancel := b.SubscribeFor("s2", 0)
	defer cancel()
	for i := 0; i < 2; i++ {
		e := <-ch
		if e.SessionID != "s2" || e.Seq != int64(i+1) {
			t.Fatalf("s2 backlog[%d]=%+v", i, e)
		}
	}
}

// PublishScoped 必须一次填充当前场次与 epoch，业务侧不再手写 0。
func TestPublishScopedFillsSessionAndEpoch(t *testing.T) {
	b := New(16)
	b.SetScope(func() (string, int64) { return "s-1", 1750000000000 })
	e := b.PublishScoped(context.Background(), "song.playing", nil)
	if e.SessionID != "s-1" || e.Epoch != 1750000000000 || e.Seq != 1 {
		t.Fatalf("scoped event=%+v", e)
	}

	b.SetScope(func() (string, int64) { return "", 0 })
	e = b.PublishScoped(context.Background(), "song.playing", nil)
	if e.SessionID != "" || e.Epoch != 0 {
		t.Fatalf("no session scope must degrade: %+v", e)
	}
}

type recordingAppender struct {
	mu   sync.Mutex
	rows []Event
}

func (r *recordingAppender) Append(_ context.Context, e Event) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.rows = append(r.rows, e)
	return nil
}

func (r *recordingAppender) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.rows)
}

// 绑定 appender 后，普通 Publish 与业务 PublishScoped 都必须写入同一 sink（REST/WS 同源）。
func TestAppenderReceivesAllPublishes(t *testing.T) {
	b := New(16)
	ap := &recordingAppender{}
	b.SetAppender(ap)
	b.SetScope(func() (string, int64) { return "s-1", 7 })

	b.Publish(context.Background(), "s-1", "live.started", 7, nil)
	b.PublishScoped(context.Background(), "song.playing", nil)

	if ap.count() != 2 {
		t.Fatalf("appender 收到 %d 条，want 2", ap.count())
	}
	ap.mu.Lock()
	defer ap.mu.Unlock()
	if ap.rows[1].Type != "song.playing" || ap.rows[1].SessionID != "s-1" || ap.rows[1].Epoch != 7 {
		t.Fatalf("scoped 事件未按场次落 sink: %+v", ap.rows[1])
	}
}
