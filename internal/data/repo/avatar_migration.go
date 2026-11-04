package repo

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/google/uuid"
	"github.com/teamwork/digital-human/internal/biz/avatar"
	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/ent/persona"
	"github.com/teamwork/digital-human/internal/data/ent/setting"
)

// MigrateAvatars v3 §7.2：settings 旧键 → 默认数字人档案，一次性回填。
//
// H8 决策：幂等标记落点选 settings 表保留行 id=__meta（与 __revision 保留行同表先例），
// 不经 settings.Store（validateGroups 会拒绝未知组），ent client 直写。
// 重启重入安全：先查标记；标记缺失时以"是否存在档案行"做天然幂等，slug 唯一兜底；
// 任一步失败不设标记，下次启动重试。
//
// 迁移映射（v2 §2.5）：
//
//	session.avatar_id / assets.avatar_id → vidu_avatar_id（冲突 session 优先，记日志）
//	session.image_uri → vidu_image_uri
//	voice.voice_id → voice_id
//	action_library.library_id → action_library_id
//	persona.content → system_prompt
//	persona.active_id 指向既有 Persona（id 或 slug）→ 置 is_active；否则忽略记日志
//	llm.system → 不迁移（死配置），仅日志记原值
func MigrateAvatars(ctx context.Context, client *ent.Client, logger *slog.Logger) error {
	const markerID, markerKey = "__meta", "avatar_migration_v1"
	if row, err := client.Setting.Get(ctx, markerID); err == nil {
		if done, _ := row.ValueJSON[markerKey].(bool); done {
			return nil // 幂等：已迁移
		}
	} else if !ent.IsNotFound(err) {
		return err
	}

	rows, err := client.Setting.Query().All(ctx)
	if err != nil {
		return err
	}
	groups := make(map[string]map[string]any, len(rows))
	for _, r := range rows {
		groups[r.ID] = r.ValueJSON
	}
	get := func(group, key string) string {
		if m := groups[group]; m != nil {
			if v, ok := m[key].(string); ok {
				return v
			}
		}
		return ""
	}

	sessionAvatarID := get("session", "avatar_id")
	assetsAvatarID := get("assets", "avatar_id")
	viduAvatarID := sessionAvatarID
	if viduAvatarID == "" {
		viduAvatarID = assetsAvatarID
	} else if assetsAvatarID != "" && assetsAvatarID != sessionAvatarID {
		logger.Info("avatar migration: assets.avatar_id 与 session.avatar_id 冲突，session 优先",
			"session", sessionAvatarID, "assets", assetsAvatarID)
	}
	viduImageURI := get("session", "image_uri")
	voiceID := get("voice", "voice_id")
	libraryID := get("action_library", "library_id")
	personaContent := get("persona", "content")
	personaActiveID := get("persona", "active_id")

	store := NewEntAvatarStore(client)
	n, err := client.Persona.Query().Where(persona.DeletedAtMs(0)).Count(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		// 无任何档案且旧键至少一项非空 → 建"默认形象"档案回填、is_active=true。
		if viduAvatarID != "" || viduImageURI != "" || voiceID != "" || libraryID != "" || personaContent != "" {
			slug := "default"
			for i := 2; ; i++ { // slug 唯一兜底重试
				_, ok, err := store.GetBySlug(ctx, slug)
				if err != nil {
					return err
				}
				if !ok {
					break
				}
				slug = fmt.Sprintf("default-%d", i)
			}
			a := avatar.Avatar{
				ID:              uuid.NewString(),
				Slug:            slug,
				Name:            "默认形象",
				SystemPrompt:    personaContent,
				VoiceID:         voiceID,
				ViduAvatarID:    viduAvatarID,
				ViduImageURI:    viduImageURI,
				ActionLibraryID: libraryID,
				IsActive:        true,
			}
			if err := store.Create(ctx, a); err != nil {
				return fmt.Errorf("avatar migration: create default: %w", err)
			}
			logger.Info("avatar migration: 已建默认档案", "id", a.ID, "slug", slug)
		}
	} else if personaActiveID != "" {
		// persona.active_id 指向既有 Persona（按 id，再按 slug）→ 置 is_active；否则忽略记日志。
		target := ""
		if a, ok, err := store.Get(ctx, personaActiveID); err != nil {
			return err
		} else if ok {
			target = a.ID
		}
		if target == "" {
			if a, ok, err := store.GetBySlug(ctx, personaActiveID); err != nil {
				return err
			} else if ok {
				target = a.ID
			}
		}
		if target != "" {
			if err := store.SetActive(ctx, target); err != nil {
				return fmt.Errorf("avatar migration: set active: %w", err)
			}
			logger.Info("avatar migration: persona.active_id 已置 active", "id", target)
		} else {
			logger.Info("avatar migration: persona.active_id 无匹配档案，忽略", "active_id", personaActiveID)
		}
	}

	// llm.system 死配置：不迁移，仅日志记原值（v3 §7.2）。
	if llmSystem := get("llm", "system"); llmSystem != "" {
		logger.Info("avatar migration: llm.system 不迁移（死配置）", "value", llmSystem)
	}

	now := time.Now().UnixMilli()
	markerVal := map[string]any{markerKey: true, "migrated_at_ms": now}
	// 无 upsert feature：手动 Update→Create；并发撞唯一键幂等。
	if n, err := client.Setting.Update().
		Where(setting.ID(markerID)).
		SetValueJSON(markerVal).SetUpdatedAtMs(now).SetUpdatedBy("migration").
		Save(ctx); err != nil {
		return fmt.Errorf("avatar migration: write marker: %w", err)
	} else if n == 0 {
		if _, err := client.Setting.Create().
			SetID(markerID).
			SetValueJSON(markerVal).SetUpdatedAtMs(now).SetUpdatedBy("migration").
			Save(ctx); err != nil && !isUniqueViolation(err) {
			return fmt.Errorf("avatar migration: write marker: %w", err)
		}
	}
	return nil
}
