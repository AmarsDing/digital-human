package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// InteractionDecision 互动一致性闭环的决策层（深化方案 v2 §5.4 逐字段）：
// 每个原始事件仅一条生产 Decision，UNIQUE(source_event_row_id) 保证崩溃重放/重试不双建；
// rule_version/policy_revision 是该决策的冻结属性，策略变化不得借新 revision 重放旧事件。
type InteractionDecision struct{ ent.Schema }

func (InteractionDecision) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Immutable().Unique(),
		field.String("session_id"),
		// 原始事件行 ID（§5.2 第一层平台源事件落库行）；空串表示无外部源（如本地恢复补建）。
		field.String("source_event_row_id").Optional(),
		// eventbus 内部分发 event_id（§5.2 第二层），只关联投递不作为平台去重键。
		field.String("internal_event_id").Optional(),
		field.String("rule_version").Optional(),
		field.String("policy_revision").Optional(),
		field.String("intent").Optional(), // 如 gift_thanks / chat_reply / song_request
		field.String("target_id").Optional(),
		// execute/confirm/queued/skipped/blocked/degraded（§5.4 枚举）。
		field.String("decision"),
		field.String("reason_code").Optional(),
		// 脱敏后的安全载荷；绝不写入原文以外的敏感信息。
		field.JSON("payload_safe_json", map[string]any{}).Optional(),
		field.Int64("created_at_ms").Default(0),
	}
}

func (InteractionDecision) Indexes() []ent.Index {
	return []ent.Index{
		// §5.2：每个原始事件仅一条生产 Decision；空串不参与唯一（SQLite/PG 对 '' 视为值，
		// 故无源 Decision 的 source_event_row_id 落 NULL 语义由存储层用 id 兜底，见 repo）。
		index.Fields("source_event_row_id").Unique(),
		index.Fields("session_id", "created_at_ms"),
	}
}

// EffectOutbox 副作用 outbox（深化方案 v2 §5.4 逐字段）：与 Decision 同事务写入；
// effect_idempotency_key = session_id:decision_id:effect_type:target_id:effect_seq，DB 唯一。
type EffectOutbox struct{ ent.Schema }

func (EffectOutbox) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Immutable().Unique(),
		field.String("decision_id"),
		field.String("session_id"),
		field.String("effect_type"), // 如 gift_thanks / say / action / song_play
		field.String("target_id").Optional(),
		field.Int("effect_seq").Default(0),
		field.String("effect_idempotency_key").Unique(),
		field.JSON("payload_safe_json", map[string]any{}).Optional(),
		// pending/dispatching/succeeded/failed_retryable/failed/skipped/expired/
		// unknown/reconciled_succeeded/reconciled_failed（§5.4 状态机）。
		field.String("status").Default("pending"),
		field.Int("attempt").Default(0),
		field.Int("max_attempts").Default(3),
		field.String("lease_owner").Optional(),
		field.Int64("lease_until_ms").Default(0),
		field.Int64("next_retry_at_ms").Default(0),
		field.String("dispatch_id").Optional(),
		field.String("downstream_receipt_id").Optional(),
		field.String("last_error").Optional(),
		// none/pending/in_progress/resolved/manual_required（§5.5 reconcile 状态机）。
		field.String("reconcile_status").Default("none"),
		field.Int64("created_at_ms").Default(0),
		field.Int64("dispatched_at_ms").Default(0),
		field.Int64("finished_at_ms").Default(0),
		field.Int64("reconciled_at_ms").Default(0),
		field.String("terminal_reason").Optional(),
	}
}

func (EffectOutbox) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("decision_id"),
		index.Fields("status", "lease_until_ms"),
		index.Fields("session_id", "created_at_ms"),
	}
}
