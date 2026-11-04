package server

import (
	"encoding/binary"
	"errors"
	"net/http"
	"strconv"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/gorilla/websocket"
	"github.com/teamwork/digital-human/internal/conf"
	"github.com/teamwork/digital-human/internal/publish"
)

const rtcAudioSubprotocol = "dh.rtc-audio.v1"

func registerRTCAudioRoute(srv *khttp.Server, cfg *conf.Config, tickets TicketStore, bridge *publish.PCMBridge) {
	srv.Route("/").GET(wsRTCAudioPath, func(c khttp.Context) error {
		return serveRTCAudioWS(c, cfg, tickets, bridge)
	})
}

// serveRTCAudioWS 只接收本机公开采集页的 Vidu RTC 远端音轨。
// 一次性票据绑定 session；二进制合同为 4 字节大端 sequence + 1920 字节 PCM。
func serveRTCAudioWS(c khttp.Context, cfg *conf.Config, tickets TicketStore, bridge *publish.PCMBridge) error {
	req := c.Request()
	if !isLoopback(req.RemoteAddr) {
		return c.JSON(http.StatusForbidden, map[string]any{"error": "rtc_audio_loopback_only"})
	}
	sessionID := req.URL.Query().Get("session_id")
	epoch, err := strconv.ParseInt(req.URL.Query().Get("epoch"), 10, 64)
	if sessionID == "" || err != nil || epoch < 0 {
		return c.JSON(http.StatusBadRequest, map[string]any{"error": "rtc_audio_session_invalid"})
	}
	ticket := subprotocolTicket(req)
	ok, ticketSession := tickets.Peek(ticket)
	if !ok || ticketSession != sessionID {
		return c.JSON(http.StatusUnauthorized, map[string]any{"error": "rtc_audio_ticket_invalid_or_used"})
	}
	up := websocket.Upgrader{
		ReadBufferSize: 4096, WriteBufferSize: 1024, Subprotocols: []string{rtcAudioSubprotocol},
		CheckOrigin: func(r *http.Request) bool {
			origin := r.Header.Get("Origin")
			if origin == "" || (cfg.Security.Mode == "local" && isAllowedLocalOrigin(origin)) {
				return true
			}
			for _, allowed := range cfg.Security.AllowedOrigins {
				if origin == allowed {
					return true
				}
			}
			return false
		},
	}
	conn, err := up.Upgrade(c.Response(), req, nil)
	if err != nil {
		return err
	}
	defer conn.Close()
	ok, consumed := tickets.Consume(ticket)
	if !ok || consumed != sessionID {
		return rtcAudioClose(conn, "rtc_audio_ticket_invalid_or_used")
	}
	collector, err := bridge.Attach(sessionID, epoch)
	if err != nil {
		return rtcAudioClose(conn, "rtc_audio_collector_active")
	}
	defer bridge.Detach(collector)
	conn.SetReadLimit(4 + publish.PCMFrameBytes)
	for {
		kind, frame, err := conn.ReadMessage()
		if err != nil {
			return nil
		}
		if kind != websocket.BinaryMessage || len(frame) != 4+publish.PCMFrameBytes {
			return rtcAudioClose(conn, "rtc_audio_frame_invalid")
		}
		sequence := binary.BigEndian.Uint32(frame[:4])
		if err := bridge.Write(collector, sequence, frame[4:]); err != nil {
			if errors.Is(err, publish.ErrPCMBackpressure) {
				return rtcAudioClose(conn, "rtc_audio_backpressure")
			}
			return rtcAudioClose(conn, "rtc_audio_frame_rejected")
		}
	}
}

func rtcAudioClose(conn *websocket.Conn, reason string) error {
	_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
	_ = conn.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(websocket.ClosePolicyViolation, reason), time.Now().Add(2*time.Second))
	return nil
}
