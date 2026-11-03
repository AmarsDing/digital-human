package live

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/avatar"
	"github.com/teamwork/digital-human/internal/biz/capability"
	"github.com/teamwork/digital-human/internal/publish"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

// fakePushGate 推流门 fake（测试专用，非生产 fake 实现）。
type fakePushGate struct{ st publish.Status }

func (g fakePushGate) Status() publish.Status { return g.st }

func allRealCaps(context.Context) capability.Matrix {
	return capability.Matrix{
		"action_sender": capability.Real, "song_player": capability.Real,
		"tts": capability.Real, "vidu": capability.Real,
		"push_gate": capability.Real, "storage_persistent": capability.Real,
	}
}

func allFakeCaps(context.Context) capability.Matrix {
	return capability.Matrix{
		"action_sender": capability.Fake, "song_player": capability.Fake,
		"tts": capability.Fake, "vidu": capability.Fake,
		"push_gate": capability.Fake, "storage_persistent": capability.Fake,
	}
}

func newAvatarSvc(t *testing.T, id string) *avatar.Service {
	t.Helper()
	ctx := context.Background()
	store := avatar.NewMemoryStore()
	if err := store.Create(ctx, avatar.Avatar{ID: id, Slug: "s-" + id, ViduImageURI: "vidu://img"}); err != nil {
		t.Fatal(err)
	}
	return avatar.NewService(store)
}

// TestPreflightPublicBlocksFakeCaps B2：能力矩阵 fake 时 public 预检 ready=false 且逐条
// 标注 fail；local-preview 同条件 ready=true（fake 显式 warn 不阻断）。unknown 绝不默认 pass。
func TestPreflightPublicBlocksFakeCaps(t *testing.T) {
	ctx := context.Background()
	pf := NewPreflight(nil, allFakeCaps, func() (publish.PushGate, bool) { return nil, false }, func() bool { return false })

	pub := pf.Run(ctx, PreflightRequest{Mode: ModePublicLive})
	if pub.Ready {
		t.Fatal("fake 能力下 public ready 必须为 false")
	}
	mustFail := map[string]bool{"action_sender": true, "song_player": true, "tts": true, "vidu": true, "push_gate": true, "storage_persistent": true}
	seen := map[string]CheckStatus{}
	for _, c := range pub.Checks {
		if mustFail[c.Code] && c.Status != CheckFail {
			t.Fatalf("check %s 应为 fail, got %s", c.Code, c.Status)
		}
		seen[c.Code] = c.Status
	}
	for code := range mustFail {
		if _, ok := seen[code]; !ok {
			t.Fatalf("缺少 check %s", code)
		}
	}
	if pub.PreflightToken == "" || pub.ExpiresAtMs-pub.ObservedAtMs != PreflightTTL.Milliseconds() {
		t.Fatalf("token/新鲜期缺失: %+v", pub)
	}

	loc := pf.Run(ctx, PreflightRequest{Mode: ModeLocalPreview})
	if !loc.Ready {
		t.Fatal("local-preview 下 fake 为 warn 不阻断，ready 应为 true")
	}
	for _, c := range loc.Checks {
		if c.Code == "action_sender" && c.Status != CheckWarn {
			t.Fatalf("local fake 应显式标注 warn: %+v", c)
		}
	}
}

// TestPreflightUnknownBlocksPublic unknown 与 fail 同阻断。
func TestPreflightUnknownBlocks(t *testing.T) {
	pf := NewPreflight(nil, func(context.Context) capability.Matrix {
		return capability.Matrix{"action_sender": capability.Unknown} // 其余未登记=unknown
	}, nil, nil)
	res := pf.Run(context.Background(), PreflightRequest{Mode: ModePublicLive})
	if res.Ready {
		t.Fatal("unknown 绝不默认 pass：ready 必须为 false")
	}
}

// TestPublicStartRequiresToken B1/§7.6-2：public-live 无 token / 错 token 直接拒绝，
// 不创建场次（无 session 残留）。
func TestPublicStartRequiresToken(t *testing.T) {
	ctx := context.Background()
	ctrl := NewFakeController(quietLogger())
	m := NewManagerWithStores(eventbus.New(64), NewMemorySink(64), ctrl, nil, nil, nil)
	pf := NewPreflight(nil, allRealCaps, func() (publish.PushGate, bool) {
		return fakePushGate{st: publish.Status{State: publish.StatePushing}}, false
	}, func() bool { return true })
	m.SetPreflight(pf)
	m.SetPushGate(fakePushGate{st: publish.Status{State: publish.StatePushing}}, false)
	m.SetDefaultMode(ModePublicLive)

	if _, _, _, err := m.Start(ctx, "", "", ""); !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("无 token 必须拒绝: %v", err)
	}
	if _, _, _, _, err := m.StartWithMode(ctx, "", "", "", StartRequest{PreflightToken: "pft_bogus"}); !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("错 token 必须拒绝: %v", err)
	}
	if snap := m.Snapshot(); snap.Phase != PhaseIdle || snap.SessionID != "" {
		t.Fatalf("拒绝后不得有场次残留: %+v", snap)
	}
}

// TestPublicStartRejectedWithFakeSender B1/B2 DoD：fake Sender 装配下 public Start 被拒。
func TestPublicStartRejectedWithFakeSender(t *testing.T) {
	ctx := context.Background()
	ctrl := NewFakeController(quietLogger())
	m := NewManagerWithStores(eventbus.New(64), NewMemorySink(64), ctrl, nil, nil, nil)
	pf := NewPreflight(nil, allFakeCaps, func() (publish.PushGate, bool) {
		return fakePushGate{st: publish.Status{State: publish.StatePushing}}, false
	}, func() bool { return true })
	m.SetPreflight(pf)
	m.SetPushGate(fakePushGate{st: publish.Status{State: publish.StatePushing}}, false)

	res := pf.Run(ctx, PreflightRequest{Mode: ModePublicLive})
	if res.Ready {
		t.Fatal("fake 能力矩阵下 ready 必须 false")
	}
	_, _, _, _, err := m.StartWithMode(ctx, "", "", "", StartRequest{Mode: ModePublicLive, PreflightToken: res.PreflightToken})
	if !errors.Is(err, ErrCapabilityBlocked) {
		t.Fatalf("fake Sender 下 public Start 必须 ErrCapabilityBlocked, got %v", err)
	}
	if snap := m.Snapshot(); snap.SessionID != "" {
		t.Fatalf("被拒后不得有 session: %+v", snap)
	}
}

// TestPublicStartTokenExpired preflight token 过 30s 新鲜期被拒。
func TestPublicStartTokenExpired(t *testing.T) {
	ctx := context.Background()
	ctrl := NewFakeController(quietLogger())
	m := NewManagerWithStores(eventbus.New(64), NewMemorySink(64), ctrl, nil, nil, nil)
	pf := NewPreflight(nil, allRealCaps, func() (publish.PushGate, bool) {
		return fakePushGate{st: publish.Status{State: publish.StatePushing}}, false
	}, func() bool { return true })
	m.SetPreflight(pf)
	m.SetPushGate(fakePushGate{st: publish.Status{State: publish.StatePushing}}, false)

	res := pf.Run(ctx, PreflightRequest{Mode: ModePublicLive})
	if !res.Ready {
		t.Fatalf("all-real 应 ready: %+v", res.Checks)
	}
	// 拨快服务端时钟越过新鲜期（in-package 测试钩子）。
	pf.now = func() time.Time { return time.Now().Add(2 * PreflightTTL) }
	_, _, _, _, err := m.StartWithMode(ctx, "", "", "", StartRequest{Mode: ModePublicLive, PreflightToken: res.PreflightToken})
	if !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("过期 token 必须拒绝: %v", err)
	}
}

// TestPublicStartHappyPath public + 全 real + 有效 token + 推流中 + 档案一致 → 开播成功，
// 响应/事件标注 mode=public-live。
func TestPublicStartHappyPath(t *testing.T) {
	ctx := context.Background()
	sink := NewMemorySink(64)
	ctrl := NewFakeController(quietLogger())
	avatars := newAvatarSvc(t, "p1")
	m := NewManagerWithStores(eventbus.New(64), sink, ctrl, nil, nil, avatars)
	gate := fakePushGate{st: publish.Status{State: publish.StatePushing}}
	pf := NewPreflight(avatars, allRealCaps, func() (publish.PushGate, bool) { return gate, false }, func() bool { return true })
	m.SetPreflight(pf)
	m.SetPushGate(gate, false)

	res := pf.Run(ctx, PreflightRequest{Mode: ModePublicLive, PersonaID: "p1"})
	if !res.Ready {
		t.Fatalf("应 ready: %+v", res.Checks)
	}
	sid, phase, _, mode, err := m.StartWithMode(ctx, "p1", "", "", StartRequest{Mode: ModePublicLive, PreflightToken: res.PreflightToken})
	if err != nil || phase != PhaseLive {
		t.Fatalf("public start: phase=%s err=%v", phase, err)
	}
	if mode != ModePublicLive || sid == "" {
		t.Fatalf("mode 标注: mode=%s sid=%s", mode, sid)
	}
	if snap := m.Snapshot(); snap.Mode != string(ModePublicLive) {
		t.Fatalf("快照 mode 标注: %+v", snap)
	}
	if _, _, err := m.Stop(ctx, "test-done"); err != nil {
		t.Fatal(err)
	}
}

// TestLocalPreviewStartAnnotatesMode local-preview 显式开播：允许 fake，响应与事件带 mode。
func TestLocalPreviewStartAnnotatesMode(t *testing.T) {
	ctx := context.Background()
	sink := NewMemorySink(64)
	ctrl := NewFakeController(quietLogger())
	m := NewManagerWithStores(eventbus.New(64), sink, ctrl, nil, nil, nil)

	sid, phase, _, mode, err := m.StartWithMode(ctx, "", "", "", StartRequest{Mode: ModeLocalPreview})
	if err != nil || phase != PhaseLive {
		t.Fatalf("local-preview start: phase=%s err=%v", phase, err)
	}
	if mode != ModeLocalPreview || sid == "" {
		t.Fatalf("mode 标注: mode=%s sid=%s", mode, sid)
	}
	events, err := sink.List(ctx, sid, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, ev := range events {
		if ev.Type == "live.started" {
			if mv, _ := ev.Payload["mode"].(string); mv == string(ModeLocalPreview) {
				found = true
			}
		}
	}
	if !found {
		t.Fatalf("live.started 事件必须标注 mode=local-preview: %+v", events)
	}
	if _, _, err := m.Stop(ctx, "done"); err != nil {
		t.Fatal(err)
	}
}

// TestLocalPreviewWhilePushingRejected §7.6：local-preview 禁止连接公开推流。
func TestLocalPreviewWhilePushingRejected(t *testing.T) {
	ctx := context.Background()
	ctrl := NewFakeController(quietLogger())
	m := NewManagerWithStores(eventbus.New(64), NewMemorySink(64), ctrl, nil, nil, nil)
	m.SetPushGate(fakePushGate{st: publish.Status{State: publish.StatePushing}}, false)
	_, _, _, _, err := m.StartWithMode(ctx, "", "", "", StartRequest{Mode: ModeLocalPreview})
	if !errors.Is(err, ErrPreviewWhilePushing) {
		t.Fatalf("推流中 local-preview 必须拒绝: %v", err)
	}
}

// TestPublicStartPushGateNotWired §7.6-5：public 模式 PushGate 未装配显式拒绝。
func TestPublicStartPushGateNotWired(t *testing.T) {
	ctx := context.Background()
	ctrl := NewFakeController(quietLogger())
	m := NewManagerWithStores(eventbus.New(64), NewMemorySink(64), ctrl, nil, nil, nil)
	m.SetPreflight(NewPreflight(nil, allRealCaps, func() (publish.PushGate, bool) { return nil, false }, func() bool { return true }))
	_, _, _, _, err := m.StartWithMode(ctx, "", "", "", StartRequest{Mode: ModePublicLive, PreflightToken: "pft_x"})
	if !errors.Is(err, ErrPushGateRequired) {
		t.Fatalf("public 无 PushGate 必须拒绝: %v", err)
	}
}
