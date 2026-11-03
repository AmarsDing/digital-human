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
		field.Int64("updated_at_ms").Default(0),
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
		field.String("decision").Default(""), // t0 | t2 | confirm | blocked | dropped
		field.Int64("created_at_ms").Default(0),
	}
}

func (DanmakuEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("session_id", "created_at_ms"),
	}
}

// Setting 12 组设置中的共享项（不落本机偏好）。
type Setting struct{ ent.Schema }

func (Setting) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Immutable().Unique(), // group id 或 group.key
		field.JSON("value_json", map[string]any{}).Optional(),
		field.Int64("updated_at_ms").Default(0),
		field.String("updated_by").Optional(),
	}
}
