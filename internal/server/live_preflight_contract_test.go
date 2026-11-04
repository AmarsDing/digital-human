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
	"github.com/teamwork/digital-human/internal/biz/contentpkg"
	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/publish"
	"github.com/teamwork/digital-human/internal/service"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

type preflightContractGate struct {
	target publish.TargetVerification
}

func (g preflightContractGate) Status() publish.Status {
	return publish.Status{State: publish.StatePushing, TargetVerification: g.target}
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

type contractDraftSource struct{}

func (contractDraftSource) GetDraft(context.Context, string) (contentpkg.Draft, bool, error) {
	d := contentpkg.Draft{
		ID: "d1", Revision: 3, Status: contentpkg.DraftCommitted,
		Snapshot: map[string]any{
			"assets": []any{map[string]any{"id": "song1", "revision": float64(4), "content_hash": "sha256:song"}},
		},
	}
	d.SnapshotHash = contentpkg.HashSnapshot(d.Snapshot)
	return d, true, nil
}

func publicPreflightContractHTTP(t *testing.T, avatars *avatar.Service) *preflightHTTPFixture {
	t.Helper()
	mgr := live.NewManagerWithStores(eventbus.New(64), live.NewMemorySink(64), live.NewFakeController(slog.Default()), nil, nil, avatars)
	gate := preflightContractGate{target: publish.TargetDouyin}
	pf := live.NewPreflight(avatars, func(context.Context) capability.Matrix {
		return capability.Matrix{
			"action_sender": capability.Real, "song_player": capability.Real,
			"tts": capability.Real, "vidu": capability.Real, "koubo_actiongen": capability.Real,
			"content_storage": capability.Real,
		}
	}, func() (publish.PushGate, bool) { return gate, false }, func() bool { return true })
	pf.SetDraftSource(contractDraftSource{})
	mgr.SetPreflight(pf)
	mgr.SetPushGate(gate, false)
	return startPreflightContractHTTP(t, mgr, pf)
}

func contractAvatar(t *testing.T, store *avatar.MemoryAvatarStore, id string) {
	t.Helper()
	if err := store.Create(context.Background(), avatar.Avatar{ID: id, Slug: "slug-" + id, ViduImageURI: "vidu://" + id}); err != nil {
		t.Fatal(err)
	}
}

func TestLiveHTTPPreflightReturnsTopLevelStartBinding(t *testing.T) {
	store := avatar.NewMemoryStore()
	contractAvatar(t, store, "p1")
	f := publicPreflightContractHTTP(t, avatar.NewService(store))
	defer f.stop()

	resp, body := postLive(t, f.base, "/api/v1/live/preflight", "", map[string]any{
		"mode": "public-live", "persona_id": "p1", "draft_id": "d1", "expected_revision": 3,
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("preflight status=%d body=%v", resp.StatusCode, body)
	}
	if body["persona_id"] != "p1" || body["revision"] != float64(1) {
		t.Fatalf("预检必须顶层返回 persona_id/revision: %v", body)
	}
	if body["result_digest"] == "" || body["preflight_token"] == "" {
		t.Fatalf("预检必须顶层返回 result_digest/preflight_token: %v", body)
	}
	t.Logf("preflight start binding: persona_id=%q revision=%v result_digest=%q preflight_token=%q",
		body["persona_id"], body["revision"], body["result_digest"], body["preflight_token"])
}

func TestLiveHTTPStartWithStalePersonaRevisionReturns409(t *testing.T) {
	store := avatar.NewMemoryStore()
	contractAvatar(t, store, "p1")
	f := publicPreflightContractHTTP(t, avatar.NewService(store))
	defer f.stop()

	_, pf := postLive(t, f.base, "/api/v1/live/preflight", "", map[string]any{
		"mode": "public-live", "persona_id": "p1", "draft_id": "d1", "expected_revision": 3,
	})
	resp, body := postLive(t, f.base, "/api/v1/live/start", "", map[string]any{
		"mode": "public-live", "persona_id": "p1", "revision": float64(99),
		"draft_id": "d1", "expected_revision": 3,
		"result_digest": pf["result_digest"], "preflight_token": pf["preflight_token"],
	})
	if resp.StatusCode != http.StatusConflict || body["reason"] != "profile_changed" {
		t.Fatalf("档案版本不一致必须明确返回 409/profile_changed: status=%d body=%v", resp.StatusCode, body)
	}
}

func TestLiveHTTPStartWithStaleDigestReturns428(t *testing.T) {
	store := avatar.NewMemoryStore()
	contractAvatar(t, store, "p1")
	f := publicPreflightContractHTTP(t, avatar.NewService(store))
	defer f.stop()

	_, pf := postLive(t, f.base, "/api/v1/live/preflight", "", map[string]any{
		"mode": "public-live", "persona_id": "p1", "draft_id": "d1", "expected_revision": 3,
	})
	resp, body := postLive(t, f.base, "/api/v1/live/start", "", map[string]any{
		"mode": "public-live", "persona_id": "p1", "revision": pf["revision"],
		"draft_id": "d1", "expected_revision": 3,
		"result_digest": "sha256:stale", "preflight_token": pf["preflight_token"],
	})
	if resp.StatusCode != http.StatusPreconditionRequired || body["reason"] != "preflight_expired" {
		t.Fatalf("预检摘要失效必须明确返回 428/preflight_expired: status=%d body=%v", resp.StatusCode, body)
	}
}

func TestLiveHTTPStartWithoutPreflightReturns428AndNoSession(t *testing.T) {
	f := publicPreflightContractHTTP(t, nil)
	defer f.stop()

	resp, body := postLive(t, f.base, "/api/v1/live/start", "", map[string]any{
		"mode": "public-live", "draft_id": "d1", "expected_revision": 3,
	})
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

		resp, pf := postLive(t, f.base, "/api/v1/live/preflight", "", map[string]any{
			"mode": "public-live", "persona_id": "p1", "draft_id": "d1", "expected_revision": 3,
		})
		if resp.StatusCode != http.StatusOK || pf["ready"] != true {
			t.Fatalf("preflight: status=%d body=%v", resp.StatusCode, pf)
		}
		resp, body := postLive(t, f.base, "/api/v1/live/start", "", map[string]any{
			"mode": "public-live", "persona_id": "p2", "revision": pf["revision"],
			"draft_id": "d1", "expected_revision": 3, "result_digest": pf["result_digest"],
			"preflight_token": pf["preflight_token"],
		})
		if resp.StatusCode != http.StatusConflict || body["reason"] != "profile_changed" {
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

		_, pf := postLive(t, f.base, "/api/v1/live/preflight", "", map[string]any{
			"mode": "public-live", "persona_id": "p1", "draft_id": "d1", "expected_revision": 3,
		})
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
			"mode": "public-live", "persona_id": "p1", "revision": pf["revision"],
			"draft_id": "d1", "expected_revision": 3, "result_digest": pf["result_digest"],
			"preflight_token": pf["preflight_token"],
		})
		if resp.StatusCode != http.StatusConflict || body["reason"] != "profile_changed" {
			t.Fatalf("revision 变化必须要求重检: status=%d body=%v", resp.StatusCode, body)
		}
		if snap := f.mgr.Snapshot(); snap.SessionID != "" || snap.Phase != live.PhaseIdle {
			t.Fatalf("拒绝后不得有场次: %+v", snap)
		}
	})
}

func TestLiveHTTPModeSpoofReturns409(t *testing.T) {
	f := publicPreflightContractHTTP(t, nil)
	defer f.stop()

	resp, body := postLive(t, f.base, "/api/v1/live/preflight", "", map[string]any{"mode": "local-preview"})
	if resp.StatusCode != http.StatusConflict || body["reason"] != "mode_not_allowed" || body["mode"] != "public-live" {
		t.Fatalf("伪造 preflight mode: status=%d body=%v", resp.StatusCode, body)
	}
	t.Logf("spoofed local-preview response: status=%d body=%v", resp.StatusCode, body)

	resp, body = postLive(t, f.base, "/api/v1/live/start", "", map[string]any{"mode": "local-preview"})
	if resp.StatusCode != http.StatusConflict || body["reason"] != "mode_not_allowed" {
		t.Fatalf("伪造 Start mode: status=%d body=%v", resp.StatusCode, body)
	}
	t.Logf("spoofed Start local-preview response: status=%d body=%v", resp.StatusCode, body)
	if snap := f.mgr.Snapshot(); snap.SessionID != "" || snap.Phase != live.PhaseIdle {
		t.Fatalf("mode 拒绝后不得创建场次: %+v", snap)
	}
}

func TestLiveHTTPStartWithUnverifiedTargetReturns409(t *testing.T) {
	for _, target := range []publish.TargetVerification{publish.TargetLocal, publish.TargetUnknown} {
		t.Run(string(target), func(t *testing.T) {
			mgr := live.NewManagerWithStores(eventbus.New(64), live.NewMemorySink(64), live.NewFakeController(slog.Default()), nil, nil, nil)
			gate := preflightContractGate{target: target}
			mgr.SetPushGate(gate, false)
			f := startPreflightContractHTTP(t, mgr, nil)
			defer f.stop()

			resp, body := postLive(t, f.base, "/api/v1/live/start", "", map[string]any{
				"mode": "public-live", "draft_id": "d1", "expected_revision": 3,
			})
			t.Logf("%s %s\n%s", resp.Proto, resp.Status, body["_raw"])
			if resp.StatusCode != http.StatusConflict || body["code"] != float64(http.StatusConflict) || body["reason"] != "publish_target_unverified" {
				t.Fatalf("未验证目标必须返回 409/publish_target_unverified: status=%d body=%v", resp.StatusCode, body)
			}
			if snap := f.mgr.Snapshot(); snap.SessionID != "" || snap.Phase != live.PhaseIdle {
				t.Fatalf("拒绝后不得创建场次: %+v", snap)
			}
		})
	}
}

func TestLiveHTTPPublicPreflightTokenReplayReturns428(t *testing.T) {
	store := avatar.NewMemoryStore()
	contractAvatar(t, store, "p1")
	f := publicPreflightContractHTTP(t, avatar.NewService(store))
	defer f.stop()

	resp, pf := postLive(t, f.base, "/api/v1/live/preflight", "", map[string]any{
		"mode": "public-live", "persona_id": "p1", "draft_id": "d1", "expected_revision": 3,
	})
	if resp.StatusCode != http.StatusOK || pf["ready"] != true {
		t.Fatalf("preflight: status=%d body=%v", resp.StatusCode, pf)
	}
	start := map[string]any{
		"mode": "public-live", "persona_id": "p1", "revision": pf["revision"],
		"draft_id": "d1", "expected_revision": 3, "result_digest": pf["result_digest"],
		"preflight_token": pf["preflight_token"],
	}
	resp, started := postLive(t, f.base, "/api/v1/live/start", "", start)
	if resp.StatusCode != http.StatusOK || started["sessionId"] == "" {
		t.Fatalf("首次消费 token: status=%d body=%v", resp.StatusCode, started)
	}
	resp, stopped := postLive(t, f.base, "/api/v1/live/stop", "", map[string]any{})
	if resp.StatusCode != http.StatusOK || stopped["ok"] != true {
		t.Fatalf("停止首次场次: status=%d body=%v", resp.StatusCode, stopped)
	}
	resp, replay := postLive(t, f.base, "/api/v1/live/start", "", start)
	if resp.StatusCode != http.StatusPreconditionRequired || replay["reason"] != "preflight_required" {
		t.Fatalf("重放已消费 token 必须拒绝: status=%d body=%v", resp.StatusCode, replay)
	}
	if snap := f.mgr.Snapshot(); snap.SessionID != started["sessionId"] || snap.Phase != live.PhaseEnded {
		t.Fatalf("token 重放后不得创建新场次: %+v", snap)
	}
	t.Logf("replayed preflight token response: status=%d body=%v", resp.StatusCode, replay)
}

func TestLiveHTTPPublicRequiresDraft(t *testing.T) {
	f := publicPreflightContractHTTP(t, nil)
	defer f.stop()

	resp, body := postLive(t, f.base, "/api/v1/live/preflight", "", map[string]any{"mode": "public-live"})
	if resp.StatusCode != http.StatusConflict || body["reason"] != "draft_required" || body["mode"] != "public-live" {
		t.Fatalf("public 空 draft 预检: status=%d body=%v", resp.StatusCode, body)
	}
	resp, body = postLive(t, f.base, "/api/v1/live/start", "", map[string]any{"mode": "public-live"})
	if resp.StatusCode != http.StatusConflict || body["reason"] != "draft_required" {
		t.Fatalf("public 空 draft Start: status=%d body=%v", resp.StatusCode, body)
	}
}

func TestLiveHTTPLocalPreviewPreflightThenStartSucceeds(t *testing.T) {
	mgr := live.NewManagerWithStores(eventbus.New(64), live.NewMemorySink(64), live.NewFakeController(slog.Default()), nil, live.NewMemoryIdempotencyStore(time.Hour), nil)
	pf := live.NewPreflight(nil, func(context.Context) capability.Matrix {
		return capability.Matrix{
			"action_sender": capability.Fake, "song_player": capability.Fake,
			"tts": capability.Fake, "vidu": capability.Fake,
		}
	}, func() (publish.PushGate, bool) { return nil, false }, func() bool { return false })
	policy := live.ModePolicy{AllowLocalPreview: true, EngineMode: "dev"}
	pf.SetModePolicy(policy)
	mgr.SetModePolicy(policy)
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
