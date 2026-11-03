// Package publish 实现抖音 RTMP 推流执行器：无头浏览器抓帧 → ffmpeg 编码 →
// FLV 经 stdout 管道 → 内嵌最小 RTMP 客户端发布。stream key 只经环境变量/
// 密钥存储进入 RTMP 客户端，绝不出现在进程 argv、日志、yaml。
// ffmpeg/浏览器缺失时 fail-closed，不降级 fake。
package publish

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// State 推流状态机。idle→starting→pushing；异常→degraded（自动重连）；
// 平台拒绝→rejected（终态，需人工）；Stop→stopped。
type State string

const (
	StateIdle     State = "idle"
	StateStarting State = "starting"
	StatePushing  State = "pushing"
	StateDegraded State = "degraded"
	StateRejected State = "rejected"
	StateStopped  State = "stopped"
)

// Metrics 一次采样的推流指标。
type Metrics struct {
	BitrateKbps   int
	FPS           float64
	DroppedFrames int64
}

// Status 是推流状态的快照（对前端/API 暴露的形状）。
type Status struct {
	State           State   `json:"state"`
	Platform        string  `json:"platform"`
	BitrateKbps     int     `json:"bitrate_kbps"`
	FPS             float64 `json:"fps"`
	DroppedFrames   int64   `json:"dropped_frames"`
	KeyFingerprint8 string  `json:"key_fingerprint8"`
	LastError       string  `json:"last_error"`
}

// Event 推流状态变化事件（供主实施线接 eventbus，携带 epoch/seq）。
type Event struct {
	State  State
	Reason string
	At     time.Time
}

// ErrInvalidTransition 表示非法状态迁移。
var ErrInvalidTransition = errors.New("publish: 非法状态迁移")

// machine 是推流状态机（Publisher 内部持有，外部只读快照）。
type machine struct {
	mu      sync.Mutex
	st      Status
	onEvent func(Event)
}

func newMachine(platform string, onEvent func(Event)) *machine {
	return &machine{st: Status{State: StateIdle, Platform: platform}, onEvent: onEvent}
}

var transitions = map[State][]State{
	StateIdle:     {StateStarting},
	StateStarting: {StatePushing, StateDegraded, StateRejected, StateStopped},
	StatePushing:  {StateDegraded, StateRejected, StateStopped},
	StateDegraded: {StateStarting, StateRejected, StateStopped},
	StateRejected: {StateStarting, StateStopped},
	StateStopped:  {StateStarting},
}

// transit 迁移状态；非法迁移返回 ErrInvalidTransition。reason 记录到 LastError。
func (m *machine) transit(to State, reason string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, s := range transitions[m.st.State] {
		if s == to {
			m.st.State = to
			if reason != "" {
				m.st.LastError = reason
			} else if to == StatePushing || to == StateIdle {
				m.st.LastError = ""
			}
			if m.onEvent != nil {
				m.onEvent(Event{State: to, Reason: reason, At: time.Now()})
			}
			return nil
		}
	}
	return fmt.Errorf("%w: %s -> %s", ErrInvalidTransition, m.st.State, to)
}

func (m *machine) snapshot() Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.st
}

func (m *machine) setMetrics(me Metrics) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.st.BitrateKbps = me.BitrateKbps
	m.st.FPS = me.FPS
	m.st.DroppedFrames = me.DroppedFrames
}

func (m *machine) setFingerprint(fp string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.st.KeyFingerprint8 = fp
}
