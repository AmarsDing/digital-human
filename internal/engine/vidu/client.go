// Package vidu 是 Vidu 云渲染的纯客户端层（HTTP + WebSocket 信令）。
// 只依赖 net/http/gorilla，不依赖 Ent/框架，便于用 httptest 与本地 fake WS 做契约测试。
package vidu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// 端点（蓝本 src/vidu/client.py:66-198 与官方文档）。
const (
	PathCreateComponent = "/live/s_avatar/component"
	PathLive            = "/live/v1/lives/" // + {id}
	PathRecording       = "/live/v1/lives/%s/recording"
	PathLives           = "/live/v1/lives"
	PathCredits         = "/ent/v2/credits"
	PathUploads         = "/tools/v2/files/uploads"
	PathUploadFinish    = "/tools/v2/files/uploads/%s/finish"
)

// 内部错误码（§5.3.2）。这些会写进 LiveSession.error_code 与 UI 文案。
const (
	CodeBadRequest        = "vidu_bad_request"
	CodeAuthFailed        = "vidu_auth_failed"
	CodeInsufficient      = "vidu_insufficient_credits"
	CodeActionForbidden   = "vidu_action_forbidden"
	CodeForbidden         = "vidu_forbidden"
	CodeLiveNotFound      = "vidu_live_not_found"
	CodeRateLimited       = "vidu_rate_limited"
	CodeUpstreamError     = "vidu_upstream_error"
	CodeCreateTimeout     = "vidu_create_timeout"
	CodeNetwork           = "vidu_network"
	CodeUploadETagMissing = "vidu_upload_etag_missing"
	CodeUploadNoURI       = "vidu_upload_no_uri"
)

// 端点超时（§5.3.1）。
const (
	DefaultCreateTimeout       = 120 * time.Second
	DefaultLiveTimeout         = 8 * time.Second
	DefaultRecordingTimeout    = 15 * time.Second
	DefaultListTimeout         = 8 * time.Second
	DefaultCreditsTimeout      = 8 * time.Second
	DefaultUploadStartTimeout  = 60 * time.Second
	DefaultUploadPUTTimeout    = 300 * time.Second
	DefaultUploadFinishTimeout = 60 * time.Second
	DefaultRetryDelay          = time.Second
)

// APIError 保留上游 code/message 前 500 字，便于面板显示“被谁拒了”。
type APIError struct {
	Code            string
	Status          int
	Message         string
	UpstreamCode    string
	UpstreamMessage string
	RetryAfter      time.Duration
	// ActionForbidden 对应“创建时带 action 被 403”，引擎据此去掉 action 重建一次。
	ActionForbidden bool
	// Network 表示 DNS/TLS/连接失败，Timeout 表示上下文超时。
	Network bool
	Timeout bool
}

func (e *APIError) Error() string {
	if e.UpstreamCode != "" {
		return fmt.Sprintf("vidu %s (status=%d code=%s): %s", e.Code, e.Status, e.UpstreamCode, e.UpstreamMessage)
	}
	return fmt.Sprintf("vidu %s (status=%d): %s", e.Code, e.Status, e.Message)
}

// IsRetryable 仅用于 GET 的一次重试；POST 创建永不重试。
func (e *APIError) IsRetryable() bool {
	return e.Network || e.Status >= 500
}

type Options struct {
	BaseURL    string // 默认 https://api.vidu.cn
	APIKey     string
	CreatePath string
	HTTP       *http.Client

	CreateTimeout       time.Duration
	LiveTimeout         time.Duration
	RecordingTimeout    time.Duration
	ListTimeout         time.Duration
	CreditsTimeout      time.Duration
	UploadStartTimeout  time.Duration
	UploadPUTTimeout    time.Duration
	UploadFinishTimeout time.Duration
	RetryDelay          time.Duration
}

type Client struct {
	opt Options
}

func NewClient(opt Options) *Client {
	if opt.BaseURL == "" {
		opt.BaseURL = "https://api.vidu.cn"
	}
	if opt.CreatePath == "" {
		opt.CreatePath = PathCreateComponent
	}
	if opt.HTTP == nil {
		opt.HTTP = &http.Client{}
	}
	if opt.CreateTimeout <= 0 {
		opt.CreateTimeout = DefaultCreateTimeout
	}
	if opt.LiveTimeout <= 0 {
		opt.LiveTimeout = DefaultLiveTimeout
	}
	if opt.RecordingTimeout <= 0 {
		opt.RecordingTimeout = DefaultRecordingTimeout
	}
	if opt.ListTimeout <= 0 {
		opt.ListTimeout = DefaultListTimeout
	}
	if opt.CreditsTimeout <= 0 {
		opt.CreditsTimeout = DefaultCreditsTimeout
	}
	if opt.UploadStartTimeout <= 0 {
		opt.UploadStartTimeout = DefaultUploadStartTimeout
	}
	if opt.UploadPUTTimeout <= 0 {
		opt.UploadPUTTimeout = DefaultUploadPUTTimeout
	}
	if opt.UploadFinishTimeout <= 0 {
		opt.UploadFinishTimeout = DefaultUploadFinishTimeout
	}
	if opt.RetryDelay <= 0 {
		opt.RetryDelay = DefaultRetryDelay
	}
	return &Client{opt: opt}
}

func (c *Client) url(path string) string {
	return strings.TrimRight(c.opt.BaseURL, "/") + path
}

func (c *Client) authHeader() map[string]string {
	return map[string]string{
		"Authorization": "Token " + c.opt.APIKey,
		"Content-Type":  "application/json",
	}
}

// CreateSession POST 创建组件版会话；120s 超时、绝不重试。
// 带 action 建场遇 403 时按动作库降级策略：去掉 action 重建一次。
func (c *Client) CreateSession(ctx context.Context, body map[string]any) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opt.CreateTimeout)
	defer cancel()
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	err = c.doJSON(ctx, http.MethodPost, c.url(c.opt.CreatePath), c.authHeader(), raw, &out, false, true, body)
	if err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.ActionForbidden && hasAction(body) {
			degraded := make(map[string]any, len(body))
			for k, v := range body {
				if k != "action" {
					degraded[k] = v
				}
			}
			raw2, merr := json.Marshal(degraded)
			if merr != nil {
				return nil, err
			}
			var out2 map[string]any
			if err2 := c.doJSON(ctx, http.MethodPost, c.url(c.opt.CreatePath), c.authHeader(), raw2, &out2, false, true, degraded); err2 != nil {
				return nil, err2
			}
			return out2, nil
		}
		return nil, err
	}
	return out, nil
}

// GetLive 查询场次；GET 失败重试 1 次（1s 抖动）。
func (c *Client) GetLive(ctx context.Context, liveID string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opt.LiveTimeout)
	defer cancel()
	var out map[string]any
	if err := c.doJSON(ctx, http.MethodGet, c.url(PathLive+url.PathEscape(liveID)), c.authHeader(), nil, &out, true, false, nil); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) GetRecording(ctx context.Context, liveID string) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opt.RecordingTimeout)
	defer cancel()
	var out map[string]any
	if err := c.doJSON(ctx, http.MethodGet, c.url(fmt.Sprintf(PathRecording, url.PathEscape(liveID))), c.authHeader(), nil, &out, true, false, nil); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) ListLives(ctx context.Context, page, size int) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opt.ListTimeout)
	defer cancel()
	q := url.Values{}
	q.Set("pager.page", strconv.Itoa(page))
	q.Set("pager.pagesz", strconv.Itoa(size))
	var out map[string]any
	if err := c.doJSON(ctx, http.MethodGet, c.url(PathLives)+"?"+q.Encode(), c.authHeader(), nil, &out, true, false, nil); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *Client) Credits(ctx context.Context) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, c.opt.CreditsTimeout)
	defer cancel()
	q := url.Values{}
	q.Set("show_detail", "false")
	var out map[string]any
	if err := c.doJSON(ctx, http.MethodGet, c.url(PathCredits)+"?"+q.Encode(), c.authHeader(), nil, &out, true, false, nil); err != nil {
		return nil, err
	}
	return out, nil
}

// Upload 三步式上传：POST 拿 put_url → PUT 对象存储（不带鉴权头）→ finish 拿 ssupload 短链。
func (c *Client) Upload(ctx context.Context, content []byte, contentType string) (string, error) {
	startCtx, cancel := context.WithTimeout(ctx, c.opt.UploadStartTimeout)
	defer cancel()
	var sess struct {
		ID     string `json:"id"`
		PutURL string `json:"put_url"`
	}
	err := c.doJSON(startCtx, http.MethodPost, c.url(PathUploads), c.authHeader(),
		[]byte(`{"scene":"vidu"}`), &sess, false, false, nil)
	if err != nil {
		return "", err
	}
	if sess.ID == "" || sess.PutURL == "" {
		return "", &APIError{Code: CodeUpstreamError, Message: "upload start missing id/put_url"}
	}

	putCtx, cancelPUT := context.WithTimeout(ctx, c.opt.UploadPUTTimeout)
	defer cancelPUT()
	req, err := http.NewRequestWithContext(putCtx, http.MethodPut, sess.PutURL, bytes.NewReader(content))
	if err != nil {
		return "", &APIError{Code: CodeNetwork, Message: err.Error(), Network: true}
	}
	// 第 2 步直传对象存储：绝不带 Authorization（蓝本 client.py:104-105）。
	req.Header.Set("Content-Type", contentType)
	resp, err := c.opt.HTTP.Do(req)
	if err != nil {
		return "", mapTransportError(err)
	}
	etag := strings.Trim(strings.TrimSpace(resp.Header.Get("etag")), `"`)
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode >= 400 {
		// 补偿：上传会话无取消接口，记录 orphan id 便于人工清理。
		return "", &APIError{Code: CodeUpstreamError, Status: resp.StatusCode,
			Message: "upload PUT failed; orphan upload id=" + sess.ID}
	}
	if etag == "" {
		return "", &APIError{Code: CodeUploadETagMissing, Status: resp.StatusCode,
			Message: "PUT 成功但响应无 etag; orphan upload id=" + sess.ID}
	}

	finCtx, cancelFin := context.WithTimeout(ctx, c.opt.UploadFinishTimeout)
	defer cancelFin()
	finBody, _ := json.Marshal(map[string]string{"etag": etag})
	var fin struct {
		URI string `json:"uri"`
	}
	err = c.doJSON(finCtx, http.MethodPut, c.url(fmt.Sprintf(PathUploadFinish, url.PathEscape(sess.ID))),
		c.authHeader(), finBody, &fin, false, false, nil)
	if err != nil {
		// 补偿：finish 失败同样留下孤儿会话，明确报出 id。
		return "", &APIError{Code: CodeUpstreamError,
			Message: "upload finish failed; orphan upload id=" + sess.ID + ": " + err.Error()}
	}
	if fin.URI == "" {
		return "", &APIError{Code: CodeUploadNoURI,
			Message: "upload finish 未返回 uri; orphan upload id=" + sess.ID}
	}
	return fin.URI, nil
}

func (c *Client) doJSON(
	ctx context.Context, method, rawURL string, headers map[string]string, body []byte,
	out any, retryOnce, isCreate bool, echoBody map[string]any,
) error {
	var lastErr error
	attempts := 1
	if retryOnce {
		attempts = 2
	}
	for i := 0; i < attempts; i++ {
		if i > 0 {
			delay := c.opt.RetryDelay
			var prevErr *APIError
			if errors.As(lastErr, &prevErr) && prevErr.Code == CodeRateLimited {
				// 429：按 Retry-After 退避并加随机抖动，避免雪崩重试。
				delay = prevErr.RetryAfter
				if delay <= 0 {
					delay = c.opt.RetryDelay
				}
				delay += time.Duration(rand.Int63n(int64(500 * time.Millisecond)))
			}
			select {
			case <-ctx.Done():
				return c.mapCtxErr(ctx, isCreate)
			case <-time.After(delay):
			}
		}
		lastErr = c.once(ctx, method, rawURL, headers, body, out, isCreate, echoBody)
		var apiErr *APIError
		if errors.As(lastErr, &apiErr) && (apiErr.IsRetryable() || apiErr.Code == CodeRateLimited) && i+1 < attempts {
			continue
		}
		return lastErr
	}
	return lastErr
}

func (c *Client) once(
	ctx context.Context, method, rawURL string, headers map[string]string, body []byte,
	out any, isCreate bool, echoBody map[string]any,
) error {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, bytes.NewReader(body))
	if err != nil {
		return &APIError{Code: CodeNetwork, Message: err.Error(), Network: true}
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := c.opt.HTTP.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return c.mapCtxErr(ctx, isCreate)
		}
		return mapTransportError(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))

	if resp.StatusCode >= 400 {
		return classify(resp.StatusCode, resp.Header.Get("Retry-After"), raw, isCreate, echoBody)
	}
	if out != nil && len(raw) > 0 {
		if err := json.Unmarshal(raw, out); err != nil {
			return &APIError{Code: CodeUpstreamError, Status: resp.StatusCode, Message: "invalid json: " + err.Error()}
		}
	}
	return nil
}

func (c *Client) mapCtxErr(ctx context.Context, isCreate bool) error {
	if isCreate {
		return &APIError{Code: CodeCreateTimeout, Message: "创建会话超过 120s；可能已创建，请到控制台确认", Timeout: true}
	}
	return &APIError{Code: CodeNetwork, Message: ctx.Err().Error(), Timeout: true, Network: true}
}

func mapTransportError(err error) error {
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return &APIError{Code: CodeNetwork, Message: err.Error(), Network: true, Timeout: true}
	}
	return &APIError{Code: CodeNetwork, Message: err.Error(), Network: true}
}

func classify(status int, retryAfter string, raw []byte, isCreate bool, echoBody map[string]any) error {
	var up struct {
		Code    any    `json:"code"`
		Message string `json:"message"`
	}
	_ = json.Unmarshal(raw, &up)
	msg := strings.TrimSpace(string(raw))
	if len(msg) > 500 {
		msg = msg[:500]
	}
	e := &APIError{Status: status, Message: msg}
	if up.Message != "" {
		e.UpstreamMessage = up.Message
	}
	switch v := up.Code.(type) {
	case string:
		e.UpstreamCode = v
	case float64:
		e.UpstreamCode = strconv.Itoa(int(v))
	}

	switch status {
	case http.StatusBadRequest:
		e.Code = CodeBadRequest
	case http.StatusUnauthorized:
		e.Code = CodeAuthFailed
	case http.StatusPaymentRequired:
		e.Code = CodeInsufficient
	case http.StatusForbidden:
		if isCreate && hasAction(echoBody) {
			e.Code = CodeActionForbidden
			e.ActionForbidden = true
		} else {
			e.Code = CodeForbidden
		}
	case http.StatusNotFound, http.StatusGone:
		e.Code = CodeLiveNotFound
	case http.StatusTooManyRequests:
		e.Code = CodeRateLimited
		e.RetryAfter = parseRetryAfter(retryAfter)
	default:
		if status >= 500 {
			e.Code = CodeUpstreamError
		} else {
			e.Code = CodeBadRequest
		}
	}
	return e
}

func hasAction(body map[string]any) bool {
	if body == nil {
		return false
	}
	_, ok := body["action"]
	return ok
}

func parseRetryAfter(v string) time.Duration {
	if v == "" {
		return 0
	}
	if secs, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && secs >= 0 {
		return time.Duration(secs) * time.Second
	}
	if t, err := http.ParseTime(v); err == nil {
		if d := time.Until(t); d > 0 {
			return d
		}
	}
	return 0
}
