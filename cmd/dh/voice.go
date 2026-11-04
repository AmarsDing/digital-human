package main

import (
	"context"

	"github.com/teamwork/digital-human/internal/biz/chat"
	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/biz/settings"
	"github.com/teamwork/digital-human/internal/biz/voice"
	"github.com/teamwork/digital-human/internal/conf"
	"github.com/teamwork/digital-human/internal/server"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

// provideVoiceASR 只在显式 dev+fake 装配模拟识别；sherpa 未获本机 sidecar/模型许可
// 或 cloud 未实现时都返回 unconfigured，Capability/Start 不会伪报 ready。
func provideVoiceASR(cfg *conf.Config) (voice.Recognizer, func()) {
	noop := func() {}
	switch cfg.Voice.ASR.Provider {
	case "fake":
		if cfg.Engine.Mode != "dev" {
			return voice.NewUnavailableRecognizer("fake_asr_requires_engine_dev"), noop
		}
		return voice.NewFakeRecognizer(cfg.Voice.ASR.FakeText), noop
	case "cloud":
		if !cfg.Voice.ASR.AllowCloud {
			return voice.NewUnavailableRecognizer("cloud_asr_disabled"), noop
		}
		return voice.NewUnavailableRecognizer("cloud_asr_not_implemented"), noop
	case "sherpa":
		if !cfg.Voice.ASR.AllowLocalSidecar {
			return voice.NewUnavailableRecognizer("local_asr_sidecar_disabled"), noop
		}
		if !cfg.Voice.ASR.ModelLicenseAcknowledged {
			return voice.NewUnavailableRecognizer("asr_model_license_not_acknowledged"), noop
		}
		r := voice.NewSherpaRecognizer(voice.SherpaOptions{
			PythonBin: cfg.Voice.ASR.PythonBin, ScriptPath: cfg.Voice.ASR.ScriptPath,
			ModelType: cfg.Voice.ASR.ModelType, Tokens: cfg.Voice.ASR.Tokens,
			Encoder: cfg.Voice.ASR.Encoder, Decoder: cfg.Voice.ASR.Decoder, Joiner: cfg.Voice.ASR.Joiner,
			Threads: cfg.Voice.ASR.Threads, StartupTimeout: cfg.Voice.ASR.StartupTimeout,
		})
		return r, func() { _ = r.Close() }
	default:
		return voice.NewUnavailableRecognizer("asr_provider_invalid"), noop
	}
}

func voiceConfigFrom(cfg *conf.Config) voice.Config {
	return voice.Config{
		ASRTimeout: cfg.Voice.ASRTimeout, LLMTimeout: cfg.Voice.LLMTimeout,
		TurnTimeout: cfg.Voice.TurnTimeout, MaxAudio: cfg.Voice.MaxAudio,
		PartialInterval: cfg.Voice.PartialInterval, BargeIn: cfg.Voice.BargeIn,
		AllowContinuous: cfg.Voice.AllowContinuous, AllowCrossOwnerPreempt: cfg.Voice.AllowCrossOwnerPreempt,
		InterruptSongs: cfg.Voice.InterruptSongs, PersistText: cfg.Voice.PersistText,
		RetainAudio: cfg.Voice.RetainAudio,
		VAD: voice.VADConfig{Silence: cfg.Voice.VAD.Silence, MinSpeech: cfg.Voice.VAD.MinSpeech,
			MinStartSpeech: cfg.Voice.VAD.MinStartSpeech, AbsoluteDBFS: cfg.Voice.VAD.AbsoluteDBFS},
	}
}

func provideVoiceTickets() server.VoiceTicketStore { return server.NewMemoryVoiceTicketStore() }

func provideVoiceCoordinator(
	asr voice.Recognizer,
	cfg voice.Config,
	chatter *chat.Manager,
	mgr *live.Manager,
	bus *eventbus.Bus,
) *voice.Coordinator {
	coordinator := voice.NewCoordinatorWithVAD(asr, nil, chatter, mgr, bus, cfg)
	mgr.AddSessionHook(coordinator)
	return coordinator
}

func provideVoiceStatus(
	asr voice.Recognizer,
	cfg voice.Config,
	st *settings.Service,
	caps live.CapabilitySource,
	mgr *live.Manager,
) *voice.StatusService {
	return &voice.StatusService{
		ASR: asr, Config: cfg, Capabilities: caps, Speaker: mgr,
		LLMConfigured: func(ctx context.Context) bool {
			return settingStr(st, ctx, "llm", "api_key") != ""
		},
	}
}
