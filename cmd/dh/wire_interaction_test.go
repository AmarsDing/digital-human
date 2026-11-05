package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	"github.com/teamwork/digital-human/internal/biz/action"
	"github.com/teamwork/digital-human/internal/biz/chat"
	"github.com/teamwork/digital-human/internal/biz/danmaku"
	"github.com/teamwork/digital-human/internal/biz/interaction"
	"github.com/teamwork/digital-human/internal/biz/joke"
	"github.com/teamwork/digital-human/internal/biz/song"
	"github.com/teamwork/digital-human/internal/data/ent/enttest"
	"github.com/teamwork/digital-human/internal/data/repo"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

type ruleTestChatter struct{ texts []string }

func (s *ruleTestChatter) Chat(ctx context.Context, _ string, text string, sink chat.SentenceSink) (string, error) {
	s.texts = append(s.texts, text)
	if err := sink(ctx, "规则回复"); err != nil {
		return "", err
	}
	return "规则回复", nil
}

type ruleTestSpeaker struct{ texts []string }

func (s *ruleTestSpeaker) Say(_ context.Context, text string) error {
	s.texts = append(s.texts, text)
	return nil
}

func TestInteractionRuntimeRecoversPersistentOrphanChat(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, dialect.SQLite, "file:"+uuid.NewString()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	source := repo.NewEntDanmakuSink(client)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	if _, err := source.RecordEvent(ctx, danmaku.Event{
		ID: "evt-orphan-production", SessionID: "s1", Kind: "chat", UserID: "u1", Text: "点歌 晴天",
		Decision: "rules", ReceivedAtMs: now.Add(-time.Minute).UnixMilli(), ExpiresAtMs: now.Add(-time.Second).UnixMilli(),
	}); err != nil {
		t.Fatal(err)
	}
	store := repo.NewEntInteractionStore(client)
	ix := &interactionRuntime{svc: interaction.NewService(store), store: store}
	ix.chatSource.Store(source)
	ix.rules.Store(interaction.NewRuleEngine(ix.svc, interaction.RulePolicy{
		RuleVersion: "rules-v1", Revision: "policy-1", Songs: map[string]string{"晴天": "sunny.mp3"},
	}, func() time.Time { return now }))
	disp, err := interaction.NewDispatcher(store, time.Minute, time.Minute, ix.orphanChatDecisions)
	if err != nil {
		t.Fatal(err)
	}
	if err := disp.Sweep(ctx); err != nil {
		t.Fatal(err)
	}
	got, ok, err := store.GetDecisionBySource(ctx, "evt-orphan-production")
	if err != nil || !ok || got.Decision != interaction.DecideSkipped || got.ReasonCode != "skipped_expired" {
		t.Fatalf("生产孤儿恢复: got=%+v ok=%v err=%v", got, ok, err)
	}
}

func TestWireInteractionRulesTriggersJokeWithoutChat(t *testing.T) {
	store := interaction.NewMemoryStore()
	svc := interaction.NewService(store)
	dispatcher, err := interaction.NewDispatcher(store, time.Minute, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	ix := &interactionRuntime{svc: svc, disp: dispatcher}
	dm := danmaku.NewService(danmaku.ModeAuto)
	chatter := &ruleTestChatter{}
	speaker := &ruleTestSpeaker{}
	library := filepath.Join(t.TempDir(), "jokes.yaml")
	if err := os.WriteFile(library, []byte(`jokes:
  - id: wire-original
    text: 集成原创段子
    keywords: [讲个笑话]
    category: 搞笑
    source: 项目组原创
    license: 项目内部使用授权
    enabled: true
`), 0o600); err != nil {
		t.Fatal(err)
	}
	jokes := joke.NewService(joke.Options{Enabled: true, LibraryPath: library})
	wireInteractionRules(dm, ix, chatter, speaker, speaker, nil, interaction.RulePolicy{
		RuleVersion: "rules-v1", Revision: "rev-1", LLMAvailable: true, Jokes: jokes,
	}, func() (string, bool) { return "s1", true })

	first := dm.IngestEvent(context.Background(), danmaku.Envelope{
		Kind: "chat", UserID: "u1", Text: "讲个笑话", Source: "test", SourceEventID: "joke-source-1",
	})
	if !first.OK {
		t.Fatalf("首次段子弹幕失败: %+v", first)
	}
	if len(chatter.texts) != 0 || len(speaker.texts) != 1 || speaker.texts[0] != "集成原创段子" {
		t.Fatalf("段子必须走独立出口且不得进入 LLM: chatter=%v speaker=%v", chatter.texts, speaker.texts)
	}
	second := dm.IngestEvent(context.Background(), danmaku.Envelope{
		Kind: "chat", UserID: "u2", Text: "再讲个笑话", Source: "test", SourceEventID: "joke-source-2",
	})
	if !second.OK || len(speaker.texts) != 1 {
		t.Fatalf("同场同段子不得重复: result=%+v speaker=%v", second, speaker.texts)
	}
	effects, err := store.ListEffects(context.Background(), "")
	if err != nil || len(effects) != 1 || effects[0].EffectType != interaction.EffectTypeJoke || effects[0].Status != interaction.EffectSucceeded {
		t.Fatalf("joke effect 闭环: effects=%+v err=%v", effects, err)
	}
}

func TestRuleActionMappingsUsesSelectedLibraryAndFailsClosed(t *testing.T) {
	items := []action.Action{
		{ID: "a1", LibraryID: "lib-allowed", Name: "跳舞", Instruction: "dance-01"},
		{ID: "a2", LibraryID: "lib-other", Name: "挥手", Instruction: "wave-01"},
	}
	mappings := ruleActionMappings(items, "lib-allowed", "rev-1", "real")
	if len(mappings) != 2 {
		t.Fatalf("候选动作数量: %+v", mappings)
	}
	if !mappings[0].Allowed || mappings[0].Trigger != "跳舞" || mappings[0].Instruction != "dance-01" || mappings[0].Revision != "rev-1" || mappings[0].CapabilityStatus != "real" {
		t.Fatalf("选中库动作必须进入同 revision 白名单: %+v", mappings[0])
	}
	if mappings[1].Allowed {
		t.Fatalf("非选中库只能作为拒绝候选: %+v", mappings[1])
	}
	for _, mapping := range ruleActionMappings(items, "", "rev-1", "real") {
		if mapping.Allowed {
			t.Fatalf("未选动作库时白名单必须为空并默认拒绝: %+v", mapping)
		}
	}
}

func TestWireInteractionRulesActionWhitelist(t *testing.T) {
	ctx := context.Background()
	store := interaction.NewMemoryStore()
	svc := interaction.NewService(store)
	dispatcher, err := interaction.NewDispatcher(store, time.Minute, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	ix := &interactionRuntime{svc: svc, disp: dispatcher}
	dm := danmaku.NewService(danmaku.ModeAuto)
	chatter := &ruleTestChatter{}
	speaker := &ruleTestSpeaker{}
	sender := action.NewFakeSender()
	bus := eventbus.New(16)
	scheduler := action.NewScheduler(sender, bus, action.DefaultConfig())
	defer scheduler.Close()
	events, cancel := bus.Subscribe(0)
	defer cancel()
	wireInteractionRules(dm, ix, chatter, speaker, speaker, scheduler, interaction.RulePolicy{
		RuleVersion: "rules-v1", Revision: "rev-1", LLMAvailable: true,
		Actions: []interaction.ActionMapping{
			{Trigger: "跳舞", Instruction: "dance-01", Revision: "rev-1", Allowed: true, CapabilityStatus: "real"},
			{Trigger: "挥手", Instruction: "wave-01", Revision: "rev-1", Allowed: false, CapabilityStatus: "real"},
		},
	}, func() (string, bool) { return "s1", true })

	allowed := dm.IngestEvent(ctx, danmaku.Envelope{
		Kind: "chat", UserID: "u1", Text: "跳舞", Source: "test", SourceEventID: "action-allowed",
	})
	if !allowed.OK || len(sender.Sent()) != 1 || sender.Sent()[0] != "dance-01" {
		t.Fatalf("白名单动作必须经 Scheduler 执行: result=%+v sent=%v", allowed, sender.Sent())
	}
	select {
	case event := <-events:
		if event.Type != "action.sent" {
			t.Fatalf("白名单动作事件类型: %+v", event)
		}
	case <-time.After(time.Second):
		t.Fatal("白名单动作缺少真实 action.sent")
	}

	denied := dm.IngestEvent(ctx, danmaku.Envelope{
		Kind: "chat", UserID: "u2", Text: "挥手", Source: "test", SourceEventID: "action-denied",
	})
	if !denied.OK {
		t.Fatalf("白名单外动作应记录拒绝决策而不是 ingestion 失败: %+v", denied)
	}
	decision, ok, err := store.GetDecisionBySource(ctx, denied.Event.ID)
	if err != nil || !ok || decision.Intent != interaction.IntentAction || decision.Decision != interaction.DecideBlocked || decision.ReasonCode != "action_not_allowed" {
		t.Fatalf("白名单外动作必须明确拒绝: decision=%+v ok=%v err=%v", decision, ok, err)
	}
	if len(chatter.texts) != 0 || len(speaker.texts) != 0 || len(sender.Sent()) != 1 || scheduler.Status().Fired != 1 {
		t.Fatalf("拒绝动作不得进入聊天或执行: chatter=%v speaker=%v sent=%v status=%+v", chatter.texts, speaker.texts, sender.Sent(), scheduler.Status())
	}
	effects, err := store.ListEffects(ctx, "")
	if err != nil || len(effects) != 1 || effects[0].EffectType != interaction.EffectTypeAction || effects[0].Status != interaction.EffectSucceeded {
		t.Fatalf("拒绝动作不得新增执行 effect: effects=%+v err=%v", effects, err)
	}
	select {
	case event := <-events:
		if event.Type == "action.sent" {
			t.Fatalf("白名单外动作不得伪造 action.sent: %+v", event)
		}
	case <-time.After(20 * time.Millisecond):
	}
}

func TestAppRecoverSongRequestsMarksPlayingUnknown(t *testing.T) {
	ctx := context.Background()
	store := song.NewMemoryRequestStore(song.QueuePolicy{}, time.Now)
	req, _, err := store.Enqueue(ctx, song.Request{SessionID: "s1", SourceEventRowID: "app-recover", UserKey: "u1", File: "a.wav"})
	if err != nil {
		t.Fatal(err)
	}
	claimed, ok, err := store.ClaimNext(ctx, "play-before-crash")
	if err != nil || !ok {
		t.Fatalf("准备崩溃前 playing: ok=%v err=%v", ok, err)
	}
	app := App{SongScheduler: song.NewScheduler(store, nil, time.Now)}
	if err := app.recoverSongRequests(ctx); err != nil {
		t.Fatal(err)
	}
	got, _, err := store.Get(ctx, req.ID)
	if err != nil || got.Status != song.RequestUnknown || got.PlayID != claimed.PlayID {
		t.Fatalf("启动恢复必须保留现场并转 unknown: got=%+v err=%v", got, err)
	}
}

func TestWireInteractionRulesConsumesStableChatOnce(t *testing.T) {
	store := interaction.NewMemoryStore()
	svc := interaction.NewService(store)
	dispatcher, err := interaction.NewDispatcher(store, time.Minute, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	ix := &interactionRuntime{svc: svc, disp: dispatcher}
	dm := danmaku.NewService(danmaku.ModeAuto)
	chatter := &ruleTestChatter{}
	speaker := &ruleTestSpeaker{}
	wireInteractionRules(dm, ix, chatter, speaker, nil, nil, interaction.RulePolicy{
		RuleVersion: "rules-v1", Revision: "rev-1", LLMAvailable: true,
	}, func() (string, bool) { return "s1", true })

	envelope := danmaku.Envelope{
		Kind: "chat", UserID: "u1", UserName: "观众", Text: "你好", Source: "test", SourceEventID: "source-1",
	}
	first := dm.IngestEvent(context.Background(), envelope)
	if !first.OK {
		t.Fatalf("首次弹幕失败: %+v", first)
	}
	if len(chatter.texts) != 1 || len(speaker.texts) != 1 {
		t.Fatalf("规则聊天未闭环: chatter=%v speaker=%v", chatter.texts, speaker.texts)
	}
	second := dm.IngestEvent(context.Background(), envelope)
	if !second.OK || !second.Duplicate || len(chatter.texts) != 1 {
		t.Fatalf("稳定来源事件不得重复消费: %+v chatter=%v", second, chatter.texts)
	}
	effects, err := store.ListEffects(context.Background(), "")
	if err != nil || len(effects) != 1 || effects[0].Status != interaction.EffectSucceeded {
		t.Fatalf("chat_reply 回执: effects=%+v err=%v", effects, err)
	}
}
