// Package engine 是数字人直播的纯逻辑层：会话状态机、喂流权、实时节奏与外部客户端。
// 该包不依赖 Ent/HTTP 框架，便于用虚拟时钟与 fake 端口做契约测试。
package engine

import (
	"fmt"
	"sync"
	"time"
)

// Phase 与 proto GetLiveStatus 的 phase 字符串保持一致。
type Phase string

const (
	PhaseIdle     Phase = "idle"
	PhaseStarting Phase = "starting"
	PhaseLive     Phase = "live"
	PhaseStopping Phase = "stopping"
	PhaseEnded    Phase = "ended"
	PhaseError    Phase = "error"
)

// transition 定义唯一合法的状态迁移。写死在表里，避免散落的分支出现“隐形状态”。
var transitions = map[Phase][]Phase{
	PhaseIdle:     {PhaseStarting},
	PhaseStarting: {PhaseLive, PhaseStopping, PhaseError},
	PhaseLive:     {PhaseStopping, PhaseError},
	PhaseStopping: {PhaseEnded, PhaseError},
	PhaseEnded:    {PhaseStarting},
	PhaseError:    {PhaseStarting, PhaseEnded},
}

// InvalidTransitionError 表示调用了非法迁移，调用方必须视为编程错误而非用户错误。
type InvalidTransitionError struct {
	From, To Phase
}

func (e *InvalidTransitionError) Error() string {
	return fmt.Sprintf("engine: invalid phase transition %s -> %s", e.From, e.To)
}

// StateMachine 是单会话的状态机；不负责并发调度，只保证迁移合法与错误快照。
type StateMachine struct {
	mu        sync.RWMutex
	phase     Phase
	errorCode string
	errorMsg  string
	since     time.Time
}

func NewStateMachine() *StateMachine {
	return &StateMachine{phase: PhaseIdle, since: time.Now()}
}

func (s *StateMachine) Phase() Phase {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.phase
}

func (s *StateMachine) ErrorInfo() (code, msg string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.errorCode, s.errorMsg
}

// Since 返回进入当前阶段的时刻。
func (s *StateMachine) Since() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.since
}

// Transition 校验并切换阶段；进入非 error 阶段时清空错误信息。
func (s *StateMachine) Transition(to Phase) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if to == s.phase {
		return nil
	}
	allowed := false
	for _, p := range transitions[s.phase] {
		if p == to {
			allowed = true
			break
		}
	}
	if !allowed {
		return &InvalidTransitionError{From: s.phase, To: to}
	}
	s.phase = to
	s.since = time.Now()
	if to != PhaseError {
		s.errorCode, s.errorMsg = "", ""
	}
	return nil
}

// Fail 原子地把阶段置为 error；只允许从非 ended 状态进入（ended 是终态展示）。
func (s *StateMachine) Fail(code, msg string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.phase == PhaseError {
		s.errorCode, s.errorMsg = code, msg
		return nil
	}
	allowed := false
	for _, p := range transitions[s.phase] {
		if p == PhaseError {
			allowed = true
			break
		}
	}
	if !allowed {
		return &InvalidTransitionError{From: s.phase, To: PhaseError}
	}
	s.phase = PhaseError
	s.since = time.Now()
	s.errorCode, s.errorMsg = code, msg
	return nil
}
