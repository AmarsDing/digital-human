package relay

// 采集器服务端跟踪器：接收 dh-relay 心跳（HMAC 验签），
// 向面板提供 /api/v1/collector/status。挂载与鉴权接线见 dz-integration-notes.md。

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/teamwork/digital-human/internal/collectorauth"
)

// Tracker 采集器状态跟踪器（进程内）。
type Tracker struct {
	mu         sync.Mutex
	state      string
	roomID     string
	lastMsgMs  int64
	msgsPerMin int
	lastBeatAt time.Time
}

// NewTracker 构造。
func NewTracker() *Tracker { return &Tracker{state: "off"} }

type heartbeatPayload struct {
	State      string `json:"state"`
	RoomID     string `json:"room_id"`
	LastMsgMs  int64  `json:"last_msg_ms"`
	MsgsPerMin int    `json:"msgs_per_min"`
}

type statusReply struct {
	State      string `json:"state"`
	RoomID     string `json:"room_id"`
	LastMsgMs  int64  `json:"last_msg_ms"`
	MsgsPerMin int    `json:"msgs_per_min"`
}

// StatusHandler GET /api/v1/collector/status
func (t *Tracker) StatusHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.state
	// 超过 3 个心跳周期无心跳视为 stopped
	if st != "off" && time.Since(t.lastBeatAt) > 35*time.Second {
		st = "stopped"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(statusReply{
		State: st, RoomID: t.roomID, LastMsgMs: t.lastMsgMs, MsgsPerMin: t.msgsPerMin,
	})
}

// HeartbeatHandler POST /api/v1/collector/heartbeat（HMAC 验签）。
// secret 由装配层注入（DH_COLLECTOR_SECRET）。
func (t *Tracker) HeartbeatHandler(secret string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
		if err != nil {
			http.Error(w, `{"error":"bad_body"}`, http.StatusBadRequest)
			return
		}
		ts := r.Header.Get(collectorauth.HeaderTs)
		nonce := r.Header.Get(collectorauth.HeaderNonce)
		sign := r.Header.Get(collectorauth.HeaderSign)
		if !collectorauth.Verify(secret, http.MethodPost, "/api/v1/collector/heartbeat", body, ts, nonce, sign, time.Now()) {
			http.Error(w, `{"error":"collector_signature_invalid"}`, http.StatusUnauthorized)
			return
		}
		var p heartbeatPayload
		if err := json.Unmarshal(body, &p); err != nil {
			http.Error(w, `{"error":"bad_json"}`, http.StatusBadRequest)
			return
		}
		t.mu.Lock()
		t.state = p.State
		t.roomID = p.RoomID
		t.lastMsgMs = p.LastMsgMs
		t.msgsPerMin = p.MsgsPerMin
		t.lastBeatAt = time.Now()
		t.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}
}

// RoomConfig 面板下发的采集配置（进程内；relay 进程实际读自己的 env，
// 这里用于面板展示与将来 managed 模式）。
type roomConfig struct {
	mu        sync.Mutex
	roomID    string
	relayMode string
}

// RoomConfigStore 供面板读写采集配置。
type RoomConfigStore struct{ rc roomConfig }

// NewRoomConfigStore 构造。
func NewRoomConfigStore() *RoomConfigStore { return &RoomConfigStore{} }

// ConfigHandler POST /api/v1/collector/config
func (s *RoomConfigStore) ConfigHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, `{"error":"method_not_allowed"}`, http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Platform  string `json:"platform"`
		RoomID    string `json:"room_id"`
		RelayMode string `json:"relay_mode"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		http.Error(w, `{"error":"bad_json"}`, http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.RoomID) == "" {
		http.Error(w, `{"ok":false,"error":"room_id 必填"}`, http.StatusBadRequest)
		return
	}
	s.rc.mu.Lock()
	s.rc.roomID = strings.TrimSpace(req.RoomID)
	s.rc.relayMode = req.RelayMode
	s.rc.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}
