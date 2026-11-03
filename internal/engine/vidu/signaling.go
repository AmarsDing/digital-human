package vidu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// 信令类型（蓝本 src/vidu/signaling.py:39-44）。
const (
	TypeConnInit           = 1
	TypeConnInitAck        = 2
	TypeHangup             = 5
	TypeForceHangup        = 6
	TypeInterrupt          = 7
	TypeInputText          = 9
	TypeOutputText         = 10
	TypePromptOperation    = 11
	TypePromptOperationAck = 12
	TypePromptOperationErr = 14
	// TypeWSClosed 是本地补的事件：无论对端正常关闭还是异常，都必须让上层知道会话已断。
	TypeWSClosed = -1
)

// 心跳与初始化参数以代码为准（蓝本注释的 5s/15s 不作数）：
// websockets.connect(ping_interval=10, ping_timeout=20, close_timeout=5)。
const (
	DefaultPingInterval     = 10 * time.Second
	DefaultPingTimeout      = 20 * time.Second
	DefaultCloseTimeout     = 5 * time.Second
	DefaultConnInitRetries  = 15
	DefaultConnInitInterval = 2 * time.Second
	DefaultWriteTimeout     = 3 * time.Second
	DefaultSignalQueue      = 4
	// NOT_READY 是唯一可重试(重发 conn_init，不重连)的错误码。
	connInitRetryableCode = "NOT_READY"
)

const (
	defaultHost = "api.vidu.cn"
	defaultPath = "/live/v1/external-lives/{live_id}/stream"
)

var (
	// ErrNotReady 表示 conn_init 未成功，音频/type7/type11 被丢弃并计数。
	ErrNotReady = errors.New("vidu: signaling not ready; frame dropped")
	// ErrClosed 表示信令已关闭。
	ErrClosed = errors.New("vidu: signaling closed")
)

// ConnInitError 是 conn_init 终态失败。注意：调用方不得自动重建会话
// （NOT_RESTART 语义；§4.6）——避免重复计费，交由上层显式决定。
type ConnInitError struct {
	Code      string
	Message   string
	Attempts  int
	Exhausted bool
}

func (e *ConnInitError) Error() string {
	return fmt.Sprintf("vidu: conn_init failed code=%s attempts=%d msg=%s", e.Code, e.Attempts, e.Message)
}

// Signal 是上行/下行文本帧。
type Signal struct {
	Type    int             `json:"type"`
	LiveID  string          `json:"live_id"`
	ConnID  string          `json:"conn_id"`
	SeqID   int64           `json:"seq_id"`
	Payload json.RawMessage `json:"payload"`
}

// CloseInfo 记录“谁先关、什么码”，用于 ws.closed 事件取证。
type CloseInfo struct {
	Who    string `json:"who"` // local | peer | timeout | write
	Code   int    `json:"code"`
	Reason string `json:"reason"`
	Error  string `json:"error"`
}

// OutputText 是 type10 口播字幕：句子文本/序号/时间戳。
type OutputText struct {
	MsgID     string `json:"msg_id"`
	Content   string `json:"content"`
	Timestamp int64  `json:"timestamp"`
}

// Event 是下行事件；Type=-1 表示连接关闭。
type Event struct {
	Type    int
	Payload json.RawMessage
	Unknown bool
	Close   *CloseInfo
	Raw     json.RawMessage
	// Text 仅 TypeOutputText（type10 口播字幕）非空。
	Text *OutputText
}

type WSOptions struct {
	URL    string // 完整 ws(s):// URL，可用 {live_id} 占位；为空时按 Host/Path 拼
	Host   string
	Path   string
	Header http.Header

	PingInterval     time.Duration
	PingTimeout      time.Duration
	CloseTimeout     time.Duration
	ConnInitRetries  int
	ConnInitInterval time.Duration
	WriteTimeout     time.Duration
	QueueSize        int

	// OnPong 在每次收到对端 pong 时回调（引擎层据此统计 ws_pong_ms）。
	OnPong func(time.Time)

	Dialer *websocket.Dialer
}

type outMsg struct {
	binary bool
	data   []byte
	resp   chan error
}

type ackResult struct {
	Success   bool
	ErrorCode string
	ErrorMsg  string
}

// Signaling 保证单写者：所有 Write 只发生在 writer goroutine。
type Signaling struct {
	opt       WSOptions
	conn      *websocket.Conn
	liveID    string
	connID    string
	seq       atomic.Int64
	initOK    atomic.Bool
	drops     atomic.Int64
	unknown   atomic.Int64
	out       chan outMsg
	events    chan Event
	ackCh     chan ackResult
	done      chan struct{}
	closing   atomic.Bool
	closeOnce sync.Once

	infoMu sync.Mutex
	info   CloseInfo
}

func NewSignaling(liveID, connID string, opt WSOptions) *Signaling {
	if opt.Host == "" {
		opt.Host = defaultHost
	}
	if opt.Path == "" {
		opt.Path = defaultPath
	}
	if opt.PingInterval <= 0 {
		opt.PingInterval = DefaultPingInterval
	}
	if opt.PingTimeout <= 0 {
		opt.PingTimeout = DefaultPingTimeout
	}
	if opt.CloseTimeout <= 0 {
		opt.CloseTimeout = DefaultCloseTimeout
	}
	if opt.ConnInitRetries <= 0 {
		opt.ConnInitRetries = DefaultConnInitRetries
	}
	if opt.ConnInitInterval <= 0 {
		opt.ConnInitInterval = DefaultConnInitInterval
	}
	if opt.WriteTimeout <= 0 {
		opt.WriteTimeout = DefaultWriteTimeout
	}
	if opt.QueueSize <= 0 {
		opt.QueueSize = DefaultSignalQueue
	}
	if opt.Dialer == nil {
		// 压缩关闭：音频帧压缩只会增加 CPU 与延迟。
		d := *websocket.DefaultDialer
		d.EnableCompression = false
		opt.Dialer = &d
	}
	if connID == "" {
		connID = "comp-" + strconv.FormatInt(time.Now().UnixNano()/1e9, 10)
	}
	return &Signaling{
		opt:    opt,
		liveID: liveID,
		connID: connID,
		out:    make(chan outMsg, opt.QueueSize),
		events: make(chan Event, 32),
		ackCh:  make(chan ackResult, 8),
		done:   make(chan struct{}),
	}
}

func (s *Signaling) Events() <-chan Event { return s.events }
func (s *Signaling) InitOK() bool         { return s.initOK.Load() }
func (s *Signaling) NotReadyDrops() int64 { return s.drops.Load() }
func (s *Signaling) UnknownTypes() int64  { return s.unknown.Load() }
func (s *Signaling) ConnID() string       { return s.connID }
func (s *Signaling) CloseInfo() CloseInfo {
	s.infoMu.Lock()
	defer s.infoMu.Unlock()
	return s.info
}

func (s *Signaling) endpoint() string {
	raw := s.opt.URL
	if raw == "" {
		raw = "wss://" + s.opt.Host + s.opt.Path
	}
	raw = strings.ReplaceAll(raw, "{live_id}", s.liveID)
	if u, err := url.Parse(raw); err == nil {
		q := u.Query()
		if q.Get("conn_id") == "" {
			q.Set("conn_id", s.connID)
			u.RawQuery = q.Encode()
		}
		return u.String()
	}
	if !strings.Contains(raw, "conn_id=") {
		sep := "?"
		if strings.Contains(raw, "?") {
			sep = "&"
		}
		raw += sep + "conn_id=" + url.QueryEscape(s.connID)
	}
	return raw
}

// Connect 建连并启动读/写循环；不自动 conn_init。
func (s *Signaling) Connect(ctx context.Context) error {
	header := s.opt.Header
	if header == nil {
		header = http.Header{}
	}
	conn, resp, err := s.opt.Dialer.DialContext(ctx, s.endpoint(), header)
	if err != nil {
		if resp != nil {
			return fmt.Errorf("vidu: ws dial %d: %w", resp.StatusCode, err)
		}
		return fmt.Errorf("vidu: ws dial: %w", err)
	}
	s.conn = conn
	// 单帧上限 1MB：超大帧视为对端异常，直接断连而不是耗尽内存。
	conn.SetReadLimit(1 << 20)
	conn.SetPongHandler(func(string) error {
		if s.opt.OnPong != nil {
			s.opt.OnPong(time.Now())
		}
		return conn.SetReadDeadline(time.Now().Add(s.opt.PingTimeout))
	})
	if err := conn.SetReadDeadline(time.Now().Add(s.opt.PingTimeout)); err != nil {
		_ = conn.Close()
		return err
	}
	go s.writer()
	go s.readLoop()
	return nil
}

// ConnInit 重发 conn_init 直到成功；NOT_READY 可重试且不重连 WS，
// 其他错误码立即失败。15 次仍不成功则返回 Exhausted。
func (s *Signaling) ConnInit(ctx context.Context) error {
	lastCode := ""
	for attempt := 1; attempt <= s.opt.ConnInitRetries; attempt++ {
		if s.closing.Load() {
			return ErrClosed
		}
		if err := s.sendSignal(ctx, TypeConnInit, map[string]any{"conn_init": map[string]any{"version": 1}}); err != nil {
			return err
		}
		select {
		case ack := <-s.ackCh:
			if ack.Success {
				s.initOK.Store(true)
				return nil
			}
			lastCode = ack.ErrorCode
			if lastCode == "" {
				lastCode = "unknown"
			}
			if lastCode == connInitRetryableCode {
				// 每次 conn_init 之间按 2s 间隔（生产值），避免把 15 次挤在瞬间。
				select {
				case <-ctx.Done():
					return ctx.Err()
				case <-time.After(s.opt.ConnInitInterval):
				case <-s.done:
					return ErrClosed
				}
				continue // 只重发，不重连
			}
			return &ConnInitError{Code: lastCode, Message: ack.ErrorMsg, Attempts: attempt}
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(s.opt.ConnInitInterval):
			lastCode = "ack_timeout"
			continue
		case <-s.done:
			return ErrClosed
		}
	}
	return &ConnInitError{Code: lastCode, Message: "conn_init 多次未就绪", Attempts: s.opt.ConnInitRetries, Exhausted: true}
}

// SendPCM 发送裸 PCM（24k/s16le/mono）。未就绪时丢弃并计数，绝不假装成功。
func (s *Signaling) SendPCM(ctx context.Context, pcm []byte) error {
	if !s.initOK.Load() {
		s.drops.Add(1)
		return ErrNotReady
	}
	return s.enqueue(ctx, outMsg{binary: true, data: pcm})
}

func (s *Signaling) Interrupt(ctx context.Context) error {
	if !s.initOK.Load() {
		s.drops.Add(1)
		return ErrNotReady
	}
	return s.sendSignal(ctx, TypeInterrupt, map[string]any{})
}

func (s *Signaling) PromptOperation(ctx context.Context, opType string, images []map[string]any) error {
	if !s.initOK.Load() {
		s.drops.Add(1)
		return ErrNotReady
	}
	payload := map[string]any{"op_type": opType}
	if len(images) > 0 {
		payload["images"] = images
	}
	return s.sendSignal(ctx, TypePromptOperation, map[string]any{"prompt_operation": payload})
}

func (s *Signaling) SendInputText(ctx context.Context, text string) error {
	if !s.initOK.Load() {
		s.drops.Add(1)
		return ErrNotReady
	}
	return s.sendSignal(ctx, TypeInputText, map[string]any{"text_msg": map[string]any{
		"msg_id": "in-" + strconv.FormatInt(s.seq.Load()+1, 10), "content": text, "timestamp": time.Now().UnixMilli(),
	}})
}

func (s *Signaling) SendOutputText(ctx context.Context, text string) error {
	if !s.initOK.Load() {
		s.drops.Add(1)
		return ErrNotReady
	}
	return s.sendSignal(ctx, TypeOutputText, map[string]any{"text_msg": map[string]any{
		"msg_id": "out-" + strconv.FormatInt(s.seq.Load()+1, 10), "content": text, "timestamp": time.Now().UnixMilli(),
	}})
}

func (s *Signaling) Hangup(ctx context.Context, reason string) error {
	return s.sendSignal(ctx, TypeHangup, map[string]any{"hangup": map[string]any{"hangup_reason": reason}})
}

func (s *Signaling) sendSignal(ctx context.Context, typ int, payload map[string]any) error {
	seq := s.seq.Add(1)
	rawPayload, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	msg, err := json.Marshal(Signal{
		Type: typ, LiveID: s.liveID, ConnID: s.connID, SeqID: seq, Payload: rawPayload,
	})
	if err != nil {
		return err
	}
	return s.enqueue(ctx, outMsg{data: msg})
}

func (s *Signaling) enqueue(ctx context.Context, m outMsg) error {
	if s.closing.Load() {
		return ErrClosed
	}
	m.resp = make(chan error, 1)
	select {
	case s.out <- m:
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return ErrClosed
	}
	select {
	case err := <-m.resp:
		return err
	case <-ctx.Done():
		return ctx.Err()
	case <-s.done:
		return ErrClosed
	}
}

func (s *Signaling) writer() {
	ticker := time.NewTicker(s.opt.PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-s.done:
			return
		case m := <-s.out:
			_ = s.conn.SetWriteDeadline(time.Now().Add(s.opt.WriteTimeout))
			var err error
			if m.binary {
				err = s.conn.WriteMessage(websocket.BinaryMessage, m.data)
			} else {
				err = s.conn.WriteMessage(websocket.TextMessage, m.data)
			}
			m.resp <- err
			if err != nil {
				s.terminate(CloseInfo{Who: "write", Error: err.Error()})
				return
			}
		case <-ticker.C:
			if err := s.conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(s.opt.WriteTimeout)); err != nil {
				s.terminate(CloseInfo{Who: "ping", Error: err.Error()})
				return
			}
		}
	}
}

func (s *Signaling) readLoop() {
	var info CloseInfo
	for {
		msgType, data, err := s.conn.ReadMessage()
		if err != nil {
			switch {
			case s.closing.Load():
				info = CloseInfo{Who: "local"}
			case errors.Is(err, context.DeadlineExceeded) || isTimeout(err):
				info = CloseInfo{Who: "timeout", Error: err.Error()}
			default:
				info = CloseInfo{Who: "peer", Error: err.Error()}
				var ce *websocket.CloseError
				if errors.As(err, &ce) {
					info.Code = ce.Code
					info.Reason = ce.Text
				}
			}
			break
		}
		_ = s.conn.SetReadDeadline(time.Now().Add(s.opt.PingTimeout))
		// 服务端理论上不下发二进制；收到也忽略，但仍视为“有帧”续期。
		if msgType != websocket.TextMessage {
			continue
		}
		var sig Signal
		if err := json.Unmarshal(data, &sig); err != nil {
			s.unknown.Add(1)
			continue
		}
		if sig.Type == TypeConnInitAck {
			var p struct {
				Ack struct {
					Success   bool   `json:"success"`
					ErrorCode string `json:"error_code"`
					ErrorMsg  string `json:"error_msg"`
				} `json:"conn_init_ack"`
			}
			_ = json.Unmarshal(sig.Payload, &p)
			select {
			case s.ackCh <- ackResult{Success: p.Ack.Success, ErrorCode: p.Ack.ErrorCode, ErrorMsg: p.Ack.ErrorMsg}:
			default:
			}
			continue
		}
		ev := Event{Type: sig.Type, Payload: sig.Payload, Raw: data}
		switch sig.Type {
		case TypeForceHangup, TypeHangup, TypePromptOperationAck, TypePromptOperationErr:
		case TypeOutputText:
			// type10 口播字幕：显式解析为独立事件类型，不再落入 Unknown。
			var p struct {
				TextMsg OutputText `json:"text_msg"`
			}
			if err := json.Unmarshal(sig.Payload, &p); err == nil {
				txt := p.TextMsg
				ev.Text = &txt
			}
		default:
			ev.Unknown = true
			s.unknown.Add(1)
		}
		select {
		case s.events <- ev:
		case <-s.done:
		}
	}
	s.terminate(info)
	s.infoMu.Lock()
	final := s.info
	s.infoMu.Unlock()
	// close 事件必须送达：缓冲满时腾出一个最旧的普通事件，绝不丢 close。
	closed := Event{Type: TypeWSClosed, Close: &final}
	select {
	case s.events <- closed:
	default:
		select {
		case <-s.events:
		default:
		}
		s.events <- closed
	}
	close(s.events)
}

func isTimeout(err error) bool {
	type timeout interface{ Timeout() bool }
	var t timeout
	return errors.As(err, &t) && t.Timeout()
}

func (s *Signaling) terminate(info CloseInfo) {
	s.closeOnce.Do(func() {
		s.infoMu.Lock()
		if s.info.Who == "" {
			s.info = info
		}
		s.infoMu.Unlock()
		close(s.done)
		if s.conn != nil {
			_ = s.conn.Close()
		}
	})
}

// Close 本地关闭；仍会由 readLoop 补一个 type=-1 事件。
func (s *Signaling) Close() error {
	s.closing.Store(true)
	s.terminate(CloseInfo{Who: "local"})
	return nil
}
