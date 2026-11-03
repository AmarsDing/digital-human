// Package tts 是 OmniVoice Python 边车的 HTTP 契约客户端（§5.2 v2）。
// 契约是新写的：Go 不感知 Gradio/session_hash，只认 /v1/jobs 等端点。
package tts

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	PathHealth  = "/v1/health"
	PathVoices  = "/v1/voices"
	PathJobs    = "/v1/jobs"
	PathPreview = "/v1/preview"

	// 排队上限 8（蓝本 engine.py:1568 Queue(maxsize=8)）。
	MaxQueue = 8
	// Go 端总 deadline 120s；边车内部 110s 终止（§5.2 超时）。
	DefaultJobTimeout     = 120 * time.Second
	DefaultPollInterval   = 200 * time.Millisecond
	DefaultPreviewTimeout = 10 * time.Second
	DefaultHealthTimeout  = 3 * time.Second
	DefaultModelWaitMax   = 60 * time.Second
	DefaultRetry429Max    = 30 * time.Second
	DefaultRetryInterval  = 2 * time.Second
	// 连续 3 次失败/60s → 熔断 60s。
	DefaultBreakerThreshold = 3
	DefaultBreakerWindow    = 60 * time.Second
	DefaultBreakerCooldown  = 60 * time.Second
)

// 边车状态。
const (
	StateQueued    = "queued"
	StateRunning   = "running"
	StateDone      = "done"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

// 错误码：沿用边车契约里的 error_code；视频面板直接展示。
const (
	CodeTextTooLong   = "text_too_long"
	CodeModelLoading  = "model_loading"
	CodeSynthFailed   = "synth_failed"
	CodeTimeout       = "timeout"
	CodeQueueFull     = "queue_full"
	CodeAuthFailed    = "sidecar_auth_failed"
	CodeNotFound      = "job_not_found"
	CodeAudioNotReady = "audio_not_ready"
	CodeBadResponse   = "sidecar_bad_response"
)

var (
	ErrCircuitOpen = errors.New("tts: sidecar circuit open")
)

type APIError struct {
	Code       string
	Status     int
	Message    string
	RetryAfter time.Duration
}

func (e *APIError) Error() string {
	return fmt.Sprintf("tts %s (status=%d): %s", e.Code, e.Status, e.Message)
}

type Options struct {
	BaseURL string // 默认 http://127.0.0.1:9880
	Token   string // X-Sidecar-Token
	HTTP    *http.Client

	JobTimeout     time.Duration
	PollInterval   time.Duration
	PreviewTimeout time.Duration
	HealthTimeout  time.Duration
	ModelWaitMax   time.Duration
	Retry429Max    time.Duration
	RetryInterval  time.Duration

	BreakerThreshold int
	BreakerWindow    time.Duration
	BreakerCooldown  time.Duration
}

type Client struct {
	opt     Options
	breaker *breaker
}

func NewClient(opt Options) *Client {
	if opt.BaseURL == "" {
		opt.BaseURL = "http://127.0.0.1:9880"
	}
	if opt.HTTP == nil {
		opt.HTTP = &http.Client{}
	}
	if opt.JobTimeout <= 0 {
		opt.JobTimeout = DefaultJobTimeout
	}
	if opt.PollInterval <= 0 {
		opt.PollInterval = DefaultPollInterval
	}
	if opt.PreviewTimeout <= 0 {
		opt.PreviewTimeout = DefaultPreviewTimeout
	}
	if opt.HealthTimeout <= 0 {
		opt.HealthTimeout = DefaultHealthTimeout
	}
	if opt.ModelWaitMax <= 0 {
		opt.ModelWaitMax = DefaultModelWaitMax
	}
	if opt.Retry429Max <= 0 {
		opt.Retry429Max = DefaultRetry429Max
	}
	if opt.RetryInterval <= 0 {
		opt.RetryInterval = DefaultRetryInterval
	}
	if opt.BreakerThreshold <= 0 {
		opt.BreakerThreshold = DefaultBreakerThreshold
	}
	if opt.BreakerWindow <= 0 {
		opt.BreakerWindow = DefaultBreakerWindow
	}
	if opt.BreakerCooldown <= 0 {
		opt.BreakerCooldown = DefaultBreakerCooldown
	}
	return &Client{
		opt: opt,
		breaker: &breaker{
			threshold: opt.BreakerThreshold,
			window:    opt.BreakerWindow,
			cooldown:  opt.BreakerCooldown,
		},
	}
}

type Health struct {
	OK      bool   `json:"ok"`
	Model   string `json:"model"`
	Device  string `json:"device"`
	Queue   int    `json:"queue"`
	VRAMMB  int    `json:"vram_mb"`
	Version string `json:"version"`
}

type JobRequest struct {
	Text      string  `json:"text"`
	VoiceID   string  `json:"voice_id"`
	Speed     float64 `json:"speed,omitempty"`
	Format    string  `json:"format"`
	RequestID string  `json:"request_id,omitempty"`
}

type JobRef struct {
	JobID    string `json:"job_id"`
	QueuePos int    `json:"queue_pos"`
}

type JobStatus struct {
	State      string `json:"state"`
	ErrorCode  string `json:"error_code,omitempty"`
	DurationMS int64  `json:"duration_ms,omitempty"`
}

func (c *Client) url(path string) string {
	return strings.TrimRight(c.opt.BaseURL, "/") + path
}

func (c *Client) headers() map[string]string {
	h := map[string]string{"Content-Type": "application/json"}
	if c.opt.Token != "" {
		h["X-Sidecar-Token"] = c.opt.Token
	}
	return h
}

// Health 探测边车；模型未加载时返回 ok=false（不视为 error）。
func (c *Client) Health(ctx context.Context) (Health, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opt.HealthTimeout)
	defer cancel()
	var h Health
	if err := c.json(ctx, http.MethodGet, c.url(PathHealth), nil, &h, false); err != nil {
		return h, err
	}
	return h, nil
}

// WaitHealthy 在模型加载期间等待（最多 ModelWaitMax，§5.2 失败语义）。
func (c *Client) WaitHealthy(ctx context.Context, max time.Duration) error {
	if max <= 0 {
		max = c.opt.ModelWaitMax
	}
	deadline := time.Now().Add(max)
	var last error
	for {
		h, err := c.Health(ctx)
		if err == nil && h.OK {
			return nil
		}
		if err != nil {
			last = err
		} else {
			last = &APIError{Code: CodeModelLoading, Message: "model not loaded"}
		}
		if !time.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(c.opt.PollInterval):
		}
	}
	return last
}

// Submit 提交合成任务；429 会阻塞重试（不丢句），最多 30s；熔断打开时直接失败。
func (c *Client) Submit(ctx context.Context, req JobRequest) (*JobRef, error) {
	if !c.breaker.allow(time.Now()) {
		return nil, ErrCircuitOpen
	}
	if req.Format == "" {
		req.Format = "wav"
	}
	body, _ := json.Marshal(req)
	deadline := time.Now().Add(c.opt.Retry429Max)
	for {
		var out JobRef
		status, err := c.do(ctx, http.MethodPost, c.url(PathJobs), body, &out)
		if err == nil {
			c.breaker.success()
			if out.JobID == "" {
				return nil, &APIError{Code: CodeBadResponse, Status: status, Message: "missing job_id"}
			}
			return &out, nil
		}
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Code == CodeQueueFull {
			wait := apiErr.RetryAfter
			if wait <= 0 {
				wait = c.opt.RetryInterval
			}
			remain := time.Until(deadline)
			if remain <= 0 {
				c.breaker.fail(time.Now())
				return nil, err
			}
			if wait > remain {
				wait = remain
			}
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(wait):
			}
			continue
		}
		c.breaker.fail(time.Now())
		return nil, err
	}
}

// Status 查询一次任务状态。
func (c *Client) Status(ctx context.Context, jobID string) (JobStatus, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opt.HealthTimeout)
	defer cancel()
	var st JobStatus
	err := c.json(ctx, http.MethodGet, c.url(PathJobs+"/"+jobID), nil, &st, false)
	return st, err
}

// WaitJob 轮询到 done/failed/cancelled；超时/取消时尽力逻辑取消边车任务。
func (c *Client) WaitJob(ctx context.Context, jobID string) (JobStatus, error) {
	deadline := time.Now().Add(c.opt.JobTimeout)
	for {
		st, err := c.Status(ctx, jobID)
		if err != nil {
			c.breaker.fail(time.Now())
			return st, err
		}
		switch st.State {
		case StateDone:
			c.breaker.success()
			return st, nil
		case StateFailed:
			c.breaker.fail(time.Now())
			code := st.ErrorCode
			if code == "" {
				code = CodeSynthFailed
			}
			return st, &APIError{Code: code, Message: "job failed"}
		case StateCancelled:
			return st, &APIError{Code: "cancelled", Message: "job cancelled"}
		}
		if !time.Now().Before(deadline) {
			c.Cancel(context.Background(), jobID) // 逻辑取消；不等 GPU 抢占
			c.breaker.fail(time.Now())
			return st, &APIError{Code: CodeTimeout, Message: "job exceeded 120s"}
		}
		select {
		case <-ctx.Done():
			c.Cancel(context.Background(), jobID)
			return st, ctx.Err()
		case <-time.After(c.opt.PollInterval):
		}
	}
}

// Cancel 总是 200 语义；queued 立即 cancelled，running 标记 cancel_requested。
func (c *Client) Cancel(ctx context.Context, jobID string) (bool, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opt.HealthTimeout)
	defer cancel()
	var out struct {
		Cancelled bool `json:"cancelled"`
	}
	if err := c.json(ctx, http.MethodPost, c.url(PathJobs+"/"+jobID+":cancel"), []byte(`{}`), &out, false); err != nil {
		return false, err
	}
	return out.Cancelled, nil
}

// Audio 取 wav；409=未完成、410=失败。
func (c *Client) Audio(ctx context.Context, jobID string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opt.JobTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url(PathJobs+"/"+jobID+"/audio"), nil)
	if err != nil {
		return nil, err
	}
	for k, v := range c.headers() {
		req.Header.Set(k, v)
	}
	resp, err := c.opt.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &APIError{Code: CodeTimeout, Message: ctx.Err().Error()}
		}
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<20))
	if resp.StatusCode == http.StatusConflict {
		return nil, &APIError{Code: CodeAudioNotReady, Status: resp.StatusCode, Message: "job not finished"}
	}
	if resp.StatusCode == http.StatusGone {
		return nil, c.classify(resp.StatusCode, raw, resp.Header.Get("Retry-After"))
	}
	if resp.StatusCode >= 400 {
		return nil, c.classify(resp.StatusCode, raw, resp.Header.Get("Retry-After"))
	}
	if len(raw) == 0 {
		return nil, &APIError{Code: CodeBadResponse, Status: resp.StatusCode, Message: "empty wav"}
	}
	return raw, nil
}

// Preview 同步预览：<=200 字、10s 超时、返回 wav。
func (c *Client) Preview(ctx context.Context, text, voiceID string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opt.PreviewTimeout)
	defer cancel()
	body, _ := json.Marshal(map[string]any{"text": text, "voice_id": voiceID, "format": "wav"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url(PathPreview), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	for k, v := range c.headers() {
		req.Header.Set(k, v)
	}
	resp, err := c.opt.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, &APIError{Code: CodeTimeout, Message: ctx.Err().Error()}
		}
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if resp.StatusCode >= 400 {
		return nil, c.classify(resp.StatusCode, raw, resp.Header.Get("Retry-After"))
	}
	return raw, nil
}

// Synthesize 是 Submit→WaitJob→Audio 的便捷组合；任一步失败都会逻辑取消。
func (c *Client) Synthesize(ctx context.Context, req JobRequest) ([]byte, error) {
	ref, err := c.Submit(ctx, req)
	if err != nil {
		return nil, err
	}
	if _, err := c.WaitJob(ctx, ref.JobID); err != nil {
		_, _ = c.Cancel(context.Background(), ref.JobID)
		return nil, err
	}
	wav, err := c.Audio(ctx, ref.JobID)
	if err != nil {
		_, _ = c.Cancel(context.Background(), ref.JobID)
		return nil, err
	}
	return wav, nil
}

func (c *Client) json(ctx context.Context, method, rawURL string, body []byte, out any, retryOnce bool) error {
	status, err := c.do(ctx, method, rawURL, body, out)
	if err == nil {
		return nil
	}
	var apiErr *APIError
	if retryOnce && errors.As(err, &apiErr) && apiErr.Status >= 500 {
		status, err = c.do(ctx, method, rawURL, body, out)
	}
	_ = status
	return err
}

func (c *Client) do(ctx context.Context, method, rawURL string, body []byte, out any) (int, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	for k, v := range c.headers() {
		req.Header.Set(k, v)
	}
	resp, err := c.opt.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return 0, &APIError{Code: CodeTimeout, Message: ctx.Err().Error()}
		}
		return 0, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return resp.StatusCode, c.classify(resp.StatusCode, raw, resp.Header.Get("Retry-After"))
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return resp.StatusCode, &APIError{Code: CodeBadResponse, Status: resp.StatusCode, Message: err.Error()}
		}
	}
	return resp.StatusCode, nil
}

func (c *Client) classify(status int, raw []byte, retryAfter string) error {
	var body struct {
		ErrorCode string `json:"error_code"`
		Message   string `json:"message"`
	}
	_ = json.Unmarshal(raw, &body)
	code := body.ErrorCode
	if code == "" {
		switch status {
		case http.StatusUnprocessableEntity:
			code = CodeTextTooLong
		case http.StatusServiceUnavailable:
			code = CodeModelLoading
		case http.StatusTooManyRequests:
			code = CodeQueueFull
		case http.StatusUnauthorized, http.StatusForbidden:
			code = CodeAuthFailed
		case http.StatusNotFound:
			code = CodeNotFound
		default:
			if status >= 500 {
				code = CodeSynthFailed
			} else {
				code = CodeBadResponse
			}
		}
	}
	msg := body.Message
	if msg == "" {
		msg = strings.TrimSpace(string(raw))
		if len(msg) > 200 {
			msg = msg[:200]
		}
	}
	e := &APIError{Code: code, Status: status, Message: msg}
	if status == http.StatusTooManyRequests {
		e.RetryAfter = parseRetryAfter(retryAfter, raw)
	}
	return e
}

// parseRetryAfter 优先 HTTP 头（契约固定 Retry-After: 2），兼容 body.retry_after。
func parseRetryAfter(header string, raw []byte) time.Duration {
	if header != "" {
		if secs, err := strconv.Atoi(strings.TrimSpace(header)); err == nil && secs >= 0 {
			return time.Duration(secs) * time.Second
		}
	}
	var body struct {
		RetryAfter int `json:"retry_after"`
	}
	if err := json.Unmarshal(raw, &body); err == nil && body.RetryAfter > 0 {
		return time.Duration(body.RetryAfter) * time.Second
	}
	return 0
}

type breaker struct {
	mu        sync.Mutex
	threshold int
	window    time.Duration
	cooldown  time.Duration
	fails     int
	firstFail time.Time
	openUntil time.Time
}

func (b *breaker) allow(now time.Time) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return !now.Before(b.openUntil)
}

func (b *breaker) fail(now time.Time) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if now.Sub(b.firstFail) > b.window {
		b.fails = 0
		b.firstFail = now
	}
	if b.firstFail.IsZero() {
		b.firstFail = now
	}
	b.fails++
	if b.fails >= b.threshold {
		b.openUntil = now.Add(b.cooldown)
		b.fails = 0
		b.firstFail = time.Time{}
	}
}

func (b *breaker) success() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.fails = 0
	b.firstFail = time.Time{}
}
