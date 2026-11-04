package audit

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/contentgate"
)

func TestContentDecisionAuditFieldsAndNoOriginal(t *testing.T) {
	svc := NewService(NewMemorySink(10))
	if err := svc.RecordContentDecision(context.Background(), contentgate.AuditRecord{
		Outlet: "koubo.tts", Category: "vulgar", Disposition: contentgate.DispositionSpeakTemplate,
	}); err != nil {
		t.Fatal(err)
	}
	rows, err := svc.ListContentDecisions(context.Background(), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 1 {
		t.Fatalf("want 1 content audit, got %+v", rows)
	}
	row := rows[0]
	if row.ID == "" || row.TsMs <= 0 || row.Outlet != "koubo.tts" || row.Category != "vulgar" || row.Disposition != "speak_template" {
		t.Fatalf("审计字段不完整: %+v", row)
	}
	raw, err := json.Marshal(row)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"text", "original", "低俗词", "唯一原文"} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("审计输出不得夹带原文或命中词 %q: %s", forbidden, raw)
		}
	}
}
