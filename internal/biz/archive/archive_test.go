package archive

import (
	"context"
	"strings"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/avatar"
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

// TestDetailPersonaBrief A20：详情人设展示——无快照旧场次回落 ProfileResolver 读当前
// 档案名；快照冻结名优先；空 persona_id → nil；档案软删 → Deleted=true + 仅 ID。
func TestDetailPersonaBrief(t *testing.T) {
	ctx := context.Background()
	store := live.NewMemorySessionStore()
	_ = store.Save(ctx, live.SessionRecord{ID: "s1", Phase: live.PhaseEnded, PersonaID: "p1"})
	_ = store.Save(ctx, live.SessionRecord{ID: "s2", Phase: live.PhaseEnded, PersonaID: "p1",
		ConfigSnapshot: map[string]any{"v": 1, "persona_name": "冻结名"}})
	_ = store.Save(ctx, live.SessionRecord{ID: "s3", Phase: live.PhaseEnded})
	avatars := avatar.NewMemoryStore()
	if err := avatars.Create(ctx, avatar.Avatar{ID: "p1", Slug: "s1", Name: "小雅", VoiceID: "v"}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(store, nil, billing.NewService(billing.NewMemoryStore()))
	svc.SetPersonaResolver(avatar.NewService(avatars))

	// 无快照 → 回落当前档案名。
	d, err := svc.Detail(ctx, "s1", false)
	if err != nil || d.Persona == nil || d.Persona.Name != "小雅" || d.Persona.Deleted {
		t.Fatalf("s1 persona=%+v err=%v", d.Persona, err)
	}
	// 快照冻结名优先（不读档案）。
	d, err = svc.Detail(ctx, "s2", false)
	if err != nil || d.Persona == nil || d.Persona.Name != "冻结名" {
		t.Fatalf("s2 persona=%+v err=%v", d.Persona, err)
	}
	// 空 persona_id → nil。
	d, err = svc.Detail(ctx, "s3", false)
	if err != nil || d.Persona != nil {
		t.Fatalf("s3 persona=%+v err=%v, want nil", d.Persona, err)
	}
	// 软删 → Deleted=true，仅 ID 无 Name。
	if err := avatars.SoftDelete(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	d, err = svc.Detail(ctx, "s1", false)
	if err != nil || d.Persona == nil || d.Persona.ID != "p1" || !d.Persona.Deleted || d.Persona.Name != "" {
		t.Fatalf("软删后 persona=%+v err=%v", d.Persona, err)
	}
	// 快照场不受软删影响。
	d, err = svc.Detail(ctx, "s2", false)
	if err != nil || d.Persona == nil || d.Persona.Name != "冻结名" || d.Persona.Deleted {
		t.Fatalf("快照场 persona=%+v err=%v", d.Persona, err)
	}
}
