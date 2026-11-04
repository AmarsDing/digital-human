// Package action 实现动作库 CRUD 与动作调度（优先级 / 冷却 / 队列）。
//
// 蓝本事实：bridge.py `/api/actions/libraries`、`/api/actions` 是 Vidu 动作库的透传；
// engine.py `_act_prio/_act_gap_for/cmd/act_interrupt` 定义优先级 3(vip)>2(manual)>1(choreo)>0，
// 队列上限 3、同动作去重、VIP 直发并让编排暂停 4.5s。本包只保留这些可测语义，
// 真实 Vidu 指令发送走 Sender 端口（FakeSender 记录调用）。
package action

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/teamwork/digital-human/internal/biz/capability"
	"github.com/teamwork/digital-human/internal/biz/contentgate"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

var (
	ErrActionNotFound  = errors.New("动作不存在")
	ErrLibraryNotFound = errors.New("动作库不存在")
	ErrEmptyCommand    = errors.New("text 为空")
)

type Library struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	CreatedAtMs int64  `json:"created_at_ms"`
	ActionCount int    `json:"action_count,omitempty"`
}

type Action struct {
	ID          string `json:"id"`
	LibraryID   string `json:"library_id"`
	Name        string `json:"name"`
	Instruction string `json:"instruction"`
	CreatedAtMs int64  `json:"created_at_ms"`
}

// Remote 是动作库远端端口（真实实现为 Vidu API）。
type Remote interface {
	Libraries(ctx context.Context) ([]Library, error)
	CreateLibrary(ctx context.Context, name, description string) (Library, error)
	DeleteLibrary(ctx context.Context, id string) error
	Actions(ctx context.Context, libraryID string) ([]Action, error)
	CreateAction(ctx context.Context, libraryID, name, instruction string) (Action, error)
	DeleteAction(ctx context.Context, id string) error
}

// MemoryRemote 是进程内实现，保证无 Vidu Key 时 CRUD 可用且可测。
type MemoryRemote struct {
	mu   sync.Mutex
	libs map[string]Library
	acts map[string]Action
	now  func() time.Time
}

func NewMemoryRemote() *MemoryRemote {
	return &MemoryRemote{libs: map[string]Library{}, acts: map[string]Action{}, now: time.Now}
}

func (m *MemoryRemote) Libraries(context.Context) ([]Library, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]Library, 0, len(m.libs))
	for _, lib := range m.libs {
		count := 0
		for _, a := range m.acts {
			if a.LibraryID == lib.ID {
				count++
			}
		}
		lib.ActionCount = count
		out = append(out, lib)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAtMs < out[j].CreatedAtMs })
	return out, nil
}

func (m *MemoryRemote) CreateLibrary(_ context.Context, name, description string) (Library, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Library{}, errors.New("库名不能为空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, lib := range m.libs {
		if lib.Name == name {
			return Library{}, fmt.Errorf("动作库已存在: %s", name)
		}
	}
	lib := Library{ID: uuid.NewString(), Name: name, Description: strings.TrimSpace(description), CreatedAtMs: m.now().UnixMilli()}
	m.libs[lib.ID] = lib
	return lib, nil
}

func (m *MemoryRemote) DeleteLibrary(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.libs[id]; !ok {
		return ErrLibraryNotFound
	}
	delete(m.libs, id)
	for aid, a := range m.acts {
		if a.LibraryID == id {
			delete(m.acts, aid)
		}
	}
	return nil
}

func (m *MemoryRemote) Actions(_ context.Context, libraryID string) ([]Action, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if libraryID != "" {
		if _, ok := m.libs[libraryID]; !ok {
			return nil, ErrLibraryNotFound
		}
	}
	out := make([]Action, 0, len(m.acts))
	for _, a := range m.acts {
		if libraryID == "" || a.LibraryID == libraryID {
			out = append(out, a)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAtMs < out[j].CreatedAtMs })
	return out, nil
}

func (m *MemoryRemote) CreateAction(_ context.Context, libraryID, name, instruction string) (Action, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return Action{}, errors.New("动作名不能为空")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.libs[libraryID]; !ok {
		return Action{}, ErrLibraryNotFound
	}
	act := Action{ID: uuid.NewString(), LibraryID: libraryID, Name: name, Instruction: strings.TrimSpace(instruction), CreatedAtMs: m.now().UnixMilli()}
	m.acts[act.ID] = act
	return act, nil
}

func (m *MemoryRemote) DeleteAction(_ context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.acts[id]; !ok {
		return ErrActionNotFound
	}
	delete(m.acts, id)
	return nil
}

// UnavailableRemote 在真实 Vidu 动作库未装配时 fail-closed，避免退化为内存 CRUD。
type UnavailableRemote struct{}

func (UnavailableRemote) Libraries(context.Context) ([]Library, error) {
	return nil, errors.New("action_remote_unconfigured: Vidu 动作库未配置")
}
func (UnavailableRemote) CreateLibrary(context.Context, string, string) (Library, error) {
	return Library{}, errors.New("action_remote_unconfigured: Vidu 动作库未配置")
}
func (UnavailableRemote) DeleteLibrary(context.Context, string) error {
	return errors.New("action_remote_unconfigured: Vidu 动作库未配置")
}
func (UnavailableRemote) Actions(context.Context, string) ([]Action, error) {
	return nil, errors.New("action_remote_unconfigured: Vidu 动作库未配置")
}
func (UnavailableRemote) CreateAction(context.Context, string, string, string) (Action, error) {
	return Action{}, errors.New("action_remote_unconfigured: Vidu 动作库未配置")
}
func (UnavailableRemote) DeleteAction(context.Context, string) error {
	return errors.New("action_remote_unconfigured: Vidu 动作库未配置")
}

// Sender 是 type9 指令出口；真实实现写 Vidu 信令。
type Sender interface {
	SendCommand(ctx context.Context, text string) error
}

type FakeSender struct {
	mu     sync.Mutex
	sent   []string
	last   time.Time
	failOn string
}

func NewFakeSender() *FakeSender { return &FakeSender{} }

func (s *FakeSender) SendCommand(_ context.Context, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failOn != "" && text == s.failOn {
		return errors.New("fake sender failure")
	}
	s.sent = append(s.sent, text)
	s.last = time.Now()
	return nil
}

func (s *FakeSender) Sent() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.sent...)
}

func (s *FakeSender) SetFailOn(text string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failOn = text
}

// Config 对齐蓝本 action 配置默认值。
type Config struct {
	QueueMax      int
	ManualGap     time.Duration
	ManualSameGap time.Duration
	AutoGap       time.Duration
	AutoSameGap   time.Duration
	SongGap       time.Duration
}

func DefaultConfig() Config {
	return Config{
		QueueMax:  3,
		ManualGap: 0, ManualSameGap: 2 * time.Second,
		AutoGap: 2 * time.Second, AutoSameGap: 8 * time.Second,
		SongGap: 6 * time.Second,
	}
}

type queued struct {
	priority int
	text     string
	source   string
}

// Scheduler 是动作唯一出口：负责优先级、冷却、队列与事件。
type Scheduler struct {
	mu       sync.Mutex
	q        []queued
	lastText string
	lastAt   time.Time
	draining bool
	closed   bool
	cfg      Config

	fired, merged, dropped, deferred int64

	sender      Sender
	bus         *eventbus.Bus
	songPlaying func() bool
	vip         func(user string) bool
	pauseChoreo func(time.Duration)
	stopCurrent func(ctx context.Context)
	contentGate *contentgate.Gate
	sessionID   func() string
}

func NewScheduler(sender Sender, bus *eventbus.Bus, cfg Config) *Scheduler {
	if cfg.QueueMax <= 0 {
		cfg = DefaultConfig()
	}
	return &Scheduler{cfg: cfg, sender: sender, bus: bus}
}

func (s *Scheduler) SetSongPlaying(fn func() bool)         { s.songPlaying = fn }
func (s *Scheduler) SetVIP(fn func(string) bool)           { s.vip = fn }
func (s *Scheduler) SetPauseChoreo(fn func(time.Duration)) { s.pauseChoreo = fn }

// SetContentGate 注入动作最终出站点；命中只抑制，不发替代动作或伪造 sent 事件。
func (s *Scheduler) SetContentGate(g *contentgate.Gate, sessionID func() string) {
	s.mu.Lock()
	s.contentGate, s.sessionID = g, sessionID
	s.mu.Unlock()
}

// SetStopCurrent 注入“中断当前动作语音”的副作用（如停引擎当前动作音频）。
func (s *Scheduler) SetStopCurrent(fn func(ctx context.Context)) { s.stopCurrent = fn }

// Result 是 /cmd 的返回语义（蓝本 {ok,deferred,dropped,queued,...}）。
type Result struct {
	OK       bool    `json:"ok"`
	Deferred bool    `json:"deferred"`
	Dropped  string  `json:"dropped,omitempty"`
	VIP      bool    `json:"vip,omitempty"`
	Reason   string  `json:"reason,omitempty"`
	GapS     float64 `json:"gap_s,omitempty"`
	Queued   int     `json:"queued,omitempty"`
	Priority int     `json:"priority"`
}

func (s *Scheduler) gapForLocked(text, source string) time.Duration {
	same := s.lastText != "" && strings.TrimSpace(text) == strings.TrimSpace(s.lastText)
	switch strings.ToLower(source) {
	case "manual":
		if same {
			return s.cfg.ManualSameGap
		}
		return s.cfg.ManualGap
	}
	if s.songPlaying != nil && s.songPlaying() {
		return s.cfg.SongGap
	}
	if same {
		return s.cfg.AutoSameGap
	}
	return s.cfg.AutoGap
}

func priorityOf(source string) int {
	switch strings.ToLower(source) {
	case "vip":
		return 3
	case "manual":
		return 2
	case "choreo":
		return 1
	default:
		return 0
	}
}

func (s *Scheduler) publish(ctx context.Context, typ string, payload map[string]any) {
	if s.bus != nil {
		s.bus.PublishScoped(ctx, typ, payload)
	}
}

// Command 发送一条动作指令；满足条件时排队并返回 deferred=true（不丢）。
func (s *Scheduler) Command(ctx context.Context, text, source, user string) (Result, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return Result{}, ErrEmptyCommand
	}
	source = strings.ToLower(strings.TrimSpace(source))
	if source == "" {
		source = "auto"
	}
	// 贵宾弹幕动作升级为最高优先级（蓝本 2026-11-01 规则）。
	if s.vip != nil && user != "" && source != "manual" && source != "vip" && s.vip(user) {
		source = "vip"
	}
	if source == "vip" {
		if err := s.SendDirect(ctx, text, "vip"); err != nil {
			return Result{}, err
		}
		if s.pauseChoreo != nil {
			s.pauseChoreo(4500 * time.Millisecond)
		}
		return Result{OK: true, VIP: true, Priority: 3}, nil
	}

	s.mu.Lock()
	gap := s.gapForLocked(text, source)
	elapsed := time.Since(s.lastAt)
	if gap > 0 && elapsed < gap {
		prio := priorityOf(source)
		dup := false
		for _, it := range s.q {
			if it.text == text {
				dup = true
				break
			}
		}
		dropped := ""
		if !dup {
			if len(s.q) >= s.cfg.QueueMax {
				old := s.q[len(s.q)-1]
				s.q = s.q[:len(s.q)-1]
				s.dropped++
				dropped = old.text
			}
			pos := len(s.q)
			for i, it := range s.q {
				if it.priority < prio {
					pos = i
					break
				}
			}
			s.q = append(s.q, queued{})
			copy(s.q[pos+1:], s.q[pos:])
			s.q[pos] = queued{priority: prio, text: text, source: source}
			s.merged++
		}
		s.deferred++
		n := len(s.q)
		startDrain := !s.draining
		if startDrain {
			s.draining = true
		}
		s.mu.Unlock()
		s.publish(ctx, "action.deferred", map[string]any{
			"text": text, "source": source, "priority": prio, "gap_s": gap.Seconds(), "queued": n,
		})
		if startDrain {
			go s.drain(context.WithoutCancel(ctx))
		}
		return Result{OK: true, Deferred: true, Dropped: dropped, GapS: gap.Seconds(), Queued: n, Priority: prio}, nil
	}
	s.mu.Unlock()
	if err := s.SendDirect(ctx, text, source); err != nil {
		return Result{}, err
	}
	return Result{OK: true, Priority: priorityOf(source)}, nil
}

// SendCommand 让调度器直接实现 choreo.Sender（编排步骤立即发送，不受冷却队列影响）。
func (s *Scheduler) SendCommand(ctx context.Context, text string) error {
	return s.SendDirect(ctx, text, "choreo")
}

// SendDirect 立即发送（编排/贵宾/手动直发共用），更新冷却基准。
func (s *Scheduler) SendDirect(ctx context.Context, text, source string) error {
	s.mu.Lock()
	gate, sessionFn := s.contentGate, s.sessionID
	s.mu.Unlock()
	if gate != nil {
		sessionID := ""
		if sessionFn != nil {
			sessionID = sessionFn()
		}
		decision := gate.Check(ctx, contentgate.Input{Outlet: "action.send", SessionID: sessionID, Text: text})
		if decision.Disposition != contentgate.DispositionAllow {
			return nil
		}
	}
	if s.sender == nil {
		return errors.New("动作发送通道未就绪")
	}
	if err := s.sender.SendCommand(ctx, text); err != nil {
		s.publish(ctx, "action.send_failed", map[string]any{"text": text, "error": err.Error()})
		return err
	}
	s.mu.Lock()
	s.lastText = strings.TrimSpace(text)
	s.lastAt = time.Now()
	s.fired++
	n := len(s.q)
	s.mu.Unlock()
	s.publish(ctx, "action.sent", map[string]any{"text": text, "source": source, "queued": n})
	return nil
}

// drain 按到达顺序补发；队列空即退出。
func (s *Scheduler) drain(ctx context.Context) {
	for {
		s.mu.Lock()
		if len(s.q) == 0 || s.closed {
			s.draining = false
			s.mu.Unlock()
			return
		}
		it := s.q[0]
		gap := s.gapForLocked(it.text, it.source)
		wait := gap - time.Since(s.lastAt)
		if wait > 0 {
			s.mu.Unlock()
			select {
			case <-ctx.Done():
				s.mu.Lock()
				s.draining = false
				s.mu.Unlock()
				return
			case <-time.After(minDur(wait, 250*time.Millisecond)):
			}
			continue
		}
		s.q = s.q[1:]
		s.mu.Unlock()
		if err := s.SendDirect(ctx, it.text, it.source); err != nil {
			s.publish(ctx, "action.queue_aborted", map[string]any{"text": it.text, "error": err.Error()})
			s.mu.Lock()
			s.q = nil
			s.draining = false
			s.mu.Unlock()
			return
		}
	}
}

// Interrupt 中断当前动作语音并清空未发出的队列（蓝本 act_interrupt）。
// 动作队列与冷却同时复位，action.stopped 事件只在这里发一次。
func (s *Scheduler) Interrupt(ctx context.Context) int {
	s.mu.Lock()
	n := len(s.q)
	s.q = nil
	s.lastAt = time.Time{}
	stop := s.stopCurrent
	s.mu.Unlock()
	if stop != nil {
		stop(ctx)
	}
	s.publish(ctx, "action.stopped", map[string]any{"cleared": n})
	return n
}

// Clear 只清动作队列，不动当前正在演出的动作（不复位冷却、不发 action.stopped）。
func (s *Scheduler) Clear(ctx context.Context) int {
	s.mu.Lock()
	n := len(s.q)
	s.q = nil
	s.mu.Unlock()
	if n > 0 {
		s.publish(ctx, "action.cleared", map[string]any{"cleared": n})
	}
	return n
}

// Status 供面板展示队列/计数。
type Status struct {
	Source   capability.Status `json:"source"`
	Queue    []string          `json:"queue"`
	LastText string            `json:"last_text"`
	LastAtMs int64             `json:"last_at_ms"`
	Fired    int64             `json:"fired"`
	Merged   int64             `json:"merged"`
	Dropped  int64             `json:"dropped"`
	Deferred int64             `json:"deferred"`
	QueueMax int               `json:"queue_max"`
}

func (s *Scheduler) Status() Status {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Status{
		Source:   senderSource(s.sender),
		LastText: s.lastText, Fired: s.fired, Merged: s.merged,
		Dropped: s.dropped, Deferred: s.deferred, QueueMax: s.cfg.QueueMax,
		Queue: make([]string, 0, len(s.q)),
	}
	if !s.lastAt.IsZero() {
		out.LastAtMs = s.lastAt.UnixMilli()
	}
	for _, it := range s.q {
		out.Queue = append(out.Queue, it.text)
	}
	return out
}

func senderSource(sender Sender) capability.Status {
	switch sender.(type) {
	case nil:
		return capability.Unconfigured
	case *FakeSender:
		return capability.Fake
	default:
		return capability.Unknown
	}
}

// Close 停止 drain 循环（进程退出/测试清理）。
func (s *Scheduler) Close() {
	s.mu.Lock()
	s.closed = true
	s.q = nil
	s.draining = false
	s.mu.Unlock()
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}

// Service 组合动作库 CRUD 与调度器。
type Service struct {
	remote Remote
	sched  *Scheduler
}

func NewService(remote Remote, sched *Scheduler) *Service {
	if remote == nil {
		remote = UnavailableRemote{}
	}
	return &Service{remote: remote, sched: sched}
}

func (s *Service) Libraries(ctx context.Context) ([]Library, error) { return s.remote.Libraries(ctx) }

func (s *Service) CreateLibrary(ctx context.Context, name, desc string) (Library, error) {
	return s.remote.CreateLibrary(ctx, name, desc)
}

func (s *Service) DeleteLibrary(ctx context.Context, id string) error {
	return s.remote.DeleteLibrary(ctx, id)
}

func (s *Service) Actions(ctx context.Context, libraryID string) ([]Action, error) {
	return s.remote.Actions(ctx, libraryID)
}

func (s *Service) CreateAction(ctx context.Context, libraryID, name, instruction string) (Action, error) {
	return s.remote.CreateAction(ctx, libraryID, name, instruction)
}

func (s *Service) DeleteAction(ctx context.Context, id string) error {
	return s.remote.DeleteAction(ctx, id)
}

func (s *Service) Scheduler() *Scheduler { return s.sched }
