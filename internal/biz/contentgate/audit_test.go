package contentgate

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

type decisionRecorderStub struct{ records []AuditRecord }

func (r *decisionRecorderStub) RecordContentDecision(_ context.Context, record AuditRecord) error {
	r.records = append(r.records, record)
	return nil
}

func TestGateAuditsOnlyBlockedContentWithoutOriginal(t *testing.T) {
	recorder := &decisionRecorderStub{}
	gate := newTestGate(t, `layers:
  vulgar:
    decision: block
    words: [低俗词]
`, func(opts *Options) { opts.Recorder = recorder })
	const original = "唯一原文-低俗词-不得落审计"
	gate.Check(context.Background(), Input{Outlet: "live.say", SessionID: "s1", Text: "普通商品介绍"})
	if len(recorder.records) != 0 {
		t.Fatalf("allow 不得产生拦截审计: %+v", recorder.records)
	}

	got := gate.Check(context.Background(), Input{Outlet: "live.say", SessionID: "s1", Text: original})
	if got.Disposition != DispositionSpeakTemplate {
		t.Fatalf("首次低俗语义应口播模板: %+v", got)
	}
	if len(recorder.records) != 1 {
		t.Fatalf("want 1 audit record, got %+v", recorder.records)
	}
	record := recorder.records[0]
	if record.Outlet != "live.say" || record.Category != "vulgar" || record.Disposition != DispositionSpeakTemplate {
		t.Fatalf("审计元数据不完整: %+v", record)
	}
	raw, err := json.Marshal(recorder.records)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), original) || strings.Contains(string(raw), "低俗词") {
		t.Fatalf("审计不得包含原文或命中词: %s", raw)
	}
}
