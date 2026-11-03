package archive

import (
	"context"
	"strings"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/billing"
	"github.com/teamwork/digital-human/internal/biz/live"
)

func TestListDetailExport(t *testing.T) {
	ctx := context.Background()
	store := live.NewMemorySessionStore()
	_ = store.Save(ctx, live.SessionRecord{ID: "s1", Phase: live.PhaseEnded, Mode: "solo", StartedAtMs: 100, UpdatedAtMs: 100})
	_ = store.Save(ctx, live.SessionRecord{ID: "s2", Phase: live.PhaseError, Mode: "duo", StartedAtMs: 200, UpdatedAtMs: 200})
	bill := billing.NewService(billing.NewMemoryStore())
	_, _ = bill.Upsert(ctx, billing.Ledger{SessionID: "s1", CreditsStart: 100, CreditsEnd: 90, TokensIn: 5, TokensOut: 5})
	svc := NewService(store, nil, bill)

	rows, err := svc.List(ctx, 2, 0, "")
	if err != nil || len(rows) != 2 {
		t.Fatalf("rows=%+v err=%v", rows, err)
	}
	if rows[0].ID != "s2" {
		t.Fatalf("order=%+v", rows)
	}
	if rows[1].Cost == nil || rows[1].Cost.CreditsSpent != 10 {
		t.Fatalf("cost=%+v", rows[1].Cost)
	}
	rows, err = svc.List(ctx, 2, 0, "error")
	if err != nil || len(rows) != 1 || rows[0].ID != "s2" {
		t.Fatalf("phase filter=%+v err=%v", rows, err)
	}

	detail, err := svc.Detail(ctx, "s1", false)
	if err != nil || detail.Session.ID != "s1" || detail.Cost == nil {
		t.Fatalf("detail=%+v err=%v", detail, err)
	}
	if _, err := svc.Detail(ctx, "missing", false); err != ErrNotFound {
		t.Fatalf("err=%v", err)
	}

	raw, name, ctype, err := svc.Export(ctx, "s1", "json")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"session"`) || !strings.HasSuffix(name, ".json") || !strings.Contains(ctype, "json") {
		t.Fatalf("json export name=%q ctype=%q", name, ctype)
	}
	raw, name, ctype, err = svc.Export(ctx, "s1", "csv")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw), "seq,ts_ms,event_id") || !strings.HasSuffix(name, ".csv") {
		t.Fatalf("csv export=%q name=%q ctype=%q", string(raw), name, ctype)
	}
}
