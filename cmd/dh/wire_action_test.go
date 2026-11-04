package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/action"
	"github.com/teamwork/digital-human/internal/biz/capability"
	"github.com/teamwork/digital-human/internal/biz/media"
	"github.com/teamwork/digital-human/internal/conf"
	enginevidu "github.com/teamwork/digital-human/internal/engine/vidu"
	"github.com/teamwork/digital-human/pkg/audio"
)

type realActionSenderStub struct{}

func (realActionSenderStub) SendCommand(context.Context, string) error { return nil }

type realActionRemoteStub struct{ action.Remote }

func TestActionCapabilityThreeStates(t *testing.T) {
	cfg := &conf.Config{}
	cfg.Engine.Mode = "real"
	rt := &panelRuntime{
		sender:      realActionSenderStub{},
		remote:      realActionRemoteStub{},
		actionProbe: func(context.Context) error { return nil },
	}
	mx := provideCapabilities(cfg, rt, nil, nil, nil)(context.Background())
	if got := mx.Get("action_sender"); got != capability.Real {
		t.Fatalf("真实出口和可达探测必须 real, got %s", got)
	}
	if got := mx.Get("action_remote"); got != capability.Real {
		t.Fatalf("真实动作库和可达探测必须 real, got %s", got)
	}

	rt.actionProbe = func(context.Context) error { return errors.New("dial tcp: down") }
	mx = provideCapabilities(cfg, rt, nil, nil, nil)(context.Background())
	if got := mx.Get("action_sender"); got != capability.Unknown {
		t.Fatalf("Vidu 不可达必须 unknown, got %s", got)
	}
	if got := mx.Get("action_remote"); got != capability.Unknown {
		t.Fatalf("动作库不可达必须 unknown, got %s", got)
	}

	rt.sender = nil
	rt.remote = nil
	rt.actionProbe = nil
	mx = provideCapabilities(cfg, rt, nil, nil, nil)(context.Background())
	if got := mx.Get("action_sender"); got != capability.Unconfigured {
		t.Fatalf("未装配发送器必须 unconfigured, got %s", got)
	}
	if got := mx.Get("action_remote"); got != capability.Unconfigured {
		t.Fatalf("未装配动作库必须 unconfigured, got %s", got)
	}
}

func TestViduCapabilityUsesRealLibrariesProbe(t *testing.T) {
	t.Run("probe success is real and shared with action remote", func(t *testing.T) {
		var calls atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls.Add(1)
			if r.URL.Path != "/live/v1/action-libraries" {
				t.Fatalf("unexpected probe path: %s", r.URL.Path)
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"list":[]}`))
		}))
		defer srv.Close()

		cfg := &conf.Config{}
		cfg.Engine.Mode = "real"
		cfg.Engine.ViduKey = "vda_test"
		remote := enginevidu.NewActionRemote(enginevidu.NewClient(enginevidu.Options{
			APIKey: cfg.Engine.ViduKey, BaseURL: srv.URL, RetryDelay: time.Millisecond,
		}))
		rt := &panelRuntime{remote: remote, actionProbe: func(ctx context.Context) error {
			_, err := remote.Libraries(ctx)
			return err
		}}
		mx := provideCapabilities(cfg, rt, nil, nil, nil)(context.Background())
		if got := mx.Get("vidu"); got != capability.Real {
			t.Fatalf("Libraries 真实 HTTP 探测成功后 vidu=%s, want real", got)
		}
		if got := calls.Load(); got != 1 {
			t.Fatalf("vidu 必须复用动作库同一次探测，HTTP calls=%d want 1", got)
		}
	})

	t.Run("probe failure is unknown", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"UNAUTHORIZED","message":"bad key"}`))
		}))
		defer srv.Close()

		cfg := &conf.Config{}
		cfg.Engine.Mode = "real"
		cfg.Engine.ViduKey = "vda_bad"
		remote := enginevidu.NewActionRemote(enginevidu.NewClient(enginevidu.Options{
			APIKey: cfg.Engine.ViduKey, BaseURL: srv.URL, RetryDelay: time.Millisecond,
		}))
		rt := &panelRuntime{remote: remote, actionProbe: func(ctx context.Context) error {
			_, err := remote.Libraries(ctx)
			return err
		}}
		mx := provideCapabilities(cfg, rt, nil, nil, nil)(context.Background())
		if got := mx.Get("vidu"); got != capability.Unknown {
			t.Fatalf("Libraries 探测失败后 vidu=%s, want unknown", got)
		}
	})

	t.Run("missing key is unconfigured without probing", func(t *testing.T) {
		cfg := &conf.Config{}
		cfg.Engine.Mode = "real"
		rt := &panelRuntime{actionProbe: func(context.Context) error {
			t.Fatal("未配置 Key 不得发起 Libraries 探测")
			return nil
		}}
		mx := provideCapabilities(cfg, rt, nil, nil, nil)(context.Background())
		if got := mx.Get("vidu"); got != capability.Unconfigured {
			t.Fatalf("未配置 Key 时 vidu=%s, want unconfigured", got)
		}
	})
}

func TestSongPlayerCapabilityUsesRealLocalPlaybackEvidence(t *testing.T) {
	dec, err := media.NewFfmpegDecoder()
	if err != nil {
		t.Skipf("ffmpeg 不在 PATH，跳过真实装配探测: %v", err)
	}
	src := filepath.Join(t.TempDir(), "song.wav")
	if err := os.WriteFile(src, audio.WavBytes(audio.TonePCM(120)), 0o644); err != nil {
		t.Fatal(err)
	}
	player, err := media.NewFfmpegPlayer(dec, func(context.Context, []byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	var probes atomic.Int32
	rt := &panelRuntime{player: player, songProbe: func(ctx context.Context) error {
		probes.Add(1)
		_, err := media.ProbeLocalPlayback(ctx, dec, src)
		return err
	}}
	caps := provideCapabilities(&conf.Config{}, rt, nil, nil, nil)
	for i := 0; i < 2; i++ {
		mx := caps(context.Background())
		if got := mx.Get("song_player"); got != capability.Real {
			t.Fatalf("真实文件连续两次本地播放成功后 song_player=%s, want real", got)
		}
		if got := mx.Get("audio_delivery"); got != capability.Unknown {
			t.Fatalf("无开播后送达回执时 audio_delivery=%s, want unknown", got)
		}
	}
	if got := probes.Load(); got != 1 {
		t.Fatalf("缓存期内重复读取能力不得重复播放整首歌曲，probes=%d want 1", got)
	}
}

func TestSongPlayerCapabilityFailsClosedWithoutPlaybackEvidence(t *testing.T) {
	player, err := media.NewFfmpegPlayer(&media.FfmpegDecoder{}, func(context.Context, []byte) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name  string
		probe func(context.Context) error
	}{
		{name: "probe missing"},
		{name: "probe failed", probe: func(context.Context) error { return errors.New("全零输出") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := &panelRuntime{player: player, songProbe: tc.probe}
			mx := provideCapabilities(&conf.Config{}, rt, nil, nil, nil)(context.Background())
			if got := mx.Get("song_player"); got != capability.Unknown {
				t.Fatalf("无真实本地播放证据时 song_player=%s, want unknown", got)
			}
			if got := mx.Get("audio_delivery"); got != capability.Unknown {
				t.Fatalf("audio_delivery=%s, want unknown", got)
			}
		})
	}

	mx := provideCapabilities(&conf.Config{}, &panelRuntime{player: media.UnavailablePlayer{}}, nil, nil, nil)(context.Background())
	if got := mx.Get("song_player"); got != capability.Unconfigured {
		t.Fatalf("播放器未配置时 song_player=%s, want unconfigured", got)
	}
}

func TestCapabilityProbeCachesSuccessAndFailure(t *testing.T) {
	calls := 0
	probe := newCachedCapabilityProbe(5*time.Second, func(context.Context) error {
		calls++
		return errors.New("down")
	})
	now := time.Unix(100, 0)
	probe.now = func() time.Time { return now }
	if probe.check(context.Background()) == nil || probe.check(context.Background()) == nil {
		t.Fatal("失败必须如实返回")
	}
	if calls != 1 {
		t.Fatalf("5 秒内重复探测 calls=%d want 1", calls)
	}
	now = now.Add(6 * time.Second)
	_ = probe.check(context.Background())
	if calls != 2 {
		t.Fatalf("缓存过期后 calls=%d want 2", calls)
	}
}

func TestActionCapabilityDevExplicitlyFake(t *testing.T) {
	cfg := &conf.Config{}
	cfg.Engine.Mode = "dev"
	rt := &panelRuntime{sender: action.NewFakeSender(), remote: action.NewMemoryRemote()}
	mx := provideCapabilities(cfg, rt, nil, nil, nil)(context.Background())
	if got := mx.Get("action_sender"); got != capability.Fake {
		t.Fatalf("dev fake sender 必须如实 fake, got %s", got)
	}
	if got := mx.Get("action_remote"); got != capability.Fake {
		t.Fatalf("dev memory remote 必须如实 fake, got %s", got)
	}
}
