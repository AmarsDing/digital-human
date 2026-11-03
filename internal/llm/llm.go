// Package llm 提供 OpenAI 兼容的流式 chat 客户端，复刻蓝本 src/llm.py 的 LLMClient：
// base_url 默认 DeepSeek；SSE 边到边分句逐句产出；同模型重试 2 次 → fallback_models
// 依次降级；全部失败返回含 lastErr 的明确错误；未配置 api_key 直接 fail-closed。
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	// DefaultBaseURL 对齐蓝本 llm.py:194 默认指向 DeepSeek 官方。
	DefaultBaseURL = "https://api.deepseek.com"
	// DefaultModel 对齐蓝本 llm.py:196。
	DefaultModel = "deepseek-chat"
	// DefaultHeaderTimeout 对齐蓝本 httpx.Timeout(30.0, read=300.0) 的连接/响应头上限；
	// 流式正文读取不设总上限，由调用方 ctx 控制生命周期。
	DefaultHeaderTimeout = 30 * time.Second

	// attemptsPerModel 同一模型的尝试次数（蓝本 llm.py:256 `for _attempt in range(2)`）。
	attemptsPerModel = 2
	// 蓝本 llm.py:275 重试退避：首次失败 0.45s，第二次 0.9s（随后换下一个模型）。
	backoffFirst  = 450 * time.Millisecond
	backoffSecond = 900 * time.Millisecond
)

// ErrLLMNotConfigured 未配置 api_key：fail-closed，绝不静默成功。
var ErrLLMNotConfigured = errors.New("llm_not_configured")

// Message 是 OpenAI chat 消息。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Sentence 是流式产出的一句完整文本；Err 非 nil 时为通道最后一条（终止错误，
// 含 ctx 取消与重试降级全部失败），此后通道关闭。
type Sentence struct {
	Text string
	Err  error
}

// Config 单次调用生效的配置快照。
type Config struct {
	BaseURL        string   // 空 → DefaultBaseURL
	APIKey         string   // 空 → Stream 返回 ErrLLMNotConfigured
	Model          string   // 空 → DefaultModel
	FallbackModels []string // 主模型重试耗尽后依次降级；空串与主模型自动剔除
	Temperature    float64  // 由调用方（settings）携带默认值，本包不兜底
	HeaderTimeout  time.Duration
}

func (c Config) withDefaults() Config {
	if strings.TrimSpace(c.BaseURL) == "" {
		c.BaseURL = DefaultBaseURL
	}
	if c.Model == "" {
		c.Model = DefaultModel
	}
	if c.HeaderTimeout <= 0 {
		c.HeaderTimeout = DefaultHeaderTimeout
	}
	return c
}

// Client 是 OpenAI 兼容流式客户端。confGet 每次调用取一次快照（对齐蓝本 conf_get，
// 热改配置立即生效）。
type Client struct {
	confGet func() Config
	// backoff 供测试替换；nil 时使用真实 ctx 感知睡眠。
	backoff func(ctx context.Context, d time.Duration) bool
}

func NewClient(confGet func() Config) *Client { return &Client{confGet: confGet} }

// NewStaticClient 用固定配置构造（测试/装配过渡期用）。
func NewStaticClient(cfg Config) *Client {
	return NewClient(func() Config { return cfg })
}

// Stream 发起流式调用：返回逐句产出的通道。api_key 未配置时直接返回
// ErrLLMNotConfigured（同步 fail-closed）；其余错误经 Sentence.Err 终止通道。
func (c *Client) Stream(ctx context.Context, messages []Message) (<-chan Sentence, error) {
	if len(messages) == 0 {
		return nil, errors.New("llm: messages 为空")
	}
	cfg := c.confGet().withDefaults()
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, ErrLLMNotConfigured
	}
	out := make(chan Sentence)
	go c.run(ctx, cfg, messages, out)
	return out, nil
}

func (c *Client) run(ctx context.Context, cfg Config, messages []Message, out chan<- Sentence) {
	defer close(out)
	send := func(s Sentence) bool { // false = ctx 已取消，接收方不会再读
		select {
		case out <- s:
			return true
		case <-ctx.Done():
			return false
		}
	}
	// fail 终止错误必须送达：ctx 已取消时 select 两条分支同时就绪会随机丢消息，
	// 这里直接阻塞发送（约定消费方读至通道关闭；Chat/collect 均满足）。
	fail := func(err error) {
		out <- Sentence{Err: err}
	}

	models := []string{cfg.Model}
	for _, m := range cfg.FallbackModels {
		if m != "" && m != cfg.Model {
			models = append(models, m)
		}
	}

	split := NewSentenceSplitter()
	var lastErr error
	for _, model := range models {
		ok := false
		for attempt := 0; attempt < attemptsPerModel; attempt++ {
			got, err := c.streamOnce(ctx, cfg, model, messages, split, send)
			if err == nil {
				ok = true
				break
			}
			if ctx.Err() != nil {
				fail(ctx.Err())
				return
			}
			// 蓝本 llm.py:269-271：已吐过字中途失败 / 非可重试错误 → 不重试不降级，直接抛。
			if got || !retryable(err) {
				fail(err)
				return
			}
			lastErr = err
			d := backoffFirst
			if attempt > 0 {
				d = backoffSecond
			}
			if !c.sleep(ctx, d) {
				fail(ctx.Err())
				return
			}
		}
		if ok {
			if tail := split.Flush(); tail != "" {
				send(Sentence{Text: tail})
			}
			return
		}
	}
	fail(fmt.Errorf("llm: 全部模型失败（共 %d 个）: %w", len(models), lastErr))
}

func (c *Client) sleep(ctx context.Context, d time.Duration) bool {
	if c.backoff != nil {
		return c.backoff(ctx, d)
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// streamOnce 执行单次 HTTP 流式请求；got 表示本次是否已收到过内容增量
// （蓝本 _got：一旦吐过字，后续失败不再重试）。
func (c *Client) streamOnce(ctx context.Context, cfg Config, model string, messages []Message, split *SentenceSplitter, send func(Sentence) bool) (got bool, err error) {
	body, err := json.Marshal(chatRequest{
		Model:       model,
		Messages:    messages,
		Stream:      true,
		Temperature: cfg.Temperature,
	})
	if err != nil {
		return false, err
	}
	url := strings.TrimRight(cfg.BaseURL, "/") + "/chat/completions"
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return false, err
	}
	req.Header.Set("Authorization", "Bearer "+cfg.APIKey)
	req.Header.Set("Content-Type", "application/json")

	hc := &http.Client{Transport: &http.Transport{ResponseHeaderTimeout: cfg.HeaderTimeout}}
	defer hc.CloseIdleConnections()
	resp, err := hc.Do(req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode >= http.StatusBadRequest {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		msg := strings.TrimSpace(string(b))
		if r := []rune(msg); len(r) > 300 { // 蓝本 text[:300]
			msg = string(r[:300])
		}
		return false, &httpError{status: resp.StatusCode, body: msg}
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(line[len("data:"):])
		if payload == "[DONE]" {
			return got, nil
		}
		var chunk chatChunk
		if json.Unmarshal([]byte(payload), &chunk) != nil || len(chunk.Choices) == 0 {
			continue
		}
		piece := chunk.Choices[0].Delta.Content
		if piece == "" {
			continue
		}
		got = true
		for _, s := range split.Feed(piece) {
			if !send(Sentence{Text: s}) {
				return got, ctx.Err()
			}
		}
	}
	if err := sc.Err(); err != nil {
		return got, err
	}
	// 对端在取消瞬间优雅断开会呈现为干净 EOF；取消必须传播，不能误判成功。
	if ctx.Err() != nil {
		return got, ctx.Err()
	}
	return got, nil
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream"`
	Temperature float64   `json:"temperature"`
}

type chatChunk struct {
	Choices []struct {
		Delta struct {
			Content string `json:"content"`
		} `json:"delta"`
	} `json:"choices"`
}

// httpError 携带 HTTP 状态码，供重试判定（蓝本 e.status）。
type httpError struct {
	status int
	body   string
}

func (e *httpError) Error() string { return fmt.Sprintf("llm http %d: %s", e.status, e.body) }

// Status 返回 HTTP 状态码（供测试与上层判定）。
func (e *httpError) Status() int { return e.status }

// retryable 对齐蓝本 llm.py:266-268：429/5xx 或网络超时/连接/读取错误可重试。
func retryable(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var he *httpError
	if errors.As(err, &he) {
		switch he.status {
		case 429, 500, 502, 503, 504:
			return true
		}
		return false
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return true
	}
	return errors.Is(err, io.ErrUnexpectedEOF)
}
