// Package recording 承接 F34 云录制：Manager.Start 时随场启动录制（建场带
// enable_recording=true 由引擎装配完成），Stop 时结束并轮询出片。
// fail-closed：未开通（配置关或 Vidu 返回不支持）一律报 recording_not_enabled，
// 绝不假装在录。真实出片待真机验证。
package recording

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"
)

// 录制状态机（SessionRecord.recording_state 取值）。
const (
	StateNone        = "none"
	StateRecording   = "recording"
	StateProcessing  = "processing"
	StateReady       = "ready"
	StateFailed      = "failed"
	StateUnavailable = "unavailable"
)

// ErrNotEnabled 表示云录制未开通（配置关闭或 Vidu 返回不支持）。
var ErrNotEnabled = errors.New("recording_not_enabled")

// Status 是录制查询的归一化结果。
type Status struct {
	State       string
	URL         string
	Unsupported bool // Vidu 返回 404/不支持
}

// Client 是录制查询端口（真实实现是 vidu.Client 适配器）。
type Client interface {
	RecordingStatus(ctx context.Context, liveID string) (Status, error)
}

// 轮询节奏：15s → 30s → 60s 稳态，总上限 1800s（可注入缩短测试）。
var (
	DefaultPollSteps = []time.Duration{15 * time.Second, 30 * time.Second}
	DefaultSteady    = 60 * time.Second
	DefaultMaxWait   = 1800 * time.Second
)

// Supervisor 实现 live.Recorder 端口。
type Supervisor struct {
	enabled bool
	client  Client
	log     *slog.Logger

	PollSteps []time.Duration
	Steady    time.Duration
	MaxWait   time.Duration

	mu     sync.Mutex
	state  string
	url    string
	liveID string
}

// New 创建 Supervisor；enabled=false 时所有操作报 ErrNotEnabled（fail-closed）。
func New(enabled bool, client Client, logger *slog.Logger) *Supervisor {
	if logger == nil {
		logger = slog.Default()
	}
	s := &Supervisor{enabled: enabled, client: client, log: logger, state: StateNone}
	if !enabled || client == nil {
		s.state = StateUnavailable
	}
	return s
}

// Enabled 返回配置是否开通云录制。
func (s *Supervisor) Enabled() bool { return s.enabled && s.client != nil }

// State 返回当前录制状态；未开通恒为 unavailable。
func (s *Supervisor) State() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.state
}

// URL 返回出片地址（ready 后有效）。
func (s *Supervisor) URL() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.url
}

func (s *Supervisor) setState(state, url string) {
	s.mu.Lock()
	s.state = state
	if url != "" {
		s.url = url
	}
	s.mu.Unlock()
}

// OnSessionStart 随开播启动录制：这里只登记 liveID 并置 recording；
// enable_recording 已在建场请求里带上去（见 engine.ViduController）。
func (s *Supervisor) OnSessionStart(_ context.Context, liveID string) error {
	if !s.Enabled() {
		return ErrNotEnabled
	}
	s.mu.Lock()
	s.liveID = liveID
	s.mu.Unlock()
	s.setState(StateRecording, "")
	return nil
}

// OnSessionStop 停播后轮询出片：15s→30s→60s 稳态，上限 1800s；
// Vidu 返回不支持（404）→ ErrNotEnabled；超时 → failed。
func (s *Supervisor) OnSessionStop(ctx context.Context, liveID string) (string, error) {
	if !s.Enabled() {
		return StateUnavailable, ErrNotEnabled
	}
	if liveID == "" {
		s.mu.Lock()
		liveID = s.liveID
		s.mu.Unlock()
	}
	if liveID == "" {
		s.setState(StateFailed, "")
		return StateFailed, errors.New("recording: 缺少 live_id")
	}
	s.setState(StateProcessing, "")

	steps := s.PollSteps
	if len(steps) == 0 {
		steps = DefaultPollSteps
	}
	steady := s.Steady
	if steady <= 0 {
		steady = DefaultSteady
	}
	maxWait := s.MaxWait
	if maxWait <= 0 {
		maxWait = DefaultMaxWait
	}

	deadline := time.Now().Add(maxWait)
	poll := 0
	for {
		st, err := s.client.RecordingStatus(ctx, liveID)
		if err != nil {
			s.setState(StateFailed, "")
			return StateFailed, err
		}
		if st.Unsupported {
			s.setState(StateUnavailable, "")
			return StateUnavailable, ErrNotEnabled
		}
		switch st.State {
		case StateReady:
			s.setState(StateReady, st.URL)
			s.log.Info("recording ready", "live_id", liveID, "url", st.URL)
			return StateReady, nil
		case StateFailed:
			s.setState(StateFailed, "")
			return StateFailed, fmt.Errorf("recording: 出片失败 live_id=%s", liveID)
		default: // recording/processing：继续等
			s.setState(st.State, "")
		}

		interval := steady
		if poll < len(steps) {
			interval = steps[poll]
		}
		poll++
		if time.Now().Add(interval).After(deadline) {
			s.setState(StateFailed, "")
			return StateFailed, fmt.Errorf("recording: 出片超时（>%s）live_id=%s", maxWait, liveID)
		}
		select {
		case <-ctx.Done():
			return s.State(), ctx.Err()
		case <-time.After(interval):
		}
	}
}

// MapViduState 把 Vidu 录制响应归一化成内部状态（宽松匹配常见字段）。
func MapViduState(resp map[string]any) Status {
	var st Status
	if resp == nil {
		st.State = StateFailed
		return st
	}
	raw, _ := resp["state"].(string)
	if raw == "" {
		raw, _ = resp["status"].(string)
	}
	raw = strings.ToLower(strings.TrimSpace(raw))
	switch raw {
	case "recording", "running":
		st.State = StateRecording
	case "processing", "rendering", "merging":
		st.State = StateProcessing
	case "ready", "done", "finished", "success", "completed":
		st.State = StateReady
	case "failed", "error":
		st.State = StateFailed
	default:
		st.State = StateProcessing
	}
	for _, k := range []string{"url", "video_url", "record_url", "file_url"} {
		if u, ok := resp[k].(string); ok && u != "" {
			st.URL = u
			if st.State == StateProcessing || st.State == StateRecording {
				st.State = StateReady
			}
			break
		}
	}
	return st
}
