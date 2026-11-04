package data

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"entgo.io/ent/dialect"
	entsql "entgo.io/ent/dialect/sql"
	"github.com/redis/go-redis/v9"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/teamwork/digital-human/internal/conf"
	"github.com/teamwork/digital-human/internal/data/ent"
)

// probeInterval 是 /healthz 依赖探测缓存；避免公共端点被高频请求打穿 PG/Redis。
const probeInterval = 5 * time.Second

// Data 唯一持有 Ent client / Redis；上层不得 import ent。
type Data struct {
	Ent            *ent.Client
	Redis          *redis.Client
	DBAvailable    bool
	RedisAvailable bool

	db  *sql.DB
	cfg *conf.Config

	mu        sync.Mutex
	lastProbe time.Time
	status    Status
	onDBReady []func(*ent.Client)
	closeOnce sync.Once
}

// Status 是依赖可达性快照。
type Status struct {
	Postgres bool `json:"postgres"`
	Redis    bool `json:"redis"`
}

// NewData 尝试连接 PG/Redis；默认允许降级（配置 data.required=true 时失败即退出）。
// 降级状态通过 /healthz 暴露，绝不假装已连上。
func NewData(cfg *conf.Config, logger *slog.Logger) (*Data, func(), error) {
	d := &Data{cfg: cfg}
	timeout := cfg.Data.ConnectTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	if cfg.Data.PostgresDSN != "" {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		drv, err := openPostgres(cfg.Data.PostgresDSN, cfg.Data)
		if err == nil {
			d.Ent = ent.NewClient(ent.Driver(drv))
			if err = d.Ent.Schema.Create(ctx); err != nil {
				logger.Warn("ent migration failed", "err", err)
				_ = d.Ent.Close()
				d.Ent = nil
			} else {
				d.db = drv.DB()
				d.DBAvailable = true
			}
		} else {
			logger.Warn("postgres unavailable", "err", err)
		}
		cancel()
	} else {
		logger.Warn("postgres dsn empty; running degraded (no persistence)")
	}

	if cfg.Data.RedisAddr != "" {
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		rdb := redis.NewClient(&redis.Options{Addr: cfg.Data.RedisAddr, Password: cfg.Data.RedisPassword})
		if err := rdb.Ping(ctx).Err(); err != nil {
			logger.Warn("redis unavailable", "err", err)
			_ = rdb.Close()
		} else {
			d.Redis = rdb
			d.RedisAvailable = true
		}
		cancel()
	} else {
		logger.Warn("redis addr empty; 幂等/ticket 使用进程内实现")
	}

	if cfg.Data.Required && (!d.DBAvailable || !d.RedisAvailable) {
		d.Close()
		return nil, func() {}, fmt.Errorf("data.required=true 但 PG/Redis 未就绪")
	}

	cleanup := func() { d.Close() }
	return d, cleanup, nil
}

// Close 幂等关闭；只关一层——ent.Client.Close 已连带关闭底层 driver/db，
// 重复 db.Close 会返回 error 并可能触发驱动告警。
func (d *Data) Close() {
	d.closeOnce.Do(func() {
		d.mu.Lock()
		defer d.mu.Unlock()
		if d.Ent != nil {
			_ = d.Ent.Close()
			d.Ent = nil
			d.db = nil
		}
		if d.Redis != nil {
			_ = d.Redis.Close()
			d.Redis = nil
		}
	})
}

// SetOnDBReady 注册“PG 首次可用”回调（用于把内存实现切成 Ent）。
// 支持多个回调（EventSink、审计、场次各一个）；若注册时 PG 已可用，立即回调一次。
func (d *Data) SetOnDBReady(fn func(*ent.Client)) {
	if fn == nil {
		return
	}
	d.mu.Lock()
	d.onDBReady = append(d.onDBReady, fn)
	c := d.Ent
	d.mu.Unlock()
	if c != nil {
		fn(c)
	}
}

// Probe 返回依赖可达性；结果缓存 5s。
// /healthz 判定条件：postgres && redis 都为 true 才 200（配置为空视为未就绪）。
// 依赖后启动（PG/Redis 在进程启动后才就绪）时，Probe 会自动重连并让 /healthz 转 200，
// 无需重启进程。注意：启动后才连上的 PG 只切换 EventSink，不补写降级期间的事件。
func (d *Data) Probe(ctx context.Context) Status {
	d.mu.Lock()
	defer d.mu.Unlock()

	now := time.Now()
	if !d.lastProbe.IsZero() && now.Sub(d.lastProbe) < probeInterval {
		return d.status
	}
	d.lastProbe = now

	pingCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()

	pg := false
	if d.db != nil {
		pg = d.db.PingContext(pingCtx) == nil
	}
	if !pg && d.cfg != nil && d.cfg.Data.PostgresDSN != "" && d.Ent == nil {
		if client, db, err := connectPostgres(pingCtx, d.cfg.Data.PostgresDSN, d.cfg.Data); err == nil {
			d.Ent, d.db, pg = client, db, true
			for _, fn := range d.onDBReady {
				fn(client)
			}
		}
	}

	redisOK := false
	if d.Redis != nil {
		redisOK = d.Redis.Ping(pingCtx).Err() == nil
	}
	if !redisOK && d.cfg != nil && d.cfg.Data.RedisAddr != "" && d.Redis == nil {
		rdb := redis.NewClient(&redis.Options{Addr: d.cfg.Data.RedisAddr, Password: d.cfg.Data.RedisPassword})
		if rdb.Ping(pingCtx).Err() == nil {
			d.Redis, redisOK = rdb, true
		} else {
			_ = rdb.Close()
		}
	}

	d.DBAvailable = pg
	d.RedisAvailable = redisOK
	d.status = Status{Postgres: pg, Redis: redisOK}
	return d.status
}

func openPostgres(dsn string, dataCfg conf.Data) (*entsql.Driver, error) {
	if err := dataCfg.Validate(); err != nil {
		return nil, err
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(dataCfg.PostgresMaxOpenConns)
	db.SetMaxIdleConns(dataCfg.PostgresMaxIdleConns)
	db.SetConnMaxLifetime(dataCfg.PostgresConnMaxLifetime)
	db.SetConnMaxIdleTime(dataCfg.PostgresConnMaxIdleTime)
	return entsql.OpenDB(dialect.Postgres, db), nil
}

func connectPostgres(ctx context.Context, dsn string, dataCfg conf.Data) (*ent.Client, *sql.DB, error) {
	drv, err := openPostgres(dsn, dataCfg)
	if err != nil {
		return nil, nil, err
	}
	client := ent.NewClient(ent.Driver(drv))
	if err := client.Schema.Create(ctx); err != nil {
		_ = client.Close()
		return nil, nil, err
	}
	return client, drv.DB(), nil
}
