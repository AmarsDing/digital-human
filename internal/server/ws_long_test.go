package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/gorilla/websocket"

	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/conf"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

type wsFixture struct {
	base    string
	wsURL   string
	tickets *MemoryTicketStore
	mgr     *live.Manager
	stop    func()
}

// startWSServer 用真实 Kratos HTTP 服务 + 与生产一致的 Timeout(0)/timeoutFilter(15s)。
func startWSServer(t *testing.T) *wsFixture {
	t.Helper()
	cfg := &conf.Config{Security: conf.Security{Mode: "local", TicketTTL: time.Minute}}
	bus := eventbus.New(64)
	mgr := live.NewManager(bus, live.NewMemorySink(64), live.NewFakeController(slog.Default()))
	tickets := NewMemoryTicketStore(time.Minute)
	health := NewHealth("test", nil)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := khttp.NewServer(
		khttp.Listener(lis),
		khttp.Timeout(0),
		khttp.Filter(timeoutFilter(15*time.Second)),
	)
	r := srv.Route("/")
	r.GET("/ws/v1/events", func(c khttp.Context) error {
		return serveEventsWS(c, cfg, bus, tickets, health, mgr)
	})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	stop := func() {
		cancel()
		_ = srv.Stop(context.Background())
		<-done
	}
	addr := "127.0.0.1:" + portOf(t, lis.Addr().String())
	return &wsFixture{
		base: "http://" + addr, wsURL: "ws://" + addr + "/ws/v1/events",
		tickets: tickets, mgr: mgr, stop: stop,
	}
}

func portOf(t *testing.T, addr string) string {
	t.Helper()
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("addr %q: %v", addr, err)
	}
	return port
}

// dialWS 对传输层瞬时失败做有限重试。并行跑全量 go test / -race 时机器负载高，
// 偶发 connect refused/reset 不应被判定为 WS 契约失败；HTTP 层失败（如 401）立即失败不重试。
func dialWS(t *testing.T, dialer *websocket.Dialer, url string) *websocket.Conn {
	t.Helper()
	var lastErr error
	for attempt := 0; attempt < 5; attempt++ {
		conn, resp, err := dialer.Dial(url, nil)
		if err == nil {
			return conn
		}
		if resp != nil {
			resp.Body.Close()
			t.Fatalf("dial %s 被拒: status=%d err=%v", url, resp.StatusCode, err)
		}
		lastErr = err
		time.Sleep(time.Duration(50*(attempt+1)) * time.Millisecond)
	}
	t.Fatalf("dial %s 重试 5 次仍失败: %v", url, lastErr)
	return nil
}

// ticket 与场次绑定：跨 session 复用、缺 session_id、无效 ticket 都必须 401。
func TestWSTicketBoundToSession(t *testing.T) {
	f := startWSServer(t)
	defer f.stop()

	ticket, _, err := f.tickets.Issue("s-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	do := func(sessionQuery, ticketVal string) int {
		url := f.base + "/ws/v1/events"
		if sessionQuery != "" {
			url += "?session_id=" + sessionQuery
		}
		req, err := http.NewRequest(http.MethodGet, url, nil)
		if err != nil {
			t.Fatal(err)
		}
		hdr := eventSubprotocol
		if ticketVal != "" {
			hdr += ", ticket." + ticketVal
		}
		req.Header.Set("Sec-WebSocket-Protocol", hdr)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		return resp.StatusCode
	}

	if code := do("s-2", ticket); code != http.StatusUnauthorized {
		t.Fatalf("跨 session 复用 ticket: want 401, got %d", code)
	}
	if code := do("", ticket); code != http.StatusUnauthorized {
		t.Fatalf("缺 session_id: want 401, got %d", code)
	}
	if code := do("s-1", "not-exists"); code != http.StatusUnauthorized {
		t.Fatalf("无效 ticket: want 401, got %d", code)
	}
}

// 握手失败不烧票：未完成升级的请求不应消费 ticket，同一 ticket 仍可再次握手。
func TestWSTicketNotBurnedByFailedHandshake(t *testing.T) {
	f := startWSServer(t)
	defer f.stop()

	ticket, _, err := f.tickets.Issue("s-burn", time.Minute)
	if err != nil {
		t.Fatal(err)
	}

	// 普通 HTTP GET（无 Upgrade 头）：握手失败，ticket 必须保留。
	req, err := http.NewRequest(http.MethodGet, f.base+"/ws/v1/events?session_id=s-burn", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Sec-WebSocket-Protocol", eventSubprotocol+", ticket."+ticket)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode == http.StatusSwitchingProtocols {
		t.Fatalf("无 Upgrade 头的请求不应升级成功")
	}

	// 同一 ticket 再次握手：必须成功。
	dialer := websocket.Dialer{Subprotocols: []string{eventSubprotocol, "ticket." + ticket}}
	conn := dialWS(t, &dialer, f.wsURL+"?session_id=s-burn")
	conn.Close()

	// 成功握手后 ticket 才被消费：第三次必须 401。
	req2, _ := http.NewRequest(http.MethodGet, f.base+"/ws/v1/events?session_id=s-burn", nil)
	req2.Header.Set("Sec-WebSocket-Protocol", eventSubprotocol+", ticket."+ticket)
	resp2, err := http.DefaultClient.Do(req2)
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusUnauthorized {
		t.Fatalf("握手成功后 ticket 必须已消费: status=%d", resp2.StatusCode)
	}
}

// 长连测试：真实 WS 连接必须持续 120s 不断开（15s timeoutFilter 不得作用到 WS）。
func TestWSConnectionSurvives120s(t *testing.T) {
	if testing.Short() {
		t.Skip("120s 长连测试在 -short 下跳过")
	}
	const sessionID = "s-120"
	f := startWSServer(t)
	defer f.stop()

	ticket, _, err := f.tickets.Issue(sessionID, 2*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	dialer := websocket.Dialer{Subprotocols: []string{eventSubprotocol, "ticket." + ticket}}
	conn := dialWS(t, &dialer, f.wsURL+"?session_id="+sessionID)
	defer conn.Close()
	if got := conn.Subprotocol(); got != eventSubprotocol {
		t.Fatalf("subprotocol=%q want %q", got, eventSubprotocol)
	}

	start := time.Now()
	// 客户端 Dial 返回时，服务端 handler 可能尚未执行 WSConnected；并行跑全量时这里会有毫秒级竞态，
	// 因此等待 tracker 落定，而不是立即断言。
	waitConnected(t, f.mgr, sessionID)

	readErr := make(chan error, 1)
	go func() {
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				readErr <- err
				return
			}
		}
	}()

	deadline := start.Add(120 * time.Second)
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	keepalive := 0
	for done := false; !done; {
		select {
		case err := <-readErr:
			t.Fatalf("WS 在 %dms 断开: %v", time.Since(start).Milliseconds(), err)
		case <-ticker.C:
			if err := conn.WriteMessage(websocket.TextMessage, []byte("keepalive")); err != nil {
				t.Fatalf("WS 写失败 @%dms: %v", time.Since(start).Milliseconds(), err)
			}
			keepalive++
			if st := f.mgr.WSStatusFor(sessionID); st.WSState != "connected" {
				t.Fatalf("连接期间状态异常 @%dms: %+v", time.Since(start).Milliseconds(), st)
			}
			if time.Now().After(deadline) {
				done = true
			}
		case <-time.After(130 * time.Second):
			t.Fatalf("长连测试超时未完成")
		}
	}

	dur := time.Since(start)
	durMs := dur.Milliseconds()
	t.Logf("dur_ms=%d keepalive_writes=%d session=%s", durMs, keepalive, sessionID)
	if dur < 120*time.Second {
		t.Fatalf("dur_ms=%d < 120000", durMs)
	}

	_ = conn.Close()
	waitDisconnect(t, f.mgr, sessionID)
}

func waitConnected(t *testing.T, mgr *live.Manager, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.WSStatusFor(sessionID).WSState == "connected" {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("server tracker should record connected: %+v", mgr.WSStatusFor(sessionID))
}

func waitDisconnect(t *testing.T, mgr *live.Manager, sessionID string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if mgr.WSStatusFor(sessionID).WSState == "disconnected" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("WS 断开后 tracker 未回落 disconnected: %+v", mgr.WSStatusFor(sessionID))
}
