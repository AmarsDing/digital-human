package repo

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"

	"github.com/teamwork/digital-human/internal/biz/avatar"
	"github.com/teamwork/digital-human/internal/biz/contentpkg"
	"github.com/teamwork/digital-human/internal/biz/danmaku"
	"github.com/teamwork/digital-human/internal/biz/interaction"
	"github.com/teamwork/digital-human/internal/biz/song"
	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/ent/applyjournal"
	"github.com/teamwork/digital-human/internal/data/ent/asset"
	"github.com/teamwork/digital-human/internal/data/ent/danmakuevent"
	"github.com/teamwork/digital-human/internal/data/ent/effectoutbox"
	"github.com/teamwork/digital-human/internal/data/ent/interactiondecision"
	"github.com/teamwork/digital-human/internal/data/ent/persona"
	"github.com/teamwork/digital-human/internal/data/ent/personaref"
	"github.com/teamwork/digital-human/internal/data/ent/songrequest"
)

func pgDomainTag() string {
	return "pg-domain-" + time.Now().UTC().Format("20060102T150405.000000000Z") + "-" + uuid.NewString()
}

// EntContentpkgStore PG 契约：草稿与账本的主键唯一、写读、lease/CAS 更新和状态 CAS。
// Store 没有 Delete 方法（草稿与 apply journal 是恢复审计记录），只在清理阶段物理删除本测试行。
// 除主键外没有自然唯一约束；重复 Create 不是幂等写，必须暴露主键冲突且不得覆盖首写。
func TestEntContentpkgStorePGDomainContract(t *testing.T) {
	client := pgGate(t)
	store := NewEntContentpkgStore(client)
	ctx := context.Background()
	tag := pgDomainTag()
	draftID, applyID := tag+"-draft", tag+"-apply"
	t.Cleanup(func() {
		_, _ = client.ApplyJournal.Delete().Where(applyjournal.DraftID(draftID)).Exec(ctx)
		_ = client.LiveDraft.DeleteOneID(draftID).Exec(ctx)
	})

	if _, ok, err := store.GetDraft(ctx, draftID); err != nil || ok {
		t.Fatalf("空草稿 GetDraft: ok=%v err=%v", ok, err)
	}
	d := contentpkg.Draft{
		ID: draftID, TemplateID: tag + "-template", BaseTemplateRevision: 3,
		Revision: 1, SchemaVersion: 2, Status: contentpkg.DraftEditing,
		Snapshot: map[string]any{"bgm": tag + "-bgm"}, SnapshotHash: tag + "-hash",
		PreflightID: tag + "-preflight", CreatedAtMs: 10, UpdatedAtMs: 10,
	}
	if err := store.CreateDraft(ctx, d); err != nil {
		t.Fatal(err)
	}
	duplicateDraft := d
	duplicateDraft.Snapshot = map[string]any{"bgm": tag + "-overwritten"}
	if err := store.CreateDraft(ctx, duplicateDraft); !isUniqueViolation(err) {
		t.Fatalf("重复 draft_id 必须触发主键唯一约束: %v", err)
	}
	got, ok, err := store.GetDraft(ctx, draftID)
	if err != nil || !ok || got.TemplateID != d.TemplateID || got.Snapshot["bgm"] != d.Snapshot["bgm"] {
		t.Fatalf("CreateDraft 后回读或首写保护失败: got=%+v ok=%v err=%v", got, ok, err)
	}

	owner := tag + "-owner"
	if err := store.UpdateDraftLease(ctx, draftID, owner, 200, 7); err != nil {
		t.Fatal(err)
	}
	if _, err := store.CASUpdateDraft(ctx, draftID, 1, 8, func(cur *contentpkg.Draft) {
		cur.Revision = 2
	}); !errors.Is(err, contentpkg.ErrStaleFencing) {
		t.Fatalf("错误 fencing token 必须拒绝: %v", err)
	}
	updated, err := store.CASUpdateDraft(ctx, draftID, 1, 7, func(cur *contentpkg.Draft) {
		cur.Revision = 2
		cur.Snapshot = map[string]any{"bgm": tag + "-updated"}
		cur.SnapshotHash = tag + "-updated-hash"
		cur.UpdatedAtMs = 20
	})
	if err != nil || updated.Revision != 2 {
		t.Fatalf("草稿 CAS 更新: updated=%+v err=%v", updated, err)
	}
	if _, err := store.CASUpdateDraft(ctx, draftID, 1, 7, func(cur *contentpkg.Draft) {
		cur.Snapshot = map[string]any{"bgm": tag + "-stale"}
		cur.Revision = 2
	}); !errors.Is(err, contentpkg.ErrConflict) {
		t.Fatalf("旧 revision 必须触发 CAS 冲突: %v", err)
	}
	got, _, _ = store.GetDraft(ctx, draftID)
	if got.OwnerID != owner || got.LeaseUntilMs != 200 || got.FencingToken != 7 ||
		got.Revision != 2 || got.Snapshot["bgm"] != tag+"-updated" {
		t.Fatalf("lease/CAS 持久化不一致: %+v", got)
	}

	j := contentpkg.Journal{
		ApplyID: applyID, DraftID: draftID, BaseRevision: 1,
		Snapshot: map[string]any{"bgm": tag + "-updated"},
		Steps: []contentpkg.ApplyStep{{
			Name: "koubo", FileStore: "koubo", Action: "stage_generation+flip_pointer", Status: "pending",
			Generation: contentpkg.AssetRef{ID: tag + "-generation", Revision: 1, ContentHash: tag + "-content"},
		}},
		OldValues: map[string]any{"koubo": tag + "-old"}, Status: contentpkg.JournalPreparing,
		CreatedAtMs: 20, UpdatedAtMs: 20,
	}
	if err := store.CreateJournal(ctx, j); err != nil {
		t.Fatal(err)
	}
	if err := store.CreateJournal(ctx, j); !isUniqueViolation(err) {
		t.Fatalf("重复 apply_id 必须触发主键唯一约束: %v", err)
	}
	gotJournal, ok, err := store.GetJournal(ctx, applyID)
	if err != nil || !ok || len(gotJournal.Steps) != 1 || gotJournal.Steps[0].Generation.ID != tag+"-generation" {
		t.Fatalf("CreateJournal 后回读: journal=%+v ok=%v err=%v", gotJournal, ok, err)
	}
	if _, err := store.UpdateJournal(ctx, applyID, contentpkg.JournalPreparing, func(cur *contentpkg.Journal) {
		cur.Status = contentpkg.JournalStaging
		cur.Steps[0].Status = "done"
		cur.UpdatedAtMs = 30
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.UpdateJournal(ctx, applyID, contentpkg.JournalPreparing, func(cur *contentpkg.Journal) {
		cur.Status = contentpkg.JournalManualRequired
	}); !errors.Is(err, contentpkg.ErrConflict) {
		t.Fatalf("旧 journal 状态必须触发 CAS 冲突: %v", err)
	}
	if err := store.UpdateJournalStatus(ctx, applyID, contentpkg.JournalCommitted, "", 40); err != nil {
		t.Fatal(err)
	}
	gotJournal, _, _ = store.GetJournal(ctx, applyID)
	if gotJournal.Status != contentpkg.JournalCommitted || gotJournal.CommittedAtMs != 40 || gotJournal.Steps[0].Status != "done" {
		t.Fatalf("journal 状态更新不一致: %+v", gotJournal)
	}
	journals, err := store.ListJournalsByDraft(ctx, draftID)
	if err != nil || len(journals) != 1 || journals[0].ApplyID != applyID {
		t.Fatalf("ListJournalsByDraft: journals=%+v err=%v", journals, err)
	}
}

// EntAvatarStore PG 契约：档案 CRUD（删除为 SoftDelete）、revision CAS、slug/SHA256 唯一、
// 素材登记与 ref 重复绑定幂等，以及 RemoveRef 删除关联。接口没有物理 Delete，清理只删本测试 ID。
func TestEntAvatarStorePGDomainContract(t *testing.T) {
	client := pgGate(t)
	store := NewEntAvatarStore(client)
	ctx := context.Background()
	tag := pgDomainTag()
	avatarID, reusedID, conflictID, probeID := tag+"-avatar", tag+"-reused", tag+"-conflict", tag+"-slug-probe"
	assetID, duplicateAssetID, probeAssetID := tag+"-asset", tag+"-asset-duplicate", tag+"-asset-probe"
	slug, sha := tag+"-slug", tag+"-sha256"
	t.Cleanup(func() {
		_, _ = client.PersonaRef.Delete().Where(personaref.PersonaIDIn(avatarID, reusedID, conflictID, probeID)).Exec(ctx)
		_, _ = client.Persona.Delete().Where(persona.IDIn(avatarID, reusedID, conflictID, probeID)).Exec(ctx)
		_, _ = client.Asset.Delete().Where(asset.IDIn(assetID, duplicateAssetID, probeAssetID)).Exec(ctx)
	})

	originalAsset := avatar.Asset{ID: assetID, SHA256: sha, Kind: "image", Name: tag + ".png", Path: "storage/" + tag + ".png"}
	if err := store.PutAsset(ctx, originalAsset); err != nil {
		t.Fatal(err)
	}
	if err := store.PutAsset(ctx, avatar.Asset{ID: duplicateAssetID, SHA256: sha, Kind: "image", Name: tag + "-duplicate"}); err != nil {
		t.Fatalf("同 SHA256 登记必须幂等: %v", err)
	}
	hit, ok, err := store.FindAssetBySHA256(ctx, sha)
	if err != nil || !ok || hit.ID != assetID {
		t.Fatalf("SHA256 必须复用首行: asset=%+v ok=%v err=%v", hit, ok, err)
	}
	if _, ok, err := store.GetAsset(ctx, duplicateAssetID); err != nil || ok {
		t.Fatalf("重复 SHA256 不得创建第二个 ID: ok=%v err=%v", ok, err)
	}
	if _, err := client.Asset.Create().SetID(probeAssetID).SetKind("image").SetName(tag + "-probe").SetPath(tag + "-probe").SetSha256(sha).Save(ctx); !isUniqueViolation(err) {
		t.Fatalf("PG asset.sha256 唯一约束: %v", err)
	}
	if err := store.SetAssetViduURI(ctx, assetID, "vidu://"+tag, 12345); err != nil {
		t.Fatal(err)
	}

	in := avatar.Avatar{
		ID: avatarID, Slug: slug, Name: tag + "-name", SystemPrompt: tag + "-prompt",
		Greeting: tag + "-greeting", VoiceID: tag + "-voice", ViduSync: avatar.ViduSyncState{State: "idle"},
	}
	if err := store.Create(ctx, in); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, avatar.Avatar{ID: conflictID, Slug: slug, Name: tag + "-conflict"}); !errors.Is(err, avatar.ErrSlugConflict) {
		t.Fatalf("重复 slug 必须冲突: %v", err)
	}
	if _, err := client.Persona.Create().SetID(probeID).SetSlug(slug).SetName(tag + "-probe").Save(ctx); !isUniqueViolation(err) {
		t.Fatalf("PG persona.slug 唯一约束: %v", err)
	}
	got, ok, err := store.Get(ctx, avatarID)
	if err != nil || !ok || got.Revision != 1 || got.Name != in.Name {
		t.Fatalf("Create 后 Get: avatar=%+v ok=%v err=%v", got, ok, err)
	}
	if bySlug, ok, err := store.GetBySlug(ctx, slug); err != nil || !ok || bySlug.ID != avatarID {
		t.Fatalf("GetBySlug: avatar=%+v ok=%v err=%v", bySlug, ok, err)
	}
	if err := store.AddRef(ctx, avatarID, assetID, 2); err != nil {
		t.Fatal(err)
	}
	if err := store.AddRef(ctx, avatarID, assetID, 0); err != nil {
		t.Fatalf("重复 ref 绑定应幂等更新 ord: %v", err)
	}
	got, _, _ = store.Get(ctx, avatarID)
	if len(got.Refs) != 1 || got.Refs[0].Ord != 0 || got.Refs[0].Asset.URI != "vidu://"+tag || got.Refs[0].Asset.ExpiresAtMs != 12345 {
		t.Fatalf("ref/素材更新回读不一致: %+v", got.Refs)
	}
	got.Name = tag + "-updated"
	if err := store.UpdateWithRevision(ctx, got, got.Revision+1); !errors.Is(err, avatar.ErrRevisionConflict) {
		t.Fatalf("旧 revision 必须冲突: %v", err)
	}
	if err := store.UpdateWithRevision(ctx, got, got.Revision); err != nil {
		t.Fatal(err)
	}
	got, _, _ = store.Get(ctx, avatarID)
	if got.Revision != 2 || got.Name != tag+"-updated" {
		t.Fatalf("revision CAS 更新未持久化: %+v", got)
	}
	if err := store.RemoveRef(ctx, avatarID, assetID); err != nil {
		t.Fatal(err)
	}
	if got, _, _ = store.Get(ctx, avatarID); len(got.Refs) != 0 {
		t.Fatalf("RemoveRef 后仍有 ref: %+v", got.Refs)
	}
	if err := store.SoftDelete(ctx, avatarID); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := store.Get(ctx, avatarID); err != nil || ok {
		t.Fatalf("软删后 Get 应 miss: ok=%v err=%v", ok, err)
	}
	if err := store.SoftDelete(ctx, avatarID); !errors.Is(err, avatar.ErrNotFound) {
		t.Fatalf("重复软删应 NotFound: %v", err)
	}
	if err := store.Create(ctx, avatar.Avatar{ID: reusedID, Slug: slug, Name: tag + "-reused"}); err != nil {
		t.Fatalf("软删后 slug 应可复用: %v", err)
	}
	if got, ok, err := store.GetBySlug(ctx, slug); err != nil || !ok || got.ID != reusedID {
		t.Fatalf("复用 slug 应指向新行: avatar=%+v ok=%v err=%v", got, ok, err)
	}
}

// MigrateAvatars 使用固定 settings 键、__meta 标记和 default slug，不能直接在共享 schema 造数。
// 因此在 pg-domain 专属临时 schema 内运行真实 PG 迁移；固定键是 API 强制输入，业务值仍带 tag，
// 最后仅 DROP 本测试创建的 schema，不读取或改写共享 schema 的任何行。
func TestMigrateAvatarsPGDomainContract(t *testing.T) {
	_ = pgGate(t)
	ctx := context.Background()
	tag := pgDomainTag()
	dsn := os.Getenv("DH_TEST_PG_DSN")
	admin, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	schema := "pgd_" + strings.ReplaceAll(uuid.NewString(), "-", "")[:16]
	if _, err := admin.ExecContext(ctx, `CREATE SCHEMA "`+schema+`"`); err != nil {
		_ = admin.Close()
		t.Fatalf("创建迁移隔离 schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.ExecContext(context.Background(), `DROP SCHEMA "`+schema+`" CASCADE`)
		_ = admin.Close()
	})
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.RuntimeParams == nil {
		cfg.RuntimeParams = map[string]string{}
	}
	cfg.RuntimeParams["search_path"] = schema
	db := stdlib.OpenDB(*cfg)
	client := ent.NewClient(ent.Driver(entsql.OpenDB(dialect.Postgres, db)))
	t.Cleanup(func() { _ = client.Close() })
	if err := client.Schema.Create(ctx); err != nil {
		t.Fatalf("隔离 schema 自动迁移: %v", err)
	}

	put := func(id string, value map[string]any) {
		t.Helper()
		if _, err := client.Setting.Create().SetID(id).SetValueJSON(value).SetUpdatedAtMs(10).Save(ctx); err != nil {
			t.Fatal(err)
		}
	}
	put("session", map[string]any{"avatar_id": tag + "-session-avatar", "image_uri": "vidu://" + tag})
	put("assets", map[string]any{"avatar_id": tag + "-assets-avatar"})
	put("voice", map[string]any{"voice_id": tag + "-voice"})
	put("action_library", map[string]any{"library_id": tag + "-library"})
	put("persona", map[string]any{"content": tag + "-prompt"})
	put("llm", map[string]any{"system": tag + "-dead-config"})
	if err := MigrateAvatars(ctx, client, newTestLogger()); err != nil {
		t.Fatal(err)
	}
	store := NewEntAvatarStore(client)
	got, ok, err := store.GetBySlug(ctx, "default")
	if err != nil || !ok || !got.IsActive || got.ViduAvatarID != tag+"-session-avatar" ||
		got.ViduImageURI != "vidu://"+tag || got.VoiceID != tag+"-voice" ||
		got.ActionLibraryID != tag+"-library" || got.SystemPrompt != tag+"-prompt" {
		t.Fatalf("默认档案迁移映射: avatar=%+v ok=%v err=%v", got, ok, err)
	}
	marker, err := client.Setting.Get(ctx, "__meta")
	if err != nil {
		t.Fatal(err)
	}
	if done, _ := marker.ValueJSON["avatar_migration_v1"].(bool); !done {
		t.Fatalf("迁移幂等标记缺失: %+v", marker.ValueJSON)
	}
	if err := MigrateAvatars(ctx, client, newTestLogger()); err != nil {
		t.Fatalf("迁移重入: %v", err)
	}
	if n, err := client.Persona.Query().Count(ctx); err != nil || n != 1 {
		t.Fatalf("迁移重入不得重复建档案: n=%d err=%v", n, err)
	}
}

// EntInteractionStore PG 契约：Decision+Effect 事务写读、source 重放幂等、两个唯一索引、
// pending→dispatching 状态 CAS 与回执更新。Store 无 Delete（决策/副作用属于审计与对账记录），仅清理本测试 ID。
func TestEntInteractionStorePGDomainContract(t *testing.T) {
	client := pgGate(t)
	store := NewEntInteractionStore(client)
	ctx := context.Background()
	tag := pgDomainTag()
	sessionID, source := tag+"-session", tag+"-source"
	decisionID, duplicateDecisionID, conflictDecisionID, probeDecisionID := tag+"-decision", tag+"-decision-replay", tag+"-decision-conflict", tag+"-decision-probe"
	effectID, duplicateEffectID, conflictEffectID := tag+"-effect", tag+"-effect-replay", tag+"-effect-conflict"
	effectKey := tag + "-effect-key"
	t.Cleanup(func() {
		_, _ = client.EffectOutbox.Delete().Where(effectoutbox.IDIn(effectID, duplicateEffectID, conflictEffectID)).Exec(ctx)
		_, _ = client.InteractionDecision.Delete().Where(interactiondecision.IDIn(decisionID, duplicateDecisionID, conflictDecisionID, probeDecisionID)).Exec(ctx)
	})

	d := interaction.Decision{
		ID: decisionID, SessionID: sessionID, SourceEventRowID: source,
		InternalEventID: tag + "-internal", RuleVersion: tag + "-rule", PolicyRevision: tag + "-policy",
		Intent: interaction.IntentChatReply, TargetID: tag + "-target", Decision: interaction.DecideExecute,
		ReasonCode: tag + "-reason", PayloadSafe: map[string]any{"text": tag + "-payload"}, CreatedAtMs: 10,
	}
	effect := interaction.Effect{
		ID: effectID, DecisionID: decisionID, SessionID: sessionID,
		EffectType: interaction.EffectTypeChatReply, TargetID: tag + "-speech", EffectSeq: 1,
		EffectIdempotencyKey: effectKey, PayloadSafe: map[string]any{"text": tag + "-effect"},
		Status: interaction.EffectPending, MaxAttempts: 3, ReconcileStatus: interaction.ReconcileNone, CreatedAtMs: 10,
	}
	createdDecision, createdEffects, created, err := store.TxCreateDecisionWithEffects(ctx, d, []interaction.Effect{effect})
	if err != nil || !created || createdDecision.ID != decisionID || len(createdEffects) != 1 {
		t.Fatalf("首次事务写入: decision=%+v effects=%+v created=%v err=%v", createdDecision, createdEffects, created, err)
	}
	replay := d
	replay.ID = duplicateDecisionID
	replayEffect := effect
	replayEffect.ID, replayEffect.DecisionID = duplicateEffectID, duplicateDecisionID
	gotDecision, gotEffects, created, err := NewEntInteractionStore(client).TxCreateDecisionWithEffects(ctx, replay, []interaction.Effect{replayEffect})
	if err != nil || created || gotDecision.ID != decisionID || len(gotEffects) != 1 || gotEffects[0].ID != effectID {
		t.Fatalf("source 重放必须返回首写: decision=%+v effects=%+v created=%v err=%v", gotDecision, gotEffects, created, err)
	}
	if _, err := client.InteractionDecision.Create().SetID(probeDecisionID).SetSessionID(sessionID).SetSourceEventRowID(source).SetDecision(interaction.DecideExecute).Save(ctx); !isUniqueViolation(err) {
		t.Fatalf("PG source_event_row_id 唯一约束: %v", err)
	}
	if got, ok, err := store.GetDecisionBySource(ctx, source); err != nil || !ok || got.ID != decisionID || got.PayloadSafe["text"] != tag+"-payload" {
		t.Fatalf("GetDecisionBySource: decision=%+v ok=%v err=%v", got, ok, err)
	}
	pending, err := store.ListEffects(ctx, interaction.EffectPending)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, item := range pending {
		if item.ID == effectID {
			found = true
		}
	}
	if !found {
		t.Fatalf("ListEffects 未读到本测试 pending 行: %+v", pending)
	}
	if ok, err := store.MarkDispatching(ctx, effectID, tag+"-owner", tag+"-dispatch", 100, 20); err != nil || !ok {
		t.Fatalf("首次 MarkDispatching: ok=%v err=%v", ok, err)
	}
	if ok, err := store.MarkDispatching(ctx, effectID, tag+"-stale-owner", tag+"-stale-dispatch", 200, 21); err != nil || ok {
		t.Fatalf("非 pending 的重复派发必须 CAS 失败: ok=%v err=%v", ok, err)
	}
	if err := store.MarkSucceeded(ctx, effectID, tag+"-receipt", 30); err != nil {
		t.Fatal(err)
	}
	gotEffect, ok := store.GetEffect(effectID)
	if !ok || gotEffect.Status != interaction.EffectSucceeded || gotEffect.Attempt != 1 ||
		gotEffect.DispatchID != tag+"-dispatch" || gotEffect.DownstreamReceiptID != tag+"-receipt" {
		t.Fatalf("Effect 更新回读: %+v ok=%v", gotEffect, ok)
	}

	conflictDecision := d
	conflictDecision.ID, conflictDecision.SourceEventRowID = conflictDecisionID, tag+"-source-conflict"
	conflictEffect := effect
	conflictEffect.ID, conflictEffect.DecisionID = conflictEffectID, conflictDecisionID
	if _, _, created, err := store.TxCreateDecisionWithEffects(ctx, conflictDecision, []interaction.Effect{conflictEffect}); err == nil || created {
		t.Fatalf("重复 effect_idempotency_key 必须失败: created=%v err=%v", created, err)
	}
	if _, ok, err := store.GetDecisionBySource(ctx, conflictDecision.SourceEventRowID); err != nil || ok {
		t.Fatalf("Effect 唯一冲突必须回滚同事务 Decision: ok=%v err=%v", ok, err)
	}
}

// EntSongRequestStore（任务所称 EntSongStore）PG 契约：Enqueue/Get/List、同源重复幂等、
// (session_id,source_event_row_id) 唯一及 Complete 的 play_id CAS。RequestStore 无 Delete（保留播放对账历史），仅清理本测试行。
// 不调用 RecoverPlaying/ClaimNext 的全局扫描写路径，避免共享 PG 中改动其他场次；playing fixture 只更新本测试 ID。
func TestEntSongRequestStorePGDomainContract(t *testing.T) {
	client := pgGate(t)
	tag := pgDomainTag()
	now := time.Now()
	store := NewEntSongRequestStore(client, song.QueuePolicy{MaxQueue: 10, TTL: time.Hour, UserCooldown: time.Nanosecond}, func() time.Time { return now })
	ctx := context.Background()
	sessionID, source := tag+"-session", tag+"-source"
	probeID := tag + "-unique-probe"
	var requestID string
	t.Cleanup(func() {
		ids := []string{probeID}
		if requestID != "" {
			ids = append(ids, requestID)
		}
		_, _ = client.SongRequest.Delete().Where(songrequest.IDIn(ids...)).Exec(ctx)
		_, _ = client.SongRequest.Delete().Where(songrequest.SessionID(sessionID)).Exec(ctx)
	})

	req, duplicate, err := store.Enqueue(ctx, song.Request{
		SessionID: sessionID, SourceEventRowID: source, DecisionID: tag + "-decision",
		EffectIdempotencyKey: tag + "-effect", UserKey: tag + "-user",
		File: tag + ".wav", Script: tag + "-script", Status: song.RequestQueued,
	})
	if err != nil || duplicate {
		t.Fatalf("首次 Enqueue: request=%+v duplicate=%v err=%v", req, duplicate, err)
	}
	requestID = req.ID
	replay, duplicate, err := NewEntSongRequestStore(client, song.QueuePolicy{}, func() time.Time { return now }).Enqueue(ctx, song.Request{
		SessionID: sessionID, SourceEventRowID: source, UserKey: tag + "-different-user", File: tag + "-different.wav",
	})
	if err != nil || !duplicate || replay.ID != req.ID || replay.File != req.File {
		t.Fatalf("同源重放必须返回首写: request=%+v duplicate=%v err=%v", replay, duplicate, err)
	}
	if _, err := client.SongRequest.Create().SetID(probeID).SetSessionID(sessionID).SetSourceEventRowID(source).
		SetUserKey(tag + "-probe-user").SetFile(tag + "-probe.wav").Save(ctx); !isUniqueViolation(err) {
		t.Fatalf("PG (session_id,source_event_row_id) 唯一约束: %v", err)
	}
	if n, err := client.SongRequest.Query().Where(songrequest.SessionID(sessionID), songrequest.SourceEventRowID(source)).Count(ctx); err != nil || n != 1 {
		t.Fatalf("同源请求只能一行: n=%d err=%v", n, err)
	}
	got, ok, err := store.Get(ctx, req.ID)
	if err != nil || !ok || got.File != req.File || got.Version != 1 || got.Status != song.RequestQueued {
		t.Fatalf("Enqueue 后 Get: request=%+v ok=%v err=%v", got, ok, err)
	}
	rows, err := store.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, row := range rows {
		if row.ID == req.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("List 未读到本测试请求: %+v", rows)
	}

	playID := tag + "-play-current"
	playingRow, err := client.SongRequest.UpdateOneID(req.ID).
		SetStatus(song.RequestPlaying).SetPosition(0).SetPlayID(playID).
		SetStartedAtMs(now.UnixMilli()).AddAttempt(1).AddVersion(1).Save(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := store.Complete(ctx, req.ID, tag+"-play-stale", nil); err != nil || ok {
		t.Fatalf("旧 play_id 回执必须 CAS 失败: ok=%v err=%v", ok, err)
	}
	got, _, _ = store.Get(ctx, req.ID)
	if got.Status != song.RequestPlaying || got.PlayID != playID || got.Version != playingRow.Version {
		t.Fatalf("旧回执不得覆盖当前播放: %+v", got)
	}
	if ok, err := store.Complete(ctx, req.ID, playID, nil); err != nil || !ok {
		t.Fatalf("当前 play_id 回执应成功: ok=%v err=%v", ok, err)
	}
	got, _, _ = store.Get(ctx, req.ID)
	if got.Status != song.RequestDone || got.TerminalReason != "completed" || got.Version != playingRow.Version+1 {
		t.Fatalf("Complete 更新未持久化: %+v", got)
	}
}

// EntDanmakuSink PG 契约：Record 写入、Ent 行与 orphan 查询读取、(source,source_event_id) 唯一及重放幂等。
// Sink 只提供追加写与恢复查询，没有 Update/CAS/Delete API；事件不可变，清理仅删除本测试 source 的行。
func TestEntDanmakuSinkPGDomainContract(t *testing.T) {
	client := pgGate(t)
	sink := NewEntDanmakuSink(client)
	ctx := context.Background()
	tag := pgDomainTag()
	eventID, replayID := tag+"-event", tag+"-event-replay"
	source, sourceEventID := tag+"-source", tag+"-source-event"
	t.Cleanup(func() {
		_, _ = client.DanmakuEvent.Delete().Where(danmakuevent.Source(source)).Exec(ctx)
	})

	e := danmaku.Event{
		ID: eventID, SessionID: tag + "-session", Kind: "chat", UserID: tag + "-user",
		UserName: tag + "-name", Text: tag + "-text", Diamond: 3,
		Source: source, SourceEventID: sourceEventID, Decision: "rules",
		OccurredAtMs: 10, ReceivedAtMs: 20, ExpiresAtMs: 30, CreatedAtMs: 20,
	}
	if err := sink.Record(ctx, e); err != nil {
		t.Fatal(err)
	}
	replay := e
	replay.ID = replayID
	duplicate, err := sink.RecordEvent(ctx, replay)
	if err != nil || !duplicate {
		t.Fatalf("同源不同行 ID 重放必须由复合唯一键去重: duplicate=%v err=%v", duplicate, err)
	}
	rows, err := client.DanmakuEvent.Query().Where(danmakuevent.Source(source), danmakuevent.SourceEventID(sourceEventID)).All(ctx)
	if err != nil || len(rows) != 1 {
		t.Fatalf("同源事件只能一行: rows=%+v err=%v", rows, err)
	}
	row := rows[0]
	if row.ID != eventID || row.Text != e.Text || row.OccurredAtMs != 10 || row.ReceivedAtMs != 20 || row.ExpiresAtMs != 30 {
		t.Fatalf("弹幕字段回读不一致: %+v", row)
	}
	orphans, err := sink.ListOrphanRuleChats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, orphan := range orphans {
		if orphan.ID == eventID {
			found = true
			if orphan.SourceEventID != sourceEventID || orphan.ExpiresAtMs != 30 {
				t.Fatalf("orphan 回读字段不一致: %+v", orphan)
			}
		}
	}
	if !found {
		t.Fatalf("ListOrphanRuleChats 未读到本测试事件: %+v", orphans)
	}
}
