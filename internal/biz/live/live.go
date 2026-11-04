package live

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/teamwork/digital-human/internal/biz/avatar"
	"github.com/teamwork/digital-human/internal/biz/refs"
	"github.com/teamwork/digital-human/internal/llm"
	"github.com/teamwork/digital-human/internal/publish"
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

// ErrAvatarExpired 开播预检失败（v3 §3.3）：档案形象未同步或 Vidu ssupload 资产
// 已过 90 天到期时刻；服务层映射 400 avatar_expired，前端走「重新同步并开播」。
var ErrAvatarExpired = errors.New("avatar_expired")

// ErrPreviewWhilePushing local-preview 模式禁止连接公开推流（§7.6）：推流已建立时
// 本地预览是语义谎言，显式拒绝开播；请改用 public-live（携带 preflight_token）或先停推。
var ErrPreviewWhilePushing = errors.New("local_preview_while_pushing")

// StartOpts 开播参数（v3 方案 §3.1 字段逐字冻结）：
// 建场主形象 ViduAvatarID 与 ViduImageURI 二选一、URI 优先（蓝本 engine.py:258-270）；
// RefImageURIs 是会话运行中经信令 prompt_operation 推送的参考图（非建场 body），
// ≤3 且有序，取 Vidu ssupload URI 而非本地路径；Greeting 空串 = 不发。
type StartOpts struct {
	SessionID        string
	Model            string // 默认 vidu-s2
	ViduAvatarID     string // 与 ViduImageURI 二选一，URI 优先
	ViduImageURI     string
	VoiceID          string
	ActionLibraryID  string   // 空=不带 action 段
	RefImageURIs     []string // ≤3，有序；Vidu ssupload URI（非本地路径）
	Greeting         string
	RecordingEnabled bool
}

// Controller 是引擎端口；FakeController 只允许在显式 dev 模式装配。
type Controller interface {
	Start(ctx context.Context, opts StartOpts) (liveID string, err error)
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
	// Mode 开播模式（local-preview/public-live，§7.6）；无场次时为空串。
	Mode string
}

// Manager 单进程只允许一场；重复开播复刻 stop("reconnect") 再重开，绝不返回 409。
type Manager struct {
	mu                sync.Mutex
	phase             Phase
	session           *snapshot
	bus               *eventbus.Bus
	sink              EventSink
	ctrl              Controller
	store             SessionStore
	idem              Store
	idemMu            sync.Mutex // 串行化幂等 key 的 get→执行→put，避免并发同 key 重复启动
	opMu              sync.Mutex // 串行化 start/stop 状态迁移；stop(reconnect) 与 start 在同一临界区
	wsMu              sync.Mutex
	wsBySID           map[string]int
	wsReconnectTotal  int32
	wsPongMs          int64
	hooks             InterruptHooks
	recorder          Recorder
	poller            CreditsPoller
	dmWorker          DanmakuWorker
	sessionHooks      []DanmakuWorker
	llmMem            LlmMemory
	resolver          avatar.ProfileResolver
	refSwitch         RefSwitcher
	pushGate          publish.PushGate // nil = 推流线未启用（不做开播前置检查）
	allowUnpushedLive bool
	// preflight 唯一 ready 判定服务（§7.5）；public-live 开播必须携带其签发 token。
	preflight *Preflight
	// modePolicy 是服务端开播模式的唯一判定依据；零值严格按 public-live。
	modePolicy ModePolicy
	// captionSplit F19/G16：captions.output_sentences 热读（nil 视为 true=逐句投喂）。
	captionSplit func() bool
	// recordingFlag F34 遗留接线：settings misc.record_enabled 开播时读入 StartOpts。
	recordingFlag func() bool
}

// SetPushGate 注入推流前置检查依赖（DZ 批）。allowUnpushed 仅本地预览调试用。
// 仅装配层在 stream.enabled=true（pub 非 nil）时调用；未注入时开播不做推流前置检查，
// 保持既有行为不变（推流线未启用不阻断本地/无推流场景）。
func (m *Manager) SetPushGate(g publish.PushGate, allowUnpushed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.pushGate = g
	m.allowUnpushedLive = allowUnpushed
}

// currentPushGate 读取推流前置检查依赖（m.mu 保护，与 SetPushGate 写对称）。
func (m *Manager) currentPushGate() (publish.PushGate, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pushGate, m.allowUnpushedLive
}

// SetPreflight 注入唯一预检服务（§7.5）；装配层在全部组件就绪后调用一次。
func (m *Manager) SetPreflight(p *Preflight) {
	m.mu.Lock()
	m.preflight = p
	m.mu.Unlock()
}

// SetModePolicy 注入服务端模式策略；请求中的 mode 仅用于一致性校验，绝不参与判定。
func (m *Manager) SetModePolicy(policy ModePolicy) {
	m.mu.Lock()
	m.modePolicy = policy
	m.mu.Unlock()
}

// resolveMode 只由服务端策略判定；客户端声明不一致时明确拒绝，不静默纠正。
func (m *Manager) resolveMode(req Mode) (Mode, error) {
	m.mu.Lock()
	policy := m.modePolicy
	m.mu.Unlock()
	return resolveRequestedMode(policy, req)
}

// RefSwitcher 参考图信令推送端口（v3 §3.3 时序，M5），由 internal/biz/refs.Service 实现；
// nil 表示未装配（跳过推送）。type14/推送失败 = 降级告警事件，不算开播失败（§3.3 写死）。
type RefSwitcher interface {
	Switch(ctx context.Context, images []refs.Image) (refs.State, error)
}

// SetRefSwitcher 注入参考图推送端口；装配层在全部组件就绪后调用一次。
func (m *Manager) SetRefSwitcher(rs RefSwitcher) {
	m.mu.Lock()
	m.refSwitch = rs
	m.mu.Unlock()
}

// Recorder 是云录制端口（F34），由装配层注入；nil 表示未配置。
// 实现方：internal/biz/recording.Supervisor。
type Recorder interface {
	// Enabled 配置是否开通云录制。
	Enabled() bool
	// State 当前录制状态（none/recording/processing/ready/failed/unavailable）。
	State() string
	// OnSessionStart 开播成功后随场启动录制；未开通返回明确错误。
	OnSessionStart(ctx context.Context, liveID string) error
	// OnSessionStop 停播后结束录制并轮询出片，返回最终状态。
	OnSessionStop(ctx context.Context, liveID string) (string, error)
}

// SetRecorder 注入云录制端口；装配层在全部组件就绪后调用一次。
func (m *Manager) SetRecorder(r Recorder) {
	m.mu.Lock()
	m.recorder = r
	m.mu.Unlock()
}

// CreditsPoller 是 F36 额度轮询落账端口，由装配层注入；nil 表示未配置。
// 实现方：internal/biz/billing.Poller。
type CreditsPoller interface {
	// OnSessionStart 开播成功后记录 credits 基线并启动周期轮询落账。
	OnSessionStart(ctx context.Context, sessionID string)
	// OnSessionStop 下播后停止轮询并落最终一笔。
	OnSessionStop(ctx context.Context, sessionID string)
}

// SetCreditsPoller 注入额度轮询端口；装配层在全部组件就绪后调用一次。
func (m *Manager) SetCreditsPoller(p CreditsPoller) {
	m.mu.Lock()
	m.poller = p
	m.mu.Unlock()
}

// DanmakuWorker 是 F24 弹幕批读消费循环端口，由装配层注入；nil 表示未配置。
// 实现方：internal/biz/danmaku.Worker。
type DanmakuWorker interface {
	// OnSessionStart 开播成功后启动弹幕批读消费循环。
	OnSessionStart(ctx context.Context, sessionID string)
	// OnSessionStop 下播后停止消费循环。
	OnSessionStop(ctx context.Context, sessionID string)
}

// SetDanmakuWorker 注入弹幕批读 worker；装配层在全部组件就绪后调用一次。
func (m *Manager) SetDanmakuWorker(w DanmakuWorker) {
	m.mu.Lock()
	m.dmWorker = w
	m.mu.Unlock()
}

// AddSessionHook 追加随场启停的组件（F27 礼物合并器 / G24 贵宾欢迎等同 DanmakuWorker 生命周期的
// 会话级组件）；开播/停播时按注册顺序依次回调。装配层在全部组件就绪后调用。
func (m *Manager) AddSessionHook(h DanmakuWorker) {
	if h == nil {
		return
	}
	m.mu.Lock()
	m.sessionHooks = append(m.sessionHooks, h)
	m.mu.Unlock()
}

// RecordingState 返回当前录制状态；未配置录制时为 none（真实值，不假装在录）。
func (m *Manager) RecordingState() string {
	m.mu.Lock()
	r := m.recorder
	m.mu.Unlock()
	if r == nil {
		return "none"
	}
	return r.State()
}

// RecordingEnabled 返回云录制是否已开通装配（F34/N2 GetLiveStatus.record.enabled）。
func (m *Manager) RecordingEnabled() bool {
	m.mu.Lock()
	r := m.recorder
	m.mu.Unlock()
	return r != nil && r.Enabled()
}

// RecordingURL 返回出片地址（ready 后有效；未出片为空串）。
func (m *Manager) RecordingURL() string {
	m.mu.Lock()
	r := m.recorder
	m.mu.Unlock()
	if r == nil {
		return ""
	}
	if s, ok := r.(interface{ URL() string }); ok {
		return s.URL()
	}
	return ""
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

// SetCaptionSplitFn 注入 captions.output_sentences 热读（F19/G16）；
// 装配层读 settings，nil 或返回 true = 逐句投喂 TTS，false = 整段投喂。
func (m *Manager) SetCaptionSplitFn(fn func() bool) {
	m.mu.Lock()
	m.captionSplit = fn
	m.mu.Unlock()
}

// SetRecordingFlagFn 注入 misc.record_enabled 开播时读取口（F34 遗留接线）；
// nil 或返回 false 时 StartOpts.RecordingEnabled 保持 false（yaml 常开配置不受影响）。
func (m *Manager) SetRecordingFlagFn(fn func() bool) {
	m.mu.Lock()
	m.recordingFlag = fn
	m.mu.Unlock()
}

type snapshot struct {
	id         string
	liveID     string
	personaID  string
	avatarID   string
	mode       Mode // 开播模式（local-preview/public-live），随快照与事件标注
	startedAt  time.Time
	endedAt    time.Time
	transcript []string
	errorCode  string
	errorMsg   string
	epoch      int64
	// configSnapshot 开播配置冻结快照（v3 §7.3）；persist(PhaseLive) 前构造，
	// persist 透传给存储；为 nil 时存储方不得清空已存快照。
	configSnapshot map[string]any
}

func NewManager(bus *eventbus.Bus, sink EventSink, ctrl Controller) *Manager {
	return NewManagerWithStores(bus, sink, ctrl, nil, nil, nil)
}

// NewManagerWithStores 注入场次持久化、幂等存储与档案解析器；三者均可为 nil。
// resolver 是全系统唯一档案读取入口（v3 §8）；nil 时开播走旧路径（仅入参构造 opts）。
func NewManagerWithStores(bus *eventbus.Bus, sink EventSink, ctrl Controller, store SessionStore, idem Store, resolver avatar.ProfileResolver) *Manager {
	m := &Manager{
		phase: PhaseIdle, bus: bus, sink: sink, ctrl: ctrl, store: store, idem: idem,
		wsBySID: map[string]int{}, resolver: resolver,
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
	recorder := m.recorder
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
		ConfigSnapshot: s.configSnapshot, // nil 时存储方不清快照（契约 §7.3-1）
	}
	if recorder != nil {
		rec.RecordingState = recorder.State()
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

// buildConfigSnapshot 冻结开播配置（v3 §7.3-2：{"v":1,...} 内嵌版本）。
// 键白名单（v2 §2.2-2）：persona_id、persona_revision、persona_name、vidu_avatar_id、
// vidu_image_uri、voice_id、action_library_id、greeting、model；绝不入密钥。
func buildConfigSnapshot(personaID string, opts StartOpts, prof avatar.ResolvedProfile, profOK bool, mode Mode) map[string]any {
	snap := map[string]any{
		"v":        1,
		"model":    opts.Model,
		"greeting": opts.Greeting,
		// §7.6：开播模式随快照冻结（审计记录模式；local-preview 永不冒充 public）。
		"mode": string(mode),
	}
	if personaID != "" {
		snap["persona_id"] = personaID
	}
	if profOK {
		snap["persona_name"] = prof.PersonaName
		snap["persona_revision"] = prof.PersonaRevision
	}
	if opts.ViduAvatarID != "" {
		snap["vidu_avatar_id"] = opts.ViduAvatarID
	}
	if opts.ViduImageURI != "" {
		snap["vidu_image_uri"] = opts.ViduImageURI
	}
	if opts.VoiceID != "" {
		snap["voice_id"] = opts.VoiceID
	}
	if opts.ActionLibraryID != "" {
		snap["action_library_id"] = opts.ActionLibraryID
	}
	return snap
}

// StartRequest 严格 Start 入参（深化方案 v2 §7.5/§7.6）。
// Mode 仅作一致性校验，实际模式由服务端策略判定；public-live 必须携带 PreflightToken。
type StartRequest struct {
	Mode             Mode
	PreflightToken   string
	ResultDigest     string
	PersonaRevision  int64
	DraftID          string
	ExpectedRevision int64
}

// Start 返回 session_id / phase / live_id。已在 live/starting/stopping 时在
// 同一 opMu 临界区内先 stop(reason=reconnect) 再创建，杜绝并发第三条会话。
// 等价 StartWithMode(..., StartRequest{})：模式由服务端策略判定。
func (m *Manager) Start(ctx context.Context, personaID, avatarID, greeting string) (string, Phase, string, error) {
	sid, phase, liveID, _, err := m.StartWithMode(ctx, personaID, avatarID, greeting, StartRequest{})
	return sid, phase, liveID, err
}

// StartWithMode 与 Start 相同，额外返回生效 mode（响应/事件标注，§7.6 local-preview 可见）。
func (m *Manager) StartWithMode(ctx context.Context, personaID, avatarID, greeting string, req StartRequest) (string, Phase, string, Mode, error) {
	m.opMu.Lock()
	defer m.opMu.Unlock()
	return m.startLocked(ctx, personaID, avatarID, greeting, req)
}

func (m *Manager) startLocked(ctx context.Context, personaID, avatarID, greeting string, req StartRequest) (string, Phase, string, Mode, error) {
	mode, err := m.resolveMode(req.Mode)
	if err != nil {
		return "", PhaseIdle, "", mode, err
	}
	if mode == ModePublicLive && req.DraftID == "" {
		return "", PhaseIdle, "", mode, fmt.Errorf("%w: public-live requires draft_id", ErrDraftRequired)
	}
	m.mu.Lock()
	curPhase := m.phase
	m.mu.Unlock()
	// DZ 批推流前置检查：仅在装配层注入 PushGate（stream.enabled=true）后启用；
	// 未推上/被推拒/重连中均拒绝开播并给出可操作原因（fail-closed 于推流能力本身）。
	g, allowUnpushed := m.currentPushGate()
	if g != nil {
		if err := publish.CheckStartLive(g, allowUnpushed && mode == ModeLocalPreview); err != nil {
			return "", PhaseIdle, "", mode, err // errors.Is(err, publish.ErrPushRequired) 可供 service 层映射 409
		}
		// §7.6 local-preview 禁止连接公开推流：推流中开播预览是语义谎言，显式拒绝。
		if mode == ModeLocalPreview && g.Status().State == publish.StatePushing {
			return "", PhaseIdle, "", mode, ErrPreviewWhilePushing
		}
	} else if mode == ModePublicLive {
		// §7.6-5：public 模式 PushGate 必须已装配，未装配不是「允许未推流」。
		return "", PhaseIdle, "", mode, fmt.Errorf("%w: push gate not wired", ErrPushGateRequired)
	}
	// 开播预检（v3 §3.3 时序，M5）+ §7.6-3 严格化：带 persona_id 时先 Resolve，
	// 形象未同步/Vidu 资产过期返回 ErrAvatarExpired；**Resolve 失败（含档案已删/幽灵 id）
	// 两种模式一律直接报错，不回落旧路径、不创建场次**（堵 M1 fail-open）。
	var prof avatar.ResolvedProfile
	var profOK bool
	if m.resolver != nil && personaID != "" {
		p, err := m.resolver.Resolve(ctx, personaID)
		if err != nil {
			return "", curPhase, "", mode, fmt.Errorf("%w: %v", ErrResolveFailed, err)
		}
		prof, profOK = p, true
		if !prof.ViduSynced || prof.ImageExpired(time.Now()) {
			return "", curPhase, "", mode, ErrAvatarExpired
		}
	}
	// §7.6-2：public-live 在 opMu 临界区内复核 preflight token——有效未过期、
	// mode/persona revision 与当前 Resolve 一致、ready=true（无 fail/unknown 阻断项）。
	if mode == ModePublicLive {
		m.mu.Lock()
		pf := m.preflight
		m.mu.Unlock()
		if pf == nil {
			return "", curPhase, "", mode, fmt.Errorf("%w: preflight service not wired", ErrPreflightRequired)
		}
		if err := pf.Verify(ctx, VerifyRequest{
			Token: req.PreflightToken, Mode: mode, PersonaID: personaID, PersonaRevision: req.PersonaRevision,
			DraftID: req.DraftID, ExpectedRevision: req.ExpectedRevision, ResultDigest: req.ResultDigest,
		}); err != nil {
			return "", curPhase, "", mode, err
		}
	}

	m.mu.Lock()
	needStop := m.phase == PhaseLive || m.phase == PhaseStarting || m.phase == PhaseStopping
	phase := m.phase
	m.mu.Unlock()
	if needStop {
		// 复刻蓝本语义：先停再开；stop 与 start 处于同一 opMu 临界区。
		if _, _, err := m.stopLocked(ctx, "reconnect"); err != nil {
			return "", PhaseError, "", mode, err
		}
	} else if phase != PhaseIdle && phase != PhaseEnded && phase != PhaseError {
		return "", phase, "", mode, fmt.Errorf("session busy in phase %s", phase)
	}

	s := &snapshot{
		id: uuid.NewString(), personaID: personaID, avatarID: avatarID, mode: mode,
		startedAt: time.Now(), epoch: time.Now().UnixMilli(),
	}
	m.mu.Lock()
	m.session = s
	m.phase = PhaseStarting
	phase = m.phase
	m.mu.Unlock()
	m.persist(ctx, s, PhaseStarting)

	m.publish(ctx, "live.start_requested", map[string]any{"persona_id": personaID, "avatar_id": avatarID, "mode": string(mode)})

	// 构造 StartOpts（v3 §3.3）：ProfileResolver 为唯一档案读取入口，已在入口预检解析一次。
	opts := StartOpts{SessionID: s.id, Model: "vidu-s2", Greeting: greeting}
	if profOK {
		opts.ViduAvatarID = prof.ViduAvatarID
		opts.ViduImageURI = prof.ViduImageURI
		opts.VoiceID = prof.VoiceID
		opts.ActionLibraryID = prof.ActionLibraryID
		opts.RefImageURIs = prof.RefImageURIs
		if opts.Greeting == "" {
			opts.Greeting = prof.Greeting
		}
	}
	// F34 遗留接线：settings misc.record_enabled（next_session 生效组）在建场时落入
	// StartOpts；与装配层 yaml 常开（ViduConfig.RecordingEnabled）任一成立即带 enable_recording。
	m.mu.Lock()
	recFlag := m.recordingFlag
	m.mu.Unlock()
	if recFlag != nil && recFlag() {
		opts.RecordingEnabled = true
	}
	liveID, err := m.ctrl.Start(ctx, opts)
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
		m.publish(ctx, "live.start_failed", map[string]any{"error": err.Error(), "error_code": code, "mode": string(mode)})
		return s.id, PhaseError, "", mode, err
	}

	m.mu.Lock()
	s.liveID = liveID
	m.phase = PhaseLive
	phase = m.phase
	// §7.3-2：vidu_ready 门档（ctrl.Start 成功）后一次性冻结开播配置快照；
	// persist(PhaseStarting) 时本字段尚为 nil（快照必 nil，v3 §7.3 三细节）。
	s.configSnapshot = buildConfigSnapshot(personaID, opts, prof, profOK, mode)
	if req.DraftID != "" {
		s.configSnapshot["draft_id"] = req.DraftID
		s.configSnapshot["draft_revision"] = req.ExpectedRevision
		s.configSnapshot["preflight_result_digest"] = req.ResultDigest
	}
	recorder := m.recorder
	poller := m.poller
	dmWorker := m.dmWorker
	hooks := append([]DanmakuWorker(nil), m.sessionHooks...)
	refSwitch := m.refSwitch
	m.mu.Unlock()
	// F34 云录制：随场启动；未开通/失败只记录，不阻断开播。
	if recorder != nil {
		if err := recorder.OnSessionStart(ctx, liveID); err != nil {
			slog.Default().Warn("recording start skipped", "err", err)
		}
	}
	// F36 额度轮询：随场记录 credits 基线并周期落账；失败只记录，不阻断开播。
	if poller != nil {
		poller.OnSessionStart(ctx, s.id)
	}
	// F24 弹幕批读：随场启动消费循环。
	if dmWorker != nil {
		dmWorker.OnSessionStart(ctx, s.id)
	}
	// F27 礼物合并 / G24 贵宾欢迎等随场组件。
	for _, h := range hooks {
		h.OnSessionStart(ctx, s.id)
	}
	m.persist(ctx, s, PhaseLive)
	m.publish(ctx, "live.started", map[string]any{"live_id": liveID, "mode": string(mode)})

	// G3-2（v3 §3.3 时序）：动作库被 Vidu 403 拒绝、引擎去 action 重建成功 →
	// 降级事件告警，前端/运营可见动作库未生效；开播本身成功。
	if dg, ok := m.ctrl.(interface{ ActionDegraded() bool }); ok && dg.ActionDegraded() {
		m.publish(ctx, "action_library_degraded", map[string]any{"action_library_id": opts.ActionLibraryID})
		slog.Default().Warn("action library degraded: vidu 403, live without action",
			"action_library_id", opts.ActionLibraryID)
	}

	// M5 v3 §3.3 时序：参考图推送在 persist(PhaseLive)+快照冻结之后；
	// switch 失败（type14）= 降级告警事件，不算开播失败（§3.3 写死）。
	if refSwitch != nil && len(opts.RefImageURIs) > 0 {
		imgs := make([]refs.Image, 0, len(opts.RefImageURIs))
		for _, u := range opts.RefImageURIs {
			imgs = append(imgs, refs.Image{URL: u})
		}
		if _, err := refSwitch.Switch(ctx, imgs); err != nil {
			m.publish(ctx, "refs_push_failed", map[string]any{"error": err.Error(), "count": len(imgs)})
			slog.Default().Warn("refs push degraded, live continues", "err", err)
		}
	}
	// 未同步 Vidu 的 ref 在解析期已被 profileOf 跳过；按跳过数告警（v3 §3.3 失败分支）。
	if profOK && prof.UnsyncedRefCount > 0 {
		m.publish(ctx, "refs_push_skipped", map[string]any{"skipped": prof.UnsyncedRefCount})
		slog.Default().Warn("refs skipped: not synced to vidu", "skipped", prof.UnsyncedRefCount)
	}
	// M5 v3 §3.2：greeting 非空 → 走既有 Say 链路一次；TTS 失败发 greeting_failed，不阻断开播。
	if strings.TrimSpace(opts.Greeting) != "" {
		if _, err := m.Say(ctx, opts.Greeting, "greeting"); err != nil {
			m.publish(ctx, "greeting_failed", map[string]any{"error": err.Error()})
			slog.Default().Warn("greeting tts failed, live continues", "err", err)
		}
	}
	return s.id, phase, liveID, mode, nil
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

	// F34 云录制：停播后结束录制并轮询出片；失败只记录，不阻断收场。
	m.mu.Lock()
	recorder := m.recorder
	poller := m.poller
	dmWorker := m.dmWorker
	hooks := append([]DanmakuWorker(nil), m.sessionHooks...)
	m.mu.Unlock()
	if recorder != nil && s != nil && s.liveID != "" {
		if _, err := recorder.OnSessionStop(ctx, s.liveID); err != nil {
			slog.Default().Warn("recording stop/poll failed", "err", err)
		}
	}
	// F36 额度轮询：停止并落最终一笔；失败只记录，不阻断收场。
	if poller != nil && s != nil {
		poller.OnSessionStop(ctx, s.id)
	}
	// F24 弹幕批读：停播停止消费循环。
	if dmWorker != nil && s != nil {
		dmWorker.OnSessionStop(ctx, s.id)
	}
	// F27 礼物合并 / G24 贵宾欢迎等随场组件。
	for _, h := range hooks {
		if s != nil {
			h.OnSessionStop(ctx, s.id)
		}
	}

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
	sid, phase, liveID, _, err := m.StartIdempotentWithMode(ctx, key, personaID, avatarID, greeting, StartRequest{})
	return sid, phase, liveID, err
}

// StartIdempotentWithMode 与 StartIdempotent 相同，携带严格 Start 入参并返回生效 mode。
func (m *Manager) StartIdempotentWithMode(ctx context.Context, key, personaID, avatarID, greeting string, req StartRequest) (string, Phase, string, Mode, error) {
	const prefix = "start:"
	if key == "" || m.idem == nil {
		return m.StartWithMode(ctx, personaID, avatarID, greeting, req)
	}
	m.idemMu.Lock()
	defer m.idemMu.Unlock()
	if r, ok, err := m.idem.Get(ctx, prefix+key); err == nil && ok {
		return r.SessionID, r.Phase, r.LiveID, Mode(r.Mode), nil
	}
	sessionID, phase, liveID, mode, err := m.StartWithMode(ctx, personaID, avatarID, greeting, req)
	if err != nil {
		return sessionID, phase, liveID, mode, err
	}
	_ = m.idem.Put(ctx, prefix+key, Result{
		SessionID: sessionID, Phase: phase, LiveID: liveID, OK: true, Mode: string(mode),
	}, DefaultIdempotencyTTL)
	return sessionID, phase, liveID, mode, nil
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
	// F19/G16：captions.output_sentences 热读；nil（未装配）按默认 true 逐句。
	split := m.captionSplit == nil || m.captionSplit()
	m.mu.Unlock()

	sentences := []string{text}
	if split {
		if parts := splitSentences(text); len(parts) > 0 {
			sentences = parts
		}
	}
	var firstID string
	for _, sentence := range sentences {
		id, err := m.sayOne(ctx, sentence, mode)
		if err != nil {
			return firstID, err
		}
		if firstID == "" {
			firstID = id
		}
	}
	return firstID, nil
}

// splitSentences 按蓝本分句规则（internal/llm.SentenceSplitter）把整段文本切成
// 有序句子；无任何句读时原样返回单句。开=逐句投喂 TTS 并逐句上屏，关=整段。
func splitSentences(text string) []string {
	sp := llm.NewSentenceSplitter()
	out := sp.Feed(text)
	if tail := sp.Flush(); tail != "" {
		out = append(out, tail)
	}
	return out
}

// sayOne 单句口播：先发 partial 让字幕立即上屏，TTS 返回后再发 final。
func (m *Manager) sayOne(ctx context.Context, text, mode string) (string, error) {
	m.mu.Lock()
	sessionID, epoch := m.session.id, m.session.epoch
	index := len(m.session.transcript)
	m.mu.Unlock()

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

// LlmMemory 是 LLM 会话记忆端口（G07），由 internal/biz/chat.Manager 实现；
// nil 表示未装配（如实返回 0，不假装清过）。
type LlmMemory interface {
	// ResetHistory 清空指定会话记忆，返回真实清除的轮数。
	ResetHistory(key string) int
}

// SetLlmMemory 注入会话记忆端口；装配层在组件就绪后调用一次。
func (m *Manager) SetLlmMemory(mem LlmMemory) {
	m.mu.Lock()
	m.llmMem = mem
	m.mu.Unlock()
}

// ResetLlm 清空当前场次的 LLM 会话记忆（蓝本 bridge.py:536 /api/llm/reset →
// engine.llm.reset_history），返回真实清除的轮数；未装配记忆端口或无进行中场次时为 0。
func (m *Manager) ResetLlm(ctx context.Context) int {
	m.mu.Lock()
	mem := m.llmMem
	key := ""
	if m.session != nil {
		key = m.session.id
	}
	m.mu.Unlock()
	cleared := 0
	if mem != nil && key != "" {
		cleared = mem.ResetHistory(key)
	}
	m.publish(ctx, "llm.reset", map[string]any{"cleared_turns": cleared})
	return cleared
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
		out.Mode = string(m.session.mode)
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
