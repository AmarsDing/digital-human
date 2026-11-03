package server

import (
	"context"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Health 反映真实依赖状态，降级运行必须显式可见。
type Health struct {
	Version   string
	StartedAt time.Time
	Probe     func(context.Context) DataStatus
	Phase     func() string
	// Caps 由真实装配计算 capabilities（引擎模式/PG/Redis 可用性）；nil 时全部 false。
	Caps func(context.Context) map[string]bool
	// CapWords 深化方案 v2 §2.2 能力矩阵状态词（real/fake/unconfigured/unknown），
	// 由装配点显式计算；nil 时 capability_matrix 为空对象（不假装 real）。
	CapWords func(context.Context) map[string]string
	// Strict 为 true 时恢复旧行为：依赖未齐 Status 返回 503（health.require_dependencies）。
	// 默认 false：进程可服务即 200，降级以 payload 的 status/missing 显式标注。
	Strict bool

	events   prometheus.Counter
	wsGauge  prometheus.Gauge
	registry *prometheus.Registry
}

type DataStatus struct {
	Postgres bool `json:"postgres"`
	Redis    bool `json:"redis"`
}

func NewHealth(version string, probe func(context.Context) DataStatus) *Health {
	reg := prometheus.NewRegistry()
	events := prometheus.NewCounter(prometheus.CounterOpts{Name: "dh_events_published_total", Help: "进程内事件总线发布总数"})
	wsGauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "dh_ws_connections", Help: "当前 WS 连接数"})
	reg.MustRegister(events, wsGauge)
	return &Health{Version: version, StartedAt: time.Now(), Probe: probe, events: events, wsGauge: wsGauge, registry: reg}
}

// DataStatus 取当前依赖状态（内部已有 5s 缓存）。
func (h *Health) DataStatus(ctx context.Context) DataStatus {
	if h.Probe == nil {
		return DataStatus{}
	}
	return h.Probe(ctx)
}

// Payload 的 ok 判定条件：postgres && redis。两者接齐后自动转 true（无需重启）。
// status/missing 是三态语义的显式标注：ok → status=ok；缺依赖 → status=degraded + missing 名单。
func (h *Health) Payload(ctx context.Context) map[string]any {
	data := h.DataStatus(ctx)
	degraded := !data.Postgres || !data.Redis
	missing := []string{}
	if !data.Postgres {
		missing = append(missing, "postgres")
	}
	if !data.Redis {
		missing = append(missing, "redis")
	}
	status := "ok"
	if degraded {
		status = "degraded"
	}
	phase := ""
	if h.Phase != nil {
		phase = h.Phase()
	}
	caps := map[string]any{"vidu": false, "omnivoice": false, "ffmpeg": false}
	if h.Caps != nil {
		for k, v := range h.Caps(ctx) {
			caps[k] = v
		}
	}
	// §2.2 能力矩阵（与旧 bool capabilities 并存，旧字段不删只追加）。
	capWords := map[string]string{}
	if h.CapWords != nil {
		capWords = h.CapWords(ctx)
	}
	return map[string]any{
		"ok":                !degraded,
		"degraded":          degraded,
		"status":            status,
		"missing":           missing,
		"version":           h.Version,
		"uptime_s":          int64(time.Since(h.StartedAt).Seconds()),
		"phase":             phase,
		"data":              data,
		"limits":            map[string]any{"max_sessions": 1},
		"capabilities":      caps,
		"capability_matrix": capWords,
	}
}

// Status 把依赖状态映射为 HTTP 码并返回同一次探测的 Payload。
// 依赖就绪（postgres && redis）恒 200；缺依赖时默认也返回 200（进程可服务，
// 降级是支持的运行模式，payload 用 status=degraded + missing 显式标注），
// Strict（health.require_dependencies=true）时恢复旧行为返回 503。
// Probe 每次调用都会重新判定（Data.Probe 内部 5s 缓存），依赖接齐后无需重启即可自动转 ok。
func (h *Health) Status(ctx context.Context) (int, map[string]any) {
	p := h.Payload(ctx)
	if p["ok"] == true {
		return http.StatusOK, p
	}
	if h.Strict {
		return http.StatusServiceUnavailable, p
	}
	return http.StatusOK, p
}

func (h *Health) OnEvent()                       { h.events.Inc() }
func (h *Health) OnWSConnect()                   { h.wsGauge.Inc() }
func (h *Health) OnWSDisconnect()                { h.wsGauge.Dec() }
func (h *Health) Registry() *prometheus.Registry { return h.registry }
