package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/capability"
	"github.com/teamwork/digital-human/internal/conf"
	enginetts "github.com/teamwork/digital-human/internal/engine/tts"
)

func TestVoiceASRAssemblyHasExplicitRealFakeBoundaries(t *testing.T) {
	base, err := conf.Load("")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		mutate func(*conf.Config)
		source capability.Status
		ready  bool
	}{
		{name: "sherpa requires explicit license acknowledgement", source: capability.Unconfigured},
		{name: "dev fake is explicit", mutate: func(c *conf.Config) {
			c.Engine.Mode = "dev"
			c.Voice.ASR.Provider = "fake"
		}, source: capability.Fake, ready: true},
		{name: "fake cannot leak into real mode", mutate: func(c *conf.Config) {
			c.Voice.ASR.Provider = "fake"
		}, source: capability.Unconfigured},
		{name: "cloud remains unconfigured even when allowed", mutate: func(c *conf.Config) {
			c.Voice.ASR.Provider = "cloud"
			c.Voice.ASR.AllowCloud = true
		}, source: capability.Unconfigured},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := *base
			if tc.mutate != nil {
				tc.mutate(&cfg)
			}
			asr, cleanup := provideVoiceASR(&cfg)
			defer cleanup()
			got := asr.Capability(context.Background())
			if got.Source != tc.source || got.Ready != tc.ready {
				t.Fatalf("capability = %+v, want source=%s ready=%v", got, tc.source, tc.ready)
			}
		})
	}
}

func TestRealTTSCapabilityUsesOmniVoiceHealth(t *testing.T) {
	var healthy atomic.Bool
	healthy.Store(true)
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != enginetts.PathHealth {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":` + map[bool]string{true: "true", false: "false"}[healthy.Load()] + `,"model":"omnivoice","device":"cpu"}`))
	}))
	defer sidecar.Close()
	rt := &panelRuntime{synth: enginetts.NewSynthAdapter(enginetts.NewClient(enginetts.Options{BaseURL: sidecar.URL}), "")}
	caps := provideCapabilities(&conf.Config{}, rt, nil, nil, nil)
	if got := caps(context.Background()).Get("tts"); got != capability.Real {
		t.Fatalf("healthy OmniVoice capability = %s, want real", got)
	}
	healthy.Store(false)
	caps = provideCapabilities(&conf.Config{}, rt, nil, nil, nil)
	if got := caps(context.Background()).Get("tts"); got != capability.Unconfigured {
		t.Fatalf("unhealthy OmniVoice capability = %s, want unconfigured", got)
	}
}

func TestTTSHealthProbeCachesSuccessAndFailure(t *testing.T) {
	var healthy atomic.Bool
	var calls atomic.Int32
	healthy.Store(true)
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":` + map[bool]string{true: "true", false: "false"}[healthy.Load()] + `,"model":"omnivoice","device":"cpu"}`))
	}))
	defer sidecar.Close()
	synth := enginetts.NewSynthAdapter(enginetts.NewClient(enginetts.Options{BaseURL: sidecar.URL}), "")
	probe := newCachedTTSHealth(synth)
	now := time.Date(2026, 11, 4, 12, 0, 0, 0, time.UTC)
	probe.now = func() time.Time { return now }

	if got := probe.status(context.Background()); got != capability.Real {
		t.Fatalf("首次健康探测=%s want real", got)
	}
	healthy.Store(false)
	if got := probe.status(context.Background()); got != capability.Real || calls.Load() != 1 {
		t.Fatalf("成功结果应在短缓存内复用: status=%s calls=%d", got, calls.Load())
	}
	now = now.Add(probe.ttl)
	if got := probe.status(context.Background()); got != capability.Unconfigured || calls.Load() != 2 {
		t.Fatalf("过期后应重新探测并报告异常: status=%s calls=%d", got, calls.Load())
	}
	healthy.Store(true)
	if got := probe.status(context.Background()); got != capability.Unconfigured || calls.Load() != 2 {
		t.Fatalf("失败结果也应在短缓存内复用: status=%s calls=%d", got, calls.Load())
	}
	now = now.Add(probe.ttl)
	if got := probe.status(context.Background()); got != capability.Real || calls.Load() != 3 {
		t.Fatalf("失败缓存过期后应重新探测恢复: status=%s calls=%d", got, calls.Load())
	}
}

func TestCloudTTSAssemblyCapabilityAndOmniVoiceFallback(t *testing.T) {
	var omniHealthCalls atomic.Int32
	sidecar := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		omniHealthCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"model":"omnivoice","device":"cpu"}`))
	}))
	defer sidecar.Close()

	tests := []struct {
		name       string
		configure  func(*enginetts.CloudConfig)
		want       capability.Status
		wantStream bool
		wantErr    string
	}{
		{
			name: "incomplete cloud is unconfigured and keeps fallback",
			configure: func(cfg *enginetts.CloudConfig) {
				cfg.Provider = "dashscope"
			},
			want:    capability.Unconfigured,
			wantErr: "cloud_tts_unconfigured",
		},
		{
			name: "complete cloud is unknown before real PCM",
			configure: func(cfg *enginetts.CloudConfig) {
				*cfg = enginetts.CloudConfig{
					Provider: "dashscope", BaseURL: "ws://127.0.0.1:1/api-ws/v1",
					Model: "qwen3-tts-flash-realtime", APIKey: "test-key", Voice: "Cherry",
					SampleRate: 24000, Format: "pcm",
				}
			},
			want:       capability.Unknown,
			wantStream: true,
		},
		{
			name: "Qwen Audio stays explicitly unimplemented",
			configure: func(cfg *enginetts.CloudConfig) {
				*cfg = enginetts.CloudConfig{
					Provider: "dashscope", BaseURL: "ws://127.0.0.1:1/api-ws/v1",
					Model: "qwen-audio-3.1-tts-flash", APIKey: "test-key", Voice: "Cherry",
					SampleRate: 24000, Format: "pcm",
				}
			},
			want:    capability.Unknown,
			wantErr: "cloud_tts_protocol_unimplemented",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			omniHealthCalls.Store(0)
			cfg := &conf.Config{}
			cfg.Engine.Mode = "real"
			cfg.Engine.ViduKey = "vidu-test-key"
			cfg.Engine.OmniVoiceAddr = sidecar.URL
			tt.configure(&cfg.Engine.CloudTTS)

			deps := provideRealDeps(cfg)
			if deps == nil || deps.tts == nil {
				t.Fatal("OmniVoice whole-segment fallback must remain assembled")
			}
			if got := deps.streamTTS != nil; got != tt.wantStream {
				t.Fatalf("stream TTS assembled = %v, want %v", got, tt.wantStream)
			}
			if got := enginetts.ErrorCode(deps.cloudTTSErr); got != tt.wantErr {
				t.Fatalf("cloud assembly error code = %q, want %q (err=%v)", got, tt.wantErr, deps.cloudTTSErr)
			}
			if deps.cloudTTSCapability == nil {
				t.Fatal("cloud capability evidence was not assembled")
			}
			rt := &panelRuntime{
				synth:              enginetts.NewSynthAdapter(deps.tts, ""),
				cloudTTSCapability: deps.cloudTTSCapability,
			}
			caps := provideCapabilities(cfg, rt, nil, nil, nil)
			if got := caps(context.Background()).Get("tts"); got != tt.want {
				t.Fatalf("tts capability = %s, want %s", got, tt.want)
			}
			if got := omniHealthCalls.Load(); got != 0 {
				t.Fatalf("cloud capability borrowed OmniVoice health evidence: calls=%d", got)
			}
		})
	}
}

func TestVoiceConfigMappingPreservesPolicies(t *testing.T) {
	cfg, err := conf.Load("")
	if err != nil {
		t.Fatal(err)
	}
	cfg.Voice.ASRTimeout = 3 * time.Second
	cfg.Voice.AllowCrossOwnerPreempt = true
	cfg.Voice.InterruptSongs = true
	got := voiceConfigFrom(cfg)
	if got.ASRTimeout != 3*time.Second || !got.AllowCrossOwnerPreempt || !got.InterruptSongs ||
		got.VAD.Silence != cfg.Voice.VAD.Silence || got.BargeIn != cfg.Voice.BargeIn {
		t.Fatalf("mapped config = %+v", got)
	}
}
