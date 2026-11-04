package repo

import (
	"errors"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/teamwork/digital-human/internal/data/ent"
)

// isUniqueViolation 对 PG 只按 SQLSTATE 判断，避免 lc_messages 本地化使文本分类失效；
// SQLite 契约测试仍使用 Ent 的驱动错误分类。
func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code == "23505"
	}
	return ent.IsConstraintError(err)
}
