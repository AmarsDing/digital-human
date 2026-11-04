//go:build wireinject

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/google/wire"

	"github.com/teamwork/digital-human/internal/biz/action"
	"github.com/teamwork/digital-human/internal/biz/archive"
	"github.com/teamwork/digital-human/internal/biz/audit"
	"github.com/teamwork/digital-human/internal/biz/avatar"
	"github.com/teamwork/digital-human/internal/biz/billing"
	"github.com/teamwork/digital-human/internal/biz/capability"
	"github.com/teamwork/digital-human/internal/biz/chat"
	"github.com/teamwork/digital-human/internal/biz/choreo"
	"github.com/teamwork/digital-human/internal/biz/contentgate"
	"github.com/teamwork/digital-human/internal/biz/contentpkg"
	"github.com/teamwork/digital-human/internal/biz/danmaku"
	"github.com/teamwork/digital-human/internal/biz/gift"
	"github.com/teamwork/digital-human/internal/biz/interaction"
	"github.com/teamwork/digital-human/internal/biz/koubo"
	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/biz/media"
	"github.com/teamwork/digital-human/internal/biz/patron"
	"github.com/teamwork/digital-human/internal/biz/persona"
	"github.com/teamwork/digital-human/internal/biz/recording"
	"github.com/teamwork/digital-human/internal/biz/refs"
	"github.com/teamwork/digital-human/internal/biz/rtc"
	"github.com/teamwork/digital-human/internal/biz/settings"
	"github.com/teamwork/digital-human/internal/biz/song"
	"github.com/teamwork/digital-human/internal/biz/tts"
	"github.com/teamwork/digital-human/internal/biz/vidukeys"
	"github.com/teamwork/digital-human/internal/biz/voice"
	"github.com/teamwork/digital-human/internal/conf"
	"github.com/teamwork/digital-human/internal/data"
	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/repo"
	"github.com/teamwork/digital-human/internal/engine"
	enginetts "github.com/teamwork/digital-human/internal/engine/tts"
	enginevidu "github.com/teamwork/digital-human/internal/engine/vidu"
	"github.com/teamwork/digital-human/internal/llm"
	"github.com/teamwork/digital-human/internal/publish"
	"github.com/teamwork/digital-human/internal/relay"
	"github.com/teamwork/digital-human/internal/server"
	"github.com/teamwork/digital-human/internal/service"
	"github.com/teamwork/digital-human/pkg/eventbus"
	"github.com/teamwork/digital-human/pkg/logx"
)

func InitApp(cfgPath string, version VersionInfo) (*App, func(), error) {
	wire.Build(
		provideConfig,
		provideLogger,
		provideData,
		provideBus,
		provideSink,
		provideAudit,
		provideContentGate,
		provideSessionStore,
		provideIdempotency,
		provideBilling,
		provideCreditsPoller,
		provideRealDeps,
		provideController,
		provideRecorder,
		provideAvatarStore,
		provideAvatar,
		provideAvatarImages,
		provideAvatarSync,
		wire.Bind(new(avatar.ProfileResolver), new(*avatar.Service)),
		provideManager,
		provideDanmaku,
		provideSettings,
		providePersona,
		provideLLMClient,
		provideChat,
		provideVoiceASR,
		voiceConfigFrom,
		provideVoiceTickets,
		provideVoiceCoordinator,
		provideVoiceStatus,
		provideViduKeys,
		provideDanmakuWorker,
		provideInteraction,
		provideSongRequests,
		provideGiftPatrons,
		provideCapabilities,
		providePreflight,
		provideContentpkgStore,
		provideContentpkg,
		provideTickets,
		provideNonces,
		provideHealth,
		providePanelRuntime,
		provideLiveService,
		provideRTCIssuer,
		provideDanmakuService,
		provideSettingsService,
		providePublisher,
		provideCollectorTracker,
		provideStreamService,
		provideCollectorService,
		provideHTTPServer,
		provideApp,
	)
	return nil, nil, nil
}

func provideConfig(path string) (*conf.Config, error) { return conf.Load(path) }

func provideLogger(cfg *conf.Config) *slog.Logger { return logx.New(cfg.Log.Level, cfg.Log.Format) }

func provideData(cfg *conf.Config, logger *slog.Logger) (*data.Data, func(), error) {
	return data.NewData(cfg, logger)
}

func provideBus() *eventbus.Bus { return eventbus.New(2048) }

func provideSink(d *data.Data) live.EventSink {
	sw := repo.NewSwitchableSink(live.NewMemorySink(4096))
	d.SetOnDBReady(sw.SetEnt)
	return sw
}

// realDeps 真实引擎外部依赖（Vidu HTTP + OmniVoice 边车）；
// real 模式始终装配管理客户端，支持先在 UI 保存 Key、查询余额及同步形象。
// 开播仍由 Controller 单独检查 Vidu 与语音依赖，不能把管理 API 可达当作可开播。
type realDeps struct {
	vidu *enginevidu.Client
	tts  *enginetts.Client
}

func provideRealDeps(cfg *conf.Config) *realDeps {
	if cfg.Engine.Mode != "real" {
		return nil
	}
	return &realDeps{
		vidu: enginevidu.NewClient(enginevidu.Options{
			APIKey:  cfg.Engine.ViduKey,
			BaseURL: cfg.Engine.ViduBaseURL,
		}),
		tts: enginetts.NewClient(enginetts.Options{BaseURL: cfg.Engine.OmniVoiceAddr}),
	}
}

// provideController 默认 fail-closed：仅 engine.mode=dev 才装配 fake；
// real 模式凭据齐全时注入 ViduController 真实工厂，否则 503 engine_not_configured。
func provideController(cfg *conf.Config, deps *realDeps, logger *slog.Logger) live.Controller {
	opts := live.EngineOptions{
		Mode:          cfg.Engine.Mode,
		ViduKey:       cfg.Engine.ViduKey,
		OmniVoiceAddr: cfg.Engine.OmniVoiceAddr,
	}
	if deps != nil {
		opts.RealFactory = func(context.Context) (live.Controller, error) {
			var greetingDecoder media.Decoder
			if decoder, err := media.NewFfmpegDecoder(); err == nil {
				greetingDecoder = decoder
			} else {
				greetingDecoder = media.UnavailableDecoder{Err: err}
			}
			return engine.NewViduController(engine.ViduConfig{
				Client:           deps.vidu,
				WS:               enginevidu.WSOptions{URL: cfg.Engine.ViduWSURL},
				TTS:              deps.tts,
				GreetingDecoder:  greetingDecoder,
				Logger:           logger,
				RecordingEnabled: cfg.Engine.RecordingEnabled,
			})
		}
	}
	return live.NewConfiguredController(context.Background(), opts, logger)
}

// provideRecorder F34 云录制：real 模式且配置开通才注入 Supervisor；
// 其他情况为 nil，Manager 如实记录 none（不假装在录）。
func provideRecorder(cfg *conf.Config, deps *realDeps, logger *slog.Logger) live.Recorder {
	if deps == nil || !cfg.Engine.RecordingEnabled {
		return nil
	}
	return recording.New(true, recording.ViduClient{C: deps.vidu}, logger)
}

// recorderSupervisor 把 live.Recorder 断言回 *recording.Supervisor 供面板路由使用。
func recorderSupervisor(r live.Recorder) *recording.Supervisor {
	if s, ok := r.(*recording.Supervisor); ok {
		return s
	}
	return nil
}

// viduCreditsQuery 有凭据时返回实时额度查询闭包（透传 vidu client）；否则 nil（路由返回空态）。
func viduCreditsQuery(deps *realDeps) func(context.Context) (map[string]any, error) {
	if deps == nil || deps.vidu == nil {
		return nil
	}
	c := deps.vidu
	return func(ctx context.Context) (map[string]any, error) { return c.Credits(ctx) }
}

// provideRefsSignaler G3-1：refs 推送信令按引擎模式装配（参照 engine_select.go 先例）。
//   - mode=dev：显式 FakeSignaler（仅本地自测）并显式告警；
//   - real：桥到控制器 type11 出口（ViduController.PromptOperation），生产链路接通；
//   - real 但控制器无该出口（凭据缺失的 UnavailableController）：注入 nil 操作口，
//     SetPrompt 如实返回 ErrNoSignal（fail-closed，开播时序走 refs_push_failed 降级），
//     绝不静默成功。
func provideRefsSignaler(cfg *conf.Config, ctrl live.Controller) refs.Signaler {
	if cfg.Engine.Mode == "dev" {
		slog.Default().Warn("refs signaler mode=dev：使用 FakeSignaler，仅限本地自测")
		return refs.NewFakeSignaler()
	}
	if op, ok := ctrl.(refs.PromptOperator); ok {
		return refs.NewViduSignaler(op)
	}
	return refs.NewViduSignaler(nil)
}

// provideAudit 启动时用内存审计兜底；PG 运行期就绪后切换为 Ent。
func provideAudit(d *data.Data) *audit.Service {
	mem := audit.NewMemorySink(1000)
	sw := repo.NewSwitchableAuditSink(auditMemoryBridge{mem: mem})
	d.SetOnDBReady(sw.SetEnt)
	return audit.NewService(auditSinkBridge{sw: sw})
}

func provideContentGate(cfg *conf.Config, audits *audit.Service) *contentgate.Gate {
	return contentgate.New(contentgate.Options{
		Enabled: cfg.ContentGate.Enabled, RulesPath: cfg.ContentGate.RulesPath,
		AllowReview: cfg.ContentGate.AllowReview, CircuitBreakerThreshold: cfg.ContentGate.CircuitBreakerThreshold,
		Recorder: audits,
	})
}

// provideSessionStore 启动时用内存场次存储兜底；PG 运行期就绪后切换为 Ent。
func provideSessionStore(d *data.Data) live.SessionStore {
	sw := repo.NewSwitchableSessionStore(live.NewMemorySessionStore())
	d.SetOnDBReady(sw.SetEnt)
	return sw
}

// provideIdempotency Redis 可用时优先 Redis 版（多实例共享、重启不丢）；
// 否则内存兜底、PG 运行期就绪后切换为 Ent（首写胜出语义一致）。
// 注意：Redis 在进程启动后才就绪时不会补切换幂等存储（Probe 重连只切 PG 侧）。
func provideIdempotency(d *data.Data) live.Store {
	if d.RedisAvailable && d.Redis != nil {
		return live.NewRedisIdempotencyStore(d.Redis, live.DefaultIdempotencyTTL)
	}
	sw := repo.NewSwitchableIdempotencyStore(live.NewMemoryIdempotencyStore(live.DefaultIdempotencyTTL))
	d.SetOnDBReady(sw.SetEnt)
	return sw
}

// provideBilling 账本服务：JSON 文件作为无 PG 降级（重启不丢），
// PG 运行期就绪后切换为 Ent costledger 表；切换不搬移文件历史账目。
func provideBilling(d *data.Data) *billing.Service {
	fallback := billing.NewFileStore(filepath.Join("storage", "cost", "ledger.json"))
	sw := repo.NewSwitchableCostStore(fallback)
	d.SetOnDBReady(sw.SetEnt)
	return billing.NewService(sw)
}

// viduCreditsSource 把 Vidu /ent/v2/credits 响应适配为剩余额度（credits.remain）。
type viduCreditsSource struct{ c *enginevidu.Client }

func (v viduCreditsSource) Credits(ctx context.Context) (int64, error) {
	m, err := v.c.Credits(ctx)
	if err != nil {
		return 0, err
	}
	return parseCreditsRemain(m)
}

// parseCreditsRemain 容错解析：顶层数值、credits{remain} 嵌套、data 包裹三种形态。
func parseCreditsRemain(m map[string]any) (int64, error) {
	if v, ok := toInt64(m["remain"]); ok {
		return v, nil
	}
	if c, ok := m["credits"].(map[string]any); ok {
		if v, ok := toInt64(c["remain"]); ok {
			return v, nil
		}
	} else if v, ok := toInt64(m["credits"]); ok {
		return v, nil
	}
	if d, ok := m["data"].(map[string]any); ok {
		if v, err := parseCreditsRemain(d); err == nil {
			return v, err
		}
	}
	return 0, fmt.Errorf("credits 响应缺少 remain 字段")
}

func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	case json.Number:
		i, err := n.Int64()
		return i, err == nil
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil
	}
	return 0, false
}

// provideCreditsPoller F36：仅 real 模式且 Vidu 凭据齐全时注入额度轮询；
// 其余情况为 nil（Manager 不启动轮询，账本只来自手工/其他来源，不假装在轮询）。
// 轮询间隔取 max(30s, credits_poll_s)（蓝本口径），默认 60s。
func provideCreditsPoller(cfg *conf.Config, deps *realDeps, bill *billing.Service, bus *eventbus.Bus, logger *slog.Logger) live.CreditsPoller {
	if deps == nil {
		return nil
	}
	interval := time.Duration(cfg.Billing.CreditsPollS) * time.Second
	if interval < billing.MinPollInterval {
		interval = billing.MinPollInterval
	}
	return billing.NewPoller(bill, viduCreditsSource{c: deps.vidu}, billing.PollerOptions{
		Interval: interval,
		Logger:   logger,
		EmitEvent: func(typ string, payload map[string]any) {
			sid, _ := payload["session_id"].(string)
			bus.Publish(context.Background(), sid, typ, 0, payload)
		},
	})
}

// provideAvatarStore 数字人档案库存储（v3 §7.4）：无 PG 走 MemoryAvatarStore；
// PG 就绪经 SetOnDBReady 热切（不回迁，同 SwitchablePersonaStore 先例），
// 热切后执行 settings 旧键 → 默认档案迁移（§7.2，幂等键 avatar_migration_v1）。
func provideAvatarStore(d *data.Data, logger *slog.Logger) *repo.SwitchableAvatarStore {
	sw := repo.NewSwitchableAvatarStore(avatar.NewMemoryStore())
	d.SetOnDBReady(func(c *ent.Client) {
		sw.SetEnt(c)
		if err := repo.MigrateAvatars(context.Background(), c, logger); err != nil {
			logger.Warn("avatar migration failed（下次启动重试）", "err", err)
		}
	})
	return sw
}

// provideAvatar 数字人档案库服务（v3 §7/§8）：ProfileResolver 为全系统唯一档案读取
// 入口；CRUD/图片管线经面板 11 RPC（M2），sync-vidu 管线 M3 已落地。
func provideAvatar(store *repo.SwitchableAvatarStore) *avatar.Service {
	return avatar.NewService(store)
}

// provideAvatarImages 图片管线（内容寻址 storage/images，§5.3 校验顺序写死）；
// panelRuntime 与 sync 管线共享同一实例（同一路径根与 dedup 视图）。
func provideAvatarImages(avatarStore *repo.SwitchableAvatarStore) *avatar.ImageService {
	return avatar.NewImageService(avatarStore, filepath.Join("storage", "images"))
}

// provideAvatarSync sync-vidu 202 异步管线（v3 §2.2，M3）：real 模式且 Vidu 凭据齐全
// 时注入真实 uploader，否则 nil → sync-vidu 503 vidu_key_unconfigured（fail-closed）。
// 开机对账经 SetOnDBReady 注册：本 provider 依赖 avatarStore，wire 保证其在
// provideAvatarStore 之后执行，故对账回调晚于热切回调触发，Ent 就绪后重置非终态
// vidu_sync_state 为 error:interrupted 并 best-effort 清理孤儿资产（补偿③）。
func provideAvatarSync(
	deps *realDeps,
	avatarStore *repo.SwitchableAvatarStore,
	images *avatar.ImageService,
	d *data.Data,
	logger *slog.Logger,
) *avatar.SyncService {
	var up avatar.SyncUploader
	if deps != nil {
		up = deps.vidu
	}
	svc := avatar.NewSyncService(avatarStore, images, up, logger)
	d.SetOnDBReady(func(*ent.Client) {
		svc.ReconcileOnBoot(context.Background())
	})
	return svc
}

func provideManager(
	cfg *conf.Config,
	bus *eventbus.Bus,
	sink live.EventSink,
	ctrl live.Controller,
	sessions live.SessionStore,
	idem live.Store,
	resolver avatar.ProfileResolver,
	recorder live.Recorder,
	poller live.CreditsPoller,
	chatMgr *chat.Manager,
	pub *publish.Publisher,
	st *settings.Service,
	gate *contentgate.Gate,
) *live.Manager {
	m := live.NewManagerWithStores(bus, sink, ctrl, sessions, idem, resolver)
	m.SetContentGate(gate)
	m.SetModePolicy(modePolicyFromConfig(cfg))
	if recorder != nil {
		m.SetRecorder(recorder)
	}
	if poller != nil {
		m.SetCreditsPoller(poller)
	}
	if chatMgr != nil {
		// G07：ResetLlm 清真实会话记忆（chat.ResetHistory），返回清除轮数。
		m.SetLlmMemory(chatMgr)
	}
	if pub != nil {
		m.SetPushGate(pub, cfg.Stream.AllowUnpushedLive)
	}
	// F19/G16：captions.output_sentences 热读进 Say 路径（开=逐句投喂 TTS，关=整段）。
	m.SetCaptionSplitFn(func() bool {
		v, ok := st.Value(context.Background(), "captions", "output_sentences").(bool)
		if !ok {
			return true // 缺键按产品默认 true（settings.DefaultGroups 同值）
		}
		return v
	})
	// F34 遗留接线：misc.record_enabled 开播时读入 StartOpts.RecordingEnabled。
	m.SetRecordingFlagFn(func() bool {
		v, _ := st.Value(context.Background(), "misc", "record_enabled").(bool)
		return v
	})
	// G09：音频文件优先；只在未配置文件时保留文本/TTS 回退。
	m.SetGreetingAudioFileFn(func() string {
		v, _ := st.Value(context.Background(), "session", "greeting_audio_file").(string)
		return v
	})
	return m
}

// provideDanmaku 启动时用内存环形缓冲兜底；PG 运行期就绪后弹幕事件落 danmakuevent 表。
func provideDanmaku(d *data.Data) *danmaku.Service {
	svc := danmaku.NewService(danmaku.ModeOff)
	sw := repo.NewSwitchableDanmakuSink(danmaku.NewMemoryEventSink(512))
	d.SetOnDBReady(sw.SetEnt)
	svc.SetEventSink(sw)
	return svc
}

// provideSettings 启动时用内存设置兜底；PG 运行期就绪后切换为 Ent，
// 并对存量明文 llm.api_key 执行原地加密迁移（幂等，enc:v1: 前缀自识别，
// 失败仅告警、下次启动重试）。
func provideSettings(d *data.Data, logger *slog.Logger) *settings.Service {
	mem := settings.NewMemoryStore()
	sw := repo.NewSwitchableSettingStore(mem)
	d.SetOnDBReady(func(c *ent.Client) {
		sw.SetEnt(c)
		if err := repo.MigrateLLMAPIKey(context.Background(), c, logger); err != nil {
			logger.Warn("llm api_key migration failed（下次启动重试）", "err", err)
		}
	})
	return settings.NewService(sw)
}

// liveSpeaker 把 live.Manager.Say 适配为弹幕批读 Speaker 出口（mode 固定 danmaku）。
type liveSpeaker struct{ m *live.Manager }

func (s liveSpeaker) Say(ctx context.Context, text string) error {
	_, err := s.m.Say(ctx, text, "danmaku")
	return err
}

// provideDanmakuWorker F24 弹幕批读 worker：Speaker 接 live.Manager.Say（TTS/口播链路），
// Chatter 接 chat.Manager（dm_anchor 完整回答）；随开播/停播由 Manager 生命周期钩子启停。
func provideDanmakuWorker(
	dm *danmaku.Service,
	st *settings.Service,
	chatMgr *chat.Manager,
	mgr *live.Manager,
	bus *eventbus.Bus,
	logger *slog.Logger,
) *danmaku.Worker {
	w := danmaku.NewWorker(dm, liveSpeaker{m: mgr}, chatMgr, st, danmaku.WorkerOptions{
		Logger: logger,
		EmitEvent: func(typ string, payload map[string]any) {
			sid, _ := payload["session_id"].(string)
			bus.Publish(context.Background(), sid, typ, 0, payload)
		},
	})
	mgr.SetDanmakuWorker(w)
	return w
}

// giftSpeaker 把 live.Manager.Say 适配为 gift/patron 的 Speaker 出口（mode 区分来源）。
type giftSpeaker struct{ m *live.Manager }

// chatSpeaker 规则优先普通聊天复用唯一口播出口。
type chatSpeaker struct{ m *live.Manager }

func (s chatSpeaker) Say(ctx context.Context, text string) error {
	_, err := s.m.Say(ctx, text, "chat")
	return err
}

func (s giftSpeaker) Say(ctx context.Context, text, mode string) error {
	_, err := s.m.Say(ctx, text, mode)
	return err
}

// patronSpeaker 固定 mode=patron（贵宾欢迎播报出口，与弹幕/礼物区分便于观测）。
type patronSpeaker struct{ m *live.Manager }

func (s patronSpeaker) Say(ctx context.Context, text string) error {
	_, err := s.m.Say(ctx, text, "patron")
	return err
}

// provideGiftPatrons F 批礼物/贵宾切片总接线（F27/F28/F29/G24/G25/G26）：
//   - gift.Merger：danmaku kind=gift 事件进时间窗合并器（不直接念），满足触发条件合成
//     「谢谢 xx 的 N 个 yy」入 Say；窗口/阈值/节流键读 settings gift 组（默认对齐蓝本
//     config.example.json:99-115）；F28 贵宾欢迎（单件钻石价 > member.threshold_diamond，
//     once_per_session + cooldown_s 双闸，模板 member.template）；G26 复用 danmaku.block_words。
//   - patron.Service：贵宾名单持久化（Ent patron 表，无 PG 内存兜底热切）；
//     G24 进场欢迎（danmaku kind=join 钩子，member.forward_joins 开关 + welcome_template）。
//   - kindTap 把 danmaku 的 gift/join 事件分流到上述两者；随场启停挂 live.Manager session 钩子。
//
// 返回 patron.Service 供面板五路由使用。
func provideGiftPatrons(
	d *data.Data,
	dm *danmaku.Service,
	st *settings.Service,
	mgr *live.Manager,
	bus *eventbus.Bus,
	logger *slog.Logger,
	ix *interactionRuntime,
) *patron.Service {
	emit := func(typ string, payload map[string]any) {
		sid, _ := payload["session_id"].(string)
		bus.Publish(context.Background(), sid, typ, 0, payload)
	}
	// B3 §5.4：礼物答谢走 interaction 闭环（Decision+EffectOutbox 落库 → dispatcher →
	// Say → 回执写状态）；其他 mode（patron 欢迎等）由 GiftSpeaker 透传内层。
	giftMerger := gift.NewMerger(st, ix.gift, emit, logger)
	mgr.AddSessionHook(giftMerger)

	patStore := repo.NewSwitchablePatronStore(patron.NewMemoryStore())
	d.SetOnDBReady(patStore.SetEnt)
	patSvc := patron.NewService(patStore, st, patronSpeaker{m: mgr}, emit)
	mgr.AddSessionHook(patSvc)

	// gift/join 分流：批读 worker 对两类事件仍不念，此处进入合并器/欢迎链路。
	dm.SetKindTap(func(kind string, ev danmaku.TapEvent) {
		ctx := context.Background()
		if kind == "gift" {
			item, num := parseGiftText(ev.Text)
			giftMerger.Ingest(ctx, gift.Event{
				UserID: ev.UserID, UserName: ev.UserName,
				Item: item, Num: num, Unit: int(ev.Diamond), Value: int(ev.Diamond) * num,
			})
			return
		}
		// join：贵宾进场欢迎（G24）。
		_, _ = patSvc.OnJoin(ctx, ev.UserName, ev.UserID, "")
	})
	return patSvc
}

// parseGiftText 从 relay 礼物文本（"小心心 x5"）还原礼物名与数量；无 " xN" 后缀时 num=1。
func parseGiftText(text string) (item string, num int) {
	num = 1
	i := strings.LastIndex(text, " x")
	if i > 0 {
		if n, err := strconv.Atoi(strings.TrimSpace(text[i+2:])); err == nil && n > 0 {
			num = n
			item = strings.TrimSpace(text[:i])
			return item, num
		}
	}
	return strings.TrimSpace(text), num
}

// providePersona F38 人设兼容壳（v3 §1.2 拍板：avatar 档案服务为 Persona 行唯一写方）。
// 存储层委托 SwitchableAvatarStore（AvatarBackedStore），四路由语义不变；
// 档案字段（vidu_*/refs/greeting 等）由 avatar 侧独占，兼容壳读改写保留不抹除。
// 切换后旁路回写 settings persona.active_id 仅供抽屉显示。
func providePersona(avatarStore *repo.SwitchableAvatarStore, st *settings.Service) *persona.Service {
	svc := persona.NewService(persona.NewAvatarBackedStore(avatarStore))
	svc.SetActiveSync(func(ctx context.Context, id string) {
		_ = st.Patch(ctx, "persona", map[string]any{"active_id": id})
	})
	svc.SetPromptFallback(func(ctx context.Context) string {
		return settingStr(st, ctx, "llm", "system")
	})
	return svc
}

// provideChat F15 会话编排单例：Streamer 每次调用取 settings.llm 快照（热改立即生效）；
// ProfileResolver 用 persona.Service（G05 人设优先，llm.system 为第二来源）。
func provideLLMClient(st *settings.Service) *llm.Client {
	return llm.NewClient(func() llm.Config {
		ctx := context.Background()
		cfg := llm.Config{
			BaseURL:     settingStr(st, ctx, "llm", "base_url"),
			APIKey:      settingStr(st, ctx, "llm", "api_key"),
			Model:       settingStr(st, ctx, "llm", "model"),
			Temperature: settingFloat(st, ctx, "llm", "temperature"),
		}
		if raw, ok := st.Value(ctx, "llm", "fallback_models").([]any); ok {
			for _, v := range raw {
				if s, _ := v.(string); strings.TrimSpace(s) != "" {
					cfg.FallbackModels = append(cfg.FallbackModels, s)
				}
			}
		}
		return cfg
	})
}

func provideChat(p *persona.Service, cli *llm.Client) *chat.Manager {
	return chat.NewManager(cli, p)
}

func settingStr(st *settings.Service, ctx context.Context, group, key string) string {
	s, _ := st.Value(ctx, group, key).(string)
	return s
}

func settingFloat(st *settings.Service, ctx context.Context, group, key string) float64 {
	switch n := st.Value(ctx, group, key).(type) {
	case float64:
		return n
	case int:
		return float64(n)
	}
	return 0
}

// provideViduKeys F42/G18 多 key 管理：内存兜底、PG 热切；激活即热改 vidu client（real 模式）
// 并同步 settings vidu_key.active_tail8（抽屉显示）；DB 就绪后把库中 active key 应用到 client
// （覆盖 yaml 启动值，对齐蓝本 appconf 持久化语义）。切换后既有 Vidu 资产失效仅 UI 提示（v3 H6）。
func provideViduKeys(cfg *conf.Config, d *data.Data, deps *realDeps, st *settings.Service, logger *slog.Logger) *vidukeys.Service {
	sw := repo.NewSwitchableViduKeyStore(vidukeys.NewMemoryStore())
	apply := func(plain string) {
		if deps != nil && deps.vidu != nil {
			deps.vidu.SetAPIKey(plain)
		}
		if err := st.Patch(context.Background(), "vidu_key", map[string]any{"active_tail8": vidukeys.Tail8(plain)}); err != nil {
			logger.Warn("vidu key 激活后同步 settings 失败（不影响热切换）", "err", err)
		}
	}
	svc := vidukeys.NewService(sw, apply)
	d.SetOnDBReady(func(c *ent.Client) {
		sw.SetEnt(c)
		if err := svc.ApplyActive(context.Background()); err != nil {
			logger.Warn("vidu key 库内 active 应用失败（保持 yaml key）", "err", err)
		}
	})
	return svc
}

// provideTickets Redis 可用时用 RedisTicketStore（多实例共享、重启不丢）；
// 否则内存版（重启丢 ticket、多实例不共享，差异见 README）。
// 注意：启动后才就绪的 Redis 不会补切换 ticket 存储。
func provideTickets(cfg *conf.Config, d *data.Data) server.TicketStore {
	if d.RedisAvailable && d.Redis != nil {
		return server.NewRedisTicketStore(d.Redis, cfg.Security.TicketTTL)
	}
	return server.NewMemoryTicketStore(cfg.Security.TicketTTL)
}

// provideNonces HMAC nonce 去重：Redis 可用时用 RedisNonceStore，否则内存版。
func provideNonces(d *data.Data) server.NonceStore {
	if d.RedisAvailable && d.Redis != nil {
		return server.NewRedisNonceStore(d.Redis)
	}
	return server.NewMemoryNonceStore(0)
}

func provideHealth(version VersionInfo, cfg *conf.Config, d *data.Data, m *live.Manager, caps live.CapabilitySource) *server.Health {
	h := server.NewHealth(string(version), func(ctx context.Context) server.DataStatus {
		st := d.Probe(ctx)
		return server.DataStatus{Postgres: st.Postgres, Redis: st.Redis}
	})
	// health.require_dependencies=true 恢复旧行为（缺依赖 503）；默认 false 三态：
	// 进程可服务即 200，缺依赖以 status=degraded + missing 显式标注。
	h.Strict = cfg.Health.RequireDependencies
	h.Phase = func() string { return string(m.Snapshot().Phase) }
	// capabilities 由真实装配计算：引擎模式 dev/real 决定 vidu/omnivoice 是否可用，
	// PG/Redis 可用性取真实探活结果，不再恒 false。
	h.Caps = func(ctx context.Context) map[string]bool {
		st := d.Probe(ctx)
		real := cfg.Engine.Mode == "real"
		return map[string]bool{
			"vidu":       real && cfg.Engine.ViduKey != "",
			"omnivoice":  real && cfg.Engine.OmniVoiceAddr != "",
			"ffmpeg":     media.FfmpegAvailable(),
			"engine_dev": cfg.Engine.Mode == "dev",
			"postgres":   st.Postgres,
			"redis":      st.Redis,
		}
	}
	// §2.2 能力矩阵状态词（health/ready 输出；UI 可见）。
	h.CapWords = func(ctx context.Context) map[string]string {
		out := map[string]string{}
		for k, v := range caps(ctx) {
			out[k] = string(v)
		}
		return out
	}
	return h
}

func provideKouboActionGen(store choreo.Store, streamer koubo.ActionStreamer, llmConfigured bool) koubo.ActionGen {
	if llmConfigured {
		return koubo.NewLLMActionGen(store, streamer)
	}
	return koubo.NewRuleActionGen(store)
}

// panelRuntime 持有 M3 面板依赖与共享调度器，避免多个 *media.Library 在 wire 中同名冲突。
type panelRuntime struct {
	deps           server.PanelDeps
	scheduler      *action.Scheduler
	songScheduler  *song.Scheduler
	dm             *danmaku.Service
	player         media.Player
	songProbe      func(context.Context) error
	koubo          *koubo.Service
	actionGen      koubo.ActionGen
	actionGenProbe func(context.Context) error
	// sender/synth §2.2 能力矩阵探测源（装配点显式化：类型即能力证据）。
	sender         action.Sender
	remote         action.Remote
	actionProbe    func(context.Context) error
	synth          tts.Synth
	contentGate    *contentgate.Gate
	contentStorage []contentpkg.StorageProbe
}

func providePanelRuntime(
	bus *eventbus.Bus,
	st *settings.Service,
	dm *danmaku.Service,
	sessions live.SessionStore,
	mgr *live.Manager,
	cfg *conf.Config,
	deps *realDeps,
	ctrl live.Controller,
	bill *billing.Service,
	personas *persona.Service,
	avatars *avatar.Service,
	avatarStore *repo.SwitchableAvatarStore,
	avatarImages *avatar.ImageService,
	avatarSync *avatar.SyncService,
	chatMgr *chat.Manager,
	voiceCoordinator *voice.Coordinator,
	llmClient *llm.Client,
	vkeys *vidukeys.Service,
	recorder live.Recorder,
	patrons *patron.Service,
	ix *interactionRuntime,
	songRequests *repo.SwitchableSongRequestStore,
	gate *contentgate.Gate,
) *panelRuntime {
	isLive := func() bool { return mgr.Snapshot().Phase == live.PhaseLive }
	sessionIDFn := func() string { return mgr.Snapshot().SessionID }

	audioLib := media.NewLibrary(filepath.Join("storage", "audio"))
	songLib := media.NewLibrary(filepath.Join("storage", "songs"))

	// 逐依赖真实装配：dev 显式 fake；real 缺什么就显式报什么，绝不静默降级。
	var dec media.Decoder
	var player media.Player
	var songProbe func(context.Context) error
	var synth tts.Synth
	var remote action.Remote
	var actionProbe func(context.Context) error
	var sender action.Sender
	if cfg.Engine.Mode == "dev" {
		dec = media.FakeDecoder{}
		player = media.NewFakePlayer()
		synth = tts.NewFakeSynth()
		sender = action.NewFakeSender()
		remote = action.NewMemoryRemote()
	} else {
		if deps != nil && deps.vidu != nil {
			remote = enginevidu.NewActionRemote(deps.vidu)
			actionProbe = func(ctx context.Context) error {
				_, err := remote.Libraries(ctx)
				return err
			}
		}
		if realSender, ok := ctrl.(action.Sender); ok {
			sender = realSender
		}
		if deps != nil && cfg.Engine.OmniVoiceAddr != "" {
			synth = enginetts.NewSynthAdapter(deps.tts, "")
		} else {
			synth = tts.NewUnavailableSynth("omnivoice_addr_not_configured")
		}
		if fdec, err := media.NewFfmpegDecoder(); err == nil {
			dec = fdec
			// song_player 只证明歌曲库真实文件经本地 FfmpegPlayer 可播；
			// 捕获 PCM 不送入 Vidu，也不证明音频已送达云端或观众。
			songProbe = func(ctx context.Context) error {
				files, err := songLib.List()
				if err != nil {
					return err
				}
				if len(files) == 0 {
					return fmt.Errorf("歌曲库为空，无法建立本地真实播放证据")
				}
				path, _, err := songLib.Path(files[0].Name)
				if err != nil {
					return err
				}
				_, err = media.ProbeLocalPlayback(ctx, fdec, path)
				return err
			}
			// real 模式喂流出口：ViduController.NewFeedSink（song 持有者，守卫豁免）。
			if sinker, ok := ctrl.(interface {
				NewFeedSink(kind string, song bool) engine.FeedSink
			}); ok {
				if p, perr := media.NewFfmpegPlayer(fdec, media.FeedSink(sinker.NewFeedSink("song", true))); perr == nil {
					player = p
				}
			}
		} else {
			dec = media.UnavailableDecoder{Err: err}
		}
		if player == nil {
			player = media.UnavailablePlayer{Err: media.ErrFFmpegUnavailable}
		}
	}

	sched := action.NewScheduler(sender, bus, action.DefaultConfig())
	sched.SetContentGate(gate, sessionIDFn)
	// F26：贵宾判定首次生产接线——弹幕 Ingest 的 vip 标记登记进本场贵宾名单。
	// F29：贵宾名单（patrons）同为 VIP 判定来源之一（nick/uid 命中即贵宾）。
	sched.SetVIP(func(user string) bool {
		if dm.IsVIP(user) {
			return true
		}
		return patrons.IsPatron(context.Background(), user)
	})
	actions := action.NewService(remote, sched)

	choreoStore := choreo.NewFileStore(filepath.Join("storage", "choreo.json"))
	chor := choreo.NewService(choreoStore, sched)
	sched.SetPauseChoreo(func(d time.Duration) { chor.Pause(d) })

	songs := song.NewService(songLib, dec, player, bus, isLive)
	songs.SetContentGate(gate, mgr, sessionIDFn)
	songScheduler := song.NewScheduler(songRequests, songs, time.Now)
	song.RegisterInteractionHandler(ix.disp, songRequests, songScheduler)
	sched.SetSongPlaying(func() bool { return songs.Status().Playing })

	// 规则优先：点歌/动作先判定，普通聊天才进入 LLM；动作出口按 dev/real 如实装配。
	files, _ := songLib.List()
	ruleSongs := make(map[string]string, len(files)*2)
	fileNames := make([]string, 0, len(files))
	for _, file := range files {
		fileNames = append(fileNames, file.Name)
		ruleSongs[file.Name] = file.Name
		ruleSongs[strings.TrimSuffix(file.Name, filepath.Ext(file.Name))] = file.Name
	}
	wireInteractionRules(dm, ix, chatMgr, chatSpeaker{m: mgr}, sched, interaction.RulePolicy{
		RuleVersion:  "danmaku-rules-v1",
		Revision:     interaction.SourceKey("danmaku-rules", strings.Join(fileNames, "\x00")),
		LLMAvailable: settingStr(st, context.Background(), "llm", "api_key") != "",
		Songs:        ruleSongs,
	}, func() (string, bool) {
		snap := mgr.Snapshot()
		return snap.SessionID, snap.Phase == live.PhaseLive || snap.Phase == live.PhaseStarting
	})

	actionGen := provideKouboActionGen(choreoStore, llmClient, settingStr(st, context.Background(), "llm", "api_key") != "")
	actionGenProbe := func(ctx context.Context) error {
		_, err := actionGen.Generate(ctx, "点头")
		return err
	}
	kou := koubo.NewService(
		koubo.NewFileStore(filepath.Join("storage", "koubo", "project.json")),
		synth, player, actionGen, filepath.Join("storage", "koubo"), isLive,
	)
	kou.SetContentGate(gate, mgr, sessionIDFn)
	kou.SetSynthParamsFn(func() (string, float64) {
		ctx := context.Background()
		return settingStr(st, ctx, "voice", "voice_id"), settingFloat(st, ctx, "voice", "speed")
	})
	arch := archive.NewService(sessions, mgr, bill)
	// M5 A20：详情页无快照旧场次回落读 persona；软删后回 persona_id + 已删除语义。
	arch.SetPersonaResolver(avatars)
	// M5 v3 §3.3：开播成功后由 live.Manager 推参考图信令；type14 失败降级不算开播失败。
	// G3-1：信令出口按引擎模式装配——real 桥到 ViduController type11 生产链路。
	refsSvc := refs.NewService(provideRefsSignaler(cfg, ctrl), bus)
	mgr.SetRefSwitcher(refsSvc)
	// G22 中断副作用生产接线（对齐蓝本 engine.py interrupt/_barge_in 语义）：
	// 打断 = 停本地播放（口播/歌曲共用 player）+ 清口播批量 TTS 任务；
	// 停动作 = 中断当前动作语音并清动作队列。StopActionAudio 只在服务层无调度器的
	// 退路分支触发（本装配下 sched 恒非 nil，不会与 Scheduler.Interrupt 的事件双发）。
	mgr.SetInterruptHooks(live.InterruptHooks{
		StopPlayback:    func(ctx context.Context) { _ = player.Stop(ctx) },
		ClearTTSQueue:   func(context.Context) { kou.AbortTTSAll() },
		StopActionAudio: func(ctx context.Context) { _ = sched.Interrupt(ctx) },
	})
	// F33：本地 refs 文件目录（列表/上传），与 import-folder 白名单根一致落在 storage 下。
	refsSvc.SetDir("storage/refs")

	return &panelRuntime{
		scheduler:      sched,
		songScheduler:  songScheduler,
		dm:             dm,
		player:         player,
		songProbe:      songProbe,
		koubo:          kou,
		actionGen:      actionGen,
		actionGenProbe: actionGenProbe,
		sender:         sender,
		remote:         remote,
		actionProbe:    actionProbe,
		synth:          synth,
		contentGate:    gate,
		deps: server.PanelDeps{
			TTS:          tts.NewService(audioLib, synth, st),
			Actions:      actions,
			Choreo:       chor,
			Koubo:        kou,
			Refs:         refsSvc,
			Songs:        songs,
			SongRequests: songRequests,
			Archive:      arch,
			Billing:      bill,
			Danmaku:      dm,
			Personas:     personas,
			Patrons:      patrons,
			// G 批 M2：档案服务 + 图片管线（内容寻址 storage/images）。
			// G 批 M3：sync-vidu 202 异步管线（无 Vidu 凭据时 503 fail-closed）。
			Avatars:      avatars,
			AvatarImages: avatarImages,
			AvatarSync:   avatarSync,
			// G04：人设 select 后立即清当前场次 LLM 记忆，消除 restart_required。
			ResetLLM: mgr.ResetLlm,
			// N1 管理员对话：默认与语音共享回合协调器和 session:operator 记忆；
			// coordinate_text_chat=false 仅保留旧 admin 调试通路，不与语音争抢。
			Chat: func(ctx context.Context, text string) (string, error) {
				if !isLive() {
					return "", server.ErrChatNotLive
				}
				if cfg.Voice.CoordinateTextChat {
					return voiceCoordinator.ChatText(ctx, text)
				}
				return chatMgr.Chat(ctx, "admin", text, func(ctx context.Context, s string) error {
					_, err := mgr.Say(ctx, s, "chat")
					return err
				})
			},
			// F42/G18：多 key 管理；ViduKeyCurrent 供蓝本首访落库语义读取当前生效 key。
			ViduKeys: vkeys,
			ViduKeyCurrent: func() string {
				if deps != nil && deps.vidu != nil {
					return deps.vidu.APIKey()
				}
				return cfg.Engine.ViduKey
			},
			// F34/N2 录播：开关持久化在 settings misc.record_enabled；Supervisor 仅在
			// real 模式且配置开通时装配（dev → nil → fetch fail-closed 503）。
			Settings:  st,
			Recording: recorderSupervisor(recorder),
			// F08/F36/G03/N3 额度：有凭据实时透传 Vidu credits；无凭据 nil → 路由返回明确空态。
			Credits: viduCreditsQuery(deps),
			Effects: ix.store, InteractionDispatcher: ix.disp,
			// import-folder 白名单根：只允许 storage 之下的目录。
			StorageRoot: "storage",
		},
	}
}

func provideLiveService(m *live.Manager, rt *panelRuntime, issuer *rtc.Issuer) *service.LiveService {
	return service.NewLiveService(m, rt.scheduler, rt.dm, issuer)
}

// provideRTCIssuer 默认 disabled；mode=dev 且凭据齐全才签发本地联调凭据。
// managed 模式不在此装配真实厂商签发器，保持 fail-closed。
func provideRTCIssuer(cfg *conf.Config) *rtc.Issuer {
	return rtc.NewIssuer(rtc.Config{
		Mode:       cfg.RTC.Mode,
		Provider:   cfg.RTC.Provider,
		RoomPrefix: cfg.RTC.RoomPrefix,
		TokenTTL:   cfg.RTC.TokenTTL,
		APIKey:     cfg.RTC.APIKey,
		APISecret:  cfg.RTC.APISecret,
	})
}

func provideDanmakuService(s *danmaku.Service) *service.DanmakuService {
	return service.NewDanmakuService(s)
}

func provideSettingsService(s *settings.Service) *service.SettingsService {
	return service.NewSettingsService(s)
}

func provideHTTPServer(
	cfg *conf.Config,
	logger *slog.Logger,
	bus *eventbus.Bus,
	tickets server.TicketStore,
	voiceTickets server.VoiceTicketStore,
	nonces server.NonceStore,
	voiceCoordinator *voice.Coordinator,
	voiceStatus *voice.StatusService,
	liveSvc *service.LiveService,
	dmSvc *service.DanmakuService,
	stSvc *service.SettingsService,
	health *server.Health,
	version VersionInfo,
	auditSvc *audit.Service,
	rt *panelRuntime,
	streamSvc *publish.StreamService,
	collectorSvc *relay.CollectorService,
	collectorTracker *relay.Tracker,
	preflight *live.Preflight, // 顺序锚点：providePreflight 先于本函数执行（rt.deps.Preflight 已注入）
	_ *contentpkg.Service, // 顺序锚点：provideContentpkg 必须先注入 rt.deps.Contentpkg，再注册 live-drafts 路由
) *khttp.Server {
	return server.NewHTTPServer(cfg, logger, bus, tickets, voiceTickets, nonces, voiceCoordinator, voiceStatus,
		liveSvc, dmSvc, stSvc, health, string(version), auditSvc, rt.deps, streamSvc, collectorSvc, collectorTracker, preflight)
}

func provideApp(s *khttp.Server, d *data.Data, logger *slog.Logger, m *live.Manager, dmWorker *danmaku.Worker, ix *interactionRuntime, pkgs *contentpkg.Service, rt *panelRuntime) *App {
	return &App{Server: s, Data: d, Logger: logger, Manager: m, DanmakuWorker: dmWorker, Interaction: ix, Contentpkg: pkgs, SongScheduler: rt.songScheduler, panel: rt}
}

// providePublisher DZ 批：stream.enabled=false 时返回 nil（路由不注册，前端按 404/unconfigured
// 处理，Manager 不装配 PushGate、开播不做推流前置检查）；enabled=true 时开播必须先推流
// （fail-closed，allow_unpushed_live 为唯一显式豁免）。
// 注意：本 provider 不依赖 *live.Manager（Manager 反向依赖 Publisher 做 SetPushGate，
// 直接引用会成环）；场次/epoch 经 bus.PublishScoped 由 Manager 启动时绑定的 Scope 填充
// （live.NewManagerWithStores → bus.SetScope(m.Scope)），无场次时 session="" epoch=0。
func providePublisher(cfg *conf.Config, bus *eventbus.Bus, logger *slog.Logger) *publish.Publisher {
	if !cfg.Stream.Enabled {
		return nil
	}
	pcfg := publish.Config{
		Platform: "douyin",
		Source:   publish.SourceKind(cfg.Stream.Source),
		Stage: publish.CaptureConfig{
			URL:      cfg.Stream.StageURL,
			AllowGPU: cfg.Stream.AllowGPU,
		},
		// FFmpegBin 留空：默认从 PATH 解析；验收环境用便携版时由配置/测试注入绝对路径。
		Logf: func(f string, a ...any) { logger.Info(fmt.Sprintf("publish: "+f, a...)) },
		OnEvent: func(ev publish.Event) {
			// 推流状态事件进总线，session_id/epoch 由总线绑定的 Manager.Scope 填充。
			bus.PublishScoped(context.Background(), "push.status", map[string]any{
				"state": string(ev.State), "reason": ev.Reason, "at_ms": ev.At.UnixMilli(),
			})
		},
	}
	store := &publish.LayeredStore{
		Mem:  &publish.MemTargetStore{},
		File: publish.NewSecretsFileStore("var"),
		Env:  publish.EnvTargetStore{},
	}
	return publish.NewPublisher(pcfg, store)
}

func provideCollectorTracker(cfg *conf.Config) *relay.Tracker {
	if !cfg.Collector.Enabled {
		return nil
	}
	return relay.NewTracker()
}

// provideStreamService 推流 HTTP 接口；pub 为 nil 时返回 nil（http.go 条件注册）。
func provideStreamService(pub *publish.Publisher) *publish.StreamService {
	if pub == nil {
		return nil
	}
	return publish.NewStreamService(pub)
}

func provideCollectorService(t *relay.Tracker) *relay.CollectorService {
	if t == nil {
		return nil
	}
	return relay.NewCollectorService(t, relay.NewRoomConfigStore())
}

// ===== 深化方案 v2 切片 0（B1/B2/B3/B4）接线 =====

// interactionRuntime 互动一致性闭环装配产物（§5.4/§5.5）。
type interactionRuntime struct {
	svc        *interaction.Service
	store      interaction.Store
	disp       *interaction.Dispatcher
	gift       *interaction.GiftSpeaker
	rules      atomic.Pointer[interaction.RuleEngine]
	chatSource atomic.Pointer[repo.EntDanmakuSink]
}

// provideSongRequests 点歌队列内存启动、PG 热切；晚到的 PG 在切换时立即恢复 playing 现场。
func provideSongRequests(d *data.Data, logger *slog.Logger) *repo.SwitchableSongRequestStore {
	policy := song.QueuePolicy{}
	sw := repo.NewSwitchableSongRequestStore(song.NewMemoryRequestStore(policy, time.Now), policy, time.Now)
	d.SetOnDBReady(func(client *ent.Client) {
		sw.SetEnt(client)
		if err := sw.RecoverPlaying(context.Background()); err != nil {
			logger.Warn("recover persistent song requests failed", "err", err)
		}
	})
	return sw
}

// provideInteraction B3：Decision/EffectOutbox 存储（内存启动、PG 热切）+ dispatcher
// lease 骨架 + 礼物答谢链路（gift 合并触发 → 落库 → 派发 Say → 回执写状态）。
func provideInteraction(d *data.Data, mgr *live.Manager) (*interactionRuntime, error) {
	sw := repo.NewSwitchableInteractionStore(interaction.NewMemoryStore())
	svc := interaction.NewService(sw)
	ix := &interactionRuntime{svc: svc, store: sw}
	disp, err := interaction.NewDispatcher(sw, 30*time.Second, 30*time.Second, ix.orphanChatDecisions)
	if err != nil {
		return nil, err
	}
	ix.disp = disp
	d.SetOnDBReady(func(client *ent.Client) {
		sw.SetEnt(client)
		ix.chatSource.Store(repo.NewEntDanmakuSink(client))
	})
	gs := interaction.NewGiftSpeaker(svc, disp, giftSpeaker{m: mgr}, func() (string, bool) {
		snap := mgr.Snapshot()
		if snap.Phase == live.PhaseLive || snap.Phase == live.PhaseStarting {
			return snap.SessionID, true
		}
		return "", false
	})
	gs.RegisterGiftHandler()
	ix.gift = gs
	return ix, nil
}

// orphanChatDecisions 只从 PG 恢复已持久化的规则聊天；内存降级没有可恢复的原始行。
func (ix *interactionRuntime) orphanChatDecisions(ctx context.Context) ([]interaction.DecisionInput, error) {
	source, rules := ix.chatSource.Load(), ix.rules.Load()
	if source == nil || rules == nil {
		return nil, nil
	}
	events, err := source.ListOrphanRuleChats(ctx)
	if err != nil {
		return nil, err
	}
	inputs := make([]interaction.DecisionInput, 0, len(events))
	for _, event := range events {
		userKey := event.UserID
		if userKey == "" {
			userKey = event.UserName
		}
		inputs = append(inputs, rules.DecisionInput(interaction.RuleEvent{
			EventRowID: event.ID, InternalEventID: event.SourceEventID,
			SessionID: event.SessionID, UserKey: userKey, Text: event.Text,
			ReceivedAtMs: event.ReceivedAtMs, ExpiresAtMs: event.ExpiresAtMs,
		}))
	}
	return inputs, nil
}

// wireInteractionRules 把已持久化的 chat 原始事件接入确定性规则，并立即派发本次副作用。
// 同源重放由 danmaku 与 Decision 两层幂等共同去重，不会形成第二条消费链。
func wireInteractionRules(
	dm *danmaku.Service,
	ix *interactionRuntime,
	chatter interaction.ChatChatter,
	speaker interaction.ChatSpeaker,
	actions interaction.ActionCommander,
	policy interaction.RulePolicy,
	session func() (string, bool),
) {
	engine := interaction.NewRuleEngine(ix.svc, policy, nil)
	ix.rules.Store(engine)
	interaction.RegisterChatReplyHandler(ix.disp, chatter, speaker)
	interaction.RegisterActionHandler(ix.disp, actions)
	dm.SetRuleTap(func(ctx context.Context, _ string, ev danmaku.TapEvent) error {
		sessionID, ok := session()
		if !ok || sessionID == "" {
			return fmt.Errorf("interaction: chat rule without live session")
		}
		userKey := ev.UserID
		if userKey == "" {
			userKey = ev.UserName
		}
		_, effects, err := engine.Handle(ctx, interaction.RuleEvent{
			EventRowID: ev.EventRowID, InternalEventID: ev.SourceEventID,
			SessionID: sessionID, UserKey: userKey, Text: ev.Text,
			ReceivedAtMs: ev.ReceivedAtMs, ExpiresAtMs: ev.ExpiresAtMs,
		})
		if err != nil {
			return err
		}
		for range effects {
			if _, err := ix.disp.DispatchOnce(ctx); err != nil {
				return err
			}
		}
		return nil
	})
}

type cachedTTSHealth struct {
	synth *enginetts.SynthAdapter
	ttl   time.Duration
	now   func() time.Time

	mu        sync.Mutex
	expiresAt time.Time
	cached    capability.Status
}

func newCachedTTSHealth(synth *enginetts.SynthAdapter) *cachedTTSHealth {
	return &cachedTTSHealth{synth: synth, ttl: 5 * time.Second, now: time.Now}
}

func (p *cachedTTSHealth) status(ctx context.Context) capability.Status {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if !p.expiresAt.IsZero() && now.Before(p.expiresAt) {
		return p.cached
	}
	p.cached = capability.Unconfigured
	health, err := p.synth.Health(ctx)
	if err == nil && health.OK {
		p.cached = capability.Real
	}
	p.expiresAt = now.Add(p.ttl)
	return p.cached
}

// cachedCapabilityProbe 缓存真实外部依赖的成功与失败，避免每次 preflight 重复打网络。
type cachedCapabilityProbe struct {
	mu    sync.Mutex
	ttl   time.Duration
	probe func(context.Context) error
	now   func() time.Time
	last  time.Time
	err   error
}

func newCachedCapabilityProbe(ttl time.Duration, probe func(context.Context) error) *cachedCapabilityProbe {
	return &cachedCapabilityProbe{ttl: ttl, probe: probe, now: time.Now}
}

func (p *cachedCapabilityProbe) check(ctx context.Context) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	now := p.now()
	if !p.last.IsZero() && now.Sub(p.last) < p.ttl {
		return p.err
	}
	p.err = p.probe(ctx)
	p.last = now
	return p.err
}

// provideCapabilities B2 §2.2：能力矩阵由装配点显式计算——按实际装配的实现类型给
// 状态词，绝不只按配置存在报 real；探测未做的一律 unknown。
func provideCapabilities(
	cfg *conf.Config,
	rt *panelRuntime,
	pub *publish.Publisher,
	sessions live.SessionStore,
	d *data.Data,
) live.CapabilitySource {
	var ttsHealth *cachedTTSHealth
	if rt != nil {
		if synth, ok := rt.synth.(*enginetts.SynthAdapter); ok {
			ttsHealth = newCachedTTSHealth(synth)
		}
	}
	var actionProbe *cachedCapabilityProbe
	if rt != nil && rt.actionProbe != nil {
		actionProbe = newCachedCapabilityProbe(5*time.Second, rt.actionProbe)
	}
	var actionGenProbe *cachedCapabilityProbe
	if rt != nil && rt.actionGenProbe != nil {
		actionGenProbe = newCachedCapabilityProbe(5*time.Second, rt.actionGenProbe)
	}
	var songProbe *cachedCapabilityProbe
	if rt != nil && rt.songProbe != nil {
		// 与 preflight token 同新鲜期，避免 Start 复核时把整首歌再播放两遍。
		songProbe = newCachedCapabilityProbe(live.PreflightTTL, rt.songProbe)
	}
	return func(ctx context.Context) capability.Matrix {
		mx := capability.Matrix{}
		mx["content_gate"] = capability.Unconfigured
		if rt != nil && rt.contentGate != nil {
			switch rt.contentGate.Capability() {
			case contentgate.CapabilityReal:
				mx["content_gate"] = capability.Real
			case contentgate.CapabilityUnknown:
				mx["content_gate"] = capability.Unknown
			}
		}
		remoteState := capability.Unconfigured
		if rt != nil {
			switch rt.remote.(type) {
			case *action.MemoryRemote:
				remoteState = capability.Fake
			case nil, action.UnavailableRemote:
				remoteState = capability.Unconfigured
			default:
				remoteState = capability.Unknown
				if actionProbe != nil && actionProbe.check(ctx) == nil {
					remoteState = capability.Real
				}
			}
		}
		mx["action_remote"] = remoteState
		senderState := capability.Unconfigured
		if rt != nil && rt.sender != nil {
			switch rt.sender.(type) {
			case *action.FakeSender:
				senderState = capability.Fake
			default:
				senderState = capability.Unknown
				if remoteState == capability.Real {
					senderState = capability.Real
				}
			}
		}
		mx["action_sender"] = senderState
		// song_player：real 只能由真实音频文件连续两次本地播放证据建立；
		// 该证据不证明音频已送达 Vidu、云端或观众。
		if rt != nil && rt.player != nil {
			switch rt.player.(type) {
			case *media.FakePlayer:
				mx["song_player"] = capability.Fake
			case media.UnavailablePlayer:
				mx["song_player"] = capability.Unconfigured
			case *media.FfmpegPlayer:
				mx["song_player"] = capability.Unknown
				if songProbe != nil && songProbe.check(ctx) == nil {
					mx["song_player"] = capability.Real
				}
			default:
				mx["song_player"] = capability.Unknown
			}
		} else {
			mx["song_player"] = capability.Unconfigured
		}
		// audio_delivery 需要开播后的真实送达回执；没有回执通道前始终 unknown。
		// 它只进入能力面板，不进入公开 preflight checks 或 ready 判定。
		mx["audio_delivery"] = capability.Unknown
		// tts。
		if rt != nil && rt.synth != nil {
			switch rt.synth.(type) {
			case *tts.FakeSynth:
				mx["tts"] = capability.Fake
			case tts.UnavailableSynth:
				mx["tts"] = capability.Unconfigured
			case *enginetts.SynthAdapter:
				mx["tts"] = ttsHealth.status(ctx)
			default:
				mx["tts"] = capability.Unknown
			}
		} else {
			mx["tts"] = capability.Unconfigured
		}
		// greeting_audio_file：dev 未装配文件播放器；real 仅在真实控制器依赖与 ffmpeg
		// 齐全时标 unknown（未做端到端探测），否则 unconfigured，绝不冒充 real/fake 成功。
		mx["greeting_audio_file"] = capability.Unconfigured
		if cfg.Engine.Mode == "real" && cfg.Engine.ViduKey != "" && cfg.Engine.OmniVoiceAddr != "" && media.FfmpegAvailable() {
			mx["greeting_audio_file"] = capability.Unknown
		}
		// vidu：dev=fake；real 缺凭据=unconfigured；real 只能由 Libraries() 的真实
		// HTTP 探测成功建立，并复用 action_remote 的缓存结果，Key 存在本身不构成证据。
		switch {
		case cfg.Engine.Mode == "dev":
			mx["vidu"] = capability.Fake
		case cfg.Engine.ViduKey == "":
			mx["vidu"] = capability.Unconfigured
		default:
			mx["vidu"] = capability.Unknown
			if actionProbe != nil && actionProbe.check(ctx) == nil {
				mx["vidu"] = capability.Real
			}
		}
		mx["koubo_actiongen"] = capability.Unconfigured
		if rt != nil && rt.actionGen != nil {
			switch rt.actionGen.(type) {
			case koubo.FakeActionGen, *koubo.FakeActionGen:
				mx["koubo_actiongen"] = capability.Fake
			case *koubo.RuleActionGen:
				mx["koubo_actiongen"] = capability.Real
				if actionGenProbe != nil && actionGenProbe.check(ctx) != nil {
					mx["koubo_actiongen"] = capability.Unknown
				}
			case *koubo.LLMActionGen:
				mx["koubo_actiongen"] = capability.Unknown
				if actionGenProbe != nil && actionGenProbe.check(ctx) == nil {
					mx["koubo_actiongen"] = capability.Real
				}
			default:
				mx["koubo_actiongen"] = capability.Unknown
			}
		}
		// 未配置目录时明确使用内存/fake；磁盘实现必须每次都通过真实读写探测才是 real。
		mx["content_storage"] = capability.Fake
		if rt != nil && len(rt.contentStorage) > 0 {
			mx["content_storage"] = capability.Real
			for _, probe := range rt.contentStorage {
				if probe == nil || probe.Probe(ctx) != nil {
					mx["content_storage"] = capability.Unknown
					break
				}
			}
		}
		// push_gate / douyin_publish 只读 Publisher 在真实发布回执后记录的验证等级。
		if pub == nil {
			mx["push_gate"] = capability.Unconfigured
			mx["douyin_publish"] = capability.Unconfigured
		} else {
			status := publishCapabilityStatus(pub.Status())
			mx["push_gate"] = status
			mx["douyin_publish"] = status
		}
		// storage_persistent：Ent 已安装且本次 PG 探测可达才算 real；不可达必须 fail-closed。
		var postgresProbe func(context.Context) data.Status
		if d != nil {
			postgresProbe = d.Probe
		}
		mx["storage_persistent"] = storagePersistentCapability(ctx, sessions, postgresProbe)
		return mx
	}
}

func storagePersistentCapability(ctx context.Context, sessions live.SessionStore, probe func(context.Context) data.Status) capability.Status {
	p, ok := sessions.(interface{ Persistent() bool })
	if !ok || !p.Persistent() {
		return capability.Fake
	}
	if probe == nil || !probe(ctx).Postgres {
		return capability.Unknown
	}
	return capability.Real
}

func modePolicyFromConfig(cfg *conf.Config) live.ModePolicy {
	return live.ModePolicy{AllowLocalPreview: cfg.Live.AllowLocalPreview, EngineMode: cfg.Engine.Mode}
}

// providePreflight B1 §7.5：唯一 ready 判定服务装配 + 服务端 Start 模式策略（§7.6）。
func providePreflight(
	cfg *conf.Config,
	resolver avatar.ProfileResolver,
	caps live.CapabilitySource,
	pub *publish.Publisher,
	sessions live.SessionStore,
	mgr *live.Manager,
	rt *panelRuntime,
	drafts contentpkg.Store,
) *live.Preflight {
	pf := live.NewPreflight(resolver, caps,
		func() (publish.PushGate, bool) {
			if pub == nil {
				return nil, false
			}
			return pub, cfg.Stream.AllowUnpushedLive
		},
		func() bool {
			p, ok := sessions.(interface{ Persistent() bool })
			return ok && p.Persistent()
		})
	pf.SetDraftSource(drafts)
	pf.SetModePolicy(modePolicyFromConfig(cfg))
	mgr.SetPreflight(pf)
	return pf
}

// provideContentpkgStore 是草稿、预检和 Start 共用的唯一内容包事实源。
func provideContentpkgStore(d *data.Data) contentpkg.Store {
	sw := repo.NewSwitchableContentpkgStore(contentpkg.NewMemoryStore())
	d.SetOnDBReady(sw.SetEnt)
	return sw
}

type contentStorageProbeFunc func(context.Context) error

func (f contentStorageProbeFunc) Probe(ctx context.Context) error { return f(ctx) }

func provideContentpkg(store contentpkg.Store, rt *panelRuntime) *contentpkg.Service {
	root := strings.TrimSpace(os.Getenv("DH_CONTENTPKG_DIR"))
	if root == "" {
		rt.contentStorage = nil
		svc := contentpkg.NewServiceWithFileStores(store, map[string]contentpkg.FileStore{
			"koubo":  contentpkg.NewMemoryFileStore(),
			"choreo": contentpkg.NewMemoryFileStore(),
			"refs":   contentpkg.NewMemoryFileStore(),
		})
		rt.deps.Contentpkg = svc
		return svc
	}

	stores := make(map[string]contentpkg.FileStore, 3)
	probes := make([]contentpkg.StorageProbe, 0, 3)
	for _, name := range []string{"koubo", "choreo", "refs"} {
		disk, err := contentpkg.NewDiskFileStore(filepath.Join(root, name))
		if err != nil {
			rt.contentStorage = []contentpkg.StorageProbe{contentStorageProbeFunc(func(context.Context) error { return err })}
			svc := contentpkg.NewService(store)
			rt.deps.Contentpkg = svc
			return svc
		}
		stores[name] = disk
		probes = append(probes, disk)
	}
	rt.contentStorage = probes
	svc := contentpkg.NewServiceWithFileStores(store, stores)
	rt.deps.Contentpkg = svc
	return svc
}
