package server

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/gorilla/websocket"

	"github.com/teamwork/digital-human/internal/biz/voice"
	"github.com/teamwork/digital-human/internal/conf"
)

const voiceSubprotocol = "dh.voice.v1"

func registerVoiceRoutes(srv *khttp.Server, cfg *conf.Config, coordinator *voice.Coordinator, status *voice.StatusService, tickets VoiceTicketStore) {
	r := srv.Route("/")
	r.GET("/api/v1/voice/status", func(c khttp.Context) error {
		return c.JSON(http.StatusOK, status.Status(c.Request().Context()))
	})
	r.POST("/api/v1/voice/ticket", func(c khttp.Context) error {
		query := c.Request().URL.Query()
		sessionID, clientID := query.Get("session_id"), query.Get("client_id")
		if sessionID == "" || clientID == "" {
			return c.JSON(http.StatusBadRequest, map[string]any{"error": "session_and_client_required"})
		}
		ticket, expires, err := tickets.Issue(sessionID, clientID, cfg.Security.TicketTTL)
		if err != nil {
			return c.JSON(http.StatusInternalServerError, map[string]any{"error": "ticket_issue_failed"})
		}
		return c.JSON(http.StatusOK, map[string]any{
			"ticket": ticket, "purpose": "voice", "expires_at": expires.UnixMilli(),
			"ttl_s": int(cfg.Security.TicketTTL.Seconds()),
		})
	})
	r.GET("/ws/v1/voice", func(c khttp.Context) error {
		return serveVoiceWS(c, cfg, coordinator, tickets)
	})
}

type voiceControl struct {
	Type         string `json:"type"`
	ClientTurnID string `json:"client_turn_id"`
	Mode         string `json:"mode"`
	Format       string `json:"format"`
	SampleRate   int    `json:"sample_rate"`
	Channels     int    `json:"channels"`
	FrameMS      int    `json:"frame_ms"`
	Epoch        int64  `json:"epoch"`
	Frames       int    `json:"frames"`
}

type voiceWriter struct {
	mu   sync.Mutex
	conn *websocket.Conn
}

func (w *voiceWriter) write(v any) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return w.conn.WriteJSON(v)
}

func serveVoiceWS(c khttp.Context, cfg *conf.Config, coordinator *voice.Coordinator, tickets VoiceTicketStore) error {
	req := c.Request()
	ticketValue := subprotocolTicket(req)
	ticket, ok := tickets.Peek(ticketValue)
	query := req.URL.Query()
	if !ok || ticket.SessionID == "" || ticket.ClientID == "" ||
		query.Get("session_id") != ticket.SessionID || query.Get("client_id") != ticket.ClientID {
		return c.JSON(http.StatusUnauthorized, map[string]any{"error": "voice_ticket_invalid_or_used"})
	}
	up := websocket.Upgrader{
		ReadBufferSize: 4096, WriteBufferSize: 4096, Subprotocols: []string{voiceSubprotocol},
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
	consumed, ok := tickets.Consume(ticketValue)
	if !ok || consumed != ticket {
		_ = conn.WriteControl(websocket.CloseMessage,
			websocket.FormatCloseMessage(websocket.ClosePolicyViolation, "voice_ticket_invalid_or_used"), time.Now().Add(2*time.Second))
		return nil
	}
	conn.SetReadLimit(64 << 10)
	writer := &voiceWriter{conn: conn}
	events, unsubscribe := coordinator.Subscribe(ticket.SessionID)
	defer unsubscribe()
	var current *voice.Turn
	var turnMu sync.RWMutex
	currentTurnID := ""
	setCurrentTurnID := func(id string) {
		turnMu.Lock()
		currentTurnID = id
		turnMu.Unlock()
	}
	forwardDone := make(chan struct{})
	go func() {
		defer close(forwardDone)
		for event := range events {
			if !strings.HasPrefix(event.Type, "voice.") || event.Type == "voice.partial" {
				continue
			}
			turnID, _ := event.Payload["turn_id"].(string)
			turnMu.RLock()
			matches := turnID != "" && turnID == currentTurnID
			turnMu.RUnlock()
			if !matches {
				continue
			}
			message := make(map[string]any, len(event.Payload)+1)
			for key, value := range event.Payload {
				message[key] = value
			}
			message["type"] = event.Type
			if err := writer.write(message); err != nil {
				_ = conn.Close()
				return
			}
		}
	}()
	defer func() {
		setCurrentTurnID("")
		if current != nil {
			current.Cancel("cancelled")
		}
		_ = conn.Close()
		unsubscribe()
		<-forwardDone
	}()
	for {
		kind, raw, err := conn.ReadMessage()
		if err != nil {
			return nil
		}
		switch kind {
		case websocket.BinaryMessage:
			if current == nil || len(raw) != 640 {
				if current != nil {
					current.Abort("invalid_audio", errors.New("binary frame must be 640 bytes"))
				}
				_ = writer.write(map[string]any{"type": "rejected", "error": "invalid_audio"})
				continue
			}
			if err := current.WriteAudio(raw); err != nil && !errors.Is(err, voice.ErrTurnClosed) {
				_ = writer.write(map[string]any{"type": "rejected", "error": voiceErrorCode(err)})
			}
		case websocket.TextMessage:
			var control voiceControl
			if json.Unmarshal(raw, &control) != nil {
				_ = writer.write(map[string]any{"type": "rejected", "error": "invalid_control_frame"})
				continue
			}
			switch control.Type {
			case "begin":
				setCurrentTurnID("")
				if current != nil {
					current.Cancel("interrupted")
				}
				turn, err := coordinator.Begin(req.Context(), voice.BeginRequest{
					SessionID: ticket.SessionID, Epoch: control.Epoch, OwnerID: ticket.ClientID,
					ClientTurnID: control.ClientTurnID, Mode: control.Mode, Format: control.Format,
					SampleRate: control.SampleRate, Channels: control.Channels, FrameMS: control.FrameMS,
				}, func(partial voice.Partial) {
					_ = writer.write(map[string]any{"type": "voice.partial", "turn_id": partial.TurnID,
						"revision": partial.Revision, "text": partial.Text})
				})
				if err != nil {
					_ = writer.write(map[string]any{"type": "rejected", "error": voiceErrorCode(err)})
					continue
				}
				current = turn
				setCurrentTurnID(turn.ID())
				_ = writer.write(map[string]any{"type": "accepted", "stage": "listening", "turn_id": turn.ID()})
			case "end":
				if current == nil {
					_ = writer.write(map[string]any{"type": "rejected", "error": "no_active_turn"})
					continue
				}
				if control.Frames != current.Frames() {
					current.Abort("audio_frame_mismatch", errors.New("client/server audio frame count mismatch"))
					_ = writer.write(map[string]any{"type": "rejected", "error": "audio_frame_mismatch"})
					continue
				}
				_ = writer.write(map[string]any{"type": "accepted", "stage": "finalizing", "turn_id": current.ID()})
				current.End()
			case "cancel":
				if current != nil {
					current.Cancel("cancelled")
				}
				_ = writer.write(map[string]any{"type": "accepted", "stage": "cancelled"})
			default:
				_ = writer.write(map[string]any{"type": "rejected", "error": "unknown_control_type"})
			}
		}
	}
}

func voiceErrorCode(err error) string {
	switch {
	case errors.Is(err, voice.ErrTurnActive):
		return "voice_turn_active"
	case errors.Is(err, voice.ErrASRUnavailable):
		return "asr_unavailable"
	case errors.Is(err, voice.ErrNotLive):
		return "not_live"
	case errors.Is(err, voice.ErrTurnClosed):
		return "voice_turn_closed"
	default:
		return "invalid_audio"
	}
}
