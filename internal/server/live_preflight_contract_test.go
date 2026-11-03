package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"testing"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	livev1 "github.com/teamwork/digital-human/api/gen/go/live/v1"
	"github.com/teamwork/digital-human/internal/biz/avatar"
	"github.com/teamwork/digital-human/internal/biz/capability"
	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/publish"
	"github.com/teamwork/digital-human/internal/service"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

type preflightContractGate struct{}

func (preflightContractGate) Status() publish.Status {
	return publish.Status{State: publish.StatePushing}
}

type preflightHTTPFixture struct {
	base string
	mgr  *live.Manager
	stop func()
}

func startPreflightContractHTTP(t *testing.T, mgr *live.Manager, pf *live.Preflight) *preflightHTTPFixture {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := khttp.NewServer(khttp.Listener(lis), khttp.Timeout(0))
	livev1.RegisterLiveServiceHTTPServer(srv, service.NewLiveService(mgr, nil, nil, nil))
	registerPreflightRoute(srv, pf)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	return &preflightHTTPFixture{
		base: "http://" + lis.Addr().String(), mgr: mgr,
		stop: func() {
			cancel()
			_ = srv.Stop(context.Background())
			<-done
		},
	}
}

func publicPreflightContractHTTP(t *testing.T, avatars *avatar.Service) *preflightHTTPFixture {
	t.Helper()
	mgr := live.NewManagerWithStores(eventbus.New(64), live.NewMemorySink(64), live.NewFakeController(slog.Default()), nil, nil, avatars)
	gate := preflightContractGate{}
	pf := live.NewPreflight(avatars, func(context.Context) capability.Matrix {
		return capability.Matrix{
			"action_sender": capability.Real, "song_player": capability.Real,
			"tts": capability.Real, "vidu": capability.Real,
		}
	}, func() (publish.PushGate, bool) { return gate, false }, func() bool { return true })
	mgr.SetPreflight(pf)
	mgr.SetPushGate(gate, false)
	mgr.SetDefaultMode(live.ModePublicLive)
	return startPreflightContractHTTP(t, mgr, pf)
}

func contractAvatar(t *testing.T, store *avatar.MemoryAvatarStore, id string) {
	t.Helper()
	if err := store.Create(context.Background(), avatar.Avatar{ID: id, Slug: "slug-" + id, ViduImageURI: "vidu://" + id}); err != nil {
		t.Fatal(err)
	}
}

func TestLiveHTTPStartWithoutPreflightReturns428AndNoSession(t *testing.T) {
	f := publicPreflightContractHTTP(t, nil)
	defer f.stop()

	resp, body := postLive(t, f.base, "/api/v1/live/start", "", map[string]any{"mode": "public-live"})
	if resp.StatusCode != http.StatusPreconditionRequired || body["reason"] != "preflight_required" {
		t.Fatalf("无 preflight 直接 Start: status=%d body=%v", resp.StatusCode, body)
	}
	_, status := getLive(t, f.base, "/api/v1/live/status")
	if status["phase"] != "idle" || status["sessionId"] != nil && status["sessionId"] != "" {
		t.Fatalf("428 后不得创建场次: %v", status)
	}
}

func TestLiveHTTPStartAfterPreflightRejectsPersonaOrRevisionChange(t *testing.T) {
	t.Run("persona mismatch requires new preflight", func(t *testing.T) {
		store := avatar.NewMemoryStore()
		contractAvatar(t, store, "p1")
		contractAvatar(t, store, "p2")
		avatars := avatar.NewService(store)
		f := publicPreflightContractHTTP(t, avatars)
		defer f.stop()

		resp, pf := postLive(t, f.base, "/api/v1/live/preflight", "", map[string]any{"mode": "public-live", "persona_id": "p1"})
		if resp.StatusCode != http.StatusOK || pf["ready"] != true {
			t.Fatalf("preflight: status=%d body=%v", resp.StatusCode, pf)
		}
		resp, body := postLive(t, f.base, "/api/v1/live/start", "", map[string]any{
			"mode": "public-live", "persona_id": "p2", "preflight_token": pf["preflight_token"],
		})
		if resp.StatusCode != http.StatusPreconditionRequired || body["reason"] != "preflight_required" {
			t.Fatalf("persona 变化必须要求重检: status=%d body=%v", resp.StatusCode, body)
		}
		if snap := f.mgr.Snapshot(); snap.SessionID != "" || snap.Phase != live.PhaseIdle {
			t.Fatalf("拒绝后不得有场次: %+v", snap)
		}
	})

	t.Run("persona revision change requires new preflight", func(t *testing.T) {
		store := avatar.NewMemoryStore()
		contractAvatar(t, store, "p1")
		avatars := avatar.NewService(store)
		f := publicPreflightContractHTTP(t, avatars)
		defer f.stop()

		_, pf := postLive(t, f.base, "/api/v1/live/preflight", "", map[string]any{"mode": "public-live", "persona_id": "p1"})
		if pf["ready"] != true {
			t.Fatalf("preflight not ready: %v", pf)
		}
		cur, ok, err := store.Get(context.Background(), "p1")
		if err != nil || !ok {
			t.Fatalf("load p1: ok=%v err=%v", ok, err)
		}
		cur.Name = "changed-after-preflight"
		if err := store.UpdateWithRevision(context.Background(), cur, cur.Revision); err != nil {
			t.Fatal(err)
		}
		resp, body := postLive(t, f.base, "/api/v1/live/start", "", map[string]any{
			"mode": "public-live", "persona_id": "p1", "preflight_token": pf["preflight_token"],
		})
		if resp.StatusCode != http.StatusPreconditionRequired || body["reason"] != "preflight_required" {
			t.Fatalf("revision 变化必须要求重检: status=%d body=%v", resp.StatusCode, body)
		}
		if snap := f.mgr.Snapshot(); snap.SessionID != "" || snap.Phase != live.PhaseIdle {
			t.Fatalf("拒绝后不得有场次: %+v", snap)
		}
	})
}

func TestLiveHTTPLocalPreviewPreflightThenStartSucceeds(t *testing.T) {
	mgr := live.NewManagerWithStores(eventbus.New(64), live.NewMemorySink(64), live.NewFakeController(slog.Default()), nil, live.NewMemoryIdempotencyStore(time.Hour), nil)
	pf := live.NewPreflight(nil, func(context.Context) capability.Matrix {
		return capability.Matrix{
			"action_sender": capability.Fake, "song_player": capability.Fake,
			"tts": capability.Fake, "vidu": capability.Fake,
		}
	}, func() (publish.PushGate, bool) { return nil, false }, func() bool { return false })
	mgr.SetPreflight(pf)
	f := startPreflightContractHTTP(t, mgr, pf)
	defer f.stop()

	resp, preflight := postLive(t, f.base, "/api/v1/live/preflight", "", map[string]any{"mode": "local-preview"})
	if resp.StatusCode != http.StatusOK || preflight["ready"] != true || preflight["preflight_token"] == "" {
		t.Fatalf("local preflight: status=%d body=%v", resp.StatusCode, preflight)
	}
	resp, started := postLive(t, f.base, "/api/v1/live/start", "local-contract-1", map[string]any{
		"mode": "local-preview", "preflight_token": preflight["preflight_token"],
		"draft_id": "", "expected_revision": 0,
	})
	if resp.StatusCode != http.StatusOK || started["sessionId"] == "" || started["mode"] != "local-preview" {
		t.Fatalf("同 token Start: status=%d body=%v", resp.StatusCode, started)
	}
	_, status := getLive(t, f.base, "/api/v1/live/status?session_id="+started["sessionId"].(string))
	if status["phase"] != "live" {
		t.Fatalf("HTTP 受理后的真实状态: %v", status)
	}
}
