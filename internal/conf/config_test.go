package conf

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPostgresPoolDefaults(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Data.PostgresMaxOpenConns != 20 || cfg.Data.PostgresMaxIdleConns != 5 {
		t.Fatalf("连接数默认值错误: %+v", cfg.Data)
	}
	if cfg.Data.PostgresConnMaxLifetime != 30*time.Minute || cfg.Data.PostgresConnMaxIdleTime != 5*time.Minute {
		t.Fatalf("连接时间默认值错误: %+v", cfg.Data)
	}
}

func TestPostgresPoolRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		yaml string
	}{
		{name: "max open zero", yaml: "postgres_max_open_conns: 0"},
		{name: "max idle negative", yaml: "postgres_max_idle_conns: -1"},
		{name: "lifetime zero", yaml: "postgres_conn_max_lifetime: 0s"},
		{name: "idle time negative", yaml: "postgres_conn_max_idle_time: -1s"},
		{name: "idle exceeds open", yaml: "postgres_max_open_conns: 2\n  postgres_max_idle_conns: 3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			content := "data:\n  " + tt.yaml + "\n"
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := Load(path); err == nil {
				t.Fatalf("非法连接池配置必须被拒绝:\n%s", content)
			}
		})
	}
}
