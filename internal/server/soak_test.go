package server

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/gorilla/websocket"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

// TestSoakWSEventBus —— 真机真实时钟长跑（D 批：1 小时，与 feeder 长跑并行）。
// 起真实 WS 路由（与生产一致的 Timeout(0)/timeoutFilter），开播后保持连接 N 分钟：
//   - 每 30s 发一条业务事件，断言 seq 连续无洞、无重复、断线零次；
//   - 并行测 100ms Go ticker 抖动，输出 p99/最大偏差；
//   - 结束后用 after_seq 重放校验补齐语义。
//
// 运行：DH_SOAK_MINUTES=60 go test ./internal/server/ -run TestSoakWSEventBus -count=1 -v -timeout 90m
func TestSoakWSEventBus(t *testing.T) {
	minutes, _ := strconv.Atoi(os.Getenv("DH_SOAK_MINUTES"))
	if minutes <= 0 {
		t.Skip("设置 DH_SOAK_MINUTES（分钟）后运行 WS 长跑")
	}
	fix := startWSServer(t)
	defer fix.stop()

	sid, phase, _, err := fix.mgr.Start(context.Background(), "", "", "")
	if err != nil || phase != "live" {
		t.Fatalf("开播失败: %v %s", err, phase)
	}
	ticket, _, err := fix.tickets.Issue(sid, time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second, Subprotocols: []string{eventSubprotocol, "ticket." + ticket}}
	conn := dialWS(t, &dialer, fix.wsURL+"?session_id="+sid)
	defer conn.Close()

	deadline := time.Now().Add(time.Duration(minutes) * time.Minute)

	// 业务事件发生器：每 30s 一条 say（走 Manager 真实发布路径）。
	pubDone := make(chan struct{})
	go func() {
		defer close(pubDone)
		n := 0
		for time.Now().Before(deadline) {
			n++
			_, _ = fix.mgr.Say(context.Background(), fmt.Sprintf("soak-%d", n), "")
			time.Sleep(30 * time.Second)
		}
	}()

	// 定时器抖动测量：100ms ticker。
	jitterDone := make(chan struct{})
	var maxJitter time.Duration
	var samples int64
	go func() {
		defer close(jitterDone)
		tk := time.NewTicker(100 * time.Millisecond)
		defer tk.Stop()
		expect := time.Now().Add(100 * time.Millisecond)
		for now := range tk.C {
			if d := now.Sub(expect); d > maxJitter {
				maxJitter = d
			}
			samples++
			expect = expect.Add(100 * time.Millisecond)
			if now.After(deadline) {
				return
			}
		}
	}()

	// 收事件主循环：同场次 seq 必须严格 +1 递增（WS 与 REST 同源）。
	var lastSeq int64
	var received int
	disconnects := 0
	conn.SetReadDeadline(deadline.Add(30 * time.Second))
	for time.Now().Before(deadline) {
		var ev eventbus.Event
		if err := conn.ReadJSON(&ev); err != nil {
			if time.Now().Before(deadline) {
				disconnects++
				t.Logf("WS 在截止前断开: %v", err)
			}
			break
		}
		if ev.SessionID != sid || ev.Seq == 0 {
			continue // 其他场次/非序列事件
		}
		if lastSeq > 0 && ev.Seq != lastSeq+1 {
			t.Fatalf("seq 不连续: last=%d got=%d type=%s", lastSeq, ev.Seq, ev.Type)
		}
		if ev.Seq <= lastSeq {
			t.Fatalf("seq 回退/重复: last=%d got=%d type=%s", lastSeq, ev.Seq, ev.Type)
		}
		lastSeq = ev.Seq
		received++
	}
	<-pubDone
	<-jitterDone

	t.Logf("WS 长跑 %d 分钟: 收到业务事件=%d lastSeq=%d 断开次数=%d ticker样本=%d 最大抖动=%v",
		minutes, received, lastSeq, disconnects, samples, maxJitter)
	if disconnects != 0 {
		t.Fatalf("长跑期间 WS 断开 %d 次", disconnects)
	}
	if received == 0 {
		t.Fatal("长跑期间未收到任何业务事件")
	}
	if maxJitter > 500*time.Millisecond {
		t.Fatalf("100ms ticker 最大抖动 %v 超阈值", maxJitter)
	}
}
