package service

import (
	"context"
	"testing"

	livev1 "github.com/teamwork/digital-human/api/gen/go/live/v1"
	"github.com/teamwork/digital-human/internal/biz/danmaku"
)

func TestDanmakuIngestMapsStableSourceAndTimes(t *testing.T) {
	svc := danmaku.NewService(danmaku.ModeAuto)
	sink := &serviceRecordingDanmakuSink{}
	svc.SetEventSink(sink)
	api := NewDanmakuService(svc)
	reply, err := api.Ingest(context.Background(), &livev1.IngestRequest{
		Kind: "chat", UserName: "甲", Text: "点歌 晴天", Source: "douyin",
		IdempotencyKey: "source-1", OccurredAtMs: 1700, ReceivedAtMs: 1800, ExpiresAtMs: 1900,
	})
	if err != nil || !reply.GetOk() {
		t.Fatalf("ingest: reply=%+v err=%v", reply, err)
	}
	if len(sink.events) != 1 {
		t.Fatalf("事件落库次数: %d", len(sink.events))
	}
	e := sink.events[0]
	if e.SourceEventID != "source-1" || e.OccurredAtMs != 1700 || e.ReceivedAtMs != 1800 || e.ExpiresAtMs != 1900 {
		t.Fatalf("稳定来源与时间字段丢失: %+v", e)
	}
}

type serviceRecordingDanmakuSink struct{ events []danmaku.Event }

func (s *serviceRecordingDanmakuSink) Record(_ context.Context, e danmaku.Event) error {
	s.events = append(s.events, e)
	return nil
}
