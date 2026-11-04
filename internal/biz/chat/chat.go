// Package chat LLM 对话编排（F15/F16/G06）：会话记忆按 session key 独立保存；
// system prompt 唯一真相源是 Persona，本包只经 ProfileResolver 只读注入（nil 容忍）；
// 流式短句按蓝本 merge_for_tts 语义攒到阈值再交下游 TTS 回调。
package chat

import (
	"context"
	"regexp"
	"strings"
	"sync"

	"github.com/teamwork/digital-human/internal/llm"
)

// 合并阈值照搬蓝本 llm.py:99 merge_for_tts：首块攒到 14 字即出（别拖慢开口），
// 之后每块攒到 30 字再发 TTS，降低整段 GPU 合成调用频次。
const (
	DefaultMergeFirstChars = 14
	DefaultMergeMinChars   = 30
)

// DefaultMaxHistoryTurns 会话记忆窗口。蓝本 history deque maxlen=48（≈24 轮）与
// history_turns=15 之间无单一明确 N，取 20 轮；超出按 2 条/轮从最旧截断。
const DefaultMaxHistoryTurns = 20

// ProfileResolver 只读人设接口（v3 约束）：system prompt 唯一真相源是 Persona，
// 本包不从 settings 读 system prompt；resolver 为 nil 或返回空串时无 system 消息。
type ProfileResolver interface {
	SystemPrompt(ctx context.Context) string
}

// Streamer 抽象 LLM 流式客户端（由 internal/llm.Client 实现），便于 fake 注入。
type Streamer interface {
	Stream(ctx context.Context, messages []llm.Message) (<-chan llm.Sentence, error)
}

// SentenceSink 下游 TTS 回调：接收 merge_for_tts 合并后的合成单元；
// 返回错误即终止本次对话且该轮不写入记忆。
type SentenceSink func(ctx context.Context, text string) error

type Option func(*Manager)

// WithMaxHistoryTurns 覆盖记忆窗口（测试用）。
func WithMaxHistoryTurns(n int) Option {
	return func(m *Manager) {
		if n > 0 {
			m.maxTurns = n
		}
	}
}

// WithMergeThresholds 覆盖合并阈值（测试用）。
func WithMergeThresholds(first, minChars int) Option {
	return func(m *Manager) {
		if first > 0 {
			m.mergeFirst = first
		}
		if minChars > 0 {
			m.mergeMin = minChars
		}
	}
}

// Manager 会话编排器；并发安全。
type Manager struct {
	llm      Streamer
	resolver ProfileResolver

	mu       sync.Mutex
	sessions map[string][]llm.Message
	maxTurns int
	mergeFirst,
	mergeMin int
}

func NewManager(s Streamer, r ProfileResolver, opts ...Option) *Manager {
	m := &Manager{
		llm:        s,
		resolver:   r,
		sessions:   map[string][]llm.Message{},
		maxTurns:   DefaultMaxHistoryTurns,
		mergeFirst: DefaultMergeFirstChars,
		mergeMin:   DefaultMergeMinChars,
	}
	for _, o := range opts {
		o(m)
	}
	return m
}

// Chat 进行一轮对话：组装 system(可选)+历史+本轮 user → 流式消费 → 合并交 sink。
// 返回本轮完整回复文本（strip_think 后）。流式/下游失败时返回错误且不写入记忆。
func (m *Manager) Chat(ctx context.Context, key, userText string, sink SentenceSink) (string, error) {
	return m.ChatWithTokenHook(ctx, key, userText, sink, nil)
}

// ChatWithTokenHook 在首个真实 LLM 文本增量到达时回调，供语音回合测量首 token 延迟。
func (m *Manager) ChatWithTokenHook(ctx context.Context, key, userText string, sink SentenceSink, onToken func()) (string, error) {
	m.mu.Lock()
	hist := append([]llm.Message(nil), m.sessions[key]...)
	m.mu.Unlock()

	msgs := make([]llm.Message, 0, len(hist)+2)
	if m.resolver != nil {
		if sp := strings.TrimSpace(m.resolver.SystemPrompt(ctx)); sp != "" {
			msgs = append(msgs, llm.Message{Role: "system", Content: sp})
		}
	}
	msgs = append(msgs, hist...)
	msgs = append(msgs, llm.Message{Role: "user", Content: userText})

	stream, err := m.llm.Stream(ctx, msgs)
	if err != nil {
		return "", err
	}

	mg := &merger{first: m.mergeFirst, min: m.mergeMin}
	var full strings.Builder
	var sinkErr error
	tokenSeen := false
	for s := range stream {
		if s.Err != nil {
			return "", s.Err
		}
		if s.Text != "" && !tokenSeen {
			tokenSeen = true
			if onToken != nil {
				onToken()
			}
		}
		full.WriteString(s.Text)
		if sinkErr != nil {
			continue // sink 已失败：继续抽干通道，避免 llm 侧 goroutine 阻塞
		}
		for _, chunk := range mg.feed(s.Text) {
			if err := sink(ctx, chunk); err != nil {
				sinkErr = err
				break
			}
		}
	}
	if sinkErr == nil {
		if tail := mg.flush(); tail != "" {
			sinkErr = sink(ctx, tail)
		}
	}
	if sinkErr != nil {
		return "", sinkErr
	}

	// 蓝本 llm.py:280-285：strip_think 后的非空回复才写入记忆。
	reply := stripThink(full.String())
	if reply != "" {
		m.mu.Lock()
		h := append(m.sessions[key],
			llm.Message{Role: "user", Content: userText},
			llm.Message{Role: "assistant", Content: reply})
		if over := len(h) - m.maxTurns*2; over > 0 {
			h = append([]llm.Message(nil), h[over:]...)
		}
		m.sessions[key] = h
		m.mu.Unlock()
	}
	return reply, nil
}

// ResetHistory 清空指定会话记忆，返回真实清除的轮数（为 G07 接线准备）。
func (m *Manager) ResetHistory(key string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	h, ok := m.sessions[key]
	if !ok {
		return 0
	}
	delete(m.sessions, key)
	return len(h) / 2
}

// merger 流式版 merge_for_tts（蓝本 llm.py:99-123）：短句攒到阈值才放出，
// 首块用 first（14），之后用 min（30），下限 max(6, target)。
type merger struct {
	buf     []rune
	emitted bool
	first   int
	min     int
}

func (m *merger) feed(s string) []string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	m.buf = append(m.buf, []rune(s)...)
	target := m.min
	if !m.emitted {
		target = m.first
	}
	if target < 6 {
		target = 6
	}
	if len(m.buf) < target {
		return nil
	}
	out := []string{string(m.buf)}
	m.buf = m.buf[:0]
	m.emitted = true
	return out
}

func (m *merger) flush() string {
	if len(m.buf) == 0 {
		return ""
	}
	out := string(m.buf)
	m.buf = nil
	return out
}

// thinkRE 复刻蓝本 _THINK_RE：思维链不写入记忆（re.S|re.I）。
var thinkRE = regexp.MustCompile(`(?is)<think>.*?</think>`)

func stripThink(text string) string {
	return strings.TrimSpace(thinkRE.ReplaceAllString(text, ""))
}
