package billing

import (
	"context"
	"testing"
)

func TestUpsertComputesSpentAndTotals(t *testing.T) {
	svc := NewService(NewMemoryStore())
	ctx := context.Background()
	l, err := svc.Upsert(ctx, Ledger{SessionID: "s1", AccountID: "acc1", CreditsStart: 100, CreditsEnd: 80, TokensIn: 10, TokensOut: 20})
	if err != nil {
		t.Fatal(err)
	}
	if l.CreditsSpent != 20 || l.TokensTotal != 30 || l.AccountID != "acc1" {
		t.Fatalf("ledger=%+v", l)
	}
	if l.Source != "manual" || l.CostSource != "unset" {
		t.Fatalf("source=%q cost_source=%q", l.Source, l.CostSource)
	}
	if _, err := svc.Upsert(ctx, Ledger{}); err == nil {
		t.Fatal("empty session should fail")
	}
}

func TestSummaryBySessionAndAccount(t *testing.T) {
	svc := NewService(NewMemoryStore())
	ctx := context.Background()
	_, _ = svc.Upsert(ctx, Ledger{SessionID: "s1", AccountID: "acc1", CreditsSpent: 20, Diamonds: 3, GiftCount: 1})
	_, _ = svc.Upsert(ctx, Ledger{SessionID: "s2", AccountID: "acc1", CreditsSpent: 5, Diamonds: 1})
	_, _ = svc.Upsert(ctx, Ledger{SessionID: "s3", AccountID: "acc2", CreditsSpent: 7, EstimatedCost: 1.5})

	bySession, err := svc.Summary(ctx, "session")
	if err != nil || len(bySession) != 3 {
		t.Fatalf("bySession=%+v err=%v", bySession, err)
	}
	if bySession[0].Key != "s1" || bySession[0].CreditsSpent != 20 {
		t.Fatalf("order=%+v", bySession)
	}
	byAccount, err := svc.Summary(ctx, "account")
	if err != nil {
		t.Fatal(err)
	}
	if len(byAccount) != 2 || byAccount[0].Key != "acc1" || byAccount[0].Sessions != 2 || byAccount[0].CreditsSpent != 25 {
		t.Fatalf("byAccount=%+v", byAccount)
	}
	if !byAccount[1].Estimated {
		t.Fatalf("acc2 should be estimated: %+v", byAccount[1])
	}
}
