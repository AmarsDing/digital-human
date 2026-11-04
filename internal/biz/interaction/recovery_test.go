package interaction

import (
	"context"
	"testing"
	"time"
)

func TestUnknownManualReconcileSuccessAndRequeue(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore()
	svc := NewService(st)
	disp, _ := NewDispatcher(st, time.Millisecond, time.Millisecond, nil)

	_, succeeded := decideOne(t, svc, "src-manual-success")
	_, _ = st.MarkDispatching(ctx, succeeded[0].ID, "dead", "dsp-dead-1", 1, 1)
	if err := disp.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if err := disp.Reconcile(ctx, succeeded[0].ID, ReconcileActionSucceeded, "manual-receipt", "operator confirmed"); err != nil {
		t.Fatal(err)
	}
	eff, _ := st.GetEffect(succeeded[0].ID)
	if eff.Status != EffectReconcileSucceeded || eff.ReconcileStatus != ReconcileResolved || eff.DownstreamReceiptID != "manual-receipt" || eff.ReconciledAtMs == 0 {
		t.Fatalf("人工确认成功状态: %+v", eff)
	}

	_, requeued := decideOne(t, svc, "src-manual-requeue")
	_, _ = st.MarkDispatching(ctx, requeued[0].ID, "dead", "dsp-dead-2", 1, 1)
	if err := disp.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if err := disp.Reconcile(ctx, requeued[0].ID, ReconcileActionRequeue, "", "operator confirmed not executed"); err != nil {
		t.Fatal(err)
	}
	eff, _ = st.GetEffect(requeued[0].ID)
	if eff.Status != EffectPending || eff.ReconcileStatus != ReconcileResolved || eff.LeaseOwner != "" || eff.ReconciledAtMs == 0 {
		t.Fatalf("人工确认未执行后显式重排队: %+v", eff)
	}
	called := 0
	disp.RegisterHandler(EffectTypeGiftThanks, func(context.Context, Effect) (string, error) {
		called++
		return "after-manual-requeue", nil
	})
	if ok, err := disp.DispatchOnce(ctx); err != nil || !ok || called != 1 {
		t.Fatalf("只有人工 requeue 后才能再次派发: ok=%v called=%d err=%v", ok, called, err)
	}

	_, abandoned := decideOne(t, svc, "src-manual-failed")
	_, _ = st.MarkDispatching(ctx, abandoned[0].ID, "dead", "dsp-dead-3", 1, 1)
	if err := disp.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	if err := disp.Reconcile(ctx, abandoned[0].ID, ReconcileActionFailed, "", "operator abandoned"); err != nil {
		t.Fatal(err)
	}
	eff, _ = st.GetEffect(abandoned[0].ID)
	if eff.Status != EffectReconcileFailed || eff.ReconcileStatus != ReconcileResolved || eff.TerminalReason != "operator abandoned" {
		t.Fatalf("人工放弃状态: %+v", eff)
	}
}

func TestMemoryStoreDecisionEffectConflictRollsBack(t *testing.T) {
	ctx := context.Background()
	st := NewMemoryStore()
	mk := func(did, eid, src string) (Decision, []Effect) {
		return Decision{
				ID: did, SessionID: "s1", SourceEventRowID: src,
				Intent: IntentChatReply, Decision: DecideExecute, CreatedAtMs: 1,
			}, []Effect{{
				ID: eid, DecisionID: did, SessionID: "s1", EffectType: EffectTypeChatReply,
				EffectIdempotencyKey: "shared-effect-key", Status: EffectPending,
				ReconcileStatus: ReconcileNone, CreatedAtMs: 1,
			}}
	}
	d1, e1 := mk("d-1", "e-1", "src-a")
	if _, _, _, err := st.TxCreateDecisionWithEffects(ctx, d1, e1); err != nil {
		t.Fatal(err)
	}
	d2, e2 := mk("d-2", "e-2", "src-b")
	if _, _, _, err := st.TxCreateDecisionWithEffects(ctx, d2, e2); err == nil {
		t.Fatal("Effect 幂等键冲突必须失败")
	}
	if _, ok, _ := st.GetDecisionBySource(ctx, "src-b"); ok {
		t.Fatal("Effect 写入失败不得残留 Decision")
	}
	if n, _ := st.CountDecisions(ctx); n != 1 {
		t.Fatalf("冲突后 Decision 数=%d", n)
	}
}
