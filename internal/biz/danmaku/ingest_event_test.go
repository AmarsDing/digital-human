package danmaku

import (
	"context"
	"errors"
	"sync"
	"testing"
)

type recordingEventSink struct {
	mu     sync.Mutex
	events []Event
	err    error
}

func (s *recordingEventSink) Record(_ context.Context, e Event) error {
	if s.err != nil {
		return s.err
	}
	s.mu.Lock()
	s.events = append(s.events, e)
	s.mu.Unlock()
	return nil
}

func (s *recordingEventSink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.events)
}

func TestIngestEventStableIDPersistBeforeTaps(t *testing.T) {
	svc := NewService(ModeAuto)
	svc.BeginSession("s-stable")
	sink := &recordingEventSink{}
	svc.SetEventSink(sink)
	var kindTaps, ruleTaps int
	svc.SetKindTap(func(_ string, ev TapEvent) {
		kindTaps++
		if sink.count() != 1 {
			t.Errorf("KindTap 必须在原始事件提交后发布")
		}
		if ev.EventRowID == "" || ev.SourceEventID != "platform-1" {
			t.Errorf("KindTap 缺少稳定来源标识: %+v", ev)
		}
	})
	svc.SetRuleTap(func(_ context.Context, _ string, ev TapEvent) error {
		ruleTaps++
		if sink.count() == 0 {
			t.Error("RuleTap 必须在原始事件提交后发布")
		}
		return nil
	})

	in := Envelope{
		Kind: "gift", UserID: "u1", UserName: "甲", Text: "玫瑰 x1",
		Diamond: 1, Source: "douyin", SourceEventID: "platform-1",
	}
	first := svc.IngestEvent(context.Background(), in)
	second := svc.IngestEvent(context.Background(), in)
	if !first.OK || !second.OK || !second.Duplicate {
		t.Fatalf("同源重放结果: first=%+v second=%+v", first, second)
	}
	if first.Event.ID == "" || first.Event.ID != second.Event.ID {
		t.Fatalf("稳定事件 ID: %q vs %q", first.Event.ID, second.Event.ID)
	}
	if kindTaps != 1 || sink.count() != 1 {
		t.Fatalf("同源 gift 只能落库/分流一次: taps=%d rows=%d", kindTaps, sink.count())
	}

	chat := in
	chat.Kind = "chat"
	chat.Text = "你好"
	chat.SourceEventID = "platform-2"
	if got := svc.IngestEvent(context.Background(), chat); !got.OK {
		t.Fatalf("chat ingest: %+v", got)
	}
	if ruleTaps != 1 {
		t.Fatalf("chat 应进入规则入口一次: %d", ruleTaps)
	}
}

type durableRecordingEventSink struct {
	recordingEventSink
	seen map[string]Event
}

func (s *durableRecordingEventSink) RecordEvent(ctx context.Context, e Event) (bool, error) {
	key := e.Source + "\x00" + e.SourceEventID
	if previous, ok := s.seen[key]; ok {
		_ = previous
		return true, nil
	}
	if err := s.Record(ctx, e); err != nil {
		return false, err
	}
	if s.seen == nil {
		s.seen = map[string]Event{}
	}
	s.seen[key] = e
	return false, nil
}

func TestIngestEventDurableDuplicateAfterRestartDoesNotTap(t *testing.T) {
	sink := &durableRecordingEventSink{}
	in := Envelope{Kind: "chat", UserName: "甲", Text: "点歌 晴天", Source: "douyin", SourceEventID: "restart-1"}
	first := NewService(ModeAuto)
	first.BeginSession("s-restart")
	first.SetEventSink(sink)
	if got := first.IngestEvent(context.Background(), in); !got.OK {
		t.Fatalf("首次摄入失败: %+v", got)
	}

	restarted := NewService(ModeAuto)
	restarted.BeginSession("s-restart")
	restarted.SetEventSink(sink)
	taps := 0
	restarted.SetRuleTap(func(context.Context, string, TapEvent) error { taps++; return nil })
	got := restarted.IngestEvent(context.Background(), in)
	if !got.OK || !got.Duplicate || got.Event.ID == "" {
		t.Fatalf("重启后的同源重放必须识别为重复: %+v", got)
	}
	if taps != 0 || sink.count() != 1 {
		t.Fatalf("重启后的重放不得重复消费: taps=%d rows=%d", taps, sink.count())
	}
}

func TestIngestEventPersistenceFailureStopsPipeline(t *testing.T) {
	svc := NewService(ModeAuto)
	svc.BeginSession("s-persist")
	svc.SetEventSink(&recordingEventSink{err: errors.New("db down")})
	var taps int
	svc.SetKindTap(func(_ string, _ TapEvent) { taps++ })
	svc.SetRuleTap(func(_ context.Context, _ string, _ TapEvent) error {
		taps++
		return nil
	})

	got := svc.IngestEvent(context.Background(), Envelope{
		Kind: "chat", UserID: "u1", UserName: "甲", Text: "点歌 晴天",
		Source: "douyin", SourceEventID: "platform-db-down",
	})
	if got.OK || got.Reason != "persistence_failed" {
		t.Fatalf("落库失败必须显式返回: %+v", got)
	}
	if taps != 0 {
		t.Fatalf("落库失败不得发布 Tap: %d", taps)
	}
	if vip, normal := svc.PendingReads(); vip != 0 || normal != 0 {
		t.Fatalf("落库失败不得进入内存消费队列: vip=%d normal=%d", vip, normal)
	}
}
