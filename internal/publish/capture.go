package publish

// 无头浏览器采集源：启动 headless Chrome/Edge 加载 stage 页，经 CDP
// Page.startScreencast 抓 JPEG 帧喂给 ffmpeg stdin。
// fail-closed：找不到浏览器直接报 ErrBrowserUnavailable。
// 白屏/断流：Watchdog 统计帧率，超 NoFrameTimeout 无帧即报错进入 degraded。
// 显存：默认 --disable-gpu（避免与 OmniVoice 抢显存），AllowGPU 需显式开启。

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// ErrBrowserUnavailable 表示系统找不到可用浏览器。
var ErrBrowserUnavailable = errors.New("publish: 未找到 chrome/msedge/chromium，stage 采集不可用")

// CaptureConfig 无头浏览器配置。
type CaptureConfig struct {
	URL      string // stage 页地址，如 http://127.0.0.1:8795/stage
	Width    int    // 视口宽（竖屏默认 720）
	Height   int    // 视口高（默认 1280）
	AllowGPU bool   // 默认 false（--disable-gpu），避免与 OmniVoice 抢显存
	Bin      string // 浏览器可执行文件；空则自动探测
}

func (c CaptureConfig) withDefaults() CaptureConfig {
	if c.Width == 0 {
		c.Width = 720
	}
	if c.Height == 0 {
		c.Height = 1280
	}
	return c
}

// findBrowser 探测可用浏览器。
func findBrowser() (string, error) {
	for _, name := range []string{"chrome", "msedge", "chromium", "chrome.exe", "msedge.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", ErrBrowserUnavailable
}

// cdpConn 最小 CDP 会话（flatten 模式，单 ws 多 session）。
type cdpConn struct {
	ws       *websocket.Conn
	nextID   atomic.Int64
	sessions chan cdpEvent
	results  map[int64]chan cdpResult
	mu       chan struct{}
}

type cdpEvent struct {
	SessionID string          `json:"sessionId"`
	Method    string          `json:"method"`
	Params    json.RawMessage `json:"params"`
}

type cdpResult struct {
	ID        int64           `json:"id"`
	SessionID string          `json:"sessionId"`
	Result    json.RawMessage `json:"result"`
	Error     *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// StageCapture 从 stage 页抓帧。
type StageCapture struct {
	cfg     CaptureConfig
	cmd     *exec.Cmd
	conn    *cdpConn
	session string
	frames  chan []byte
	errCh   chan error
	cancel  context.CancelFunc
}

// StartStageCapture 启动浏览器与截屏会话；frames 为 JPEG 帧通道。
func StartStageCapture(ctx context.Context, cfg CaptureConfig) (*StageCapture, error) {
	cfg = cfg.withDefaults()
	bin := cfg.Bin
	if bin == "" {
		var err error
		bin, err = findBrowser()
		if err != nil {
			return nil, err
		}
	}
	args := []string{
		"--headless=new", "--remote-debugging-port=0", "--no-first-run",
		"--autoplay-policy=no-user-gesture-required",
		"--mute-audio=false",
		fmt.Sprintf("--window-size=%d,%d", cfg.Width, cfg.Height),
		"--user-data-dir=", // 空目录由系统自动分配临时 profile
	}
	if !cfg.AllowGPU {
		args = append(args, "--disable-gpu")
	}
	args = append(args, "about:blank")
	cmd := exec.Command(bin, args...)
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("publish: 浏览器启动失败: %w", err)
	}
	// 从 stderr 读 "DevTools listening on ws://..."
	wsURL, err := readDevToolsURL(stderr)
	if err != nil {
		_ = cmd.Process.Kill()
		return nil, err
	}
	ctx2, cancel := context.WithCancel(ctx)
	sc := &StageCapture{cfg: cfg, cmd: cmd, frames: make(chan []byte, 8), errCh: make(chan error, 1), cancel: cancel}
	if err := sc.attach(ctx2, wsURL); err != nil {
		cancel()
		_ = cmd.Process.Kill()
		return nil, err
	}
	return sc, nil
}

func readDevToolsURL(r io.Reader) (string, error) {
	sc := bufio.NewScanner(r)
	deadline := time.After(15 * time.Second)
	lineCh := make(chan string, 32)
	go func() {
		for sc.Scan() {
			lineCh <- sc.Text()
		}
		close(lineCh)
	}()
	for {
		select {
		case line, ok := <-lineCh:
			if !ok {
				return "", errors.New("publish: 浏览器未输出 DevTools 地址")
			}
			if i := strings.Index(line, "DevTools listening on "); i >= 0 {
				return strings.TrimSpace(line[i+len("DevTools listening on "):]), nil
			}
		case <-deadline:
			return "", errors.New("publish: 等待 DevTools 地址超时")
		}
	}
}

func (sc *StageCapture) attach(ctx context.Context, browserWS string) error {
	ws, _, err := websocket.DefaultDialer.DialContext(ctx, browserWS, http.Header{})
	if err != nil {
		return fmt.Errorf("publish: CDP 连接失败: %w", err)
	}
	conn := &cdpConn{ws: ws, results: map[int64]chan cdpResult{}, mu: make(chan struct{}, 1),
		sessions: make(chan cdpEvent, 64)}
	conn.mu <- struct{}{}
	sc.conn = conn
	go conn.readLoop(ctx)

	// 建目标页并附着
	res, err := conn.call(ctx, "", "Target.createTarget", map[string]any{
		"url": sc.cfg.URL, "width": sc.cfg.Width, "height": sc.cfg.Height,
	})
	if err != nil {
		return err
	}
	var target struct {
		TargetID string `json:"targetId"`
	}
	if err := json.Unmarshal(res, &target); err != nil {
		return err
	}
	res, err = conn.call(ctx, "", "Target.attachToTarget", map[string]any{
		"targetId": target.TargetID, "flatten": true,
	})
	if err != nil {
		return err
	}
	var att struct {
		SessionID string `json:"sessionId"`
	}
	if err := json.Unmarshal(res, &att); err != nil {
		return err
	}
	sc.session = att.SessionID
	if _, err := conn.call(ctx, sc.session, "Page.enable", nil); err != nil {
		return err
	}
	if _, err := conn.call(ctx, sc.session, "Page.startScreencast", map[string]any{
		"format": "jpeg", "quality": 80, "everyNthFrame": 1,
	}); err != nil {
		return err
	}
	go sc.frameLoop(ctx)
	return nil
}

func (c *cdpConn) call(ctx context.Context, session, method string, params map[string]any) (json.RawMessage, error) {
	id := c.nextID.Add(1)
	ch := make(chan cdpResult, 1)
	<-c.mu
	c.results[id] = ch
	c.mu <- struct{}{}
	msg := map[string]any{"id": id, "method": method}
	if session != "" {
		msg["sessionId"] = session
	}
	if params != nil {
		msg["params"] = params
	}
	if err := c.ws.WriteJSON(msg); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		if r.Error != nil {
			return nil, fmt.Errorf("publish: CDP %s: %s", method, r.Error.Message)
		}
		return r.Result, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(10 * time.Second):
		return nil, fmt.Errorf("publish: CDP %s 超时", method)
	}
}

func (c *cdpConn) readLoop(ctx context.Context) {
	for {
		var raw json.RawMessage
		if err := c.ws.ReadJSON(&raw); err != nil {
			return
		}
		// 先按 result 解，再按 event 解
		var probe struct {
			ID     int64  `json:"id"`
			Method string `json:"method"`
		}
		if json.Unmarshal(raw, &probe) != nil {
			continue
		}
		if probe.ID > 0 {
			var r cdpResult
			if json.Unmarshal(raw, &r) != nil {
				continue
			}
			<-c.mu
			ch := c.results[r.ID]
			delete(c.results, r.ID)
			c.mu <- struct{}{}
			if ch != nil {
				ch <- r
			}
			continue
		}
		if probe.Method == "Page.screencastFrame" {
			var ev cdpEvent
			if json.Unmarshal(raw, &ev) == nil {
				select {
				case c.sessions <- ev:
				default:
				}
			}
		}
	}
}

type screencastFrameParams struct {
	Data      string `json:"data"`
	SessionID int    `json:"sessionId"`
}

func (sc *StageCapture) frameLoop(ctx context.Context) {
	defer close(sc.frames)
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-sc.conn.sessions:
			var p screencastFrameParams
			if json.Unmarshal(ev.Params, &p) != nil {
				continue
			}
			// 必须 ack，否则浏览器停止推帧
			ackCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			_, _ = sc.conn.call(ackCtx, sc.session, "Page.screencastFrameAck",
				map[string]any{"sessionId": p.SessionID})
			cancel()
			jpg, err := base64.StdEncoding.DecodeString(p.Data)
			if err != nil {
				continue
			}
			select {
			case sc.frames <- jpg:
			default: // 消费不及丢帧，避免阻塞 CDP 读循环
			}
		}
	}
}

// Frames 返回 JPEG 帧通道。
func (sc *StageCapture) Frames() <-chan []byte { return sc.frames }

// Close 停浏览器与连接。
func (sc *StageCapture) Close() {
	sc.cancel()
	if sc.conn != nil && sc.conn.ws != nil {
		_ = sc.conn.ws.Close()
	}
	if sc.cmd != nil && sc.cmd.Process != nil {
		_ = sc.cmd.Process.Kill()
		_ = sc.cmd.Wait()
	}
}
