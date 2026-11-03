package relay

// relay 主循环：ws 订阅 → 翻译 → HMAC 签名 POST → 断线指数退避重连。
// 心跳：周期性向 /api/v1/collector/heartbeat 上报状态（签名同源）。

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/teamwork/digital-human/internal/collectorauth"
)

// Config relay 配置。Secret 只从环境变量进入（cmd/dh-relay 注入）。
type Config struct {
	WSBase  string // douyinLive ws 基址，默认 ws://127.0.0.1:1089
	Bridge  string // digital-human 基址，默认 http://127.0.0.1:8795
	Room    string // 抖音房间号
	Secret  string // DH_COLLECTOR_SECRET
	Verbose bool

	// 退避：初始 3s，每次 +3s，上限 30s（与蓝本转发器同一行为口径）。
	BackoffInit, BackoffStep, BackoffMax time.Duration
	HeartbeatInterval                    time.Duration

	// HTTPClient / Dialer 可注入（测试）。
	HTTPClient *http.Client
	Dialer     *websocket.Dialer

	// Logf 日志回调。
	Logf func(format string, args ...any)
}

func (c *Config) withDefaults() {
	if c.WSBase == "" {
		c.WSBase = "ws://127.0.0.1:1089"
	}
	if c.Bridge == "" {
		c.Bridge = "http://127.0.0.1:8795"
	}
	if c.BackoffInit <= 0 {
		c.BackoffInit = 3 * time.Second
	}
	if c.BackoffStep <= 0 {
		c.BackoffStep = 3 * time.Second
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = 30 * time.Second
	}
	if c.HeartbeatInterval <= 0 {
		c.HeartbeatInterval = 10 * time.Second
	}
	if c.HTTPClient == nil {
		c.HTTPClient = &http.Client{Timeout: 8 * time.Second}
	}
	if c.Dialer == nil {
		c.Dialer = websocket.DefaultDialer
	}
}

// Relay 转发器。
type Relay struct {
	cfg Config

	state    atomic.Value // string: connecting|collecting|degraded|stopped
	lastMsg  atomic.Int64 // 最近一条弹幕 unix ms
	msgTimes []time.Time  // 近 1 分钟消息时间（msgs_per_min 计算）
	mu       chan struct{}
}

// NewRelay 构造。
func NewRelay(cfg Config) *Relay {
	cfg.withDefaults()
	r := &Relay{cfg: cfg, mu: make(chan struct{}, 1)}
	r.mu <- struct{}{}
	r.setState("off")
	return r
}

func (r *Relay) logf(f string, a ...any) {
	if r.cfg.Logf != nil {
		r.cfg.Logf(f, a...)
	}
}

func (r *Relay) setState(s string) { r.state.Store(s) }

// State 当前状态。
func (r *Relay) State() string { s, _ := r.state.Load().(string); return s }

func (r *Relay) noteMsg() {
	r.lastMsg.Store(time.Now().UnixMilli())
	<-r.mu
	now := time.Now()
	r.msgTimes = append(r.msgTimes, now)
	cut := now.Add(-time.Minute)
	keep := r.msgTimes[:0]
	for _, t := range r.msgTimes {
		if t.After(cut) {
			keep = append(keep, t)
		}
	}
	r.msgTimes = keep
	r.mu <- struct{}{}
}

// Run 主循环（阻塞，ctx 取消即退出）。
func (r *Relay) Run(ctx context.Context) error {
	if r.cfg.Room == "" {
		return fmt.Errorf("relay: 房间号为空（设 DH_ROOM_ID 或 --room）")
	}
	if r.cfg.Secret == "" {
		return fmt.Errorf("relay: 采集密钥为空（设 DH_COLLECTOR_SECRET）")
	}
	go r.heartbeatLoop(ctx)
	backoff := r.cfg.BackoffInit
	for {
		if ctx.Err() != nil {
			r.setState("stopped")
			return ctx.Err()
		}
		err := r.runOnce(ctx)
		if ctx.Err() != nil {
			r.setState("stopped")
			return ctx.Err()
		}
		r.setState("degraded")
		r.logf("relay: 连接断开: %v，%s 后重连", err, backoff)
		select {
		case <-ctx.Done():
			r.setState("stopped")
			return ctx.Err()
		case <-time.After(backoff):
		}
		backoff += r.cfg.BackoffStep
		if backoff > r.cfg.BackoffMax {
			backoff = r.cfg.BackoffMax
		}
	}
}

func (r *Relay) runOnce(ctx context.Context) error {
	url := fmt.Sprintf("%s/ws/%s", r.cfg.WSBase, r.cfg.Room)
	r.setState("connecting")
	ws, _, err := r.cfg.Dialer.DialContext(ctx, url, nil)
	if err != nil {
		return fmt.Errorf("relay: 连接 %s 失败: %w", url, err)
	}
	defer ws.Close()
	r.setState("collecting")
	r.logf("relay: 已连接 %s", url)

	// 心跳式读超时：60s 无任何消息视为假死
	for {
		ws.SetReadDeadline(time.Now().Add(60 * time.Second))
		var msg map[string]any
		if err := ws.ReadJSON(&msg); err != nil {
			return err
		}
		out := Translate(msg)
		if out == nil {
			continue
		}
		r.noteMsg()
		if err := r.post(ctx, "/api/danmaku", out); err != nil {
			r.logf("relay: 转发失败: %v", err) // 不中断采集，下一条继续
		}
	}
}

// post HMAC 签名 POST。签名覆盖 method+path+sha256(body)+ts+nonce。
func (r *Relay) post(ctx context.Context, path string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	ts := time.Now().Unix()
	nonce := randHex(16)
	sign := collectorauth.Sign(r.cfg.Secret, http.MethodPost, path, body, ts, nonce)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, r.cfg.Bridge+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set(collectorauth.HeaderTs, strconv.FormatInt(ts, 10))
	req.Header.Set(collectorauth.HeaderNonce, nonce)
	req.Header.Set(collectorauth.HeaderSign, sign)
	resp, err := r.cfg.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	// /api/danmaku 契约：永远 200 + {ok, reason}；>=400 才算传输层失败
	if resp.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("relay: 桥返回 %d: %s", resp.StatusCode, string(b))
	}
	return nil
}

// heartbeatLoop 周期上报采集状态。
func (r *Relay) heartbeatLoop(ctx context.Context) {
	tk := time.NewTicker(r.cfg.HeartbeatInterval)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			<-r.mu
			n := len(r.msgTimes)
			r.mu <- struct{}{}
			payload := map[string]any{
				"state":        r.State(),
				"room_id":      r.cfg.Room,
				"last_msg_ms":  r.lastMsg.Load(),
				"msgs_per_min": n,
			}
			if err := r.post(ctx, "/api/v1/collector/heartbeat", payload); err != nil && r.cfg.Verbose {
				r.logf("relay: 心跳失败: %v", err)
			}
		}
	}
}

func randHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
