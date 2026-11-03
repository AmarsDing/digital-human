package schema

import (
	"entgo.io/ent"
	"entgo.io/ent/schema/field"
	"entgo.io/ent/schema/index"
)

// LiveDraft 版本化内容包草稿（深化方案 v2 §7.1 逐字段）：
// 单写者 lease + fencing_token + revision CAS（§7.3）；snapshot_json 只含白名单配置，
// 密钥/推流码/凭证正文永不进入（§7.1 尾部红线）。
type LiveDraft struct{ ent.Schema }

func (LiveDraft) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Immutable().Unique(),
		field.String("template_id").Optional(),
		field.Int64("base_template_revision").Default(0),
		field.Int64("revision").Default(1),
		field.Int64("schema_version").Default(1),
		// 单写者：owner_id + lease_until + fencing_token（§7.3）；
		// 过期 lease 接管也必须递增 fencing token，旧写者不得继续提交。
		field.String("owner_id").Optional(),
		field.Int64("lease_until_ms").Default(0),
		field.Int64("fencing_token").Default(0),
		// editing/staging/staged/committing/committed/rolled_back/manual_required。
		field.String("status").Default("editing"),
		// 完整 staged snapshot（白名单）与内容 hash；缺字段不得视为沿用可变 live 单例。
		field.JSON("snapshot_json", map[string]any{}).Optional(),
		field.String("snapshot_hash").Optional(),
		field.String("preflight_id").Optional(),
		field.Int64("created_at_ms").Default(0),
		field.Int64("updated_at_ms").Default(0),
	}
}

func (LiveDraft) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("template_id"),
		index.Fields("status"),
	}
}

// ApplyJournal 跨 FileStore 提交点/补偿/崩溃重放账本（深化方案 v2 §7.4）：
// apply_id 唯一；commit 先在 DB 以 apply_id 唯一记录再切换 current pointer；
// 崩溃重放按 apply_id 查唯一 commit，无 commit 回滚旧完整集合，有 commit roll-forward。
type ApplyJournal struct{ ent.Schema }

func (ApplyJournal) Fields() []ent.Field {
	return []ent.Field{
		field.String("id").Immutable().Unique(), // apply_id
		field.String("draft_id"),
		field.Int64("base_revision").Default(0),
		// 完整新快照与每步计划（prepare 阶段写入，先持久化再动 FileStore）。
		field.JSON("snapshot_json", map[string]any{}).Optional(),
		field.JSON("steps_json", []map[string]any{}).Optional(),
		// 每步执行结果：step -> pending/done/failed（固定顺序 Koubo → Choreo → Ref/其他）。
		field.JSON("step_status_json", map[string]any{}).Optional(),
		// 可变 FileStore 旧值（generation/字节 hash），补偿逆序恢复用。
		field.JSON("old_values_json", map[string]any{}).Optional(),
		// preparing/staging/commit_pending/committed/rolled_back/manual_required。
		field.String("status").Default("preparing"),
		field.String("last_error").Optional(),
		field.Int64("created_at_ms").Default(0),
		field.Int64("updated_at_ms").Default(0),
		field.Int64("committed_at_ms").Default(0),
		field.Int64("rolled_back_at_ms").Default(0),
	}
}

func (ApplyJournal) Indexes() []ent.Index {
	return []ent.Index{
		index.Fields("draft_id"),
		index.Fields("status"),
	}
}
