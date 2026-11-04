package publish

// PCMBridge 只接收公开采集页的 Vidu RTC 远端音轨，不接收原始 TTS/歌曲 PCM。
// 浏览器 WS 与 FFmpeg 之间以 48kHz/mono/s16le/20ms 帧交换；RTC 尚未就绪时
// 输出全零帧以维持编码时钟。全零只代表传输占位，不构成 audio_delivery 证据。

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"sync"
	"time"
)

const (
	PCMFrameBytes = 1920
	pcmFrameMS    = 20
)

var (
	ErrCollectorActive = errors.New("publish: 当前场次已有 RTC 音频采集者")
	ErrStaleCollector  = errors.New("publish: 旧 session/epoch 采集者已失效")
	ErrPCMFrameSize    = errors.New("publish: RTC PCM 帧必须为 1920 字节")
	ErrPCMSequence     = errors.New("publish: RTC PCM sequence 必须单调递增")
	ErrPCMBackpressure = errors.New("publish: RTC PCM 桥接拥塞")
)

// PCMCollector 是一次 WS 握手获得的采集者身份；指针同时用于防旧 epoch 写入。
type PCMCollector struct {
	id        uint64
	sessionID string
	epoch     int64
	lastSeq   uint32
	hasSeq    bool
}

// PCMStats 分离传输活性与内容非零证据；均不代表 Vidu/抖音观众端可听。
type PCMStats struct {
	LastPCMAt     time.Time
	LastNonzeroAt time.Time
}

// PCMBridge 单场单采集者桥接。
type PCMBridge struct {
	mu        sync.Mutex
	collector *PCMCollector
	nextID    uint64
	frames    chan []byte
	stats     PCMStats
	listener  net.Listener
	conn      net.Conn
	cancel    context.CancelFunc
}

func NewPCMBridge() *PCMBridge {
	return &PCMBridge{frames: make(chan []byte, 128)}
}

func (b *PCMBridge) Attach(sessionID string, epoch int64) (*PCMCollector, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.collector != nil {
		return nil, ErrCollectorActive
	}
	b.nextID++
	c := &PCMCollector{id: b.nextID, sessionID: sessionID, epoch: epoch}
	b.collector = c
	b.stats = PCMStats{}
	b.drainLocked()
	return c, nil
}

func (b *PCMBridge) Detach(c *PCMCollector) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.collector == c {
		b.collector = nil
		b.drainLocked()
	}
}

func (b *PCMBridge) Write(c *PCMCollector, sequence uint32, pcm []byte) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.collector != c {
		return ErrStaleCollector
	}
	if len(pcm) != PCMFrameBytes {
		return ErrPCMFrameSize
	}
	if c.hasSeq && sequence <= c.lastSeq {
		return ErrPCMSequence
	}
	c.lastSeq, c.hasSeq = sequence, true
	frame := append([]byte(nil), pcm...)
	now := time.Now()
	b.stats.LastPCMAt = now
	for _, v := range pcm {
		if v != 0 {
			b.stats.LastNonzeroAt = now
			break
		}
	}
	select {
	case b.frames <- frame:
		return nil
	default:
		return ErrPCMBackpressure
	}
}

func (b *PCMBridge) Stats() PCMStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.stats
}

// Start 在 loopback 监听一次性 FFmpeg TCP 输入，返回不含密钥的 tcp:// 地址。
func (b *PCMBridge) Start(ctx context.Context) (string, error) {
	b.mu.Lock()
	if b.listener != nil {
		b.mu.Unlock()
		return "", errors.New("publish: PCM bridge 已启动")
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		b.mu.Unlock()
		return "", err
	}
	runCtx, cancel := context.WithCancel(ctx)
	b.listener, b.cancel = ln, cancel
	b.mu.Unlock()
	go b.accept(runCtx, ln)
	return "tcp://" + ln.Addr().String(), nil
}

func (b *PCMBridge) accept(ctx context.Context, ln net.Listener) {
	acceptCh := make(chan net.Conn, 1)
	go func() {
		conn, err := ln.Accept()
		if err == nil {
			acceptCh <- conn
		}
	}()
	select {
	case <-ctx.Done():
	case conn := <-acceptCh:
		b.mu.Lock()
		if ctx.Err() != nil {
			b.mu.Unlock()
			_ = conn.Close()
			return
		}
		b.conn = conn
		b.mu.Unlock()
		b.outputLoop(ctx, conn)
	}
}

func (b *PCMBridge) outputLoop(ctx context.Context, conn net.Conn) {
	ticker := time.NewTicker(pcmFrameMS * time.Millisecond)
	defer ticker.Stop()
	zero := make([]byte, PCMFrameBytes)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var frame []byte
			select {
			case frame = <-b.frames:
			default:
				frame = zero
			}
			_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
			if _, err := io.Copy(conn, bytes.NewReader(frame)); err != nil {
				return
			}
		}
	}
}

func (b *PCMBridge) Close() {
	b.mu.Lock()
	cancel, ln, conn := b.cancel, b.listener, b.conn
	b.cancel, b.listener, b.conn = nil, nil, nil
	b.drainLocked()
	b.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if ln != nil {
		_ = ln.Close()
	}
	if conn != nil {
		_ = conn.Close()
	}
}

func (b *PCMBridge) drainLocked() {
	for {
		select {
		case <-b.frames:
		default:
			return
		}
	}
}
