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
	if got := caps(context.Background()).Get("tts"); got != capability.Unconfigured {
		t.Fatalf("unhealthy OmniVoice capability = %s, want unconfigured", got)
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
