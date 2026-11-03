package publish

// 平台接入 HTTP 处理器（plain net/http，挂载到主 server 的接线见
// dz-integration-notes.md；鉴权复用现有全局 filter，本包不加额外鉴权）。

import (
	"encoding/json"
	"net/http"
	"sync"
)

// HTTPAPI 平台接入接口组。
type HTTPAPI struct {
	pub *Publisher

	mu   sync.Mutex
	idem map[string]int // idempotency_key -> 已执行动作（0=start,1=stop），进程内
}

// NewHTTPAPI 构造。
func NewHTTPAPI(pub *Publisher) *HTTPAPI {
	return &HTTPAPI{pub: pub, idem: map[string]int{}}
}

// Register 把路由挂到 mux（接线示例见 dz-integration-notes.md）。
func (a *HTTPAPI) Register(mux *http.ServeMux) {
	mux.HandleFunc("/api/v1/stream/target", a.handleTarget)
	mux.HandleFunc("/api/v1/stream/status", a.handleStatus)
	mux.HandleFunc("/api/v1/stream/start", a.handleStart)
	mux.HandleFunc("/api/v1/stream/stop", a.handleStop)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

type setTargetRequest struct {
	Platform       string `json:"platform"`
	RTMPURL        string `json:"rtmp_url"`
	StreamKey      string `json:"stream_key"`
	IdempotencyKey string `json:"idempotency_key"`
}

func (a *HTTPAPI) handleTarget(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	var req setTargetRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad_json"})
		return
	}
	// 注意：req.StreamKey 只进入 TargetStore，不进入任何日志（本函数无日志调用）。
	fp, err := a.pub.SetTarget(Target{RTMPURL: req.RTMPURL, Key: req.StreamKey})
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "key_fingerprint8": fp})
}

func (a *HTTPAPI) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	writeJSON(w, http.StatusOK, a.pub.Status())
}

type idemRequest struct {
	IdempotencyKey string `json:"idempotency_key"`
}

// 幂等：同 key 重复 start/stop 直接返回首次结果。
func (a *HTTPAPI) dedup(key string, action int) bool {
	if key == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if v, ok := a.idem[key]; ok && v == action {
		return true
	}
	a.idem[key] = action
	return false
}

func (a *HTTPAPI) handleStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	var req idemRequest
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req)
	if a.dedup(req.IdempotencyKey, 0) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reason": "duplicate_ignored"})
		return
	}
	if err := a.pub.Start(r.Context()); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "reason": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (a *HTTPAPI) handleStop(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"error": "method_not_allowed"})
		return
	}
	var req idemRequest
	_ = json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16)).Decode(&req)
	if a.dedup(req.IdempotencyKey, 1) {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "reason": "duplicate_ignored"})
		return
	}
	if err := a.pub.Stop(); err != nil {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "reason": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
