package server

import (
	"context"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/teamwork/digital-human/pkg/eventbus"
)

func readWSEvent(t *testing.T, conn *websocket.Conn) eventbus.Event {
	t.Helper()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	var e eventbus.Event
	if err := conn.ReadJSON(&e); err != nil {
		t.Fatalf("read ws event: %v", err)
	}
	return e
}

// 故障注入：WS 客户端断连期间业务继续发事件；带 after_seq 与新 ticket 重连后，
// 必须补齐断连窗口内的全部事件，且不重发已收到的（seq 严格连续、无重复）。
func TestWSReconnectAfterSeqBackfillsMissedEvents(t *testing.T) {
	const sessionID = "s-recon"
	f := startWSServer(t)
	defer f.stop()
	ctx := context.Background()

	// 连接 1：正常收 seq 1、2。
	ticket1, _, err := f.tickets.Issue(sessionID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	dialer1 := websocket.Dialer{Subprotocols: []string{eventSubprotocol, "ticket." + ticket1}}
	conn1 := dialWS(t, &dialer1, f.wsURL+"?session_id="+sessionID)

	for i := 1; i <= 2; i++ {
		f.bus.Publish(ctx, sessionID, "test.evt", 1, map[string]any{"n": i})
	}
	e1 := readWSEvent(t, conn1)
	e2 := readWSEvent(t, conn1)
	if e1.Seq != 1 || e2.Seq != 2 || e1.SessionID != sessionID {
		t.Fatalf("首连收到 seq=%d,%d want 1,2", e1.Seq, e2.Seq)
	}

	// 客户端断开；断连窗口内业务再发 3 条（服务端是否已察觉断连不影响结论，
	// 补齐以 bus 历史为准）。
	_ = conn1.Close()
	for i := 3; i <= 5; i++ {
		f.bus.Publish(ctx, sessionID, "test.evt", 1, map[string]any{"n": i})
	}

	// 新 ticket 重连，带 after_seq=2。
	ticket2, _, err := f.tickets.Issue(sessionID, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	dialer2 := websocket.Dialer{Subprotocols: []string{eventSubprotocol, "ticket." + ticket2}}
	conn2 := dialWS(t, &dialer2, f.wsURL+"?session_id="+sessionID+"&after_seq=2")
	defer conn2.Close()

	seen := map[int64]bool{}
	for want := int64(3); want <= 5; want++ {
		e := readWSEvent(t, conn2)
		if e.Seq != want || e.SessionID != sessionID {
			t.Fatalf("补齐事件 seq=%d session=%q, want seq=%d", e.Seq, e.SessionID, want)
		}
		if got := int(e.Payload["n"].(float64)); got != int(want) {
			t.Fatalf("补齐事件 payload n=%d, want %d", got, want)
		}
		if seen[e.Seq] {
			t.Fatalf("重复事件 seq=%d", e.Seq)
		}
		seen[e.Seq] = true
	}

	// 无重复/无多余：短窗口内不得再有事件到达。
	if err := conn2.SetReadDeadline(time.Now().Add(400 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	var extra eventbus.Event
	if err := conn2.ReadJSON(&extra); err == nil {
		t.Fatalf("重连后收到重复/多余事件: %+v", extra)
	}
}
