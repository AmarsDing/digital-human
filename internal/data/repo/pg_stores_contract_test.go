package repo

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/biz/patron"
	"github.com/teamwork/digital-human/internal/biz/settings"
	"github.com/teamwork/digital-human/internal/biz/vidukeys"
	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/ent/auditlog"
	"github.com/teamwork/digital-human/internal/data/ent/livesession"
	entpatron "github.com/teamwork/digital-human/internal/data/ent/patron"
	"github.com/teamwork/digital-human/internal/data/ent/sessionevent"
	"github.com/teamwork/digital-human/internal/data/ent/setting"
	"github.com/teamwork/digital-human/internal/data/ent/vidukey"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

func pgSimpleID(kind string) string {
	return "pg-simple-" + kind + "-" + uuid.NewString()
}

func TestPGSimpleEntSessionStore(t *testing.T) {
	client := pgGate(t)
	store := NewEntSessionStore(client)
	ctx := context.Background()
	id := pgSimpleID("session")
	duplicateLiveIDSession := pgSimpleID("session-duplicate")
	liveID := pgSimpleID("live")
	marker := pgSimpleID("snapshot")
	// live.SessionStore 没有 Delete；清理只按本次主键执行 Ent Delete，不清表。
	t.Cleanup(func() {
		_, _ = client.LiveSession.Delete().
			Where(livesession.IDIn(id, duplicateLiveIDSession)).
			Exec(context.Background())
	})

	now := time.Now().UnixMilli()
	r := live.SessionRecord{
		ID: id, LiveID: liveID, Phase: live.PhaseStarting, Mode: "managed",
		PersonaID: pgSimpleID("persona"), AvatarID: pgSimpleID("avatar"),
		StartedAtMs: time.Now().Add(time.Hour).UnixMilli(), UpdatedAtMs: now,
		RecordingState: "none", ConfigSnapshot: map[string]any{"marker": marker},
	}
	if _, ok, err := store.GetRecord(ctx, id); err != nil || ok {
		t.Fatalf("写入前 GetRecord: ok=%v err=%v", ok, err)
	}
	liveBefore, err := store.Count(ctx, string(live.PhaseLive))
	if err != nil {
		t.Fatal(err)
	}
	if err := store.Save(ctx, r); err != nil {
		t.Fatalf("Save create: %v", err)
	}

	// 同主键再次 Save 必须走更新而非新增；nil 快照不得清除原快照。
	r.Phase = live.PhaseLive
	r.DurationS = 12
	r.UpdatedAtMs = now + 10
	r.ConfigSnapshot = nil
	if err := store.Save(ctx, r); err != nil {
		t.Fatalf("Save update: %v", err)
	}
	got, ok, err := store.GetRecord(ctx, id)
	if err != nil || !ok {
		t.Fatalf("Save 后 GetRecord: ok=%v err=%v", ok, err)
	}
	if got.ID != r.ID || got.LiveID != liveID || got.Phase != live.PhaseLive ||
		got.DurationS != 12 || got.ConfigSnapshot["marker"] != marker {
		t.Fatalf("更新/回读不一致: got=%+v want=%+v", got, r)
	}
	if n, err := client.LiveSession.Query().Where(livesession.ID(id)).Count(ctx); err != nil || n != 1 {
		t.Fatalf("同主键 Save 应只有一行: n=%d err=%v", n, err)
	}
	liveAfter, err := store.Count(ctx, string(live.PhaseLive))
	if err != nil || liveAfter != liveBefore+1 {
		t.Fatalf("Count(live): before=%d after=%d err=%v", liveBefore, liveAfter, err)
	}

	rows, err := store.List(ctx, 200, 0, string(live.PhaseLive))
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.ID == id {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("List(live) 未返回本次场次 %s", id)
	}
	stale, err := store.ListStale(ctx, r.UpdatedAtMs+1)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, row := range stale {
		if row.ID == id {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("ListStale 未返回过期 live 场次 %s", id)
	}

	// live_id 有独立唯一索引；不同 session 主键也不能复用同一 live_id。
	duplicate := r
	duplicate.ID = duplicateLiveIDSession
	duplicate.ConfigSnapshot = map[string]any{"marker": marker}
	if err := store.Save(ctx, duplicate); !isUniqueViolation(err) {
		t.Fatalf("重复 live_id 应触发唯一约束: %v", err)
	}

	if err := store.MarkCrashed(ctx, id); err != nil {
		t.Fatalf("MarkCrashed: %v", err)
	}
	got, ok, err = store.GetRecord(ctx, id)
	if err != nil || !ok || got.Phase != live.PhaseError || got.ErrorCode != "crash_recovered" || got.EndedAtMs == 0 {
		t.Fatalf("MarkCrashed 回读: got=%+v ok=%v err=%v", got, ok, err)
	}
	if err := store.MarkCrashed(ctx, pgSimpleID("session-missing")); err != nil {
		t.Fatalf("MarkCrashed 不存在的主键应幂等: %v", err)
	}
}

func TestPGSimpleEntSettingStore(t *testing.T) {
	client := pgGate(t)
	store := NewEntSettingStore(client)
	ctx := context.Background()
	groups := settings.DefaultGroups()
	ids := make([]string, 0, len(groups)+1)
	for _, g := range groups {
		ids = append(ids, g.ID)
	}
	ids = append(ids, revisionRowID)
	before, err := client.Setting.Query().Where(setting.IDIn(ids...)).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	snapshot := make(map[string]*ent.Setting, len(before))
	for _, row := range before {
		snapshot[row.ID] = row
	}
	// settings.Store 没有 Delete，且 API 固定写 13 个组 ID 与 __revision，不能给这些行改名。
	// 测试标记放在 value 中；清理逐行恢复原值，仅删除本次新建行，绝不清空 settings 表。
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		tx, err := client.Tx(cleanupCtx)
		if err != nil {
			t.Errorf("设置清理开启事务: %v", err)
			return
		}
		for _, id := range ids {
			if old := snapshot[id]; old != nil {
				err = tx.Setting.UpdateOneID(id).
					SetValueJSON(old.ValueJSON).
					SetUpdatedAtMs(old.UpdatedAtMs).
					SetUpdatedBy(old.UpdatedBy).
					Exec(cleanupCtx)
			} else {
				err = tx.Setting.DeleteOneID(id).Exec(cleanupCtx)
				if ent.IsNotFound(err) {
					err = nil
				}
			}
			if err != nil {
				_ = tx.Rollback()
				t.Errorf("设置清理 %s: %v", id, err)
				return
			}
		}
		if err := tx.Commit(); err != nil {
			t.Errorf("设置清理提交: %v", err)
		}
	})

	_, baseRevision, err := store.Load(ctx)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	marker := pgSimpleID("setting")
	for i := range groups {
		if groups[i].ID == "voice" {
			groups[i].Values["voice_id"] = marker
		}
	}
	next, err := store.Save(ctx, groups, baseRevision)
	if err != nil || next != baseRevision+1 {
		t.Fatalf("Save create/upsert: next=%d base=%d err=%v", next, baseRevision, err)
	}

	// 同组 ID 再 Save 是更新路径；主键唯一约束不得产生重复组行。
	for i := range groups {
		if groups[i].ID == "voice" {
			groups[i].Values["voice_id"] = marker + "-updated"
		}
	}
	next, err = store.Save(ctx, groups, next)
	if err != nil || next != baseRevision+2 {
		t.Fatalf("Save update: next=%d err=%v", next, err)
	}
	loaded, revision, err := store.Load(ctx)
	if err != nil || revision != next {
		t.Fatalf("Save 后 Load: revision=%d next=%d err=%v", revision, next, err)
	}
	found := false
	for _, g := range loaded {
		if g.ID == "voice" {
			found = true
			if g.Values["voice_id"] != marker+"-updated" {
				t.Fatalf("voice 更新未生效: %+v", g.Values)
			}
		}
	}
	if !found {
		t.Fatal("Load 缺少 voice 组")
	}
	if n, err := client.Setting.Query().Where(setting.ID("voice")).Count(ctx); err != nil || n != 1 {
		t.Fatalf("voice 组应只有一行: n=%d err=%v", n, err)
	}
	// 直接重复插入固定组主键，验证 schema 的唯一约束本身也由真实 PG 执行。
	if _, err := client.Setting.Create().
		SetID("voice").
		SetValueJSON(map[string]any{"voice_id": marker}).
		Save(ctx); !isUniqueViolation(err) {
		t.Fatalf("重复 setting.id 应触发唯一约束: %v", err)
	}
	if _, err := store.Save(ctx, groups, baseRevision); !errors.Is(err, settings.ErrRevisionConflict) {
		t.Fatalf("过期 baseRevision 应冲突: %v", err)
	}
}

func TestPGSimpleEntPatronStore(t *testing.T) {
	client := pgGate(t)
	store := NewEntPatronStore(client)
	ctx := context.Background()
	baselineRows, err := client.Patron.Query().All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	baseline := make([]patron.Entry, 0, len(baselineRows))
	for _, row := range baselineRows {
		baseline = append(baseline, patron.Entry{
			Nick: row.Nick, UID: row.UID, Alias: row.Alias, Note: row.Note, Since: row.Since,
		})
	}
	a := patron.Entry{
		Nick: pgSimpleID("patron-a"), UID: pgSimpleID("patron-uid-a"), Alias: "A",
		Note: "first", Since: "2026-11-04",
	}
	b := patron.Entry{Nick: pgSimpleID("patron-b"), UID: pgSimpleID("patron-uid-b"), Alias: "B"}
	// patron.Store 没有 Delete；Replace 本身是全量替换 API，因此每次调用都携带既有名单，
	// 避免契约测试抹掉非测试数据。清理仅删本次两个主键，并恢复既有行的原始字段/时间。
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = client.Patron.Delete().Where(entpatron.IDIn(a.Nick, b.Nick)).Exec(cleanupCtx)
		for _, old := range baselineRows {
			if err := client.Patron.UpdateOneID(old.ID).
				SetNick(old.Nick).SetUID(old.UID).SetAlias(old.Alias).SetNote(old.Note).
				SetSince(old.Since).SetCreatedAtMs(old.CreatedAtMs).Exec(cleanupCtx); err != nil {
				t.Errorf("贵宾名单清理恢复 %s: %v", old.ID, err)
			}
		}
	})

	withBaseline := func(items ...patron.Entry) []patron.Entry {
		out := append([]patron.Entry(nil), baseline...)
		return append(out, items...)
	}
	if err := store.Replace(ctx, withBaseline(a, b)); err != nil {
		t.Fatalf("Replace create: %v", err)
	}
	rows, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	foundA, foundB := false, false
	for _, row := range rows {
		if row.Nick == a.Nick {
			foundA = true
			if row != a {
				t.Fatalf("贵宾字段回读不一致: got=%+v want=%+v", row, a)
			}
		}
		if row.Nick == b.Nick {
			foundB = true
		}
	}
	if !foundA || !foundB {
		t.Fatalf("Replace 后 List 缺少本次数据: A=%v B=%v", foundA, foundB)
	}

	// 第二次 Replace 覆盖更新 A，并通过全量替换删除 B；既有名单仍随调用保留。
	a.Alias = "A-updated"
	a.Note = "updated"
	if err := store.Replace(ctx, withBaseline(a)); err != nil {
		t.Fatalf("Replace update/delete: %v", err)
	}
	gotA, err := client.Patron.Get(ctx, a.Nick)
	if err != nil || gotA.Alias != "A-updated" || gotA.Note != "updated" {
		t.Fatalf("Replace 更新回读: got=%+v err=%v", gotA, err)
	}
	if _, err := client.Patron.Get(ctx, b.Nick); !ent.IsNotFound(err) {
		t.Fatalf("Replace 后 B 应被删除: %v", err)
	}

	// id/nick 均唯一；同一名单重复写入必须失败且事务回滚，不能留下半份替换结果。
	if err := store.Replace(ctx, withBaseline(a, a)); !isUniqueViolation(err) {
		t.Fatalf("重复 nick/id 应触发唯一约束: %v", err)
	}
	gotA, err = client.Patron.Get(ctx, a.Nick)
	if err != nil || gotA.Alias != "A-updated" {
		t.Fatalf("重复 Replace 后原行应保留: got=%+v err=%v", gotA, err)
	}
}

func TestPGSimpleEntViduKeyStore(t *testing.T) {
	client := pgGate(t)
	store := NewEntViduKeyStore(client)
	ctx := context.Background()
	activeBefore, err := client.ViduKey.Query().Where(vidukey.IsActive(true)).All(ctx)
	if err != nil {
		t.Fatal(err)
	}
	activeIDs := make([]string, 0, len(activeBefore))
	for _, row := range activeBefore {
		activeIDs = append(activeIDs, row.ID)
	}
	k1 := vidukeys.StoredKey{
		ID: pgSimpleID("vidu-key-a"), Cipher: []byte(pgSimpleID("cipher-a")),
		Tail8: uuid.NewString()[:8], AddedAtMs: time.Now().UnixMilli(),
	}
	k2 := vidukeys.StoredKey{
		ID: pgSimpleID("vidu-key-b"), Cipher: []byte(pgSimpleID("cipher-b")),
		Tail8: uuid.NewString()[:8], AddedAtMs: k1.AddedAtMs + 1,
	}
	// 清理只删本次主键；SetActive 会清全局 active，故恢复测试前的 active 行。
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		_, _ = client.ViduKey.Delete().Where(vidukey.IDIn(k1.ID, k2.ID)).Exec(cleanupCtx)
		if len(activeIDs) > 0 {
			_, _ = client.ViduKey.Update().SetIsActive(true).
				Where(vidukey.IDIn(activeIDs...)).Save(cleanupCtx)
		}
	})

	if err := store.Add(ctx, k1); err != nil {
		t.Fatalf("Add k1: %v", err)
	}
	if err := store.Add(ctx, k2); err != nil {
		t.Fatalf("Add k2: %v", err)
	}
	rows, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]vidukeys.StoredKey{}
	for _, row := range rows {
		if row.ID == k1.ID || row.ID == k2.ID {
			seen[row.ID] = row
		}
	}
	if len(seen) != 2 || !bytes.Equal(seen[k1.ID].Cipher, k1.Cipher) ||
		seen[k1.ID].Tail8 != k1.Tail8 || seen[k2.ID].AddedAtMs != k2.AddedAtMs {
		t.Fatalf("Add/List 回读不一致: %+v", seen)
	}
	// id 有唯一约束；tail8 在 schema 中只是普通检索列，无唯一约束，不能断言重复 tail8 报错。
	duplicate := k1
	duplicate.Tail8 = uuid.NewString()[:8]
	if err := store.Add(ctx, duplicate); !isUniqueViolation(err) {
		t.Fatalf("重复 ViduKey.id 应触发唯一约束: %v", err)
	}

	if err := store.SetActive(ctx, k1.Tail8); err != nil {
		t.Fatalf("SetActive k1: %v", err)
	}
	row1, err := client.ViduKey.Get(ctx, k1.ID)
	if err != nil || !row1.IsActive {
		t.Fatalf("k1 应 active: got=%+v err=%v", row1, err)
	}
	if err := store.SetActive(ctx, k2.Tail8); err != nil {
		t.Fatalf("SetActive k2: %v", err)
	}
	row1, err = client.ViduKey.Get(ctx, k1.ID)
	if err != nil || row1.IsActive {
		t.Fatalf("切换后 k1 应 inactive: got=%+v err=%v", row1, err)
	}
	row2, err := client.ViduKey.Get(ctx, k2.ID)
	if err != nil || !row2.IsActive {
		t.Fatalf("切换后 k2 应 active: got=%+v err=%v", row2, err)
	}
	if err := store.SetActive(ctx, ""); err != nil {
		t.Fatalf("SetActive 清空: %v", err)
	}
	if err := store.SetActive(ctx, pgSimpleID("missing-tail")); !errors.Is(err, vidukeys.ErrNotFound) {
		t.Fatalf("激活不存在 tail8: %v", err)
	}

	if err := store.Delete(ctx, k2.Tail8); err != nil {
		t.Fatalf("Delete k2: %v", err)
	}
	if _, err := client.ViduKey.Get(ctx, k2.ID); !ent.IsNotFound(err) {
		t.Fatalf("Delete 后 k2 仍存在: %v", err)
	}
	if err := store.Delete(ctx, k2.Tail8); !errors.Is(err, vidukeys.ErrNotFound) {
		t.Fatalf("重复 Delete 应 ErrNotFound: %v", err)
	}
}

func TestPGSimpleEntAuditSink(t *testing.T) {
	client := pgGate(t)
	sink := NewEntAuditSink(client)
	ctx := context.Background()
	ts := time.Now().Add(time.Hour).UnixMilli()
	e1 := AuditEntry{
		ID: pgSimpleID("audit-a"), Actor: "pg-simple-actor", Route: "/pg-simple/a",
		Action: "create", Target: pgSimpleID("target-a"), IP: "127.0.0.1",
		RequestID: pgSimpleID("request-a"), TsMs: ts,
	}
	e2 := AuditEntry{
		ID: pgSimpleID("audit-b"), Actor: "pg-simple-actor", Route: "/pg-simple/b",
		Action: "update", Target: pgSimpleID("target-b"), IP: "127.0.0.2",
		RequestID: pgSimpleID("request-b"), TsMs: ts + 1,
	}
	// AuditSink 是追加-only 接口，没有 Update/Delete；清理仅按本次审计主键删除。
	t.Cleanup(func() {
		_, _ = client.AuditLog.Delete().Where(auditlog.IDIn(e1.ID, e2.ID)).Exec(context.Background())
	})

	if err := sink.Append(ctx, e1); err != nil {
		t.Fatalf("Append e1: %v", err)
	}
	if err := sink.Append(ctx, e2); err != nil {
		t.Fatalf("Append e2: %v", err)
	}
	if err := sink.Append(ctx, e1); !isUniqueViolation(err) {
		t.Fatalf("重复 audit id 应触发唯一约束: %v", err)
	}
	rows, err := sink.List(ctx, 1000)
	if err != nil {
		t.Fatal(err)
	}
	indices := map[string]int{}
	got := map[string]AuditEntry{}
	for i, row := range rows {
		if row.ID == e1.ID || row.ID == e2.ID {
			indices[row.ID] = i
			got[row.ID] = row
		}
	}
	if len(got) != 2 || got[e1.ID] != e1 || got[e2.ID] != e2 {
		t.Fatalf("Append/List 回读不一致: %+v", got)
	}
	if indices[e2.ID] >= indices[e1.ID] {
		t.Fatalf("List 应按 ts_ms 倒序: e1=%d e2=%d", indices[e1.ID], indices[e2.ID])
	}
}

func TestPGSimpleEntEventSink(t *testing.T) {
	client := pgGate(t)
	sink := NewEntEventSink(client)
	ctx := context.Background()
	sessionID := pgSimpleID("event-session")
	// EntEventSink 没有 Update/Delete，且行主键由实现内部 UUID 生成；调用方 ID 均带前缀，
	// 清理按本次 session_id 定向删除，不清空 session_events 表。
	t.Cleanup(func() {
		_, _ = client.SessionEvent.Delete().Where(sessionevent.SessionID(sessionID)).Exec(context.Background())
	})
	base := eventbus.Event{
		SessionID: sessionID, Type: "pg.simple.contract", TsMs: time.Now().UnixMilli(),
		Epoch: 7, RequestID: pgSimpleID("event-request"), Payload: map[string]any{"marker": "pg-simple"},
	}
	e1 := base
	e1.Seq, e1.EventID = 1, pgSimpleID("event-a")
	e2 := base
	e2.Seq, e2.EventID = 2, pgSimpleID("event-b")
	e3 := base
	e3.Seq, e3.EventID = 3, pgSimpleID("event-c")
	for _, e := range []eventbus.Event{e1, e2, e3} {
		if err := sink.Append(ctx, e); err != nil {
			t.Fatalf("Append seq=%d: %v", e.Seq, err)
		}
	}
	if err := sink.Append(ctx, e1); !isUniqueViolation(err) {
		t.Fatalf("重复 event_id/(session_id,seq) 应触发唯一约束: %v", err)
	}
	compositeDuplicate := e1
	compositeDuplicate.EventID = pgSimpleID("event-composite-duplicate")
	if err := sink.Append(ctx, compositeDuplicate); !isUniqueViolation(err) {
		t.Fatalf("重复 (session_id,seq) 应触发唯一约束: %v", err)
	}

	rows, err := sink.List(ctx, sessionID, 1, 1)
	if err != nil || len(rows) != 1 || !reflect.DeepEqual(rows[0], e2) {
		t.Fatalf("afterSeq/limit 回读: rows=%+v want=%+v err=%v", rows, e2, err)
	}
	rows, err = sink.List(ctx, sessionID, 1, 10)
	if err != nil || len(rows) != 2 || rows[0].EventID != e2.EventID || rows[1].EventID != e3.EventID {
		t.Fatalf("afterSeq 顺序回读: rows=%+v err=%v", rows, err)
	}
	if n, err := client.SessionEvent.Query().Where(sessionevent.SessionID(sessionID)).Count(ctx); err != nil || n != 3 {
		t.Fatalf("重复 Append 不应新增行: n=%d err=%v", n, err)
	}
}

func TestPGSimpleSwitchableEntOutbox(t *testing.T) {
	client := pgGate(t)
	ctx := context.Background()
	sessionID := pgSimpleID("outbox-session")
	// SwitchableSink 没有 Update/Delete；清理只删除本次 session 的 Ent outbox 行。
	t.Cleanup(func() {
		_, _ = client.SessionEvent.Delete().Where(sessionevent.SessionID(sessionID)).Exec(context.Background())
	})
	base := eventbus.Event{
		SessionID: sessionID, Type: "pg.simple.outbox", TsMs: time.Now().UnixMilli(),
		Epoch: 9, RequestID: pgSimpleID("outbox-request"), Payload: map[string]any{"marker": "pg-simple"},
	}
	e1 := base
	e1.Seq, e1.EventID = 1, pgSimpleID("outbox-event-a")
	e2 := base
	e2.Seq, e2.EventID = 2, pgSimpleID("outbox-event-b")
	e3 := base
	e3.Seq, e3.EventID = 3, pgSimpleID("outbox-event-c")

	mem := live.NewMemorySink(8)
	if err := mem.Append(ctx, e1); err != nil {
		t.Fatal(err)
	}
	if err := mem.Append(ctx, e2); err != nil {
		t.Fatal(err)
	}
	// 预写 e1，切换时真实 PG 唯一约束应让 outbox 幂等跳过，只补写 e2。
	if err := NewEntEventSink(client).Append(ctx, e1); err != nil {
		t.Fatalf("预写 e1: %v", err)
	}
	sw := NewSwitchableSink(mem)
	sw.SetEnt(client)
	if n, err := client.SessionEvent.Query().Where(sessionevent.SessionID(sessionID)).Count(ctx); err != nil || n != 2 {
		t.Fatalf("首次 outbox 补写: n=%d want=2 err=%v", n, err)
	}
	if err := sw.Append(ctx, e3); err != nil {
		t.Fatalf("切换后 Append: %v", err)
	}
	if got := len(mem.All()); got != 2 {
		t.Fatalf("切换后新事件不得再写内存: %d", got)
	}

	// 再次切换会重放全部内存事件，三条均已落库，唯一冲突必须全部幂等跳过。
	sw.SetEnt(client)
	rows, err := sw.List(ctx, sessionID, 0, 10)
	if err != nil || len(rows) != 3 {
		t.Fatalf("outbox 重放后 List: rows=%+v err=%v", rows, err)
	}
	for i, want := range []eventbus.Event{e1, e2, e3} {
		if !reflect.DeepEqual(rows[i], want) {
			t.Fatalf("outbox seq=%d 回读不一致: got=%+v want=%+v", want.Seq, rows[i], want)
		}
	}
}
