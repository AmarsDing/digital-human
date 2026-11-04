package server

import (
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"testing"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/gorilla/websocket"
	"github.com/teamwork/digital-human/internal/conf"
	"github.com/teamwork/digital-human/internal/publish"
)

func TestRTCAudioWSConsumesTicketAndRejectsSecondCollector(t *testing.T) {
	cfg := &conf.Config{Security: conf.Security{Mode: "local", TicketTTL: time.Minute}}
	tickets := NewMemoryTicketStore(time.Minute)
	t.Cleanup(tickets.Close)
	bridge := publish.NewPCMBridge()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := khttp.NewServer(khttp.Listener(lis), khttp.Timeout(0), khttp.Filter(timeoutFilter(15*time.Second)))
	registerRTCAudioRoute(srv, cfg, tickets, bridge)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	defer func() { cancel(); _ = srv.Stop(context.Background()); <-done }()
	wsURL := "ws://" + lis.Addr().String() + "/ws/v1/rtc-audio?session_id=s-1&epoch=3"
	dial := func(ticket string) (*websocket.Conn, *http.Response, error) {
		d := websocket.Dialer{Subprotocols: []string{rtcAudioSubprotocol, "ticket." + ticket}}
		return d.Dial(wsURL, nil)
	}
	ticket, _, err := tickets.Issue("s-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	first, _, err := dial(ticket)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	frame := make([]byte, 4+publish.PCMFrameBytes)
	binary.BigEndian.PutUint32(frame, 7)
	frame[len(frame)-1] = 1
	if err := first.WriteMessage(websocket.BinaryMessage, frame); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for bridge.Stats().LastPCMAt.IsZero() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if bridge.Stats().LastNonzeroAt.IsZero() {
		t.Fatal("WS 二进制帧未进入 PCM bridge")
	}

	secondTicket, _, _ := tickets.Issue("s-1", time.Minute)
	second, _, err := dial(secondTicket)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	_ = second.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, _, err := second.ReadMessage(); !websocket.IsCloseError(err, websocket.ClosePolicyViolation) {
		t.Fatalf("第二采集者必须以 policy violation 关闭: %v", err)
	}

	replay, resp, err := dial(ticket)
	if replay != nil {
		replay.Close()
	}
	if err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("一次性票据复用必须 401: resp=%v err=%v", resp, err)
	}
	if resp != nil {
		resp.Body.Close()
	}
}
