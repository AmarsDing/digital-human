package server

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"

	"github.com/teamwork/digital-human/internal/conf"
)

// TicketStore 一次性 WS ticket：30-60s，用后即焚，不放 URL query。
type TicketStore interface {
	Issue(sessionID string, ttl time.Duration) (string, time.Time, error)
	Peek(ticket string) (bool, string)
	Consume(ticket string) (bool, string)
}

type memTicket struct {
	sessionID string
	expires   time.Time
}

type MemoryTicketStore struct {
	mu     sync.Mutex
	data   map[string]memTicket
	ttl    time.Duration
	stopGC chan struct{}
	closed bool
}

func NewMemoryTicketStore(ttl time.Duration) *MemoryTicketStore {
	if ttl <= 0 {
		ttl = time.Minute
	}
	s := &MemoryTicketStore{data: map[string]memTicket{}, ttl: ttl, stopGC: make(chan struct{})}
	// 后台 GC：每分钟清理过期 ticket，避免长进程内内存只增不减。
	go s.gcLoop()
	return s
}

func (s *MemoryTicketStore) gcLoop() {
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-s.stopGC:
			return
		case <-t.C:
			s.mu.Lock()
			now := time.Now()
			for k, v := range s.data {
				if now.After(v.expires) {
					delete(s.data, k)
				}
			}
			s.mu.Unlock()
		}
	}
}

// Close 停止后台 GC goroutine；幂等。
func (s *MemoryTicketStore) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.closed {
		s.closed = true
		close(s.stopGC)
	}
}

func (s *MemoryTicketStore) Issue(sessionID string, ttl time.Duration) (string, time.Time, error) {
	if ttl <= 0 {
		ttl = s.ttl
	}
	buf := make([]byte, 32)
	if err := readRandom(buf); err != nil {
		return "", time.Time{}, err
	}
	t := hex.EncodeToString(buf)
	s.mu.Lock()
	defer s.mu.Unlock()
	exp := time.Now().Add(ttl)
	for k, v := range s.data { // 顺带清理过期 ticket
		if time.Now().After(v.expires) {
			delete(s.data, k)
		}
	}
	s.data[t] = memTicket{sessionID: sessionID, expires: exp}
	return t, exp, nil
}

// Peek 只读校验，不消费；用于握手成功前校验 ticket 与场次绑定。
func (s *MemoryTicketStore) Peek(t string) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[t]
	if !ok {
		return false, ""
	}
	if time.Now().After(v.expires) {
		delete(s.data, t)
		return false, ""
	}
	return true, v.sessionID
}

func (s *MemoryTicketStore) Consume(t string) (bool, string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	v, ok := s.data[t]
	if !ok {
		return false, ""
	}
	delete(s.data, t)
	if time.Now().After(v.expires) {
		return false, ""
	}
	return true, v.sessionID
}

// defaultNonceWindow 是 HMAC nonce 去重窗口（与签名时间窗 300s 对齐取 10min）。
const defaultNonceWindow = 10 * time.Minute

// authFilter 覆盖全部非公开路由；默认拒绝，避免“漏口”。
func authFilter(cfg *conf.Config, tickets TicketStore) khttp.FilterFunc {
	return authFilterWithNonces(cfg, tickets, NewMemoryNonceStore(defaultNonceWindow))
}

// authFilterWithNonces 由装配层注入 nonce 去重实现（Redis 可用时用 RedisNonceStore）。
func authFilterWithNonces(cfg *conf.Config, tickets TicketStore, nonces NonceStore) khttp.FilterFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/healthz", "/api/health":
				next.ServeHTTP(w, r)
				return
			case "/metrics":
				if !isLoopback(r.RemoteAddr) {
					writeJSON(w, http.StatusForbidden, map[string]any{"error": "metrics_local_only"})
					return
				}
				next.ServeHTTP(w, r)
				return
			case "/api/danmaku", "/api/v1/danmaku", "/api/v1/collector/heartbeat":
				if cfg.Security.Mode == "collector" {
					if !checkCollectorHMAC(cfg.Security.CollectorSecret, r, nonces) {
						writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "collector_signature_invalid"})
						return
					}
					next.ServeHTTP(w, r)
					return
				}
			}

			switch cfg.Security.Mode {
			case "local":
				if !isLoopback(r.RemoteAddr) {
					writeJSON(w, http.StatusForbidden, map[string]any{"error": "loopback_only"})
					return
				}
			case "lan", "collector":
				// 浏览器无法为 WebSocket 设置 Authorization 头；该路由改由 handler
				// 单次消费 Sec-WebSocket-Protocol 中的 ticket 校验（无效/复用即 401）。
				if r.URL.Path == "/ws/v1/events" || r.URL.Path == "/ws/v1/voice" {
					next.ServeHTTP(w, r)
					return
				}
				if !checkBearer(cfg.Security.AdminTokenSHA256, r) {
					writeJSON(w, http.StatusUnauthorized, map[string]any{"error": "unauthorized"})
					return
				}
			}
			next.ServeHTTP(w, r)
		})
	}
}

// originFilter：lan 模式拒绝非白名单 Origin；local 模式不放行跨站写请求。
func originFilter(cfg *conf.Config) khttp.FilterFunc {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			if origin == "" {
				next.ServeHTTP(w, r)
				return
			}
			allowed := false
			if cfg.Security.Mode == "local" && isAllowedLocalOrigin(origin) {
				allowed = true
			}
			for _, o := range cfg.Security.AllowedOrigins {
				if strings.EqualFold(strings.TrimRight(o, "/"), strings.TrimRight(origin, "/")) {
					allowed = true
				}
			}
			if !allowed {
				writeJSON(w, http.StatusForbidden, map[string]any{"error": "origin_not_allowed"})
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func isAllowedLocalOrigin(origin string) bool {
	for _, host := range []string{"127.0.0.1", "localhost", "[::1]"} {
		if strings.HasPrefix(origin, "http://"+host+":") || origin == "http://"+host {
			return true
		}
	}
	return false
}

func checkBearer(wantSHA string, r *http.Request) bool {
	h := r.Header.Get("Authorization")
	if !strings.HasPrefix(h, "Bearer ") {
		return false
	}
	sum := sha256.Sum256([]byte(strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))))
	got := hex.EncodeToString(sum[:])
	return subtle.ConstantTimeCompare([]byte(got), []byte(strings.ToLower(wantSHA))) == 1
}

// checkCollectorHMAC：
// X-Dh-Ts（秒）、X-Dh-Nonce、X-Dh-Sign =
// HMAC_SHA256(secret, method + "\n" + path + "\n" + sha256(body) + "\n" + ts + "\n" + nonce)。
// 时间窗 300s；nonce 经 NonceStore 去重（内存版进程重启后清空，Redis 版多实例共享）。
func checkCollectorHMAC(secret string, r *http.Request, nonces NonceStore) bool {
	if secret == "" {
		return false
	}
	ts := r.Header.Get("X-Dh-Ts")
	nonce := strings.TrimSpace(r.Header.Get("X-Dh-Nonce"))
	if ts == "" || nonce == "" {
		return false
	}
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil || abs(time.Now().Unix()-n) > 300 {
		return false
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		return false
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	bodySum := sha256.Sum256(body)
	msg := strings.Join([]string{r.Method, r.URL.Path, hex.EncodeToString(bodySum[:]), ts, nonce}, "\n")
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(msg))
	want := hex.EncodeToString(mac.Sum(nil))
	got := strings.ToLower(r.Header.Get("X-Dh-Sign"))
	if !hmac.Equal([]byte(want), []byte(got)) {
		return false
	}
	return nonces.Take(r.Context(), nonce, defaultNonceWindow)
}

type nonceCache struct {
	mu     sync.Mutex
	seen   map[string]time.Time
	window time.Duration
}

func newNonceCache(window time.Duration) *nonceCache {
	return &nonceCache{seen: map[string]time.Time{}, window: window}
}

// take 原子登记 nonce；已使用过返回 false。超过窗口的条目顺带清理。
func (c *nonceCache) take(nonce string) bool {
	now := time.Now()
	c.mu.Lock()
	defer c.mu.Unlock()
	for k, t := range c.seen {
		if now.Sub(t) > c.window {
			delete(c.seen, k)
		}
	}
	if _, used := c.seen[nonce]; used {
		return false
	}
	c.seen[nonce] = now
	return true
}

func isLoopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func abs(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
