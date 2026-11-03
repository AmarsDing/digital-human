package repo

import (
	"context"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/interaction"
)

// TestEntInteractionStoreSameTxUniqueness B3 DoD：Decision+EffectOutbox 同事务写入，
// 重复 source_event 不双 Decision（§5.2 UNIQUE(source_event_row_id)）。
func TestEntInteractionStoreSameTxUniqueness(t *testing.T) {
	ctx := context.Background()
	st := NewEntInteractionStore(newEntTestClient(t))

	in := interaction.Decision{
		ID: "d-1", SessionID: "s1", SourceEventRowID: "src-ent-1",
		Intent: "gift_thanks", Decision: interaction.DecideExecute,
		ReasonCode: "gift_merged", CreatedAtMs: 1000,
	}
	eff := interaction.Effect{
		ID: "e-1", DecisionID: "d-1", SessionID: "s1", EffectType: interaction.EffectTypeGiftThanks,
		TargetID: "tts", EffectSeq: 1,
		EffectIdempotencyKey: interaction.IdempotencyKey("s1", "d-1", interaction.EffectTypeGiftThanks, "tts", 1),
		Status:               interaction.EffectPending, MaxAttempts: 3, ReconcileStatus: interaction.ReconcileNone,
		CreatedAtMs: 1000,
	}
	d1, e1, created1, err := st.TxCreateDecisionWithEffects(ctx, in, []interaction.Effect{eff})
	if err != nil || !created1 {
		t.Fatalf("首次写入: created=%v err=%v", created1, err)
	}
	// 重复 source：同事务语义下不双建，返回既有。
	in2 := in
	in2.ID = "d-2"
	d2, e2, created2, err := st.TxCreateDecisionWithEffects(ctx, in2, []interaction.Effect{eff})
	if err != nil {
		t.Fatal(err)
	}
	if created2 || d2.ID != d1.ID || len(e2) != 1 || e2[0].EffectIdempotencyKey != e1[0].EffectIdempotencyKey {
		t.Fatalf("重复 source 不双建: created=%v d2=%s e2=%+v", created2, d2.ID, e2)
	}
	if n, _ := st.CountDecisions(ctx); n != 1 {
		t.Fatalf("Decision 不得双建: %d", n)
	}
}

// TestEntInteractionStoreIdempotencyKeyUnique effect_idempotency_key 唯一（§5.4）：
// 不同 Decision 但相同幂等键 → 第二条 Effect 写入失败且 Decision 回滚（同事务）。
func TestEntInteractionStoreIdempotencyKeyUnique(t *testing.T) {
	ctx := context.Background()
	st := NewEntInteractionStore(newEntTestClient(t))
	mk := func(did, eid, src string) (interaction.Decision, []interaction.Effect) {
		return interaction.Decision{
				ID: did, SessionID: "s1", SourceEventRowID: src,
				Intent: "gift_thanks", Decision: interaction.DecideExecute, CreatedAtMs: 1,
			}, []interaction.Effect{{
				ID: eid, DecisionID: did, SessionID: "s1", EffectType: interaction.EffectTypeGiftThanks,
				EffectIdempotencyKey: "s1:d-1:gift_thanks:tts:1", // 故意撞键
				Status:               interaction.EffectPending, ReconcileStatus: interaction.ReconcileNone, CreatedAtMs: 1,
			}}
	}
	d1, e1 := mk("d-1", "e-1", "src-a")
	if _, _, _, err := st.TxCreateDecisionWithEffects(ctx, d1, e1); err != nil {
		t.Fatal(err)
	}
	d2, e2 := mk("d-2", "e-2", "src-b")
	if _, _, _, err := st.TxCreateDecisionWithEffects(ctx, d2, e2); err == nil {
		t.Fatal("撞幂等键必须失败")
	}
	// 同事务：Decision d-2 也不得残留（回滚）。
	if _, ok, _ := st.GetDecisionBySource(ctx, "src-b"); ok {
		t.Fatal("Effect 失败必须回滚同事务的 Decision")
	}
	if n, _ := st.CountDecisions(ctx); n != 1 {
		t.Fatalf("只应有 1 条 Decision: %d", n)
	}
}

// TestEntInteractionStoreDispatchFlow Ent 后端完整派发流：claim→dispatching→succeeded 回执。
func TestEntInteractionStoreDispatchFlow(t *testing.T) {
	ctx := context.Background()
	st := NewEntInteractionStore(newEntTestClient(t))
	svc := interaction.NewService(st)
	disp, err := interaction.NewDispatcher(st, 60000, 60000, nil)
	if err != nil {
		t.Fatal(err)
	}
	disp.RegisterHandler(interaction.EffectTypeGiftThanks, func(_ context.Context, eff interaction.Effect) (string, error) {
		return "rcpt-ent", nil
	})
	_, effs, err := svc.Decide(ctx, interaction.DecisionInput{
		SessionID: "s1", SourceEventRowID: "src-flow", Intent: "gift_thanks",
		Outcome: interaction.DecideExecute,
		Effects: []interaction.EffectInput{{
			EffectType: interaction.EffectTypeGiftThanks, TargetID: "tts", Seq: 1,
			PayloadSafe: map[string]any{"text": "谢谢"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	ok, err := disp.DispatchOnce(ctx)
	if err != nil || !ok {
		t.Fatalf("dispatch: ok=%v err=%v", ok, err)
	}
	eff, found := st.GetEffect(effs[0].ID)
	if !found || eff.Status != interaction.EffectSucceeded || eff.DownstreamReceiptID != "rcpt-ent" {
		t.Fatalf("Ent 回执写状态: %+v", eff)
	}
}
