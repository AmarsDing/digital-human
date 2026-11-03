//go:build wireinject

package main

import (
	"context"
	"log/slog"
	"path/filepath"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/google/wire"

	"github.com/teamwork/digital-human/internal/biz/action"
	"github.com/teamwork/digital-human/internal/biz/archive"
	"github.com/teamwork/digital-human/internal/biz/audit"
	"github.com/teamwork/digital-human/internal/biz/billing"
	"github.com/teamwork/digital-human/internal/biz/choreo"
	"github.com/teamwork/digital-human/internal/biz/danmaku"
	"github.com/teamwork/digital-human/internal/biz/koubo"
	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/biz/media"
	"github.com/teamwork/digital-human/internal/biz/refs"
	"github.com/teamwork/digital-human/internal/biz/rtc"
	"github.com/teamwork/digital-human/internal/biz/settings"
	"github.com/teamwork/digital-human/internal/biz/song"
	"github.com/teamwork/digital-human/internal/biz/tts"
	"github.com/teamwork/digital-human/internal/conf"
	"github.com/teamwork/digital-human/internal/data"
	"github.com/teamwork/digital-human/internal/data/repo"
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
		provideSessionStore,
		provideIdempotency,
		provideController,
		provideManager,
		provideDanmaku,
		provideSettings,
		provideTickets,
		provideHealth,
		providePanelRuntime,
		provideLiveService,
		provideRTCIssuer,
		provideDanmakuService,
		provideSettingsService,
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

// provideController 默认 fail-closed：仅 engine.mode=dev 才装配 fake。
func provideController(cfg *conf.Config, logger *slog.Logger) live.Controller {
	return live.NewConfiguredController(context.Background(), live.EngineOptions{
		Mode:          cfg.Engine.Mode,
		ViduKey:       cfg.Engine.ViduKey,
		OmniVoiceAddr: cfg.Engine.OmniVoiceAddr,
	}, logger)
}

// provideAudit 启动时用内存审计兜底；PG 运行期就绪后切换为 Ent。
func provideAudit(d *data.Data) *audit.Service {
	mem := audit.NewMemorySink(1000)
	sw := repo.NewSwitchableAuditSink(auditMemoryBridge{mem: mem})
	d.SetOnDBReady(sw.SetEnt)
	return audit.NewService(auditSinkBridge{sw: sw})
}

// provideSessionStore 启动时用内存场次存储兜底；PG 运行期就绪后切换为 Ent。
func provideSessionStore(d *data.Data) live.SessionStore {
	sw := repo.NewSwitchableSessionStore(live.NewMemorySessionStore())
	d.SetOnDBReady(sw.SetEnt)
	return sw
}

func provideIdempotency() live.Store {
	return live.NewMemoryIdempotencyStore(live.DefaultIdempotencyTTL)
}

func provideManager(
	bus *eventbus.Bus,
	sink live.EventSink,
	ctrl live.Controller,
	sessions live.SessionStore,
	idem live.Store,
) *live.Manager {
	return live.NewManagerWithStores(bus, sink, ctrl, sessions, idem)
}

func provideDanmaku() *danmaku.Service { return danmaku.NewService(danmaku.ModeOff) }

// provideSettings 启动时用内存设置兜底；PG 运行期就绪后切换为 Ent。
func provideSettings(d *data.Data) *settings.Service {
	mem := settings.NewMemoryStore()
	sw := repo.NewSwitchableSettingStore(mem)
	d.SetOnDBReady(sw.SetEnt)
	return settings.NewService(sw)
}

func provideTickets(cfg *conf.Config) server.TicketStore {
	return server.NewMemoryTicketStore(cfg.Security.TicketTTL)
}

func provideHealth(version VersionInfo, cfg *conf.Config, d *data.Data, m *live.Manager) *server.Health {
	h := server.NewHealth(string(version), func(ctx context.Context) server.DataStatus {
		st := d.Probe(ctx)
		return server.DataStatus{Postgres: st.Postgres, Redis: st.Redis}
	})
	h.Phase = func() string { return string(m.Snapshot().Phase) }
	// capabilities 由真实装配计算：引擎模式 dev/real 决定 vidu/omnivoice 是否可用，
	// PG/Redis 可用性取真实探活结果，不再恒 false。
	h.Caps = func(ctx context.Context) map[string]bool {
		st := d.Probe(ctx)
		real := cfg.Engine.Mode == "real"
		return map[string]bool{
			"vidu":       real && cfg.Engine.ViduKey != "",
			"omnivoice":  real && cfg.Engine.OmniVoiceAddr != "",
			"ffmpeg":     false, // 未接入，见 M6 计划
			"engine_dev": cfg.Engine.Mode == "dev",
			"postgres":   st.Postgres,
			"redis":      st.Redis,
		}
	}
	return h
}

// panelRuntime 持有 M3 面板依赖与共享调度器，避免多个 *media.Library 在 wire 中同名冲突。
type panelRuntime struct {
	deps      server.PanelDeps
	scheduler *action.Scheduler
	dm        *danmaku.Service
	player    media.Player
	koubo     *koubo.Service
}

func providePanelRuntime(
	bus *eventbus.Bus,
	st *settings.Service,
	dm *danmaku.Service,
	sessions live.SessionStore,
	mgr *live.Manager,
) *panelRuntime {
	isLive := func() bool { return mgr.Snapshot().Phase == live.PhaseLive }

	audioLib := media.NewLibrary(filepath.Join("storage", "audio"))
	songLib := media.NewLibrary(filepath.Join("storage", "songs"))
	player := media.NewFakePlayer()
	synth := tts.NewFakeSynth()

	remote := action.NewMemoryRemote()
	sched := action.NewScheduler(action.NewFakeSender(), bus, action.DefaultConfig())
	actions := action.NewService(remote, sched)

	chor := choreo.NewService(choreo.NewFileStore(filepath.Join("storage", "choreo.json")), sched)
	sched.SetPauseChoreo(func(d time.Duration) { chor.Pause(d) })

	songs := song.NewService(songLib, media.FakeDecoder{}, player, bus, isLive)
	sched.SetSongPlaying(func() bool { return songs.Status().Playing })

	kou := koubo.NewService(
		koubo.NewFileStore(filepath.Join("storage", "koubo", "project.json")),
		synth, player, koubo.FakeActionGen{}, filepath.Join("storage", "koubo"), isLive,
	)
	bill := billing.NewService(billing.NewFileStore(filepath.Join("storage", "cost", "ledger.json")))
	arch := archive.NewService(sessions, mgr, bill)

	return &panelRuntime{
		scheduler: sched,
		dm:        dm,
		player:    player,
		koubo:     kou,
		deps: server.PanelDeps{
			TTS:     tts.NewService(audioLib, synth, st),
			Actions: actions,
			Choreo:  chor,
			Koubo:   kou,
			Refs:    refs.NewService(refs.NewFakeSignaler(), bus),
			Songs:   songs,
			Archive: arch,
			Billing: bill,
			Danmaku: dm,
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
	liveSvc *service.LiveService,
	dmSvc *service.DanmakuService,
	stSvc *service.SettingsService,
	health *server.Health,
	version VersionInfo,
	auditSvc *audit.Service,
	rt *panelRuntime,
) *khttp.Server {
	return server.NewHTTPServer(cfg, logger, bus, tickets, liveSvc, dmSvc, stSvc, health, string(version), auditSvc, rt.deps)
}

func provideApp(s *khttp.Server, d *data.Data, logger *slog.Logger, m *live.Manager) *App {
	return &App{Server: s, Data: d, Logger: logger, Manager: m}
}
