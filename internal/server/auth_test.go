package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"

	"github.com/teamwork/digital-human/internal/biz/audit"
	"github.com/teamwork/digital-human/internal/conf"
)

func TestAuthFilterRejectsNonLoopbackInLocalMode(t *testing.T) {
	cfg := &conf.Config{Security: conf.Security{Mode: "local", TicketTTL: time.Minute}}
	h := authFilter(cfg, NewMemoryTicketStore(time.Minute))(okHandler())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings", nil)
	req.RemoteAddr = "203.0.113.9:5555"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("want 403, got %d", rec.Code)
	}
}

func TestAuthFilterRequiresBearerInLanMode(t *testing.T) {
	token := "s3cret-token"
	sum := sha256.Sum256([]byte(token))
	cfg := &conf.Config{Security: conf.Security{Mode: "lan", AdminTokenSHA256: hex.EncodeToString(sum[:])}}
	h := authFilter(cfg, NewMemoryTicketStore(time.Minute))(okHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	req.RemoteAddr = "192.168.1.20:1234"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("missing token: want 401, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	req.RemoteAddr = "192.168.1.20:1234"
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("valid token: want 200, got %d", rec.Code)
	}
}

// 浏览器 WebSocket 无法携带 Authorization；lan 模式下该路由交给 handler 单次消费 ticket 校验，
// 其余受保护路由仍必须带 Bearer。
func TestAuthFilterAllowsWSTicketPathWithoutBearerInLanMode(t *testing.T) {
	token := "s3cret-token"
	sum := sha256.Sum256([]byte(token))
	cfg := &conf.Config{Security: conf.Security{Mode: "lan", AdminTokenSHA256: hex.EncodeToString(sum[:])}}
	h := authFilter(cfg, NewMemoryTicketStore(time.Minute))(okHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/ws/v1/events?after_seq=0", nil)
	req.RemoteAddr = "192.168.1.20:1234"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("ws path without bearer: want 200 (handler checks ticket), got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/live/status", nil)
	req.RemoteAddr = "192.168.1.20:1234"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("other protected route: want 401, got %d", rec.Code)
	}
}

// lan 模式写接口（POST）同样必须带 Bearer，避免只在读接口上做鉴权。
func TestAuthFilterRejectsWriteWithoutBearerInLanMode(t *testing.T) {
	token := "s3cret-token"
	sum := sha256.Sum256([]byte(token))
	cfg := &conf.Config{Security: conf.Security{Mode: "lan", AdminTokenSHA256: hex.EncodeToString(sum[:])}}
	h := authFilter(cfg, NewMemoryTicketStore(time.Minute))(okHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/live/sessions", nil)
	req.RemoteAddr = "192.168.1.20:1234"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("POST without token: want 401, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/live/sessions", nil)
	req.RemoteAddr = "192.168.1.20:1234"
	req.Header.Set("Authorization", "Bearer "+token)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST with token: want 200, got %d", rec.Code)
	}
}

// 错误的 Bearer（长度相同的十六进制串）必须被拒绝，验证常量时间比较不会误判。
func TestAuthFilterRejectsWrongBearer(t *testing.T) {
	token := "s3cret-token"
	sum := sha256.Sum256([]byte(token))
	cfg := &conf.Config{Security: conf.Security{Mode: "lan", AdminTokenSHA256: hex.EncodeToString(sum[:])}}
	h := authFilter(cfg, NewMemoryTicketStore(time.Minute))(okHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings", nil)
	req.RemoteAddr = "192.168.1.20:1234"
	req.Header.Set("Authorization", "Bearer deadbeefwrong")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token: want 401, got %d", rec.Code)
	}
}

// collector 模式：/api/danmaku 必须 HMAC 覆盖 method+path+body+ts+nonce；错签/换 body/重放均 401。
func TestCollectorHMACSignature(t *testing.T) {
	const secret = "collector-secret"
	cfg := &conf.Config{Security: conf.Security{Mode: "collector", CollectorSecret: secret}}
	h := authFilter(cfg, NewMemoryTicketStore(time.Minute))(okHandler())

	ts := strconv.FormatInt(time.Now().Unix(), 10)
	path := "/api/danmaku"
	body := []byte(`{"text":"hello"}`)
	nonce := "nonce-1"

	do := func(method, p string, payload []byte, ts, nonce, sign string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, p, bytes.NewReader(payload))
		req.RemoteAddr = "192.168.1.20:1234"
		req.Header.Set("X-Dh-Ts", ts)
		if nonce != "" {
			req.Header.Set("X-Dh-Nonce", nonce)
		}
		req.Header.Set("X-Dh-Sign", sign)
		h.ServeHTTP(rec, req)
		return rec.Code
	}

	// 错签
	if got := do(http.MethodPost, path, body, ts, nonce, "deadbeef"); got != http.StatusUnauthorized {
		t.Fatalf("bad sign: want 401, got %d", got)
	}
	// 缺 nonce
	if got := do(http.MethodPost, path, body, ts, "", signCollector(secret, http.MethodPost, path, body, ts, "x")); got != http.StatusUnauthorized {
		t.Fatalf("missing nonce: want 401, got %d", got)
	}
	// 缺签名头
	if got := do(http.MethodPost, path, body, ts, nonce, ""); got != http.StatusUnauthorized {
		t.Fatalf("missing sign: want 401, got %d", got)
	}
	// 过期时间戳（超出 ±300s）
	staleTs := strconv.FormatInt(time.Now().Add(-10*time.Minute).Unix(), 10)
	if got := do(http.MethodPost, path, body, staleTs, "nonce-stale", signCollector(secret, http.MethodPost, path, body, staleTs, "nonce-stale")); got != http.StatusUnauthorized {
		t.Fatalf("stale ts: want 401, got %d", got)
	}
	// 换 body 仍 200 的回归：签名按原 body 计算，实际 body 被换掉必须 401。
	otherBody := []byte(`{"text":"evil"}`)
	if got := do(http.MethodPost, path, otherBody, ts, "nonce-swapped", signCollector(secret, http.MethodPost, path, body, ts, "nonce-swapped")); got != http.StatusUnauthorized {
		t.Fatalf("body swap: want 401, got %d", got)
	}
	// method 不匹配也必须 401。
	if got := do(http.MethodPut, path, body, ts, "nonce-method", signCollector(secret, http.MethodPost, path, body, ts, "nonce-method")); got != http.StatusUnauthorized {
		t.Fatalf("method mismatch: want 401, got %d", got)
	}
	// 正确签名
	if got := do(http.MethodPost, path, body, ts, nonce, signCollector(secret, http.MethodPost, path, body, ts, nonce)); got != http.StatusOK {
		t.Fatalf("valid sign: want 200, got %d", got)
	}
	// nonce 重放：同一签名第二次必须 401
	if got := do(http.MethodPost, path, body, ts, nonce, signCollector(secret, http.MethodPost, path, body, ts, nonce)); got != http.StatusUnauthorized {
		t.Fatalf("nonce replay: want 401, got %d", got)
	}
}

func signCollector(secret, method, path string, body []byte, ts, nonce string) string {
	sum := sha256.Sum256(body)
	msg := strings.Join([]string{method, path, hex.EncodeToString(sum[:]), ts, nonce}, "\n")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestOriginFilter(t *testing.T) {
	cfg := &conf.Config{
		Security: conf.Security{
			Mode:           "lan",
			AllowedOrigins: []string{"https://dh.example.com"},
		},
	}
	h := originFilter(cfg)(okHandler())

	cases := []struct {
		name   string
		origin string
		want   int
	}{
		{"no origin", "", http.StatusOK},
		{"matched", "https://dh.example.com", http.StatusOK},
		{"matched with trailing slash / case", "HTTPS://DH.EXAMPLE.COM/", http.StatusOK},
		{"mismatch host", "https://evil.example.com", http.StatusForbidden},
		{"scheme mismatch", "http://dh.example.com", http.StatusForbidden},
		{"localhost not allowed in lan", "http://localhost:9000", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/settings", nil)
			req.RemoteAddr = "192.168.1.20:1234"
			if tc.origin != "" {
				req.Header.Set("Origin", tc.origin)
			}
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("%s: want %d, got %d", tc.origin, tc.want, rec.Code)
			}
		})
	}
}

func TestOriginFilterAllowsLocalhostInLocalMode(t *testing.T) {
	cfg := &conf.Config{Security: conf.Security{Mode: "local"}}
	h := originFilter(cfg)(okHandler())

	for _, o := range []string{"http://127.0.0.1:8795", "http://localhost:8080", "http://[::1]:9000"} {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/settings", nil)
		req.Header.Set("Origin", o)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("local origin %s: want 200, got %d", o, rec.Code)
		}
	}
}

func TestMetricsLoopbackOnly(t *testing.T) {
	cfg := &conf.Config{Security: conf.Security{Mode: "local"}}
	h := authFilter(cfg, NewMemoryTicketStore(time.Minute))(okHandler())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "192.168.1.20:1234"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("non-loopback metrics: want 403, got %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/metrics", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("loopback metrics: want 200, got %d", rec.Code)
	}
}

// 回归测试：kratos 的 Filter 选项是整体赋值而非追加，若按多个 Filter 注册会只剩最后一个。
// serverFilters 必须把所有中间件聚合到同一条链里，这里直接验证最终链路同时生效。
func TestServerFiltersChainAppliesAll(t *testing.T) {
	token := "s3cret-token"
	sum := sha256.Sum256([]byte(token))
	cfg := &conf.Config{Security: conf.Security{
		Mode:             "lan",
		AdminTokenSHA256: hex.EncodeToString(sum[:]),
		AllowedOrigins:   []string{"https://dh.example.com"},
	}}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	chain := khttp.FilterChain(serverFilters(cfg, logger, NewMemoryTicketStore(time.Minute), audit.NewService(audit.NewMemorySink(100)))...)(okHandler())

	do := func(origin, bearer string) int {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPost, "/api/v1/ws-ticket", nil)
		req.RemoteAddr = "192.168.1.20:1234"
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		chain.ServeHTTP(rec, req)
		return rec.Code
	}

	if code := do("http://evil.example", token); code != http.StatusForbidden {
		t.Fatalf("evil origin: want 403, got %d", code)
	}
	if code := do("https://dh.example.com", ""); code != http.StatusUnauthorized {
		t.Fatalf("no bearer: want 401, got %d", code)
	}
	if code := do("https://dh.example.com", token); code != http.StatusOK {
		t.Fatalf("valid request: want 200, got %d", code)
	}
}

func TestHealthzStaysPublic(t *testing.T) {
	cfg := &conf.Config{Security: conf.Security{Mode: "lan", AdminTokenSHA256: "x"}}
	h := authFilter(cfg, NewMemoryTicketStore(time.Minute))(okHandler())
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	req.RemoteAddr = "192.168.1.20:1234"
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("healthz should bypass auth, got %d", rec.Code)
	}
}

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
}
