package server

import (
	"context"
	"log/slog"
	"net/http"
	"strings"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	collectorv1 "github.com/teamwork/digital-human/api/gen/go/collector/v1"
	configv1 "github.com/teamwork/digital-human/api/gen/go/config/v1"
	livev1 "github.com/teamwork/digital-human/api/gen/go/live/v1"
	streamv1 "github.com/teamwork/digital-human/api/gen/go/stream/v1"

	"github.com/teamwork/digital-human/internal/biz/audit"
	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/biz/voice"
	"github.com/teamwork/digital-human/internal/conf"
	"github.com/teamwork/digital-human/internal/publish"
	"github.com/teamwork/digital-human/internal/relay"
	"github.com/teamwork/digital-human/internal/service"
	"github.com/teamwork/digital-human/pkg/eventbus"
	"github.com/teamwork/digital-human/pkg/logx"
)

// NewHTTPServer 组装 Kratos HTTP 服务。所有写接口由鉴权 filter 先行拦截；
// 未登记在公开白名单中的路径默认要求鉴权，避免新增接口时漏口。
// nonces 由装配层按 Redis 可用性注入（内存版重启即清空，多实例不共享）。
func NewHTTPServer(
	cfg *conf.Config,
	logger *slog.Logger,
	bus *eventbus.Bus,
	tickets TicketStore,
	voiceTickets VoiceTicketStore,
	nonces NonceStore,
	voiceCoordinator *voice.Coordinator,
	voiceStatus *voice.StatusService,
	liveSvc *service.LiveService,
	dmSvc *service.DanmakuService,
	stSvc *service.SettingsService,
	health *Health,
	version string,
	auditSvc *audit.Service,
	panels PanelDeps,
	streamSvc *publish.StreamService,
	collectorSvc *relay.CollectorService,
	collectorTracker *relay.Tracker,
	preflight *live.Preflight, // 深化方案 v2 §7.5 唯一预检服务；nil 时无路由（fail-closed）
) *khttp.Server {
	if nonces == nil {
		nonces = NewMemoryNonceStore(defaultNonceWindow)
	}
	// 注意：kratos 的 Filter 选项是整体赋值而非追加，多个 Filter 只会保留最后一个；
	// 因此统一由 serverFilters 聚合成一次调用，避免鉴权链丢中间件。
	srv := khttp.NewServer(
		khttp.Address(cfg.Server.HTTPAddr),
		// 全局短超时不能作用到长连；改为 Timeout(0)，由 timeoutFilter 对非 WS 路由施加短超时。
		// WS handler 继续监听 req.Context().Done()，该 Done 只在客户端断开时触发。
		khttp.Timeout(0),
		khttp.Filter(serverFiltersWithNonces(cfg, logger, tickets, auditSvc, nonces)...),
	)

	r := srv.Route("/")
	healthHandler := func(c khttp.Context) error {
		code, p := health.Status(c.Request().Context())
		return c.JSON(code, p)
	}
	r.GET("/healthz", healthHandler)
	// /api/health 与 /healthz 同语义，供约定 /api 前缀的探活方使用。
	r.GET("/api/health", healthHandler)
	r.GET("/metrics", func(c khttp.Context) error {
		promhttp.HandlerFor(health.Registry(), promhttp.HandlerOpts{}).ServeHTTP(c.Response(), c.Request())
		return nil
	})
	r.POST("/api/v1/ws-ticket", func(c khttp.Context) error {
		sessionID := c.Request().URL.Query().Get("session_id")
		t, exp, err := tickets.Issue(sessionID, cfg.Security.TicketTTL)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]any{"error": "ticket_issue_failed"})
		}
		return c.JSON(http.StatusOK, map[string]any{
			"ticket": t, "expires_at": exp.UnixMilli(), "ttl_s": int(cfg.Security.TicketTTL.Seconds()),
		})
	})
	r.GET("/ws/v1/events", func(c khttp.Context) error {
		return serveEventsWS(c, cfg, bus, tickets, health, liveSvc)
	})
	registerVoiceRoutes(srv, cfg, voiceCoordinator, voiceStatus, voiceTickets)

	livev1.RegisterLiveServiceHTTPServer(srv, liveSvc)
	livev1.RegisterDanmakuServiceHTTPServer(srv, dmSvc)
	configv1.RegisterSettingsServiceHTTPServer(srv, stSvc)
	// DZ 批：推流与采集状态接口；未装配（nil）时不注册，前端按 404/unconfigured 处理。
	if streamSvc != nil {
		streamv1.RegisterStreamServiceHTTPServer(srv, streamServiceAdapter{streamSvc})
		registerRTCAudioRoute(srv, cfg, tickets, streamSvc.PCMBridge())
	}
	if collectorSvc != nil {
		collectorv1.RegisterCollectorServiceHTTPServer(srv, collectorSvc)
	}
	if collectorTracker != nil {
		// 心跳为 plain handler：HMAC 验签在 handler 内部完成（collectorauth，
		// 与服务端 checkCollectorHMAC 同口径，auth filter 侧放行见 auth.go）。
		srv.Handle("/api/v1/collector/heartbeat", collectorTracker.HeartbeatHandler(cfg.Security.CollectorSecret))
		srv.Handle("/api/v1/collector/status", http.HandlerFunc(collectorTracker.StatusHandler))
	}
	registerPanelRoutes(srv, panels)
	registerPreflightRoute(srv, preflight)
	return srv
}

// streamServiceAdapter 适配 DZ 批命名差异：publish.StreamService 实现的方法名是 SetTarget，
// 而生成接口（stream.proto rpc SetPushTarget）要求 SetPushTarget；接线层薄适配，不改 DZ 包。
type streamServiceAdapter struct{ *publish.StreamService }

// SetPushTarget 实现 streamv1.StreamServiceHTTPServer。
func (a streamServiceAdapter) SetPushTarget(ctx context.Context, req *streamv1.SetPushTargetRequest) (*streamv1.SetPushTargetReply, error) {
	return a.SetTarget(ctx, req)
}

// serverFilters 聚合全部 HTTP 中间件，顺序即执行顺序：日志 -> 审计 -> Origin -> 鉴权。
// 审计放在鉴权之前，保证被拒绝的写请求也留痕。nonce 去重默认内存实现。
func serverFilters(cfg *conf.Config, logger *slog.Logger, tickets TicketStore, auditSvc *audit.Service) []khttp.FilterFunc {
	return serverFiltersWithNonces(cfg, logger, tickets, auditSvc, NewMemoryNonceStore(defaultNonceWindow))
}

func serverFiltersWithNonces(cfg *conf.Config, logger *slog.Logger, tickets TicketStore, auditSvc *audit.Service, nonces NonceStore) []khttp.FilterFunc {
	return []khttp.FilterFunc{
		timeoutFilter(cfg.Server.ReadTimeout),
		requestLoggerFilter(logger),
		auditFilter(auditSvc),
		originFilter(cfg),
		authFilterWithNonces(cfg, tickets, nonces),
	}
}

const wsEventsPath = "/ws/v1/events"
const wsVoicePath = "/ws/v1/voice"
const wsRTCAudioPath = "/ws/v1/rtc-audio"

// timeoutFilter 只给非 WS 路由加短超时；事件/语音 WS 保留请求原始 context，
// 由 handler 监听 Done（客户端断开才结束），不会被 15s 定时器掐断。
func timeoutFilter(d time.Duration) khttp.FilterFunc {
	return func(next http.Handler) http.Handler {
		if d <= 0 {
			return next
		}
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == wsEventsPath || strings.HasPrefix(r.URL.Path, wsEventsPath+"/") ||
				r.URL.Path == wsVoicePath || strings.HasPrefix(r.URL.Path, wsVoicePath+"/") ||
				r.URL.Path == wsRTCAudioPath || strings.HasPrefix(r.URL.Path, wsRTCAudioPath+"/") {
				next.ServeHTTP(w, r)
				return
			}
			ctx, cancel := context.WithTimeout(r.Context(), d)
			defer cancel()
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

// requestLoggerFilter：每个请求分配 request_id，注入结构化 logger 并写回响应头。
func requestLoggerFilter(logger *slog.Logger) khttp.FilterFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rid := logx.NewRequestID()
			w.Header().Set("X-Request-Id", rid)
			l := logger.With("request_id", rid, "route", r.URL.Path, "method", r.Method)
			ctx := logx.WithRequestID(logx.With(r.Context(), l), rid)
			start := time.Now()
			next.ServeHTTP(w, r.WithContext(ctx))
			l.Info("http", "path", r.URL.Path,
				"dur_ms", time.Since(start).Milliseconds(), "remote", r.RemoteAddr)
		})
	}
}

func subprotocolTicket(r *http.Request) string {
	for _, part := range strings.Split(r.Header.Get("Sec-WebSocket-Protocol"), ",") {
		p := strings.TrimSpace(part)
		if strings.HasPrefix(p, "ticket.") {
			return strings.TrimPrefix(p, "ticket.")
		}
	}
	return ""
}
