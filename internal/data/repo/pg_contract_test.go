package repo

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/teamwork/digital-human/internal/biz/billing"
	"github.com/teamwork/digital-human/internal/biz/danmaku"
	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/ent/costledger"
	"github.com/teamwork/digital-human/internal/data/ent/danmakuevent"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

// pgGate 本机无 PG 时跳过：契约断言留待真机（CI/部署机）执行。
func pgGate(t *testing.T) *ent.Client {
	t.Helper()
	dsn := os.Getenv("DH_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("DH_TEST_PG_DSN 未设置：跳过 PG 契约测试，待真机验证")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatalf("pgx 驱动或 DSN 初始化失败: %v", err)
	}
	drv := entsql.OpenDB(dialect.Postgres, db)
	client := ent.NewClient(ent.Driver(drv))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := client.Schema.Create(ctx); err != nil {
		_ = client.Close()
		t.Fatalf("已显式提供 DH_TEST_PG_DSN，PG 连接或 Ent 自动迁移失败: %v", err)
	}
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// CostLedger Ent 版契约：增删改查 + session_id 唯一约束。
func TestEntCostStoreContract(t *testing.T) {
	client := pgGate(t)
	s := NewEntCostStore(client)
	ctx := context.Background()
	sid := "contract-" + time.Now().Format("150405.000000")
	defer func() { _, _ = client.CostLedger.Delete().Where(costledger.SessionID(sid)).Exec(ctx) }()

	if _, ok, err := s.Get(ctx, sid); err != nil || ok {
		t.Fatalf("空账本 Get: ok=%v err=%v", ok, err)
	}
	l := billing.Ledger{
		SessionID: sid, AccountID: "local", CreditsStart: 1000, CreditsEnd: 800,
		CreditsSpent: 200, TokensIn: 10, TokensOut: 20, TokensTotal: 30,
		Source: "vidu_poll", CostSource: "vidu_credits_api", StartedAtMs: 1, UpdatedAtMs: 2,
	}
	if err := s.Save(ctx, l); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(ctx, sid)
	if err != nil || !ok {
		t.Fatalf("Save 后 Get: ok=%v err=%v", ok, err)
	}
	if got != l {
		t.Fatalf("回读不一致:\n got=%+v\nwant=%+v", got, l)
	}
	// 同 session_id 再 Save 是更新而非新增（唯一约束）。
	l.CreditsEnd, l.CreditsSpent, l.UpdatedAtMs = 700, 300, 3
	if err := s.Save(ctx, l); err != nil {
		t.Fatal(err)
	}
	rows, err := s.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, r := range rows {
		if r.SessionID == sid {
			n++
			if r.CreditsEnd != 700 || r.CreditsSpent != 300 {
				t.Fatalf("更新未生效: %+v", r)
			}
		}
	}
	if n != 1 {
		t.Fatalf("session_id 唯一约束被破坏: %d 行", n)
	}
}

// 弹幕事件 Ent 版契约：写入可按场次读回、字段不丢。
func TestEntDanmakuSinkContract(t *testing.T) {
	client := pgGate(t)
	s := NewEntDanmakuSink(client)
	ctx := context.Background()
	sid := "contract-dm-" + time.Now().Format("150405.000000")
	defer func() { _, _ = client.DanmakuEvent.Delete().Where(danmakuevent.SessionID(sid)).Exec(ctx) }()

	e := danmaku.Event{
		SessionID: sid, Kind: "chat", UserID: "u1", UserName: "用户1",
		Text: "hello", Diamond: 3, Source: "collector", Decision: "confirm",
		CreatedAtMs: time.Now().UnixMilli(),
	}
	if err := s.Record(ctx, e); err != nil {
		t.Fatal(err)
	}
	row, err := client.DanmakuEvent.Query().Where(danmakuevent.SessionID(sid)).Only(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if row.Kind != "chat" || row.UserID != "u1" || row.Text != "hello" ||
		row.Diamond != 3 || row.Decision != "confirm" || row.Source != "collector" {
		t.Fatalf("回读字段不一致: %+v", row)
	}
}

// 幂等键 Ent 版契约：Put/Get 回读 + 首写胜出 + 到期删除并允许重建。
func TestEntIdempotencyStoreContract(t *testing.T) {
	client := pgGate(t)
	s := NewEntIdempotencyStore(client, time.Hour)
	ctx := context.Background()
	key := "start:contract-" + time.Now().Format("150405.000000")
	defer func() { _ = client.IdemKey.DeleteOneID(key).Exec(ctx) }()

	if _, ok, err := s.Get(ctx, key); err != nil || ok {
		t.Fatalf("空键 Get: ok=%v err=%v", ok, err)
	}
	want := live.Result{SessionID: "s-1", Phase: live.PhaseLive, LiveID: "lv-1", OK: true}
	if err := s.Put(ctx, key, want, 0); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get(ctx, key)
	if err != nil || !ok {
		t.Fatalf("Put 后 Get: ok=%v err=%v", ok, err)
	}
	if got != want {
		t.Fatalf("回读不一致: got=%+v want=%+v", got, want)
	}
	// 首写胜出：未过期同 key 不覆盖。
	if err := s.Put(ctx, key, live.Result{SessionID: "s-2"}, 0); err != nil {
		t.Fatal(err)
	}
	got, _, _ = s.Get(ctx, key)
	if got != want {
		t.Fatalf("首写胜出被破坏: got=%+v", got)
	}
	// 到期：手工置过期行 → Get 即删返回 miss → 可重建。
	past := time.Now().Add(-time.Hour).UnixMilli()
	if err := client.IdemKey.UpdateOneID(key).SetExpiresAtMs(past).Exec(ctx); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := s.Get(ctx, key); err != nil || ok {
		t.Fatalf("到期行应删除并 miss: ok=%v err=%v", ok, err)
	}
	rebuilt := live.Result{SessionID: "s-3", Phase: live.PhaseEnded, OK: true}
	if err := s.Put(ctx, key, rebuilt, 0); err != nil {
		t.Fatal(err)
	}
	got, ok, _ = s.Get(ctx, key)
	if !ok || got != rebuilt {
		t.Fatalf("到期重建失败: ok=%v got=%+v", ok, got)
	}
}

// PG 事务契约：回滚不落行，提交后可读回。
func TestEntPostgresTransactionContract(t *testing.T) {
	client := pgGate(t)
	ctx := context.Background()
	key := "tx:contract-" + time.Now().Format("150405.000000")
	defer func() { _ = client.IdemKey.DeleteOneID(key).Exec(ctx) }()

	create := func(tx *ent.Tx) error {
		return tx.IdemKey.Create().
			SetID(key).
			SetResult(map[string]any{"ok": true}).
			SetExpiresAtMs(time.Now().Add(time.Hour).UnixMilli()).
			SetCreatedAtMs(time.Now().UnixMilli()).
			Exec(ctx)
	}
	tx, err := client.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := create(tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	if _, err := client.IdemKey.Get(ctx, key); !ent.IsNotFound(err) {
		t.Fatalf("回滚后仍可读: %v", err)
	}

	tx, err = client.Tx(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := create(tx); err != nil {
		_ = tx.Rollback()
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	row, err := client.IdemKey.Get(ctx, key)
	if err != nil {
		t.Fatal(err)
	}
	if row.Result["ok"] != true {
		t.Fatalf("提交后回读不一致: %+v", row.Result)
	}
}

// Recover PG 路径：starting/live/stopping/ended 混合行 → 当前场次跳过，
// 其余（含刚更新的）全标 crash_recovered。本机无 PG 时 Skip（待真机演练）。
func TestManagerRecoverWithEntSessionStore(t *testing.T) {
	client := pgGate(t)
	ctx := context.Background()
	tag := time.Now().Format("150405.000000")
	ids := map[string]string{}
	for _, phase := range []string{"starting", "live", "stopping", "ended"} {
		id := "recover-" + phase + "-" + tag
		ids[phase] = id
		_, err := client.LiveSession.Create().
			SetID(id).
			SetPhase(phase).
			SetStartedAtMs(time.Now().UnixMilli()).
			SetUpdatedAtMs(time.Now().UnixMilli()).
			Save(ctx)
		if err != nil {
			t.Fatal(err)
		}
	}
	defer func() {
		for _, id := range ids {
			_ = client.LiveSession.DeleteOneID(id).Exec(ctx)
		}
	}()

	store := NewEntSessionStore(client)
	bus := eventbus.New(64)
	sink := NewEntEventSink(client)
	m := live.NewManagerWithStores(bus, sink, live.NewFakeController(slog.Default()), store, nil, nil)
	m.SetModePolicy(live.ModePolicy{AllowLocalPreview: true, EngineMode: "dev"})

	// Manager 开播一场：它是 Recover 时唯一真正活着的未完成场次，必须被跳过。
	if _, _, _, _, err := m.StartIdempotentWithMode(ctx, "", "p", "a", "", live.StartRequest{Mode: live.ModeLocalPreview}); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _, _ = m.Stop(ctx, "test_cleanup") }()
	defer func() { _ = client.LiveSession.DeleteOneID(m.Snapshot().SessionID).Exec(ctx) }()

	if err := m.Recover(ctx); err != nil {
		t.Fatal(err)
	}

	for _, phase := range []string{"starting", "live", "stopping"} {
		r, err := client.LiveSession.Get(ctx, ids[phase])
		if err != nil {
			t.Fatal(err)
		}
		if r.Phase != "error" || r.ErrorCode != "crash_recovered" {
			t.Fatalf("%s 场次应标 crash_recovered: phase=%s code=%s", phase, r.Phase, r.ErrorCode)
		}
	}
	// ended 不属于未完成，不得被标记。
	r, err := client.LiveSession.Get(ctx, ids["ended"])
	if err != nil {
		t.Fatal(err)
	}
	if r.Phase != "ended" {
		t.Fatalf("ended 场次被误标: phase=%s", r.Phase)
	}
	// 本进程当前场次（Manager.StartIdempotent 创建的 live 场）不得被标记。
	snap := m.Snapshot()
	cur, err := client.LiveSession.Get(ctx, snap.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	if cur.ErrorCode == "crash_recovered" {
		t.Fatal("当前场次被误标 crash_recovered")
	}
}
