package vidu

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

type wsHarness struct {
	srv    *httptest.Server
	url    string
	connCh chan *websocket.Conn
}

func newWSHarness(t *testing.T, fn func(*websocket.Conn)) *wsHarness {
	t.Helper()
	h := &wsHarness{connCh: make(chan *websocket.Conn, 8)}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		select {
		case h.connCh <- c:
		default:
		}
		fn(c)
	}))
	h.url = "ws" + strings.TrimPrefix(h.srv.URL, "http")
	t.Cleanup(h.srv.Close)
	return h
}

func ackFrame(success bool, code, msg string) []byte {
	p, _ := json.Marshal(map[string]any{"conn_init_ack": map[string]any{
		"success": success, "error_code": code, "error_msg": msg,
	}})
	f, _ := json.Marshal(Signal{Type: TypeConnInitAck, Payload: p})
	return f
}

func readSignal(t *testing.T, c *websocket.Conn) (Signal, bool) {
	t.Helper()
	_, data, err := c.ReadMessage()
	if err != nil {
		return Signal{}, false
	}
	var s Signal
	if err := json.Unmarshal(data, &s); err != nil {
		t.Logf("decode: %v", err)
		return Signal{}, false
	}
	return s, true
}

func TestConnInitRetriesNotReadyOnSameConnection(t *testing.T) {
	var mu sync.Mutex
	var conns, inits int
	h := newWSHarness(t, func(c *websocket.Conn) {
		mu.Lock()
		conns++
		mu.Unlock()
		for {
			s, ok := readSignal(t, c)
			if !ok {
				return
			}
			if s.Type != TypeConnInit {
				continue
			}
			mu.Lock()
			inits++
			n := inits
			mu.Unlock()
			if n == 1 {
				_ = c.WriteMessage(websocket.TextMessage, ackFrame(false, "NOT_READY", "still starting"))
				continue
			}
			_ = c.WriteMessage(websocket.TextMessage, ackFrame(true, "", ""))
			return
		}
	})

	s := NewSignaling("live-1", "conn-1", WSOptions{
		URL: h.url, ConnInitRetries: 3, ConnInitInterval: 20 * time.Millisecond,
	})
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.ConnInit(context.Background()); err != nil {
		t.Fatalf("conn init: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if conns != 1 || inits != 2 {
		t.Fatalf("conns=%d inits=%d want 1/2 (NOT_READY must resend, not reconnect)", conns, inits)
	}
	if !s.InitOK() {
		t.Fatal("init should be ok")
	}
}

func TestConnInitExhaustedAfter15(t *testing.T) {
	var mu sync.Mutex
	var conns, inits int
	h := newWSHarness(t, func(c *websocket.Conn) {
		mu.Lock()
		conns++
		mu.Unlock()
		for {
			s, ok := readSignal(t, c)
			if !ok {
				return
			}
			if s.Type != TypeConnInit {
				continue
			}
			mu.Lock()
			inits++
			mu.Unlock()
			// 不回应，制造 ack_timeout
		}
	})

	s := NewSignaling("live-1", "conn-1", WSOptions{
		URL: h.url, ConnInitRetries: DefaultConnInitRetries, ConnInitInterval: 10 * time.Millisecond,
	})
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	err := s.ConnInit(context.Background())
	var ce *ConnInitError
	if !errors.As(err, &ce) || !ce.Exhausted || ce.Attempts != DefaultConnInitRetries {
		t.Fatalf("err=%v want exhausted after 15", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if conns != 1 || inits != DefaultConnInitRetries {
		t.Fatalf("conns=%d inits=%d want 1/15", conns, inits)
	}
	if s.InitOK() {
		t.Fatal("init must stay false")
	}
}

func TestConnInitNonRetryableFailsImmediately(t *testing.T) {
	var inits int
	var mu sync.Mutex
	h := newWSHarness(t, func(c *websocket.Conn) {
		for {
			s, ok := readSignal(t, c)
			if !ok {
				return
			}
			if s.Type != TypeConnInit {
				continue
			}
			mu.Lock()
			inits++
			mu.Unlock()
			_ = c.WriteMessage(websocket.TextMessage, ackFrame(false, "LIVE_ENDED", "live ended"))
			return
		}
	})
	s := NewSignaling("live-1", "conn-1", WSOptions{URL: h.url, ConnInitRetries: 3, ConnInitInterval: 10 * time.Millisecond})
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	err := s.ConnInit(context.Background())
	var ce *ConnInitError
	if !errors.As(err, &ce) || ce.Code != "LIVE_ENDED" || ce.Attempts != 1 {
		t.Fatalf("err=%v want LIVE_ENDED attempt 1", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if inits != 1 {
		t.Fatalf("inits=%d want 1 (non-retryable must not resend)", inits)
	}
}

func TestHeartbeatTimeoutSurfacesWSClosed(t *testing.T) {
	block := make(chan struct{})
	h := newWSHarness(t, func(c *websocket.Conn) { <-block })
	s := NewSignaling("live-1", "conn-1", WSOptions{
		URL: h.url, PingInterval: 30 * time.Millisecond, PingTimeout: 60 * time.Millisecond,
	})
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { close(block); s.Close() }()

	select {
	case ev := <-s.Events():
		if ev.Type != TypeWSClosed || ev.Close == nil || ev.Close.Who != "timeout" {
			t.Fatalf("event=%+v want ws closed timeout", ev)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("no heartbeat timeout event")
	}
}

func TestNotReadyDropsAudioAndType7Type11(t *testing.T) {
	bin := make(chan []byte, 8)
	h := newWSHarness(t, func(c *websocket.Conn) {
		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			if mt == websocket.BinaryMessage {
				bin <- data
				continue
			}
			var s Signal
			_ = json.Unmarshal(data, &s)
			if s.Type == TypeConnInit {
				_ = c.WriteMessage(websocket.TextMessage, ackFrame(true, "", ""))
			}
		}
	})
	s := NewSignaling("live-1", "conn-1", WSOptions{URL: h.url, ConnInitInterval: 10 * time.Millisecond})
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if err := s.SendPCM(context.Background(), []byte("pcm")); !errors.Is(err, ErrNotReady) {
		t.Fatalf("pcm before init: %v want ErrNotReady", err)
	}
	if err := s.Interrupt(context.Background()); !errors.Is(err, ErrNotReady) {
		t.Fatalf("type7 before init: %v want ErrNotReady", err)
	}
	if err := s.PromptOperation(context.Background(), "add", nil); !errors.Is(err, ErrNotReady) {
		t.Fatalf("type11 before init: %v want ErrNotReady", err)
	}
	if s.NotReadyDrops() != 3 {
		t.Fatalf("drops=%d want 3", s.NotReadyDrops())
	}
	select {
	case b := <-bin:
		t.Fatalf("unexpected binary %q", b)
	case <-time.After(50 * time.Millisecond):
	}

	if err := s.ConnInit(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := s.SendPCM(context.Background(), []byte("pcm2")); err != nil {
		t.Fatal(err)
	}
	select {
	case b := <-bin:
		if string(b) != "pcm2" {
			t.Fatalf("pcm=%q", b)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("audio frame not delivered after init")
	}
}

func TestForceHangupThenClosedEvent(t *testing.T) {
	h := newWSHarness(t, func(c *websocket.Conn) {
		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			if mt != websocket.TextMessage {
				continue
			}
			var s Signal
			_ = json.Unmarshal(data, &s)
			if s.Type == TypeConnInit {
				_ = c.WriteMessage(websocket.TextMessage, ackFrame(true, "", ""))
				p, _ := json.Marshal(Signal{Type: TypeForceHangup})
				_ = c.WriteMessage(websocket.TextMessage, p)
				_ = c.WriteControl(websocket.CloseMessage,
					websocket.FormatCloseMessage(websocket.CloseNormalClosure, "bye"), time.Now().Add(time.Second))
				return
			}
		}
	})
	s := NewSignaling("live-1", "conn-1", WSOptions{URL: h.url, ConnInitInterval: 10 * time.Millisecond})
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.ConnInit(context.Background()); err != nil {
		t.Fatal(err)
	}

	var gotForce, gotClosed bool
	for i := 0; i < 2; i++ {
		select {
		case ev := <-s.Events():
			switch ev.Type {
			case TypeForceHangup:
				gotForce = true
			case TypeWSClosed:
				gotClosed = true
			}
		case <-time.After(300 * time.Millisecond):
			t.Fatalf("timed out waiting events, force=%v closed=%v", gotForce, gotClosed)
		}
	}
	if !gotForce || !gotClosed {
		t.Fatalf("force=%v closed=%v", gotForce, gotClosed)
	}
}

func TestUnknownTypeCounted(t *testing.T) {
	h := newWSHarness(t, func(c *websocket.Conn) {
		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			if mt != websocket.TextMessage {
				continue
			}
			var s Signal
			_ = json.Unmarshal(data, &s)
			if s.Type == TypeConnInit {
				_ = c.WriteMessage(websocket.TextMessage, ackFrame(true, "", ""))
				p, _ := json.Marshal(Signal{Type: 99, Payload: json.RawMessage(`{"x":1}`)})
				_ = c.WriteMessage(websocket.TextMessage, p)
				return
			}
		}
	})
	s := NewSignaling("live-1", "conn-1", WSOptions{URL: h.url, ConnInitInterval: 10 * time.Millisecond})
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.ConnInit(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case ev := <-s.Events():
		if !ev.Unknown || ev.Type != 99 {
			t.Fatalf("event=%+v want unknown 99", ev)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("no unknown event")
	}
	if s.UnknownTypes() == 0 {
		t.Fatal("unknown counter not incremented")
	}
}

// type10 口播字幕必须解析为独立事件类型，不再落入 Unknown。
func TestType10OutputTextParsed(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wantNil bool
		msgID   string
		content string
		ts      int64
	}{
		{"full", `{"text_msg":{"msg_id":"m-1","content":"第一句","timestamp":1730000000000}}`, false, "m-1", "第一句", 1730000000000},
		{"empty_content", `{"text_msg":{"msg_id":"m-2","content":"","timestamp":1}}`, false, "m-2", "", 1},
		{"bad_payload", `{"text_msg":1}`, true, "", "", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload := tc.payload
			h := newWSHarness(t, func(c *websocket.Conn) {
				for {
					_, data, err := c.ReadMessage()
					if err != nil {
						return
					}
					var s Signal
					_ = json.Unmarshal(data, &s)
					if s.Type == TypeConnInit {
						_ = c.WriteMessage(websocket.TextMessage, ackFrame(true, "", ""))
						f, _ := json.Marshal(Signal{Type: TypeOutputText, Payload: json.RawMessage(payload)})
						_ = c.WriteMessage(websocket.TextMessage, f)
						return
					}
				}
			})
			s := NewSignaling("live-1", "conn-1", WSOptions{URL: h.url, ConnInitInterval: 10 * time.Millisecond})
			if err := s.Connect(context.Background()); err != nil {
				t.Fatal(err)
			}
			defer s.Close()
			if err := s.ConnInit(context.Background()); err != nil {
				t.Fatal(err)
			}
			select {
			case ev := <-s.Events():
				if ev.Unknown || ev.Type != TypeOutputText {
					t.Fatalf("event=%+v want parsed type10", ev)
				}
				if tc.wantNil {
					if ev.Text != nil {
						t.Fatalf("text=%+v want nil", ev.Text)
					}
					return
				}
				if ev.Text == nil {
					t.Fatal("text is nil")
				}
				if ev.Text.MsgID != tc.msgID || ev.Text.Content != tc.content || ev.Text.Timestamp != tc.ts {
					t.Fatalf("text=%+v", ev.Text)
				}
			case <-time.After(300 * time.Millisecond):
				t.Fatal("no type10 event")
			}
			if s.UnknownTypes() != 0 {
				t.Fatalf("type10 must not count as unknown, got %d", s.UnknownTypes())
			}
		})
	}
}

func TestConcurrentSendsUseUniqueSeq(t *testing.T) {
	const n = 50
	var mu sync.Mutex
	seqs := map[int64]bool{}
	read := make(chan struct{})
	h := newWSHarness(t, func(c *websocket.Conn) {
		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			if mt != websocket.TextMessage {
				continue
			}
			var s Signal
			_ = json.Unmarshal(data, &s)
			mu.Lock()
			seqs[s.SeqID] = true
			mu.Unlock()
			if s.Type == TypeConnInit {
				_ = c.WriteMessage(websocket.TextMessage, ackFrame(true, "", ""))
				continue
			}
			if len(seqs) >= n+1 {
				select {
				case <-read:
				default:
					close(read)
				}
			}
		}
	})
	s := NewSignaling("live-1", "conn-1", WSOptions{URL: h.url, QueueSize: 8, ConnInitInterval: 10 * time.Millisecond})
	if err := s.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.ConnInit(context.Background()); err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, n)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- s.SendInputText(context.Background(), "hi")
		}()
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("send: %v", err)
		}
	}
	select {
	case <-read:
	case <-time.After(2 * time.Second):
		mu.Lock()
		got := len(seqs)
		mu.Unlock()
		t.Fatalf("server received %d signals, want %d", got, n+1)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seqs) != n+1 {
		t.Fatalf("unique seq=%d want %d", len(seqs), n+1)
	}
}

// 心跳 10/20、conn_init 15×2s、close 5s、单帧写超时 3s、压缩关闭，全部以代码常量为准。
func TestSignalingDefaultsMatchBlueprint(t *testing.T) {
	s := NewSignaling("live-1", "comp-1", WSOptions{})
	if s.opt.PingInterval != 10*time.Second || s.opt.PingTimeout != 20*time.Second {
		t.Fatalf("ping=%v/%v", s.opt.PingInterval, s.opt.PingTimeout)
	}
	if s.opt.ConnInitRetries != 15 || s.opt.ConnInitInterval != 2*time.Second {
		t.Fatalf("conn_init=%d/%v", s.opt.ConnInitRetries, s.opt.ConnInitInterval)
	}
	if s.opt.CloseTimeout != 5*time.Second || s.opt.WriteTimeout != 3*time.Second {
		t.Fatalf("close/write=%v/%v", s.opt.CloseTimeout, s.opt.WriteTimeout)
	}
	if s.opt.QueueSize != 4 {
		t.Fatalf("queue=%d", s.opt.QueueSize)
	}
	if s.opt.Host != "api.vidu.cn" || s.opt.Path != "/live/v1/external-lives/{live_id}/stream" {
		t.Fatalf("endpoint=%s%s", s.opt.Host, s.opt.Path)
	}
	if s.opt.Dialer.EnableCompression {
		t.Fatalf("compression should be disabled for audio frames")
	}
}
