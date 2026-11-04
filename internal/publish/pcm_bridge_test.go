package publish

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

func TestPCMBridgeRejectsSecondCollectorStaleEpochAndBadFrames(t *testing.T) {
	b := NewPCMBridge()
	first, err := b.Attach("s-1", 2)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Attach("s-1", 2); !errors.Is(err, ErrCollectorActive) {
		t.Fatalf("第二采集者必须拒绝: %v", err)
	}
	b.Detach(first)

	current, err := b.Attach("s-1", 3)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Detach(current)
	if err := b.Write(first, 9, make([]byte, PCMFrameBytes)); !errors.Is(err, ErrStaleCollector) {
		t.Fatalf("旧 epoch 采集者帧必须拒绝: %v", err)
	}
	if err := b.Write(current, 1, make([]byte, PCMFrameBytes-1)); !errors.Is(err, ErrPCMFrameSize) {
		t.Fatalf("非 20ms 帧必须拒绝: %v", err)
	}
	if err := b.Write(current, 1, make([]byte, PCMFrameBytes)); err != nil {
		t.Fatal(err)
	}
	if err := b.Write(current, 1, make([]byte, PCMFrameBytes)); !errors.Is(err, ErrPCMSequence) {
		t.Fatalf("重复 sequence 必须拒绝: %v", err)
	}
}

func TestPCMBridgeSeparatesTransportAndNonzeroEvidence(t *testing.T) {
	b := NewPCMBridge()
	c, err := b.Attach("s-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Detach(c)
	if err := b.Write(c, 1, make([]byte, PCMFrameBytes)); err != nil {
		t.Fatal(err)
	}
	st := b.Stats()
	if st.LastPCMAt.IsZero() || !st.LastNonzeroAt.IsZero() {
		t.Fatalf("全零帧只能证明传输活性: %+v", st)
	}
	nonzero := make([]byte, PCMFrameBytes)
	binary.LittleEndian.PutUint16(nonzero, 1000)
	if err := b.Write(c, 2, nonzero); err != nil {
		t.Fatal(err)
	}
	st = b.Stats()
	if st.LastNonzeroAt.IsZero() || st.LastNonzeroAt.Before(st.LastPCMAt) {
		t.Fatalf("非零证据时间无效: %+v", st)
	}
}

func TestPCMBridgeLoopbackTCPOutputsSilenceThenRemotePCM(t *testing.T) {
	b := NewPCMBridge()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	rawURL, err := b.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(rawURL, "tcp://"), 2*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if err := conn.SetReadDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}

	first := make([]byte, PCMFrameBytes)
	if _, err := io.ReadFull(conn, first); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first, make([]byte, PCMFrameBytes)) {
		t.Fatal("RTC 尚未就绪时桥接应按时钟输出全零帧")
	}

	c, err := b.Attach("s-1", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Detach(c)
	pcm := make([]byte, PCMFrameBytes)
	for i := range pcm {
		pcm[i] = byte(i + 1)
	}
	if err := b.Write(c, 1, pcm); err != nil {
		t.Fatal(err)
	}
	for {
		got := make([]byte, PCMFrameBytes)
		if _, err := io.ReadFull(conn, got); err != nil {
			t.Fatal(err)
		}
		if bytes.Equal(got, make([]byte, PCMFrameBytes)) {
			continue
		}
		if !bytes.Equal(got, pcm) {
			t.Fatal("桥接输出不是采集者提供的原始 PCM 帧")
		}
		break
	}
}
