package repo

import (
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestIsUniqueViolationUsesSQLSTATE(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "localized unique", err: fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: "23505", Message: "重复键违反唯一约束"}), want: true},
		{name: "foreign key", err: &pgconn.PgError{Code: "23503", Message: "duplicate unique text must not classify"}, want: false},
		{name: "text only", err: fmt.Errorf("duplicate unique constraint"), want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isUniqueViolation(tt.err); got != tt.want {
				t.Fatalf("isUniqueViolation()=%v want=%v", got, tt.want)
			}
		})
	}
}
