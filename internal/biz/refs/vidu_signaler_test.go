package refs

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

	"github.com/teamwork/digital-human/internal/engine/vidu"
)

// wsServer 是最小 fake Vidu 信令服务端：conn_init 即 ack，记录全部上行文本帧。
type wsServer struct {
	srv *httptest.Server
	url string

	mu     sync.Mutex
	frames []vidu.Signal
}

func newWSServer(t *testing.T) *wsServer {
	t.Helper()
	f := &wsServer{}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			if mt != websocket.TextMessage {
				continue
			}
			var s vidu.Signal
			if err := json.Unmarshal(data, &s); err != nil {
				continue
			}
			f.mu.Lock()
			f.frames = append(f.frames, s)
			f.mu.Unlock()
			if s.Type == vidu.TypeConnInit {
				p, _ := json.Marshal(map[string]any{"conn_init_ack": map[string]any{
					"success": true, "error_code": "", "error_msg": "",
				}})
				ack, _ := json.Marshal(vidu.Signal{Type: vidu.TypeConnInitAck, Payload: p})
				_ = c.WriteMessage(websocket.TextMessage, ack)
			}
		}
	}))
	f.url = "ws" + strings.TrimPrefix(f.srv.URL, "http")
	t.Cleanup(f.srv.Close)
	return f
}

func (f *wsServer) find(typ int) *vidu.Signal {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i := range f.frames {
		if f.frames[i].Type == typ {
			return &f.frames[i]
		}
	}
	return nil
}

func waitFrame(t *testing.T, f *wsServer, typ int) *vidu.Signal {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if s := f.find(typ); s != nil {
			return s
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting type%d frame", typ)
	return nil
}

// TestViduSignalerForwardsType11 G3-1：真实适配器把 SetPrompt 桥到 fake signaling
// server 的 type11 报文，op/images 字段如实透传。
func TestViduSignalerForwardsType11(t *testing.T) {
	srv := newWSServer(t)
	sig := vidu.NewSignaling("live-1", "", vidu.WSOptions{
		URL: srv.url, ConnInitRetries: 3, ConnInitInterval: 10 * time.Millisecond,
	})
	if err := sig.Connect(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer sig.Close()
	if err := sig.ConnInit(context.Background()); err != nil {
		t.Fatal(err)
	}

	vs := NewViduSignaler(sig)
	err := vs.SetPrompt(context.Background(), "switch", []Image{
		{ImageID: "a1", URL: "vidu://r1"},
		{URL: "vidu://r2"},
	})
	if err != nil {
		t.Fatal(err)
	}

	frame := waitFrame(t, srv, vidu.TypePromptOperation)
	var payload struct {
		PromptOperation struct {
			OpType string           `json:"op_type"`
			Images []map[string]any `json:"images"`
		} `json:"prompt_operation"`
	}
	if err := json.Unmarshal(frame.Payload, &payload); err != nil {
		t.Fatalf("payload decode: %v", err)
	}
	if payload.PromptOperation.OpType != "switch" {
		t.Fatalf("op_type=%q", payload.PromptOperation.OpType)
	}
	imgs := payload.PromptOperation.Images
	if len(imgs) != 2 || imgs[0]["image_id"] != "a1" || imgs[0]["url"] != "vidu://r1" || imgs[1]["url"] != "vidu://r2" {
		t.Fatalf("images=%+v", imgs)
	}
	if _, ok := imgs[1]["image_id"]; ok {
		t.Fatalf("空 image_id 不得出现在报文: %+v", imgs[1])
	}
}

// TestViduSignalerNilOperatorFailsClosed real 模式引擎无 type11 出口时
// （UnavailableController），SetPrompt 如实返回 ErrNoSignal，绝不静默成功。
func TestViduSignalerNilOperatorFailsClosed(t *testing.T) {
	vs := NewViduSignaler(nil)
	err := vs.SetPrompt(context.Background(), "switch", []Image{{URL: "vidu://r1"}})
	if !errors.Is(err, ErrNoSignal) {
		t.Fatalf("err=%v want ErrNoSignal", err)
	}
}
