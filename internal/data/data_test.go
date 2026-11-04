package data

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"entgo.io/ent/dialect"
	"github.com/teamwork/digital-human/internal/conf"
)

func TestOpenPostgresDriver(t *testing.T) {
	if !slices.Contains(sql.Drivers(), "pgx") {
		t.Fatal("pgx database/sql 驱动未注册")
	}
	drv, err := openPostgres("postgres://postgres:postgres@127.0.0.1:5432/postgres?sslmode=disable", testPostgresPool())
	if err != nil {
		t.Fatalf("打开 pgx 驱动失败: %v", err)
	}
	defer func() { _ = drv.Close() }()
	if got := drv.Dialect(); got != dialect.Postgres {
		t.Fatalf("Ent 方言错误: got=%q want=%q", got, dialect.Postgres)
	}
}

func TestNewDataWithRealPostgres(t *testing.T) {
	dsn := os.Getenv("DH_TEST_PG_DSN")
	if dsn == "" {
		t.Skip("DH_TEST_PG_DSN 未设置：跳过真实 PG NewData 冒烟（需要可用 PostgreSQL DSN）")
	}
	cfg := &conf.Config{}
	cfg.Data.PostgresDSN = dsn
	cfg.Data.PostgresMaxOpenConns = 7
	cfg.Data.PostgresMaxIdleConns = 2
	cfg.Data.PostgresConnMaxLifetime = 10 * time.Minute
	cfg.Data.PostgresConnMaxIdleTime = time.Minute
	cfg.Data.ConnectTimeout = 10 * time.Second
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	d, cleanup, err := NewData(cfg, logger)
	if err != nil {
		t.Fatalf("NewData 真实 PG 初始化失败: %v", err)
	}
	defer cleanup()
	if !d.DBAvailable || d.Ent == nil {
		t.Fatal("NewData 未持有可用 Ent client")
	}
	if status := d.Probe(context.Background()); !status.Postgres {
		t.Fatalf("Probe 未报告真实 PG 可用: %+v", status)
	}
	if got := d.db.Stats().MaxOpenConnections; got != 7 {
		t.Fatalf("连接池最大打开连接未生效: got=%d want=7", got)
	}
}

func TestConnectPostgresRejectsInvalidDSNWithoutDriverError(t *testing.T) {
	client, db, err := connectPostgres(context.Background(), "://bad", testPostgresPool())
	if err == nil {
		if client != nil {
			_ = client.Close()
		}
		t.Fatal("无效 DSN 必须显式失败")
	}
	if client != nil || db != nil {
		t.Fatalf("失败后不得返回可用句柄: client=%v db=%v", client, db)
	}
	if strings.Contains(err.Error(), "unknown driver") {
		t.Fatalf("postgres DSN 未进入驱动解析阶段: %v", err)
	}
}

func testPostgresPool() conf.Data {
	return conf.Data{
		PostgresMaxOpenConns:    5,
		PostgresMaxIdleConns:    2,
		PostgresConnMaxLifetime: 30 * time.Minute,
		PostgresConnMaxIdleTime: 5 * time.Minute,
	}
}
