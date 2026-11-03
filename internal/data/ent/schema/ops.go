package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// ViduKey 只存 DPAPI 密文与尾 8 位，完整 key 永不落库/出日志。
type ViduKey struct{ ent.Schema }

func (ViduKey) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Immutable().Unique(),
		field.Bytes("cipher"),
		field.String("tail8"),
		field.Bool("is_active").Default(false),
		field.Int64("added_at_ms").Default(0),
		field.Int64("last_ok_at_ms").Default(0),
		field.String("last_error").Optional(),
	}
}

func (ViduKey) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("is_active"),
	}
}

// AuditLog 所有写操作审计；180 天保留。
type AuditLog struct{ ent.Schema }

func (AuditLog) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Immutable().Unique(),
		field.String("actor").Default("local"),
		field.String("route"),
		field.String("action"),
		field.String("target").Optional(),
		field.String("ip").Optional(),
		field.String("request_id").Optional(),
		field.Int64("ts_ms").Default(0),
	}
}

func (AuditLog) Indexes() []ent.Index {
	return []ent.Index{index.Fields("ts_ms")}
}
