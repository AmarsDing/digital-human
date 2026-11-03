package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// LiveSession 一场直播一行。config_snapshot 只存非敏感字段（§7.2）。
type LiveSession struct{ ent.Schema }

func (LiveSession) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Immutable().Unique(),
		field.String("live_id").Optional().Nillable(),
		field.String("phase").Default("idle"),
		field.String("mode").Default("managed"),
		field.String("persona_id").Optional(),
		field.String("avatar_id").Optional(),
		field.Int64("started_at_ms").Default(0),
		field.Int64("ended_at_ms").Default(0),
		field.Int64("duration_s").Default(0),
		field.JSON("config_snapshot", map[string]any{}).Optional(),
		field.JSON("stats", map[string]any{}).Optional(),
		field.String("recording_state").Default("none"),
		field.String("recording_url").Optional(),
		field.String("error_code").Optional(),
		field.Int64("updated_at_ms").Default(0),
	}
}

func (LiveSession) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("live_id").Unique(),
		index.Fields("phase"),
	}
}

// SessionEvent 断线补齐与复盘的事件源；唯一 (session_id, seq)，event_id 全局唯一。
type SessionEvent struct{ ent.Schema }

func (SessionEvent) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Immutable().Unique(),
		field.String("session_id"),
		field.Int64("seq"),
		field.String("event_id").Unique(),
		field.String("type"),
		field.Int64("ts_ms"),
		field.Int64("epoch").Default(0),
		field.String("request_id").Optional(),
		field.JSON("payload", map[string]any{}).Optional(),
	}
}

func (SessionEvent) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("session_id", "seq").Unique(),
		index.Fields("session_id"),
	}
}

// Recording 录播拉取状态。
type Recording struct{ ent.Schema }

func (Recording) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Immutable().Unique(),
		field.String("session_id").Unique(),
		field.String("state").Default("none"),
		field.String("url").Optional(),
		field.Int64("fetched_at_ms").Default(0),
		field.Int64("expires_at_ms").Default(0),
	}
}

// CostLedger 复盘成本口径：credits 与 tokens 为真实值，其余标估算/不提供。
// C 批补齐 account/source/时间戳字段，使 Ent 行与 billing.Ledger 一一对应。
type CostLedger struct{ ent.Schema }

func (CostLedger) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Immutable().Unique(),
		field.String("session_id").Unique(),
		field.String("account_id").Default("local"),
		field.Int64("credits_start").Default(0),
		field.Int64("credits_end").Default(0),
		field.Int64("credits_spent").Default(0),
		field.Int64("tokens_in").Default(0),
		field.Int64("tokens_out").Default(0),
		field.Int64("tokens_total").Default(0),
		field.Int64("llm_calls").Default(0),
		field.Int64("diamonds").Default(0),
		field.Int64("gift_count").Default(0),
		field.Float("estimated_cost").Default(0),
		field.String("cost_source").Default("unset"),
		field.String("source").Default("manual"),
		field.Int64("started_at_ms").Default(0),
		field.Int64("ended_at_ms").Default(0),
		field.Int64("updated_at_ms").Default(0),
	}
}
