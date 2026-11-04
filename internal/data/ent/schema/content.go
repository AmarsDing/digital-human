package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// Persona 对齐蓝本 personas 目录：slug 唯一，切换后需重新开播。
type Persona struct{ ent.Schema }

func (Persona) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Immutable().Unique(),
		field.String("slug").Unique(),
		field.String("name"),
		field.String("tagline").Optional(),
		field.String("voice_id").Optional(),
		field.String("avatar_note").Optional(),
		field.String("system_prompt").Optional(),
		field.Bool("is_active").Default(false),
		// G 批数字人档案扩展（v2 §1.1）：vidu_* 与形象/动作库引用、乐观锁与软删。
		field.String("avatar_asset_id").Optional(),
		field.String("vidu_avatar_id").Optional(),
		field.String("vidu_image_uri").Optional(),
		field.String("action_library_id").Optional(),
		field.String("greeting").Optional(),
		field.Int64("revision").Default(1),
		field.Int64("deleted_at_ms").Default(0),
		// vidu 同步轮询闭环（v3 §2.2）：state/stage/error/job_id/updated_at_ms。
		field.JSON("vidu_sync_state", map[string]any{}).Optional(),
		field.Int64("updated_at_ms").Default(0),
	}
}

func (Persona) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("is_active"),
		index.Fields("deleted_at_ms"),
	}
}

// PersonaRef 档案参考图关联（v3 §7.1）：ord 有序，≤3 上限在 biz 层强制。
// 唯一索引仅 (persona_id,asset_id)；ord 用普通索引——mem 语义允许 AddRef 幂等改
// ord 产生瞬时重复，接口契约优先于 v2 §2.1 的 (persona_id,ord) 唯一索引。
type PersonaRef struct{ ent.Schema }

func (PersonaRef) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Immutable().Unique(), // persona_id + "/" + asset_id
		field.String("persona_id"),
		field.String("asset_id"),
		field.Int("ord").Default(0),
	}
}

func (PersonaRef) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("persona_id", "asset_id").Unique(),
		index.Fields("persona_id", "ord"),
	}
}

// Asset 图片/音频/头像/参考图统一元数据；sha256 唯一，90 天 expires_at。
type Asset struct{ ent.Schema }

func (Asset) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Immutable().Unique(),
		field.String("kind"), // image | audio | avatar | ref
		field.String("name"),
		field.String("path"),
		field.String("sha256").Unique(),
		// v3 §5.2：mime 不入库，读取时 http.DetectContentType 嗅探；
		// H10：ref 与 avatar 共用 vidu ssupload uri 落点（biz Asset.URI 映射本列）。
		field.String("vidu_uri").Optional(),
		field.Int64("bytes").Default(0),
		field.Int64("duration_ms").Default(0),
		field.Int64("uploaded_at_ms").Default(0),
		field.Int64("expires_at_ms").Default(0),
		field.Int64("ref_count").Default(0),
	}
}

// DanmakuEvent 弹幕风控与复盘；30 天保留。
type DanmakuEvent struct{ ent.Schema }

func (DanmakuEvent) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Immutable().Unique(),
		field.String("session_id"),
		field.String("kind"), // chat | gift | join
		field.String("user_id").Optional(),
		field.String("user_name").Optional(),
		field.String("text").Optional(),
		field.Int64("diamond").Default(0),
		field.String("source").Optional(),
		field.String("source_event_id").Optional(),
		field.String("decision").Default(""), // t0 | t2 | confirm | blocked | dropped | rules
		field.Int64("occurred_at_ms").Default(0),
		field.Int64("received_at_ms").Default(0),
		field.Int64("expires_at_ms").Default(0),
		field.Int64("created_at_ms").Default(0),
	}
}

func (DanmakuEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("session_id", "created_at_ms"),
		index.Fields("source", "source_event_id").Unique(),
	}
}

// Setting 13 组设置中的共享项（不落本机偏好）。
type Setting struct{ ent.Schema }

func (Setting) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Immutable().Unique(), // group id 或 group.key
		field.JSON("value_json", map[string]any{}).Optional(),
		field.Int64("updated_at_ms").Default(0),
		field.String("updated_by").Optional(),
	}
}

// Patron 贵宾名单（F29，蓝本 data/patrons.json）：nick 唯一；uid/alias/note/since 可空。
type Patron struct{ ent.Schema }

func (Patron) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Immutable().Unique(), // nick（归一 trim 后）
		field.String("nick").Unique(),
		field.String("uid").Optional(),
		field.String("alias").Optional(),
		field.String("note").Optional(),
		field.String("since").Optional(), // YYYY-MM-DD
		field.Int64("created_at_ms").Default(0),
	}
}

func (Patron) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("nick").Unique(),
		index.Fields("uid"),
	}
}
