package live

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/teamwork/digital-human/pkg/eventbus"
	"github.com/teamwork/digital-human/pkg/eventcontract"
)

type Phase string

const (
	PhaseIdle     Phase = "idle"
	PhaseStarting Phase = "starting"
	PhaseLive     Phase = "live"
	PhaseStopping Phase = "stopping"
	PhaseEnded    Phase = "ended"
	PhaseError    Phase = "error"
)

// ErrEngineUnavailable 表示真实引擎未配置；服务层必须映射为 503，不能退回 fake 成功。
var ErrEngineUnavailable = errors.New("engine_not_configured")

// Controller 是引擎端口；FakeController 只允许在显式 dev 模式装配。
type Controller interface {
	Start(ctx context.Context, sessionID string) (liveID string, err error)
	Stop(ctx context.Context, reason string) error
	Say(ctx context.Context, text, mode string) (utteranceID string, err error)
}

// RuntimeStatus 是引擎暴露的真实运行态；Known=false 表示尚无真实引擎可读取，
// 状态接口必须输出 unknown，不能伪造 owner/connected。
type RuntimeStatus struct {
	FeedKnown        bool
	FeedOwner        bool
	FeedKind         string
	FeedEpoch        int64
	FeedLagMs        int64
	WSKnown          bool
	WSState          string
	WSReconnectTotal int32
	WSPongMs         int64
	RTCKnown         bool
	RTCMode          string
	RTCProvider      string
	RTCTokenExpireAt int64
}

// StatusProvider 由真实 Controller 可选实现；未实现即视为 unknown。
type StatusProvider interface {
	RuntimeStatus(ctx context.Context) RuntimeStatus
}

// EventSink 落库端口；M0 提供内存实现，PG 可用时换成 Ent 实现。
type EventSink interface {
	Append(ctx context.Context, e eventbus.Event) error
	List(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]eventbus.Event, error)
}

type Snapshot struct {
	SessionID   string
	Phase       Phase
	LiveID      string
	StartedAtMs int64
	DurationS   int64
	ErrorCode   string
	ErrorMsg    string
	EventSeq    int64
	Transcript  []string
}

// Manager 单进程只允许一场；重复开播复刻 stop("reconnect") 再重开，绝不返回 409。
type Manager struct {
	mu               sync.Mutex
	phase            Phase
	session          *snapshot
	bus              *eventbus.Bus
	sink             EventSink
	ctrl             Controller
	store            SessionStore
	idem             Store
	idemMu           sync.Mutex // 串行化幂等 key 的 get→执行→put，避免并发同 key 重复启动
	opMu             sync.Mutex // 串行化 start/stop 状态迁移；stop(reconnect) 与 start 在同一临界区
	wsMu             sync.Mutex
	wsBySID          map[string]int
	wsReconnectTotal int32
	wsPongMs         int64
	hooks            InterruptHooks
}

// InterruptHooks 口播/动作侧的中断副作用，由装配层注入；均为可选。
// Interrupt/StopActions/ClearActions 负责业务语义与事件，真正的引擎副作用经这里下钻。
type InterruptHooks struct {
	StopPlayback    func(ctx context.Context) // 取消 player 当前播放（口播/歌曲共用）
	ClearTTSQueue   func(ctx context.Context) // 停止进行中的 TTS 批量合成
	StopActionAudio func(ctx context.Context) // 中断当前动作语音
}

// SetInterruptHooks 注入中断副作用；装配层在全部组件就绪后调用一次。
func (m *Manager) SetInterruptHooks(h InterruptHooks) {
	m.mu.Lock()
	m.hooks = h
	m.mu.Unlock()
}

type snapshot struct {
	id         string
	liveID     string
	personaID  string
	avatarID   string
	startedAt  time.Time
	endedAt    time.Time
	transcript []string
	errorCode  string
	errorMsg   string
	epoch      int64
}

func NewManager(bus *eventbus.Bus, sink EventSink, ctrl Controller) *Manager {
	return NewManagerWithStores(bus, sink, ctrl, nil, nil)
}

// NewManagerWithStores 注入场次持久化与幂等存储；两者均可为 nil。
func NewManagerWithStores(bus *eventbus.Bus, sink EventSink, ctrl Controller, store SessionStore, idem Store) *Manager {
	m := &Manager{
		phase: PhaseIdle, bus: bus, sink: sink, ctrl: ctrl, store: store, idem: idem,
		wsBySID: map[string]int{},
	}
	if bus != nil {
		bus.SetScope(m.Scope)
		// 所有事件（含 song/refs/action 的 PublishScoped）统一经 bus 写同一 sink，
		// 避免 REST 列表与 WS 广播分别来自不同数据源。
		if sink != nil {
			bus.SetAppender(sink)
		}
	}
	return m
}

// Scope 返回当前场次与 epoch，供 eventbus.PublishScoped 统一填充事件信封。
func (m *Manager) Scope() (string, int64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session == nil {
		return "", 0
	}
	return m.session.id, m.session.epoch
}

func (m *Manager) publish(ctx context.Context, typ string, payload map[string]any) eventbus.Event {
	sessionID, epoch := m.Scope()
	return m.publishFor(ctx, sessionID, epoch, typ, payload)
}

// publishFor 用指定场次/epoch 发布；落库由 bus 的 appender 统一完成，避免双写。
func (m *Manager) publishFor(ctx context.Context, sessionID string, epoch int64, typ string, payload map[string]any) eventbus.Event {
	return m.bus.Publish(ctx, sessionID, typ, epoch, payload)
}

// persist 把场次快照写入存储；store 为 nil 或写入失败均不影响直播主流程。
func (m *Manager) persist(ctx context.Context, s *snapshot, phase Phase) {
	if m.store == nil || s == nil {
		return
	}
	now := time.Now()
	m.mu.Lock()
	rec := SessionRecord{
		ID:             s.id,
		LiveID:         s.liveID,
		Phase:          phase,
		Mode:           "managed",
		PersonaID:      s.personaID,
		AvatarID:       s.avatarID,
		StartedAtMs:    s.startedAt.UnixMilli(),
		RecordingState: "none",
		ErrorCode:      s.errorCode,
		UpdatedAtMs:    now.UnixMilli(),
	}
	if !s.startedAt.IsZero() {
		rec.DurationS = int64(now.Sub(s.startedAt).Seconds())
	}
	if !s.endedAt.IsZero() {
		rec.EndedAtMs = s.endedAt.UnixMilli()
	}
	m.mu.Unlock()
	_ = m.store.Save(ctx, rec)
}

// Start 返回 session_id / phase / live_id。已在 live/starting/stopping 时在
// 同一 opMu 临界区内先 stop(reason=reconnect) 再创建，杜绝并发第三条会话。
func (m *Manager) Start(ctx context.Context, personaID, avatarID, greeting string) (string, Phase, string, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.startLocked(ctx, personaID, avatarID, greeting)
}

func (m *Manager) startLocked(ctx context.Context, personaID, avatarID, greeting string) (string, Phase, string, error) {
	m.mu.Lock()
	needStop := m.phase == PhaseLive || m.phase == PhaseStarting || m.phase == PhaseStopping
	phase := m.phase
	m.mu.Unlock()
	if needStop {
		// 复刻蓝本语义：先停再开；stop 与 start 处于同一 opMu 临界区。
		if _, _, err := m.stopLocked(ctx, "reconnect"); err != nil {
			return "", PhaseError, "", err
		}
	} else if phase != PhaseIdle && phase != PhaseEnded && phase != PhaseError {
		return "", phase, "", fmt.Errorf("session busy in phase %s", phase)
	}

	s := &snapshot{
		id: uuid.NewString(), personaID: personaID, avatarID: avatarID,
		startedAt: time.Now(), epoch: time.Now().UnixMilli(),
	}
	m.mu.Lock()
	m.session = s
	m.phase = PhaseStarting
	phase = m.phase
	m.mu.Unlock()
	m.persist(ctx, s, PhaseStarting)

	m.publish(ctx, "live.start_requested", map[string]any{"persona_id": personaID, "avatar_id": avatarID})

	liveID, err := m.ctrl.Start(ctx, s.id)
	if err != nil {
		code := "engine_start_failed"
		if errors.Is(err, ErrEngineUnavailable) {
			code = "engine_not_configured"
		}
		m.mu.Lock()
		m.phase = PhaseError
		s.errorCode = code
		s.errorMsg = err.Error()
		m.mu.Unlock()
		m.persist(ctx, s, PhaseError)
		m.publish(ctx, "live.start_failed", map[string]any{"error": err.Error(), "error_code": code})
		return s.id, PhaseError, "", err
	}

	m.mu.Lock()
	s.liveID = liveID
	m.phase = PhaseLive
	phase = m.phase
	m.mu.Unlock()
	m.persist(ctx, s, PhaseLive)
	m.publish(ctx, "live.started", map[string]any{"live_id": liveID})
	return s.id, phase, liveID, nil
}

// Stop 幂等：idle/ended 返回 ok=true 与说明；与 Start 串行在同一 opMu 临界区。
func (m *Manager) Stop(ctx context.Context, reason string) (bool, string, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.stopLocked(ctx, reason)
}

func (m *Manager) stopLocked(ctx context.Context, reason string) (bool, string, error) {
	m.mu.Lock()
	if m.phase == PhaseIdle || m.phase == PhaseEnded {
		m.mu.Unlock()
		return true, "没有进行中的会话", nil
	}
	s := m.session
	m.phase = PhaseStopping
	m.mu.Unlock()
	m.persist(ctx, s, PhaseStopping)

	if err := m.ctrl.Stop(ctx, reason); err != nil {
		m.publish(ctx, "live.stop_failed", map[string]any{"reason": reason, "error": err.Error()})
	}
	// 对齐 finalize 的 0.4s 收尾窗口。
	time.Sleep(400 * time.Millisecond)

	m.mu.Lock()
	m.phase = PhaseEnded
	if s != nil {
		s.endedAt = time.Now()
	}
	m.mu.Unlock()
	m.persist(ctx, s, PhaseEnded)
	if s != nil {
		m.publish(ctx, "live.stopped", map[string]any{"reason": reason, "session_id": s.id})
	}
	return true, "", nil
}

// StartIdempotent 与 Start 相同，但 key 非空时按 key 复放结果，避免重复启动。
// 幂等 key 的 get→执行→put 在 idemMu 内串行，避免并发同 key 的 check-then-act 竞态。
func (m *Manager) StartIdempotent(ctx context.Context, key, personaID, avatarID, greeting string) (string, Phase, string, error) {
	const prefix = "start:"
	if key == "" || m.idem == nil {
		return m.Start(ctx, personaID, avatarID, greeting)
	}
	m.idemMu.Lock()
	defer m.idemMu.Unlock()
	if r, ok, err := m.idem.Get(ctx, prefix+key); err == nil && ok {
		return r.SessionID, r.Phase, r.LiveID, nil
	}
	sessionID, phase, liveID, err := m.Start(ctx, personaID, avatarID, greeting)
	if err != nil {
		return sessionID, phase, liveID, err
	}
	_ = m.idem.Put(ctx, prefix+key, Result{
		SessionID: sessionID, Phase: phase, LiveID: liveID, OK: true,
	}, DefaultIdempotencyTTL)
	return sessionID, phase, liveID, nil
}

// StopIdempotent 与 Stop 相同，但 key 非空时按 key 复放结果，避免重复停止。
// 幂等 key 的 get→执行→put 在 idemMu 内串行，避免并发同 key 重复停止。
func (m *Manager) StopIdempotent(ctx context.Context, key, reason string) (bool, string, error) {
	const prefix = "stop:"
	if key == "" || m.idem == nil {
		return m.Stop(ctx, reason)
	}
	m.idemMu.Lock()
	defer m.idemMu.Unlock()
	if r, ok, err := m.idem.Get(ctx, prefix+key); err == nil && ok {
		return r.OK, r.Note, nil
	}
	ok, note, err := m.Stop(ctx, reason)
	if err != nil {
		return ok, note, err
	}
	snap := m.Snapshot()
	_ = m.idem.Put(ctx, prefix+key, Result{
		SessionID: snap.SessionID, Phase: PhaseEnded, OK: ok, Note: note,
	}, DefaultIdempotencyTTL)
	return ok, note, nil
}

// Recover 处理进程崩溃遗留的 starting/live/stopping 场次：只标记为
// error(crash_recovered) 并发布事件，绝不自动恢复喂流或会话。
// 由 App.Run 在 Server.Start 之前调用一次，DB 晚就绪时由装配层再入一次。
//
// 判据是“归属”而不是时间窗：starting/live/stopping 表示有进程声称该场次
// 未完成，而 Recover 执行时唯一可能真正活着的未完成场次就是本 Manager 当前
// 持有的 m.session；其余一律是已死进程的遗留——哪怕它 5 秒前还在更新
// （崩溃前最后一次写入）。曾经的 60s 陈旧窗口会把“刚崩溃”的场次漏成永久
// 幽灵 live（P2-13），故废弃；cutoff 取 now+1 表示全量扫描未完成行。
func (m *Manager) Recover(ctx context.Context) error {
	if m.store == nil {
		return nil
	}
	m.mu.Lock()
	currentID := ""
	if m.session != nil {
		currentID = m.session.id
	}
	m.mu.Unlock()
	rows, err := m.store.ListStale(ctx, time.Now().UnixMilli()+1)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if r.ID == currentID {
			continue // 本进程在场场次，不是崩溃遗留
		}
		if err := m.store.MarkCrashed(ctx, r.ID); err != nil {
			return err
		}
		e := m.bus.Publish(ctx, r.ID, "live.crash_recovered", 0, map[string]any{
			"session_id": r.ID, "error_code": "crash_recovered", "from_phase": string(r.Phase),
		})
		if m.sink != nil {
			_ = m.sink.Append(ctx, e)
		}
	}
	return nil
}

func (m *Manager) Say(ctx context.Context, text, mode string) (string, error) {
	m.mu.Lock()
	if m.phase != PhaseLive || m.session == nil {
		m.mu.Unlock()
		return "", errors.New("not live")
	}
	sessionID, epoch := m.session.id, m.session.epoch
	index := len(m.session.transcript)
	m.mu.Unlock()

	// 手工 say 的文本即整句：先发 partial 让字幕立即上屏，TTS 返回后再发 final。
	m.publishFor(ctx, sessionID, epoch, eventcontract.TranscriptPartial, map[string]any{
		eventcontract.FieldText: text, eventcontract.FieldIndex: index, eventcontract.FieldMode: mode,
	})
	id, err := m.ctrl.Say(ctx, text, mode)
	if err != nil {
		return "", err
	}

	m.mu.Lock()
	if m.session != nil && m.session.id == sessionID {
		m.session.transcript = append(m.session.transcript, text)
		index = len(m.session.transcript) - 1
	}
	m.mu.Unlock()
	m.publishFor(ctx, sessionID, epoch, eventcontract.TranscriptFinal, map[string]any{
		eventcontract.FieldText: text, eventcontract.FieldUtteranceID: id, eventcontract.FieldIndex: index,
	})
	return id, nil
}

// Interrupt 中断当前口播发言：取消 player 播放、清空 TTS 队列，并发布
// type7（audio_interrupted）信令语义事件。type7 与 ownership 抢占路径共用
// 同一事件名，所有权层的重试窗口不在这里实现。
func (m *Manager) Interrupt(ctx context.Context) {
	m.mu.Lock()
	hooks := m.hooks
	m.mu.Unlock()
	if hooks.StopPlayback != nil {
		hooks.StopPlayback(ctx)
	}
	if hooks.ClearTTSQueue != nil {
		hooks.ClearTTSQueue(ctx)
	}
	m.publish(ctx, "audio_interrupted", map[string]any{"signal_type": 7})
}
func (m *Manager) ClearTranscript(ctx context.Context) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.session == nil {
		return 0
	}
	n := len(m.session.transcript)
	m.session.transcript = nil
	return n
}

// StopActions 中断当前动作语音；动作队列的清理由 action.Scheduler.Interrupt 负责
// （装配了调度器时由服务层直接调用，事件只发一次）。
func (m *Manager) StopActions(ctx context.Context) {
	m.mu.Lock()
	hooks := m.hooks
	m.mu.Unlock()
	if hooks.StopActionAudio != nil {
		hooks.StopActionAudio(ctx)
	}
	m.publish(ctx, "action.stopped", nil)
}

// ClearActions 只清动作队列，不影响当前播放中的动作。
func (m *Manager) ClearActions(ctx context.Context) { m.publish(ctx, "action.cleared", nil) }

// ResetLlm 保留事件占位；LLM 链路未交付，尚无真实副作用可执行。
func (m *Manager) ResetLlm(ctx context.Context) int {
	m.publish(ctx, "llm.reset", nil)
	return 0
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.snapshotLocked(0)
}

// ErrSessionMismatch 表示请求的 session_id 不是当前场次，禁止跨场次读取状态。
var ErrSessionMismatch = errors.New("session_mismatch")

// SnapshotFor 按 session_id 过滤，并按 transcript_since 返回增量字幕。
// 未指定 session_id 时返回当前场次；指定时必须与当前场次一致。
func (m *Manager) SnapshotFor(sessionID string, transcriptSince int) (Snapshot, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if sessionID != "" {
		if m.session == nil || m.session.id != sessionID {
			return Snapshot{Phase: m.phase, ErrorCode: "session_not_found", ErrorMsg: "session_id 不属于当前场次"}, ErrSessionMismatch
		}
	}
	from := transcriptSince
	if from < 0 {
		from = 0
	}
	out := m.snapshotLocked(from)
	return out, nil
}

func (m *Manager) snapshotLocked(transcriptSince int) Snapshot {
	out := Snapshot{Phase: m.phase}
	if m.session != nil {
		out.EventSeq = m.bus.SeqFor(m.session.id)
	} else {
		out.EventSeq = m.bus.Seq()
	}
	if m.session != nil {
		out.SessionID = m.session.id
		out.LiveID = m.session.liveID
		if !m.session.startedAt.IsZero() {
			out.StartedAtMs = m.session.startedAt.UnixMilli()
		}
		if m.phase != PhaseIdle && !m.session.startedAt.IsZero() {
			end := time.Now()
			if !m.session.endedAt.IsZero() {
				end = m.session.endedAt
			}
			out.DurationS = int64(end.Sub(m.session.startedAt).Seconds())
		}
		out.ErrorCode = m.session.errorCode
		out.ErrorMsg = m.session.errorMsg
		if transcriptSince < len(m.session.transcript) {
			out.Transcript = append([]string(nil), m.session.transcript[transcriptSince:]...)
		}
	}
	return out
}

// RuntimeStatus 读取真实引擎状态；控制器未实现 StatusProvider 时全部为 unknown。
func (m *Manager) RuntimeStatus(ctx context.Context) RuntimeStatus {
	if p, ok := m.ctrl.(StatusProvider); ok {
		return p.RuntimeStatus(ctx)
	}
	return RuntimeStatus{
		FeedKnown: false, FeedKind: "unknown",
		WSKnown: false, WSState: "unknown",
		RTCKnown: false, RTCMode: "unconfigured",
	}
}

// WSConnected 记录一条已通过 ticket 校验的 WS 连接，供状态接口读取真实连接态。
func (m *Manager) WSConnected(sessionID string) {
	m.wsMu.Lock()
	defer m.wsMu.Unlock()
	m.wsBySID[sessionID]++
	m.wsReconnectTotal++
	m.wsPongMs = time.Now().UnixMilli()
}

// WSDisconnected 在连接结束时扣减；计数只反映真实存活的 WS。
func (m *Manager) WSDisconnected(sessionID string) {
	m.wsMu.Lock()
	defer m.wsMu.Unlock()
	if m.wsBySID[sessionID] > 0 {
		m.wsBySID[sessionID]--
	}
}

// WSStatusFor 返回该场次真实 WS 连接态；无连接时状态为 disconnected，而不是伪造的 unknown。
func (m *Manager) WSStatusFor(sessionID string) RuntimeStatus {
	m.wsMu.Lock()
	defer m.wsMu.Unlock()
	n := m.wsBySID[sessionID]
	state := "disconnected"
	if n > 0 {
		state = "connected"
	}
	return RuntimeStatus{
		WSKnown: true, WSState: state, WSReconnectTotal: m.wsReconnectTotal,
		WSPongMs: m.wsPongMs,
	}
}

func (m *Manager) ListEvents(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]eventbus.Event, error) {
	if m.sink == nil {
		return nil, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	return m.sink.List(ctx, sessionID, afterSeq, limit)
}
