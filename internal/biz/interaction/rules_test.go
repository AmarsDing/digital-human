package interaction

import (
	"context"
	"testing"
	"time"
)

func testRuleEngine() (*RuleEngine, *MemoryStore, *time.Time) {
	st := NewMemoryStore()
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	engine := NewRuleEngine(NewService(st), RulePolicy{
		RuleVersion: "rules-v1", Revision: "policy-1", LLMAvailable: true,
		Songs: map[string]string{"晴天": "sunny.mp3"},
		Actions: []ActionMapping{{
			Trigger: "跳舞", Instruction: "dance-01", Revision: "policy-1",
			Allowed: true, CapabilityStatus: "real",
		}},
	}, func() time.Time { return now })
	return engine, st, &now
}

func ruleEvent(id, user, text string) RuleEvent {
	return RuleEvent{
		EventRowID: id, SessionID: "s-rules", UserKey: user, Text: text,
		ReceivedAtMs: time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC).UnixMilli(),
	}
}

func TestRuleEngineDeterministicSongBeforeChat(t *testing.T) {
	engine, st, _ := testRuleEngine()
	d, effects, err := engine.Handle(context.Background(), ruleEvent("src-song", "u1", "点歌 晴天"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Intent != IntentSongRequest || d.Decision != DecideQueued {
		t.Fatalf("确定性点歌必须优先于普通聊天: %+v", d)
	}
	if len(effects) != 1 || effects[0].EffectType != EffectTypeSongRequest || effects[0].PayloadSafe["file"] != "sunny.mp3" {
		t.Fatalf("点歌 effect: %+v", effects)
	}
	if effects[0].PayloadSafe["source_event_row_id"] != "src-song" || effects[0].PayloadSafe["user_key"] != "u1" {
		t.Fatalf("点歌 effect 必须携带队列幂等元数据: %+v", effects[0].PayloadSafe)
	}
	if n, _ := st.CountDecisions(context.Background()); n != 1 {
		t.Fatalf("decision count=%d", n)
	}
}

func TestRuleEngineActionMappingAndCooldown(t *testing.T) {
	engine, _, now := testRuleEngine()
	d, effects, err := engine.Handle(context.Background(), ruleEvent("src-action-1", "u1", "跳舞"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Intent != IntentAction || d.Decision != DecideExecute || len(effects) != 1 || effects[0].EffectType != EffectTypeAction {
		t.Fatalf("白名单动作映射: d=%+v effects=%+v", d, effects)
	}
	if effects[0].PayloadSafe["instruction"] != "dance-01" || effects[0].PayloadSafe["user_key"] != "u1" {
		t.Fatalf("动作必须映射到 vendor instruction 并保留观众键: %+v", effects[0])
	}

	*now = now.Add(3 * time.Second)
	d, effects, err = engine.Handle(context.Background(), ruleEvent("src-action-2", "u1", "跳舞"))
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != DecideBlocked || d.ReasonCode != "action_user_cooldown" || len(effects) != 0 {
		t.Fatalf("动作单用户冷却: d=%+v effects=%+v", d, effects)
	}
}

func TestRuleEngineUserRateLimit(t *testing.T) {
	engine, _, _ := testRuleEngine()
	first, effects, err := engine.Handle(context.Background(), ruleEvent("src-chat-1", "u1", "你好"))
	if err != nil || first.Decision != DecideExecute || len(effects) != 1 {
		t.Fatalf("首条普通聊天: d=%+v effects=%+v err=%v", first, effects, err)
	}
	second, effects, err := engine.Handle(context.Background(), ruleEvent("src-chat-2", "u1", "刷屏"))
	if err != nil {
		t.Fatal(err)
	}
	if second.Decision != DecideBlocked || second.ReasonCode != "rate_limited" || len(effects) != 0 {
		t.Fatalf("2 秒内第二条必须限流: d=%+v effects=%+v", second, effects)
	}
}

func TestRuleEngineSevereLagDegradesButSongStillQueues(t *testing.T) {
	engine, _, _ := testRuleEngine()
	engine.SetHealth(Health{Known: true, AudioLagMs: 1600})

	chat, effects, err := engine.Handle(context.Background(), ruleEvent("src-lag-chat", "u-chat", "你好"))
	if err != nil || chat.Decision != DecideDegraded || chat.ReasonCode != "playback_lag" || len(effects) != 0 {
		t.Fatalf("严重卡顿普通聊天应降级: d=%+v effects=%+v err=%v", chat, effects, err)
	}
	act, effects, err := engine.Handle(context.Background(), ruleEvent("src-lag-action", "u-action", "跳舞"))
	if err != nil || act.Decision != DecideDegraded || len(effects) != 0 {
		t.Fatalf("严重卡顿动作应降级: d=%+v effects=%+v err=%v", act, effects, err)
	}
	song, effects, err := engine.Handle(context.Background(), ruleEvent("src-lag-song", "u-song", "点歌 晴天"))
	if err != nil || song.Decision != DecideQueued || len(effects) != 1 || effects[0].EffectType != EffectTypeSongRequest {
		t.Fatalf("严重卡顿点歌仍应只入队: d=%+v effects=%+v err=%v", song, effects, err)
	}
}

func TestRuleEngineExpiredEventSkipped(t *testing.T) {
	engine, _, now := testRuleEngine()
	ev := ruleEvent("src-expired", "u1", "点歌 晴天")
	ev.ExpiresAtMs = now.Add(-time.Millisecond).UnixMilli()
	d, effects, err := engine.Handle(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	if d.Decision != DecideSkipped || d.ReasonCode != "skipped_expired" || len(effects) != 0 {
		t.Fatalf("过期事件不得执行: d=%+v effects=%+v", d, effects)
	}
}

func TestOrphanDecisionInputReusesRulesWithoutWriting(t *testing.T) {
	engine, st, now := testRuleEngine()
	ev := ruleEvent("src-orphan-expired", "u1", "点歌 晴天")
	ev.ExpiresAtMs = now.Add(-time.Millisecond).UnixMilli()
	in := engine.DecisionInput(ev)
	if in.Outcome != DecideSkipped || in.ReasonCode != "skipped_expired" || len(in.Effects) != 0 {
		t.Fatalf("孤儿必须复用过期规则: %+v", in)
	}
	if n, _ := st.CountDecisions(context.Background()); n != 0 {
		t.Fatalf("生成 DecisionInput 不得提前写库: %d", n)
	}
	disp, err := NewDispatcher(st, time.Minute, time.Minute, func(context.Context) ([]DecisionInput, error) {
		return []DecisionInput{in}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := disp.Sweep(context.Background()); err != nil {
		t.Fatal(err)
	}
	got, ok, err := st.GetDecisionBySource(context.Background(), ev.EventRowID)
	if err != nil || !ok || got.Decision != DecideSkipped {
		t.Fatalf("Sweep 必须补建孤儿 Decision: got=%+v ok=%v err=%v", got, ok, err)
	}
}

func TestRuleEngineSameSourceKeepsFrozenDecision(t *testing.T) {
	engine, st, _ := testRuleEngine()
	ev := ruleEvent("src-frozen", "u1", "点歌 晴天")
	first, firstEffects, err := engine.Handle(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	engine.SetHealth(Health{Known: true, AudioLagMs: 2000})
	second, secondEffects, err := engine.Handle(context.Background(), ev)
	if err != nil {
		t.Fatal(err)
	}
	if first.ID != second.ID || first.Decision != second.Decision || len(firstEffects) != len(secondEffects) {
		t.Fatalf("同源重放不得按新策略另建决策: first=%+v/%+v second=%+v/%+v", first, firstEffects, second, secondEffects)
	}
	if n, _ := st.CountDecisions(context.Background()); n != 1 {
		t.Fatalf("同源只能一条 Decision: %d", n)
	}
}
