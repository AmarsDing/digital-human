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
func (h *Health) Payload(ctx context.Context) map[string]any {
	data := h.DataStatus(ctx)
	degraded := !data.Postgres || !data.Redis
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
	return map[string]any{
		"ok":           !degraded,
		"degraded":     degraded,
		"version":      h.Version,
		"uptime_s":     int64(time.Since(h.StartedAt).Seconds()),
		"phase":        phase,
		"data":         data,
		"limits":       map[string]any{"max_sessions": 1},
		"capabilities": caps,
	}
}

// Status 把依赖状态映射为 HTTP 码并返回同一次探测的 Payload，
// 判定条件 postgres && redis 全部就绪才 200，否则 503。
// Probe 每次调用都会重新判定（Data.Probe 内部 5s 缓存），依赖接齐后无需重启即可自动转 200。
func (h *Health) Status(ctx context.Context) (int, map[string]any) {
	p := h.Payload(ctx)
	if p["ok"] == true {
		return http.StatusOK, p
	}
	return http.StatusServiceUnavailable, p
}

func (h *Health) OnEvent()                       { h.events.Inc() }
func (h *Health) OnWSConnect()                   { h.wsGauge.Inc() }
func (h *Health) OnWSDisconnect()                { h.wsGauge.Dec() }
func (h *Health) Registry() *prometheus.Registry { return h.registry }
