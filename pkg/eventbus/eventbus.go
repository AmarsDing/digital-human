package eventbus

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/teamwork/digital-human/pkg/logx"
)

// Event 是事件信封。seq 在同一场次内单调递增，用于断线补齐；event_id 全局唯一用于幂等/去重。
type Event struct {
	Seq       int64          `json:"seq"`
	EventID   string         `json:"event_id"`
	SessionID string         `json:"session_id"`
	Type      string         `json:"type"`
	TsMs      int64          `json:"ts_ms"`
	Epoch     int64          `json:"epoch"`
	RequestID string         `json:"request_id,omitempty"`
	Payload   map[string]any `json:"payload,omitempty"`
}

type subscriber struct {
	ch     chan Event
	closed bool
}

// ScopeFunc 返回当前场次与 epoch，供各业务统一填充事件信封。
type ScopeFunc func() (sessionID string, epoch int64)

// Appender 事件持久化钩子。所有发布（含业务 PublishScoped）都经同一 sink，
// 保证 REST 列表（读 sink）与 WS 广播（读 bus 历史）同源。
type Appender interface {
	Append(ctx context.Context, e Event) error
}

// Bus 进程内事件总线：发布即写入环形历史并广播；历史用于 WS 断线补齐。
// seq 按 session 单调递增（换场次从 1 重新开始），无 session 的事件用全局 seq。
type Bus struct {
	mu           sync.Mutex
	seq          int64
	seqBySession map[string]int64
	scope        ScopeFunc
	appender     Appender
	hist         []Event
	maxHist      int
	subs         map[string]*subscriber
}

func New(maxHistory int) *Bus {
	if maxHistory <= 0 {
		maxHistory = 2048
	}
	return &Bus{
		maxHist:      maxHistory,
		subs:         map[string]*subscriber{},
		seqBySession: map[string]int64{},
	}
}

// SetScope 绑定当前场次来源；后续 PublishScoped 自动填充 session_id/epoch。
func (b *Bus) SetScope(fn ScopeFunc) {
	b.mu.Lock()
	b.scope = fn
	b.mu.Unlock()
}

// SetAppender 绑定持久化 sink；每条事件发布后异步顺序写入，REST 与 WS 因此同源。
func (b *Bus) SetAppender(a Appender) {
	b.mu.Lock()
	b.appender = a
	b.mu.Unlock()
}

// Publish 分配 seq 与 event_id、落历史、广播，并写入已绑定 sink。payload 不应含密钥。
func (b *Bus) Publish(ctx context.Context, sessionID, typ string, epoch int64, payload map[string]any) Event {
	b.mu.Lock()
	var seq int64
	if sessionID != "" {
		if b.seqBySession == nil {
			b.seqBySession = map[string]int64{}
		}
		b.seqBySession[sessionID]++
		seq = b.seqBySession[sessionID]
	} else {
		b.seq++
		seq = b.seq
	}
	e := Event{
		Seq:       seq,
		EventID:   uuid.NewString(),
		SessionID: sessionID,
		Type:      typ,
		TsMs:      time.Now().UnixMilli(),
		Epoch:     epoch,
		RequestID: logx.RequestID(ctx),
		Payload:   payload,
	}
	b.hist = append(b.hist, e)
	if len(b.hist) > b.maxHist {
		b.hist = b.hist[len(b.hist)-b.maxHist:]
	}
	for id, s := range b.subs {
		select {
		case s.ch <- e:
		default:
			// 慢消费者：关闭并移除，前端会用 after_seq 重连补齐。
			close(s.ch)
			s.closed = true
			delete(b.subs, id)
		}
	}
	appender := b.appender
	b.mu.Unlock()

	// 持久化在锁外执行，避免 sink I/O 阻塞广播；同一 sink 的顺序由调用方串行保证
	// （现阶段所有发布都在 Manager 的互斥范围内或单 goroutine 链路）。
	if appender != nil {
		_ = appender.Append(ctx, e)
	}
	return e
}

// Subscribe 从 afterSeq 之后开始接收；先补历史再收实时，保证不丢不重。
func (b *Bus) Subscribe(afterSeq int64) (<-chan Event, func()) {
	return b.SubscribeFor("", afterSeq)
}

// SubscribeFor 只补齐指定场次的历史与实时事件；sessionID 为空时接收全部。
func (b *Bus) SubscribeFor(sessionID string, afterSeq int64) (<-chan Event, func()) {
	b.mu.Lock()
	defer b.mu.Unlock()
	var backlog []Event
	for _, e := range b.hist {
		if sessionID != "" && e.SessionID != sessionID {
			continue
		}
		if e.Seq > afterSeq {
			backlog = append(backlog, e)
		}
	}
	ch := make(chan Event, len(backlog)+64)
	for _, e := range backlog {
		ch <- e
	}
	id := uuid.NewString()
	b.subs[id] = &subscriber{ch: ch}
	cancel := func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if s, ok := b.subs[id]; ok {
			delete(b.subs, id)
			if !s.closed {
				close(s.ch)
				s.closed = true
			}
		}
	}
	return ch, cancel
}

// Seq 无 session 事件的全局序号。
func (b *Bus) Seq() int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.seq
}

// SeqFor 指定场次的当前序号（用于 status / 事件游标）。
func (b *Bus) SeqFor(sessionID string) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if sessionID == "" {
		return b.seq
	}
	return b.seqBySession[sessionID]
}

// PublishScoped 按绑定的 ScopeFunc 填充 session_id/epoch 后发布。
// 未绑定 scope 时退化为无场次事件，便于单测直接构造 Bus。
func (b *Bus) PublishScoped(ctx context.Context, typ string, payload map[string]any) Event {
	b.mu.Lock()
	fn := b.scope
	b.mu.Unlock()
	var sessionID string
	var epoch int64
	if fn != nil {
		sessionID, epoch = fn()
	}
	return b.Publish(ctx, sessionID, typ, epoch, payload)
}
