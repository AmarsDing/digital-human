package server

import (
	"net/http"
	"strconv"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/gorilla/websocket"

	"github.com/teamwork/digital-human/internal/conf"
	"github.com/teamwork/digital-human/pkg/eventbus"
	"github.com/teamwork/digital-human/pkg/logx"
)

const eventSubprotocol = "dh.v1"

type wsTracker interface {
	WSConnected(sessionID string)
	WSDisconnected(sessionID string)
}

func serveEventsWS(c khttp.Context, cfg *conf.Config, bus *eventbus.Bus, tickets TicketStore, health *Health, tracker wsTracker) error {
	req := c.Request()
	ticket := subprotocolTicket(req)
	// 先只读校验：握手失败不烧票；握手成功后再 Consume。
	ok, ticketSession := tickets.Peek(ticket)
	if !ok {
		return c.JSON(http.StatusUnauthorized, map[string]any{"error": "ticket_invalid_or_used"})
	}
	reqSession := req.URL.Query().Get("session_id")
	// ticket 与场次绑定：跨 session（或绑定场次却缺 session_id）一律 401。
	if ticketSession != "" && reqSession != ticketSession {
		return c.JSON(http.StatusUnauthorized, map[string]any{"error": "ticket_session_mismatch"})
	}
	after, _ := strconv.ParseInt(req.URL.Query().Get("after_seq"), 10, 64)

	up := websocket.Upgrader{
		ReadBufferSize:  1024,
		WriteBufferSize: 4096,
		Subprotocols:    []string{eventSubprotocol},
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" {
				return true
			}
			if cfg.Security.Mode == "local" && isAllowedLocalOrigin(origin) {
				return true
			}
			for _, o := range cfg.Security.AllowedOrigins {
				if o == origin {
					return true
				}
			}
			return false
		},
	}
	conn, err := up.Upgrade(c.Response(), req, nil)
	if err != nil {
		// 握手失败：ticket 未被消费，客户端可重试。
		return err
	}
	defer conn.Close()
	// 握手成功后才消费；并发窗口内若票已被别的连接用掉，直接关闭。
	ok, consumed := tickets.Consume(ticket)
	if !ok || consumed != ticketSession {
		_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "ticket_invalid_or_used"),
			time.Now().Add(2*time.Second))
		return nil
	}
	health.OnWSConnect()
	defer health.OnWSDisconnect()
	wsSession := reqSession
	if wsSession == "" {
		wsSession = ticketSession
	}
	if tracker != nil {
		tracker.WSConnected(wsSession)
		defer tracker.WSDisconnected(wsSession)
	}

	logger := logx.From(req.Context()).With("component", "ws.events", "after_seq", after)
	logger.Info("ws connected")

	events, cancel := bus.SubscribeFor(wsSession, after)
	defer cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}()

	pings := time.NewTicker(30 * time.Second)
	defer pings.Stop()

	for {
		select {
		case e, ok := <-events:
			if !ok {
				return nil
			}
			// 场次过滤：不把其他 session 的事件推给本连接；空 session 的全局事件保留。
			if ticketSession != "" && e.SessionID != "" && e.SessionID != ticketSession {
				continue
			}
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteJSON(e); err != nil {
				return nil
			}
			health.OnEvent()
		case <-pings.C:
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.PingMessage, nil); err != nil {
				return nil
			}
		case <-done:
			return nil
		case <-req.Context().Done():
			return nil
		}
	}
}
