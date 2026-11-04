package interaction

import (
	"context"
	"errors"
	"testing"
	"time"
)

func decideOne(t *testing.T, svc *Service, source string) (Decision, []Effect) {
	t.Helper()
	d, effs, err := svc.Decide(context.Background(), DecisionInput{
		SessionID: "s1", SourceEventRowID: source, Intent: "gift_thanks",
		Outcome: DecideExecute, ReasonCode: "gift_merged",
		PayloadSafe: map[string]any{"text": "谢谢"},
		Effects: []EffectInput{{
			EffectType: EffectTypeGiftThanks, TargetID: "tts", Seq: 1,
			PayloadSafe: map[string]any{"text": "谢谢"},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return d, effs
}

// TestDecideDuplicateSourceNotDouble 同事务唯一性：重复 source_event 不双 Decision（§5.2）。
func TestDecideDuplicateSourceNotDouble(t *testing.T) {
	st := NewMemoryStore()
	svc := NewService(st)
	d1, e1 := decideOne(t, svc, "src-1")
	d2, e2 := decideOne(t, svc, "src-1")
	if d1.ID != d2.ID {
		t.Fatalf("重复 source 必须返回同一 Decision: %s vs %s", d1.ID, d2.ID)
	}
	if len(e1) != 1 || len(e2) != 1 || e1[0].EffectIdempotencyKey != e2[0].EffectIdempotencyKey {
		t.Fatalf("effect 幂等键必须一致: %+v vs %+v", e1, e2)
	}
	if n, _ := st.CountDecisions(context.Background()); n != 1 {
		t.Fatalf("Decision 不得双建: %d", n)
	}
}

// TestDispatchSuccessReceipt dispatcher 派发成功写回执（§5.4 downstream_receipt_id）。
func TestDispatchSuccessReceipt(t *testing.T) {
	st := NewMemoryStore()
	svc := NewService(st)
	disp, err := NewDispatcher(st, 30*time.Second, 30*time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	called := 0
	disp.RegisterHandler(EffectTypeGiftThanks, func(_ context.Context, eff Effect) (string, error) {
		called++
		return "receipt-1", nil
	})
	_, effs := decideOne(t, svc, "src-dispatch")
	ok, err := disp.DispatchOnce(context.Background())
	if err != nil || !ok {
		t.Fatalf("dispatch: ok=%v err=%v", ok, err)
	}
	if called != 1 {
		t.Fatalf("handler 必须被调用一次: %d", called)
	}
	eff, _ := st.GetEffect(effs[0].ID)
	if eff.Status != EffectSucceeded || eff.DownstreamReceiptID != "receipt-1" || eff.FinishedAtMs == 0 {
		t.Fatalf("回执写状态失败: %+v", eff)
	}
	if eff.Attempt != 1 || eff.DispatchID == "" || eff.LeaseOwner == "" {
		t.Fatalf("lease/dispatch 骨架字段缺失: %+v", eff)
	}
}

// TestDispatchNoHandlerFailsClosed 无执行器的链路显式失败（不假装成功）。
func TestDispatchNoHandlerFailsClosed(t *testing.T) {
	st := NewMemoryStore()
	svc := NewService(st)
	disp, _ := NewDispatcher(st, 30*time.Second, 30*time.Second, nil)
	_, effs := decideOne(t, svc, "src-nohandler")
	if _, err := disp.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	eff, _ := st.GetEffect(effs[0].ID)
	if eff.Status != EffectFailed || eff.TerminalReason != "handler_missing" {
		t.Fatalf("无 handler 必须显式 failed: %+v", eff)
	}
}

// TestDispatchRetryThenSuccess 失败后退避重试，attempt 递增。
func TestDispatchRetryThenSuccess(t *testing.T) {
	st := NewMemoryStore()
	svc := NewService(st)
	disp, _ := NewDispatcher(st, 30*time.Second, 30*time.Second, nil)
	fail := true
	disp.RegisterHandler(EffectTypeGiftThanks, func(_ context.Context, eff Effect) (string, error) {
		if fail {
			fail = false
			return "", errors.New("boom")
		}
		return "r2", nil
	})
	_, effs := decideOne(t, svc, "src-retry")
	if _, err := disp.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	eff, _ := st.GetEffect(effs[0].ID)
	if eff.Status != EffectPending || eff.Attempt != 1 || eff.NextRetryAtMs == 0 {
		t.Fatalf("首次失败应 pending+退避: %+v", eff)
	}
	// 拨到退避后可领。
	st.mu.Lock()
	e := st.effects[effs[0].ID]
	e.NextRetryAtMs = 0
	st.effects[effs[0].ID] = e
	st.mu.Unlock()
	if _, err := disp.DispatchOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	eff, _ = st.GetEffect(effs[0].ID)
	if eff.Status != EffectSucceeded || eff.Attempt != 2 {
		t.Fatalf("重试成功 attempt=2: %+v", eff)
	}
}

// TestSweepLeaseExpiredUnknown 崩溃扫描：过期 dispatching 结果不可知，禁止自动重放。
func TestSweepLeaseExpiredUnknown(t *testing.T) {
	st := NewMemoryStore()
	svc := NewService(st)
	disp, _ := NewDispatcher(st, 30*time.Second, 30*time.Second, nil)
	disp.RegisterHandler(EffectTypeGiftThanks, func(_ context.Context, eff Effect) (string, error) {
		return "ok", nil
	})
	_, effs := decideOne(t, svc, "src-lease")
	// 手工置为 dispatching 且 lease 已过期（模拟派发中途崩溃）。
	if _, err := st.MarkDispatching(context.Background(), effs[0].ID, "dead-owner", "dsp-x", 1, 1); err != nil {
		t.Fatal(err)
	}
	if err := disp.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	eff, _ := st.GetEffect(effs[0].ID)
	if eff.Status != EffectUnknown || eff.ReconcileStatus != ReconcilePending {
		t.Fatalf("过期 lease 必须 unknown+reconcile_pending，禁止盲目重放: %+v", eff)
	}
	if ok, err := disp.DispatchOnce(context.Background()); err != nil || ok {
		t.Fatalf("unknown 不得被自动再次派发: ok=%v err=%v", ok, err)
	}
}

// TestSweepReceiptTimeoutUnknown 崩溃扫描：超回执期 dispatching 转 unknown +
// reconcile_pending（§5.5-3：绝不默认 succeeded）。
func TestSweepReceiptTimeoutUnknown(t *testing.T) {
	st := NewMemoryStore()
	svc := NewService(st)
	disp, _ := NewDispatcher(st, 30*time.Second, 30*time.Second, nil)
	_, effs := decideOne(t, svc, "src-timeout")
	// 手工置为 dispatching，dispatched_at 远早于回执期（lease 未过期，避免被 lease 回收抢先）。
	nowMs := time.Now().UnixMilli()
	if _, err := st.MarkDispatching(context.Background(), effs[0].ID, "owner", "dsp-y", nowMs+60000, nowMs-3600000); err != nil {
		t.Fatal(err)
	}
	if err := disp.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	eff, _ := st.GetEffect(effs[0].ID)
	if eff.Status != EffectUnknown || eff.ReconcileStatus != ReconcilePending {
		t.Fatalf("超回执期必须 unknown+reconcile_pending: %+v", eff)
	}
}

// TestSweepBackfillMissingDecision 崩溃扫描补建缺失 Decision（§5.5-1，source 提供方）。
func TestSweepBackfillMissingDecision(t *testing.T) {
	st := NewMemoryStore()
	disp, _ := NewDispatcher(st, 30*time.Second, 30*time.Second, func(ctx context.Context) ([]DecisionInput, error) {
		return []DecisionInput{{
			SessionID: "s9", SourceEventRowID: "orphan-1", Intent: "chat_reply",
			Outcome: DecideQueued, ReasonCode: "crash_backfill",
		}}, nil
	})
	if err := disp.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	d, ok, err := st.GetDecisionBySource(context.Background(), "orphan-1")
	if err != nil || !ok {
		t.Fatalf("补建缺失 Decision: ok=%v err=%v", ok, err)
	}
	if d.Decision != DecideQueued {
		t.Fatalf("补建 Decision 冻结属性: %+v", d)
	}
}

// TestGiftSpeakerHappyPath 礼物答谢链路：合并触发 → 落库 → dispatcher 调 Say → 回执。
func TestGiftSpeakerHappyPath(t *testing.T) {
	st := NewMemoryStore()
	svc := NewService(st)
	disp, _ := NewDispatcher(st, 30*time.Second, 30*time.Second, nil)
	inner := &captureSayer{}
	gs := NewGiftSpeaker(svc, disp, inner, func() (string, bool) { return "s1", true })
	gs.RegisterGiftHandler()
	if err := gs.Say(context.Background(), "谢谢A的墨镜", "gift"); err != nil {
		t.Fatal(err)
	}
	if len(inner.got) != 1 || inner.got[0] != "谢谢A的墨镜" {
		t.Fatalf("内层 Say 未被调用: %+v", inner.got)
	}
	d, ok, _ := st.GetDecisionBySource(context.Background(), SourceKey("gift_thanks", "s1", "谢谢A的墨镜"))
	if !ok || d.Intent != EffectTypeGiftThanks {
		t.Fatalf("Decision 落库: %+v ok=%v", d, ok)
	}
	// 重复答谢同一内容：不双建 Decision，但已 succeeded 的 effect 不会重放
	//（DispatchOnce 只领 pending）——幂等性成立。
	if err := gs.Say(context.Background(), "谢谢A的墨镜", "gift"); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountDecisions(context.Background()); n != 1 {
		t.Fatalf("重复合并触发不双 Decision: %d", n)
	}
	if len(inner.got) != 1 {
		t.Fatalf("已成功回执的 effect 不得重复 Say: %+v", inner.got)
	}
}

func TestGiftSpeakerSameTextDifferentBatchesAreDistinct(t *testing.T) {
	st := NewMemoryStore()
	svc := NewService(st)
	disp, _ := NewDispatcher(st, 30*time.Second, 30*time.Second, nil)
	inner := &captureSayer{}
	gs := NewGiftSpeaker(svc, disp, inner, func() (string, bool) { return "s1", true })
	gs.RegisterGiftHandler()
	if err := gs.SayGiftBatch(context.Background(), "谢谢甲的玫瑰", "batch-1"); err != nil {
		t.Fatal(err)
	}
	if err := gs.SayGiftBatch(context.Background(), "谢谢甲的玫瑰", "batch-2"); err != nil {
		t.Fatal(err)
	}
	if n, _ := st.CountDecisions(context.Background()); n != 2 {
		t.Fatalf("同文案不同批次必须各有 Decision: %d", n)
	}
	if len(inner.got) != 2 {
		t.Fatalf("不同批次不得被错误去重: %v", inner.got)
	}
	d, ok, _ := st.GetDecisionBySource(context.Background(), SourceKey("gift_thanks", "s1", "batch-1"))
	if !ok {
		t.Fatal("批次来源键未持久化")
	}
	for _, eff := range st.effects {
		if eff.DecisionID == d.ID && (eff.DispatchID == "" || eff.DownstreamReceiptID != "say:"+eff.DispatchID) {
			t.Fatalf("回执必须包含实际 dispatch ID: %+v", eff)
		}
	}
}

// TestGiftSpeakerNoSessionFailClosed 无场次时礼物答谢显式拒绝（不静默丢失、不落库）。
func TestGiftSpeakerNoSessionFailClosed(t *testing.T) {
	st := NewMemoryStore()
	svc := NewService(st)
	disp, _ := NewDispatcher(st, 30*time.Second, 30*time.Second, nil)
	gs := NewGiftSpeaker(svc, disp, &captureSayer{}, func() (string, bool) { return "", false })
	if err := gs.Say(context.Background(), "x", "gift"); err == nil {
		t.Fatal("无场次必须报错")
	}
	if n, _ := st.CountDecisions(context.Background()); n != 0 {
		t.Fatal("无场次不得落库")
	}
}

// TestGiftSpeakerPatronPassThrough 非礼物 mode（骨架）透传内层。
func TestGiftSpeakerPatronPassThrough(t *testing.T) {
	inner := &captureSayer{}
	gs := NewGiftSpeaker(nil, nil, inner, nil)
	if err := gs.Say(context.Background(), "欢迎舰长", "patron"); err != nil {
		t.Fatal(err)
	}
	if len(inner.got) != 1 {
		t.Fatal("patron 应透传内层")
	}
}

func TestDispatcherRunDispatchesPendingEffects(t *testing.T) {
	st := NewMemoryStore()
	svc := NewService(st)
	_, effects := decideOne(t, svc, "src-run")
	disp, _ := NewDispatcher(st, time.Second, time.Second, nil)
	handled := make(chan struct{}, 1)
	disp.RegisterHandler(EffectTypeGiftThanks, func(context.Context, Effect) (string, error) {
		handled <- struct{}{}
		return "receipt-run", nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	disp.Run(ctx, 5*time.Millisecond)
	select {
	case <-handled:
	case <-time.After(time.Second):
		t.Fatal("Run 只扫描崩溃状态，没有派发 pending effect")
	}
	deadline := time.Now().Add(time.Second)
	for {
		eff, _ := st.GetEffect(effects[0].ID)
		if eff.Status == EffectSucceeded && eff.DownstreamReceiptID == "receipt-run" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("后台派发未写回执: %+v", eff)
		}
		time.Sleep(time.Millisecond)
	}
}

type captureSayer struct{ got []string }

func (c *captureSayer) Say(_ context.Context, text string, _ string) error {
	c.got = append(c.got, text)
	return nil
}
