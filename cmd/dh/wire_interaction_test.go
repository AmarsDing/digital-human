package main

import (
	"context"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	"github.com/teamwork/digital-human/internal/biz/chat"
	"github.com/teamwork/digital-human/internal/biz/danmaku"
	"github.com/teamwork/digital-human/internal/biz/interaction"
	"github.com/teamwork/digital-human/internal/biz/song"
	"github.com/teamwork/digital-human/internal/data/ent/enttest"
	"github.com/teamwork/digital-human/internal/data/repo"
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
	wireInteractionRules(dm, ix, chatter, speaker, nil, interaction.RulePolicy{
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
