package server

import (
	"bufio"
	"errors"
	"net"
	"net/http"
	"strings"

	khttp "github.com/go-kratos/kratos/v2/transport/http"

	"github.com/teamwork/digital-human/internal/biz/audit"
	"github.com/teamwork/digital-human/pkg/logx"
)

// auditFilter 对所有非 GET/HEAD/OPTIONS 的 /api/v1、/api/danmaku 请求，
// 在 handler 返回后记录一条审计。草稿 owner 请求额外记录未验证主体事件。
// 只记录 path（不记录 query），也不记录请求体、密钥或 token。
func auditFilter(svc *audit.Service) khttp.FilterFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rec, r)
			if svc == nil || !auditPath(r.URL.Path) {
				return
			}
			// 共享管理令牌没有用户主体，无法判断 owner_id 是否属于调用方。
			// 对所有草稿列表、详情和 apply 请求保守留痕；owner_id 仍只是
			// 多会话/多实例的应用级查询范围，不构成认证或授权边界。
			if (r.Method == http.MethodGet || r.Method == http.MethodPost) && draftOwnerScopePath(r.URL.Path) {
				_ = svc.Record(r.Context(), audit.Entry{
					Actor:     "unknown_principal",
					Route:     r.URL.Path,
					Action:    "draft_owner_unverified",
					Target:    "owner_id",
					IP:        remoteHost(r.RemoteAddr),
					RequestID: logx.RequestID(r.Context()),
					Status:    rec.status,
				})
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

// Hijack 透传：gorilla WS 升级要求 http.Hijacker；缺了它握手会 500。
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	h, ok := r.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("audit: underlying ResponseWriter does not implement http.Hijacker")
	}
	return h.Hijack()
}

func auditPath(path string) bool {
	return strings.HasPrefix(path, "/api/v1") || path == "/api/danmaku" || strings.HasPrefix(path, "/api/danmaku/")
}

func draftOwnerScopePath(path string) bool {
	return path == "/api/v1/live-drafts" || strings.HasPrefix(path, "/api/v1/live-drafts/")
}

func remoteHost(remote string) string {
	if host, _, err := net.SplitHostPort(remote); err == nil {
		return host
	}
	return remote
}
