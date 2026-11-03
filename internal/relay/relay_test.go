package relay

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/teamwork/digital-human/internal/collectorauth"
)

// fakeBridge 假桥：用 collectorauth.Verify 验签（与服务端同一口径），记录消息。
type fakeBridge struct {
	srv     *httptest.Server
	mu      sync.Mutex
	got     []Out
	gotBeat []heartbeatPayload
	badSign int
}

func newFakeBridge(t *testing.T, secret string) *fakeBridge {
	t.Helper()
	fb := &fakeBridge{}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/danmaku", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		ok := collectorauth.Verify("s3cret", r.Method, r.URL.Path, body,
			r.Header.Get(collectorauth.HeaderTs), r.Header.Get(collectorauth.HeaderNonce),
			r.Header.Get(collectorauth.HeaderSign), time.Now())
		if !ok {
			fb.mu.Lock()
			fb.badSign++
			fb.mu.Unlock()
			http.Error(w, `{"error":"collector_signature_invalid"}`, http.StatusUnauthorized)
			return
		}
		var out Out
		_ = json.Unmarshal(body, &out)
		fb.mu.Lock()
		fb.got = append(fb.got, out)
		fb.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("/api/v1/collector/heartbeat", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !collectorauth.Verify("s3cret", r.Method, r.URL.Path, body,
			r.Header.Get(collectorauth.HeaderTs), r.Header.Get(collectorauth.HeaderNonce),
			r.Header.Get(collectorauth.HeaderSign), time.Now()) {
			http.Error(w, `{"error":"collector_signature_invalid"}`, http.StatusUnauthorized)
			return
		}
		var p heartbeatPayload
		_ = json.Unmarshal(body, &p)
		fb.mu.Lock()
		fb.gotBeat = append(fb.gotBeat, p)
		fb.mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	fb.srv = httptest.NewServer(mux)
	t.Cleanup(fb.srv.Close)
	return fb
}

// fakeDouyinWS 假 douyinLive：HTTP 升级到 ws，推两条消息，随后关闭触发重连。
type fakeDouyinWS struct {
	srv     *httptest.Server
	mu      sync.Mutex
	connN   int
	lastURL string
}

func newFakeDouyinWS(t *testing.T) *fakeDouyinWS {
	t.Helper()
	f := &fakeDouyinWS{}
	up := websocket.Upgrader{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/ws/") {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		f.mu.Lock()
		f.connN++
		f.lastURL = r.URL.Path
		f.mu.Unlock()
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		_ = c.WriteJSON(map[string]any{
			"method": "WebcastChatMessage", "msgId": "m1",
			"user": map[string]any{"nickname": "观众甲", "id": "u1"}, "content": "666",
		})
		_ = c.WriteJSON(map[string]any{
			"method": "WebcastGiftMessage", "groupId": "g1", "repeatCount": float64(1),
			"user": map[string]any{"nickname": "观众乙"}, "gift": map[string]any{"name": "啤酒"},
		})
		// 连击中帧被过滤：只应有一条入队
		time.Sleep(50 * time.Millisecond)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func wsURL(httpURL string) string { return "ws" + strings.TrimPrefix(httpURL, "http") }

func TestRelayEndToEnd(t *testing.T) {
	const secret = "s3cret"
	bridge := newFakeBridge(t, secret)
	ws := newFakeDouyinWS(t)

	r := NewRelay(Config{
		WSBase:            wsURL(ws.srv.URL),
		Bridge:            bridge.srv.URL,
		Room:              "12345",
		Secret:            secret,
		BackoffInit:       50 * time.Millisecond,
		BackoffStep:       50 * time.Millisecond,
		BackoffMax:        100 * time.Millisecond,
		HeartbeatInterval: 100 * time.Millisecond,
	})

	ctx, cancel := context.WithCancel(context.Background())
	go func() { _ = r.Run(ctx) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		bridge.mu.Lock()
		n := len(bridge.got)
		nb := len(bridge.gotBeat)
		bridge.mu.Unlock()
		ws.mu.Lock()
		cn := ws.connN
		ws.mu.Unlock()
		if n >= 1 && nb >= 1 && cn >= 2 { // 断线后至少重连一次
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()

	bridge.mu.Lock()
	defer bridge.mu.Unlock()
	ws.mu.Lock()
	defer ws.mu.Unlock()
	if len(bridge.got) < 1 {
		t.Fatal("桥未收到弹幕")
	}
	if bridge.got[0].Kind != "chat" || bridge.got[0].Text != "666" || bridge.got[0].UserName != "观众甲" {
		t.Fatalf("翻译错误: %+v", bridge.got[0])
	}
	if bridge.badSign != 0 {
		t.Fatalf("出现验签失败 %d 次", bridge.badSign)
	}
	if ws.lastURL != "/ws/12345" {
		t.Fatalf("ws 路径: %s", ws.lastURL)
	}
	if ws.connN < 2 {
		t.Fatalf("未发生重连: %d", ws.connN)
	}
	if len(bridge.gotBeat) < 1 || bridge.gotBeat[0].RoomID != "12345" {
		t.Fatalf("心跳未上报: %+v", bridge.gotBeat)
	}
}

func TestRelayRejectsEmptyConfig(t *testing.T) {
	r := NewRelay(Config{Secret: "x"})
	if err := r.Run(t.Context()); err == nil {
		t.Fatal("空房间号应报错")
	}
	r2 := NewRelay(Config{Room: "1"})
	if err := r2.Run(t.Context()); err == nil {
		t.Fatal("空密钥应报错")
	}
}

func TestTrackerHandlers(t *testing.T) {
	const secret = "s3cret"
	tr := NewTracker()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/collector/status", tr.StatusHandler)
	mux.HandleFunc("/api/v1/collector/heartbeat", tr.HeartbeatHandler(secret))
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// 初始 off
	resp, _ := http.Get(srv.URL + "/api/v1/collector/status")
	b, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `"off"`) {
		t.Fatalf("初始状态: %s", b)
	}

	// 无签名心跳 → 401
	resp, _ = http.Post(srv.URL+"/api/v1/collector/heartbeat", "application/json",
		strings.NewReader(`{"state":"collecting","room_id":"1"}`))
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无签名心跳应 401，got %d", resp.StatusCode)
	}

	// 签名心跳 → 200 且状态更新
	body := []byte(`{"state":"collecting","room_id":"99","last_msg_ms":123,"msgs_per_min":7}`)
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/collector/heartbeat", strings.NewReader(string(body)))
	ts := time.Now().Unix()
	req.Header.Set(collectorauth.HeaderTs, strconv.FormatInt(ts, 10))
	req.Header.Set(collectorauth.HeaderNonce, "n1")
	req.Header.Set(collectorauth.HeaderSign, collectorauth.Sign(secret, "POST", "/api/v1/collector/heartbeat", body, ts, "n1"))
	resp, _ = http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("签名心跳应 200，got %d", resp.StatusCode)
	}

	resp, _ = http.Get(srv.URL + "/api/v1/collector/status")
	b, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if !strings.Contains(string(b), `"collecting"`) || !strings.Contains(string(b), `"99"`) {
		t.Fatalf("状态未更新: %s", b)
	}
}
