package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// SongRequest 持久化点歌队列、播放回执与人工对账所需的全部状态。
type SongRequest struct{ ent.Schema }

func (SongRequest) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Immutable().Unique(),
		field.String("session_id"),
		field.String("source_event_row_id"),
		field.String("decision_id").Default(""),
		field.String("effect_idempotency_key").Default(""),
		field.String("user_key"),
		field.String("file"),
		field.String("script").Default(""),
		field.String("status").Default("queued"),
		field.Int64("queue_seq").Default(0),
		field.Int("position").Default(0),
		field.Int64("version").Default(1),
		field.String("play_id").Default(""),
		field.Int("attempt").Default(0),
		field.String("last_error").Default(""),
		field.String("reconcile_status").Default("none"),
		field.Int64("created_at_ms").Default(0),
		field.Int64("expires_at_ms").Default(0),
		field.Int64("started_at_ms").Default(0),
		field.Int64("terminal_at_ms").Default(0),
		field.String("terminal_reason").Default(""),
	}
}

func (SongRequest) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("session_id", "source_event_row_id").Unique(),
		index.Fields("queue_seq"),
		index.Fields("session_id", "status"),
	}
}
