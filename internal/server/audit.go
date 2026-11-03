package server

import (
	"net"
	"net/http"
	"strings"

	khttp "github.com/go-kratos/kratos/v2/transport/http"

	"github.com/teamwork/digital-human/internal/biz/audit"
	"github.com/teamwork/digital-human/pkg/logx"
)

// auditFilter 对所有非 GET/HEAD/OPTIONS 的 /api/v1、/api/danmaku 请求，
// 在 handler 返回后记录一条审计。只记录 path（不记录 query），
// 也不记录请求体、密钥或 token。
func auditFilter(svc *audit.Service) khttp.FilterFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			if svc == nil || !auditPath(r.URL.Path) {
				return
			}
			if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
				return
			}
			_ = svc.Record(r.Context(), audit.Entry{
				Actor:     "local",
				Route:     r.URL.Path,
				Action:    r.Method,
				IP:        remoteHost(r.RemoteAddr),
				RequestID: logx.RequestID(r.Context()),
				Status:    rec.status,
			})
		})
	}
}

// statusRecorder 包装 ResponseWriter 记录响应状态码；默认 200（未显式 WriteHeader 时）。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush / Hijack 等可选接口需透传，避免破坏 WS 升级与流式响应。
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func auditPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1") || path == "/api/danmaku" || strings.HasPrefix(path, "/api/danmaku/")
}

func remoteHost(remote string) string {
	if host, _, err := net.SplitHostPort(remote); err == nil {
		return host
	}
	return remote
}
