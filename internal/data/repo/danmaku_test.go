package repo

import (
	"context"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"

	"github.com/teamwork/digital-human/internal/biz/danmaku"
	"github.com/teamwork/digital-human/internal/biz/interaction"
	"github.com/teamwork/digital-human/internal/data/ent/danmakuevent"
	"github.com/teamwork/digital-human/internal/data/ent/enttest"
)

func TestEntDanmakuSinkPreservesIdentityAndDeduplicates(t *testing.T) {
	client := enttest.Open(t, dialect.SQLite, "file:"+uuid.NewString()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	sink := NewEntDanmakuSink(client)
	e := danmaku.Event{
		ID: "evt-stable", SessionID: "s1", Kind: "chat", UserID: "u1", UserName: "甲",
		Text: "点歌 晴天", Source: "douyin", SourceEventID: "source-1", Decision: "rules",
		OccurredAtMs: 1700, ReceivedAtMs: 1800, ExpiresAtMs: 1900, CreatedAtMs: 1800,
	}
	duplicate, err := sink.RecordEvent(context.Background(), e)
	if err != nil || duplicate {
		t.Fatalf("首次写入: duplicate=%v err=%v", duplicate, err)
	}
	duplicate, err = sink.RecordEvent(context.Background(), e)
	if err != nil || !duplicate {
		t.Fatalf("同源重放: duplicate=%v err=%v", duplicate, err)
	}
	rows, err := client.DanmakuEvent.Query().Where(danmakuevent.SourceEventID("source-1")).All(context.Background())
	if err != nil || len(rows) != 1 {
		t.Fatalf("同源事件只能一行: rows=%d err=%v", len(rows), err)
	}
	row := rows[0]
	if row.ID != e.ID || row.OccurredAtMs != 1700 || row.ReceivedAtMs != 1800 || row.ExpiresAtMs != 1900 {
		t.Fatalf("稳定 ID 或时间字段未持久化: %+v", row)
	}
}

func TestEntDanmakuSinkListsOnlyOrphanRuleChats(t *testing.T) {
	ctx := context.Background()
	client := enttest.Open(t, dialect.SQLite, "file:"+uuid.NewString()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { _ = client.Close() })
	sink := NewEntDanmakuSink(client)
	for _, e := range []danmaku.Event{
		{ID: "evt-orphan", SessionID: "s1", Kind: "chat", UserID: "u1", Text: "你好", Decision: "rules", ReceivedAtMs: 10, ExpiresAtMs: 20},
		{ID: "evt-gift", SessionID: "s1", Kind: "gift", UserID: "u2", Decision: "t0", ReceivedAtMs: 11},
	} {
		if _, err := sink.RecordEvent(ctx, e); err != nil {
			t.Fatal(err)
		}
	}
	orphans, err := sink.ListOrphanRuleChats(ctx)
	if err != nil || len(orphans) != 1 || orphans[0].ID != "evt-orphan" || orphans[0].ExpiresAtMs != 20 {
		t.Fatalf("初始孤儿扫描: rows=%+v err=%v", orphans, err)
	}
	if _, _, err := interaction.NewService(NewEntInteractionStore(client)).Decide(ctx, interaction.DecisionInput{
		SessionID: "s1", SourceEventRowID: "evt-orphan", Intent: interaction.IntentChatReply, Outcome: interaction.DecideSkipped,
	}); err != nil {
		t.Fatal(err)
	}
	orphans, err = sink.ListOrphanRuleChats(ctx)
	if err != nil || len(orphans) != 0 {
		t.Fatalf("已有 Decision 的事件不得重复恢复: rows=%+v err=%v", orphans, err)
	}
}
