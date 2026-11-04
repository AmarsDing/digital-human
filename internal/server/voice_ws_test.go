package server

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"testing"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/gorilla/websocket"

	"github.com/teamwork/digital-human/internal/biz/chat"
	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/biz/voice"
	"github.com/teamwork/digital-human/internal/conf"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

type voiceTestSpeaker struct{}

func (voiceTestSpeaker) Snapshot() live.Snapshot {
	return live.Snapshot{SessionID: "s-voice", Epoch: 3, Phase: live.PhaseLive}
}
func (voiceTestSpeaker) Say(ctx context.Context, _, _ string) (string, error) {
	live.NotifyFirstPCM(ctx)
	return "u-voice", nil
}

type voiceTestChat struct{}

func (voiceTestChat) ChatWithTokenHook(ctx context.Context, _ string, _ string, sink chat.SentenceSink, token func()) (string, error) {
	token()
	if err := sink(ctx, "语音回应"); err != nil {
		return "", err
	}
	return "语音回应", nil
}

type voiceFixture struct {
	base    string
	wsURL   string
	tickets *MemoryVoiceTicketStore
	bus     *eventbus.Bus
	stop    func()
}

func startVoiceServer(t *testing.T) *voiceFixture {
	t.Helper()
	return startVoiceServerWithChat(t, voiceTestChat{})
}

func startVoiceServerWithChat(t *testing.T, chatter voice.Chat) *voiceFixture {
	t.Helper()
	cfg := &conf.Config{Security: conf.Security{Mode: "local", TicketTTL: time.Minute}}
	bus := eventbus.New(64)
	vcfg := voice.DefaultConfig()
	coord := voice.NewCoordinator(voice.NewFakeRecognizer("测试识别"), nil, chatter, voiceTestSpeaker{}, bus, vcfg)
	status := &voice.StatusService{ASR: voice.NewFakeRecognizer("测试识别"), Config: vcfg,
		LLMConfigured: func(context.Context) bool { return true }}
	tickets := NewMemoryVoiceTicketStore()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := khttp.NewServer(khttp.Listener(lis), khttp.Timeout(0), khttp.Filter(timeoutFilter(15*time.Second)))
	registerVoiceRoutes(srv, cfg, coord, status, tickets)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	return &voiceFixture{
		base: "http://" + lis.Addr().String(), wsURL: "ws://" + lis.Addr().String() + "/ws/v1/voice",
		tickets: tickets, bus: bus,
		stop: func() { cancel(); _ = srv.Stop(context.Background()); <-done },
	}
}

func (f *voiceFixture) ticket(t *testing.T, session, client string) string {
	t.Helper()
	ticket, _, err := f.tickets.Issue(session, client, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	return ticket
}

func dialVoice(t *testing.T, rawURL, ticket string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	dialer := websocket.Dialer{Subprotocols: []string{voiceSubprotocol, "ticket." + ticket}}
	return dialer.Dial(rawURL, nil)
}

func TestVoiceWSRequiresBoundSingleUseTicket(t *testing.T) {
	f := startVoiceServer(t)
	defer f.stop()
	ticket := f.ticket(t, "s-voice", "client-a")
	for _, query := range []string{"session_id=s-other&client_id=client-a", "session_id=s-voice&client_id=client-b"} {
		conn, resp, err := dialVoice(t, f.wsURL+"?"+query, ticket)
		if conn != nil {
			conn.Close()
		}
		if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("query %q: conn=%v resp=%v err=%v", query, conn, resp, err)
		}
		if resp != nil {
			resp.Body.Close()
		}
	}
	// 前两次握手校验失败不烧票；第三次合法握手成功。
	conn, _, err := dialVoice(t, f.wsURL+"?session_id=s-voice&client_id=client-a", ticket)
	if err != nil {
		t.Fatal(err)
	}
	if conn.Subprotocol() != voiceSubprotocol {
		t.Fatalf("subprotocol = %q", conn.Subprotocol())
	}
	conn.Close()
	// 成功握手后复用必须 401。
	replay, resp, err := dialVoice(t, f.wsURL+"?session_id=s-voice&client_id=client-a", ticket)
	if replay != nil {
		replay.Close()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("ticket replay resp=%v err=%v", resp, err)
	}
	resp.Body.Close()
}

func TestVoiceWSFakeFullChain(t *testing.T) {
	f := startVoiceServer(t)
	defer f.stop()
	ticket := f.ticket(t, "s-voice", "client-a")
	conn, _, err := dialVoice(t, f.wsURL+"?session_id=s-voice&client_id=client-a", ticket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	begin := map[string]any{
		"type": "begin", "client_turn_id": "client-turn-1", "mode": "ptt", "format": "pcm_s16le",
		"sample_rate": 16000, "channels": 1, "frame_ms": 20,
	}
	if err := conn.WriteJSON(begin); err != nil {
		t.Fatal(err)
	}
	_, raw, err := conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	var accepted map[string]any
	if json.Unmarshal(raw, &accepted) != nil || accepted["type"] != "accepted" || accepted["turn_id"] == "" {
		t.Fatalf("accepted = %s", raw)
	}
	for i := 0; i < 20; i++ {
		if err := conn.WriteMessage(websocket.BinaryMessage, loudVoiceFrame()); err != nil {
			t.Fatal(err)
		}
	}
	var partial map[string]any
	for {
		_, raw, err = conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if json.Unmarshal(raw, &partial) != nil {
			t.Fatalf("invalid pre-final event = %s", raw)
		}
		if partial["type"] == "voice.partial" {
			break
		}
		if partial["type"] != "voice.listening" {
			t.Fatalf("unexpected pre-final event = %s", raw)
		}
	}
	if partial["text"] != "测试识别" {
		t.Fatalf("partial = %v", partial)
	}
	if err := conn.WriteJSON(map[string]any{"type": "end", "frames": 20}); err != nil {
		t.Fatal(err)
	}
	_, raw, err = conn.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(raw, &accepted) != nil || accepted["type"] != "accepted" {
		t.Fatalf("end accepted = %s", raw)
	}
	seen := map[string]bool{}
	for {
		if err := conn.SetReadDeadline(time.Now().Add(2 * time.Second)); err != nil {
			t.Fatal(err)
		}
		_, raw, err = conn.ReadMessage()
		if err != nil {
			t.Fatalf("voice events over voice WS: %v", err)
		}
		var event map[string]any
		if json.Unmarshal(raw, &event) != nil {
			t.Fatalf("invalid voice event = %s", raw)
		}
		typ, _ := event["type"].(string)
		if typ == "voice.error" {
			t.Fatalf("voice error: %v", event)
		}
		if typ == "voice.speaking" && event["utterance_id"] != "u-voice" {
			t.Fatalf("speaking = %v", event)
		}
		if typ == "voice.first_audio" {
			timings, _ := event["timings"].(map[string]any)
			if timings["speech_end_to_first_audio_ms"] == nil {
				t.Fatalf("first audio timings = %v", event)
			}
		}
		seen[typ] = true
		if typ == "voice.completed" {
			if event["reason"] != "completed" {
				t.Fatalf("completed = %v", event)
			}
			for _, required := range []string{"voice.final", "voice.thinking", "voice.speaking", "voice.first_audio"} {
				if !seen[required] {
					t.Fatalf("missing %s before completed; seen=%v", required, seen)
				}
			}
			return
		}
	}
}

func TestVoiceWSRejectsFrameCountMismatch(t *testing.T) {
	f := startVoiceServer(t)
	defer f.stop()
	ticket := f.ticket(t, "s-voice", "client-a")
	conn, _, err := dialVoice(t, f.wsURL+"?session_id=s-voice&client_id=client-a", ticket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.WriteJSON(map[string]any{"type": "begin", "mode": "ptt", "format": "pcm_s16le", "sample_rate": 16000, "channels": 1, "frame_ms": 20})
	_, _, _ = conn.ReadMessage()
	if err := conn.WriteMessage(websocket.BinaryMessage, loudVoiceFrame()); err != nil {
		t.Fatal(err)
	}
	_, _, _ = conn.ReadMessage() // partial
	_ = conn.WriteJSON(map[string]any{"type": "end", "frames": 2})
	var rejected map[string]any
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		if json.Unmarshal(raw, &rejected) != nil {
			t.Fatalf("invalid mismatch event = %s", raw)
		}
		if rejected["type"] == "rejected" {
			break
		}
		if rejected["type"] != "voice.partial" && !(rejected["type"] == "voice.error" && rejected["error_code"] == "audio_frame_mismatch") {
			t.Fatalf("unexpected mismatch event = %v", rejected)
		}
	}
	if rejected["error"] != "audio_frame_mismatch" {
		t.Fatalf("rejected = %v", rejected)
	}
}

type voiceBlockingChat struct {
	started   chan struct{}
	cancelled chan struct{}
}

func (f *voiceBlockingChat) ChatWithTokenHook(ctx context.Context, _ string, _ string, _ chat.SentenceSink, _ func()) (string, error) {
	close(f.started)
	<-ctx.Done()
	close(f.cancelled)
	return "", ctx.Err()
}

func TestVoiceWSDisconnectCancelsInflightLLM(t *testing.T) {
	chatter := &voiceBlockingChat{started: make(chan struct{}), cancelled: make(chan struct{})}
	f := startVoiceServerWithChat(t, chatter)
	defer f.stop()
	ticket := f.ticket(t, "s-voice", "client-a")
	conn, _, err := dialVoice(t, f.wsURL+"?session_id=s-voice&client_id=client-a", ticket)
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.WriteJSON(map[string]any{
		"type": "begin", "mode": "ptt", "format": "pcm_s16le", "sample_rate": 16000, "channels": 1, "frame_ms": 20,
	}); err != nil {
		t.Fatal(err)
	}
	_, _, _ = conn.ReadMessage() // accepted
	for i := 0; i < 20; i++ {
		if err := conn.WriteMessage(websocket.BinaryMessage, loudVoiceFrame()); err != nil {
			t.Fatal(err)
		}
	}
	if err := conn.WriteJSON(map[string]any{"type": "end", "frames": 20}); err != nil {
		t.Fatal(err)
	}
	for {
		_, raw, err := conn.ReadMessage()
		if err != nil {
			t.Fatal(err)
		}
		var event map[string]any
		if json.Unmarshal(raw, &event) != nil {
			t.Fatalf("invalid finalizing event = %s", raw)
		}
		if event["type"] == "accepted" && event["stage"] == "finalizing" {
			break
		}
	}
	select {
	case <-chatter.started:
	case <-time.After(time.Second):
		t.Fatal("LLM did not start")
	}
	_ = conn.Close()
	select {
	case <-chatter.cancelled:
	case <-time.After(time.Second):
		t.Fatal("WS disconnect did not cancel in-flight LLM")
	}
}

func TestVoiceWSRejectsOriginWithoutConsumingTicket(t *testing.T) {
	f := startVoiceServer(t)
	defer f.stop()
	ticket := f.ticket(t, "s-voice", "client-a")
	dialer := websocket.Dialer{Subprotocols: []string{voiceSubprotocol, "ticket." + ticket}}
	header := http.Header{"Origin": []string{"https://evil.example"}}
	conn, resp, err := dialer.Dial(f.wsURL+"?session_id=s-voice&client_id=client-a", header)
	if conn != nil {
		conn.Close()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("disallowed origin resp=%v err=%v", resp, err)
	}
	if resp != nil {
		resp.Body.Close()
	}
	valid, _, err := dialVoice(t, f.wsURL+"?session_id=s-voice&client_id=client-a", ticket)
	if err != nil {
		t.Fatalf("origin rejection consumed ticket: %v", err)
	}
	valid.Close()
}

func loudVoiceFrame() []byte {
	pcm := make([]byte, 640)
	for i := 0; i < len(pcm); i += 2 {
		pcm[i], pcm[i+1] = 0x10, 0x27
	}
	return pcm
}

func TestVoiceErrorClassification(t *testing.T) {
	if !errors.Is(voice.ErrASRUnavailable, voice.ErrASRUnavailable) {
		t.Fatal("sentinel error must remain classifiable")
	}
}
