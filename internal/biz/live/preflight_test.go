package live

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/avatar"
	"github.com/teamwork/digital-human/internal/biz/capability"
	"github.com/teamwork/digital-human/internal/biz/contentpkg"
	"github.com/teamwork/digital-human/internal/publish"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

// fakePushGate 推流门 fake（测试专用，非生产 fake 实现）。
type fakePushGate struct{ st publish.Status }

func (g fakePushGate) Status() publish.Status { return g.st }

func allRealCaps(context.Context) capability.Matrix {
	return capability.Matrix{
		"action_sender": capability.Real, "song_player": capability.Real,
		"tts": capability.Real, "vidu": capability.Real, "koubo_actiongen": capability.Real,
		"content_storage": capability.Real, "push_gate": capability.Real, "storage_persistent": capability.Real,
	}
}

func allFakeCaps(context.Context) capability.Matrix {
	return capability.Matrix{
		"action_sender": capability.Fake, "song_player": capability.Fake,
		"tts": capability.Fake, "vidu": capability.Fake, "koubo_actiongen": capability.Fake,
		"content_storage": capability.Fake, "push_gate": capability.Fake, "storage_persistent": capability.Fake,
	}
}

func runPreflight(t *testing.T, pf *Preflight, ctx context.Context, req PreflightRequest) PreflightResult {
	t.Helper()
	res, err := pf.Run(ctx, req)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	return res
}

func localPreviewPolicy() ModePolicy {
	return ModePolicy{AllowLocalPreview: true, EngineMode: "dev"}
}

func newPreviewManager(bus *eventbus.Bus, sink EventSink, ctrl Controller, store SessionStore, idem Store, resolver avatar.ProfileResolver) *Manager {
	m := NewManagerWithStores(bus, sink, ctrl, store, idem, resolver)
	m.SetModePolicy(localPreviewPolicy())
	return m
}

func newPreviewBasicManager(bus *eventbus.Bus, sink EventSink, ctrl Controller) *Manager {
	return newPreviewManager(bus, sink, ctrl, nil, nil, nil)
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
	pf.SetDraftSource(&fakeDraftSource{draft: committedDraft(1)})

	pub := runPreflight(t, pf, ctx, PreflightRequest{Mode: ModePublicLive, DraftID: "d1", ExpectedRevision: 1})
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

	localPF := NewPreflight(nil, allFakeCaps, func() (publish.PushGate, bool) { return nil, false }, func() bool { return false })
	localPF.SetModePolicy(localPreviewPolicy())
	loc := runPreflight(t, localPF, ctx, PreflightRequest{Mode: ModeLocalPreview})
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
	pf.SetDraftSource(&fakeDraftSource{draft: committedDraft(1)})
	res := runPreflight(t, pf, context.Background(), PreflightRequest{Mode: ModePublicLive, DraftID: "d1", ExpectedRevision: 1})
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
		return fakePushGate{st: publish.Status{State: publish.StatePushing, TargetVerification: publish.TargetDouyin}}, false
	}, func() bool { return true })
	m.SetPreflight(pf)
	m.SetPushGate(fakePushGate{st: publish.Status{State: publish.StatePushing, TargetVerification: publish.TargetDouyin}}, false)
	m.SetModePolicy(ModePolicy{})

	if _, _, _, _, err := m.StartWithMode(ctx, "", "", "", StartRequest{DraftID: "d1", ExpectedRevision: 1}); !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("无 token 必须拒绝: %v", err)
	}
	if _, _, _, _, err := m.StartWithMode(ctx, "", "", "", StartRequest{PreflightToken: "pft_bogus", DraftID: "d1", ExpectedRevision: 1}); !errors.Is(err, ErrPreflightRequired) {
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
		return fakePushGate{st: publish.Status{State: publish.StatePushing, TargetVerification: publish.TargetDouyin}}, false
	}, func() bool { return true })
	pf.SetDraftSource(&fakeDraftSource{draft: committedDraft(1)})
	m.SetPreflight(pf)
	m.SetPushGate(fakePushGate{st: publish.Status{State: publish.StatePushing, TargetVerification: publish.TargetDouyin}}, false)

	res := runPreflight(t, pf, ctx, PreflightRequest{Mode: ModePublicLive, DraftID: "d1", ExpectedRevision: 1})
	if res.Ready {
		t.Fatal("fake 能力矩阵下 ready 必须 false")
	}
	_, _, _, _, err := m.StartWithMode(ctx, "", "", "", StartRequest{Mode: ModePublicLive, PreflightToken: res.PreflightToken, DraftID: "d1", ExpectedRevision: 1})
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
		return fakePushGate{st: publish.Status{State: publish.StatePushing, TargetVerification: publish.TargetDouyin}}, false
	}, func() bool { return true })
	pf.SetDraftSource(&fakeDraftSource{draft: committedDraft(1)})
	m.SetPreflight(pf)
	m.SetPushGate(fakePushGate{st: publish.Status{State: publish.StatePushing, TargetVerification: publish.TargetDouyin}}, false)

	res := runPreflight(t, pf, ctx, PreflightRequest{Mode: ModePublicLive, DraftID: "d1", ExpectedRevision: 1})
	if !res.Ready {
		t.Fatalf("all-real 应 ready: %+v", res.Checks)
	}
	// 拨快服务端时钟越过新鲜期（in-package 测试钩子）。
	pf.now = func() time.Time { return time.Now().Add(2 * PreflightTTL) }
	_, _, _, _, err := m.StartWithMode(ctx, "", "", "", StartRequest{Mode: ModePublicLive, PreflightToken: res.PreflightToken, DraftID: "d1", ExpectedRevision: 1})
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
	gate := fakePushGate{st: publish.Status{State: publish.StatePushing, TargetVerification: publish.TargetDouyin}}
	pf := NewPreflight(avatars, allRealCaps, func() (publish.PushGate, bool) { return gate, false }, func() bool { return true })
	pf.SetDraftSource(&fakeDraftSource{draft: committedDraft(3)})
	m.SetPreflight(pf)
	m.SetPushGate(gate, false)

	res := runPreflight(t, pf, ctx, PreflightRequest{Mode: ModePublicLive, PersonaID: "p1", DraftID: "d1", ExpectedRevision: 3})
	if !res.Ready {
		t.Fatalf("应 ready: %+v", res.Checks)
	}
	sid, phase, _, mode, err := m.StartWithMode(ctx, "p1", "", "", StartRequest{
		Mode: ModePublicLive, PreflightToken: res.PreflightToken, ResultDigest: res.ResultDigest,
		PersonaRevision: res.Revision, DraftID: "d1", ExpectedRevision: 3,
	})
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
	_, _, _, _, err = m.StartWithMode(ctx, "p1", "", "", StartRequest{
		Mode: ModePublicLive, PreflightToken: res.PreflightToken, ResultDigest: res.ResultDigest,
		PersonaRevision: res.Revision, DraftID: "d1", ExpectedRevision: 3,
	})
	if !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("已成功消费的 token 重放必须拒绝: %v", err)
	}
}

// TestLocalPreviewStartAnnotatesMode local-preview 显式开播：允许 fake，响应与事件带 mode。
func TestLocalPreviewStartAnnotatesMode(t *testing.T) {
	ctx := context.Background()
	sink := NewMemorySink(64)
	ctrl := NewFakeController(quietLogger())
	m := NewManagerWithStores(eventbus.New(64), sink, ctrl, nil, nil, nil)
	m.SetModePolicy(localPreviewPolicy())

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
	m.SetModePolicy(localPreviewPolicy())
	m.SetPushGate(fakePushGate{st: publish.Status{State: publish.StatePushing, TargetVerification: publish.TargetDouyin}}, false)
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
	_, _, _, _, err := m.StartWithMode(ctx, "", "", "", StartRequest{Mode: ModePublicLive, PreflightToken: "pft_x", DraftID: "d1", ExpectedRevision: 1})
	if !errors.Is(err, ErrPushGateRequired) {
		t.Fatalf("public 无 PushGate 必须拒绝: %v", err)
	}
}

type fakeDraftSource struct{ draft contentpkg.Draft }

func (s *fakeDraftSource) GetDraft(context.Context, string) (contentpkg.Draft, bool, error) {
	return s.draft, s.draft.ID != "", nil
}

func committedDraft(revision int64) contentpkg.Draft {
	d := contentpkg.Draft{
		ID: "d1", Revision: revision, Status: contentpkg.DraftCommitted,
		Snapshot: map[string]any{
			"assets": []any{map[string]any{"id": "song1", "revision": float64(4), "content_hash": "sha256:song"}},
		},
	}
	d.SnapshotHash = contentpkg.HashSnapshot(d.Snapshot)
	return d
}

func TestPreflightLocalPushReturnsTargetUnverified(t *testing.T) {
	gate := fakePushGate{st: publish.Status{State: publish.StatePushing, TargetVerification: publish.TargetLocal}}
	pf := NewPreflight(nil, allRealCaps, func() (publish.PushGate, bool) { return gate, true }, func() bool { return true })
	pf.SetDraftSource(&fakeDraftSource{draft: committedDraft(1)})
	res := runPreflight(t, pf, context.Background(), PreflightRequest{Mode: ModePublicLive, DraftID: "d1", ExpectedRevision: 1})
	if res.Ready {
		t.Fatal("本地协议推流不得放行公开开播，allowUnpushed 也不能绕过 public 门禁")
	}
	for _, check := range res.Checks {
		if check.Code == "push_gate" {
			if !strings.Contains(check.Detail, "[publish_target_unverified]") || strings.Contains(check.Detail, "push_gate_blocked") {
				t.Fatalf("错误码被泛化: %+v", check)
			}
			return
		}
	}
	t.Fatal("缺少 push_gate 检查")
}

func newDraftPreflight(source *fakeDraftSource, caps CapabilitySource) (*Preflight, fakePushGate) {
	gate := fakePushGate{st: publish.Status{State: publish.StatePushing, TargetVerification: publish.TargetDouyin}}
	pf := NewPreflight(nil, caps, func() (publish.PushGate, bool) { return gate, false }, func() bool { return true })
	pf.SetDraftSource(source)
	return pf, gate
}

func TestPreflightDigestStableAcrossObservationTime(t *testing.T) {
	source := &fakeDraftSource{draft: committedDraft(3)}
	pf, _ := newDraftPreflight(source, allRealCaps)
	now := time.Unix(100, 0)
	pf.now = func() time.Time { return now }
	req := PreflightRequest{Mode: ModePublicLive, DraftID: "d1", ExpectedRevision: 3}
	first := runPreflight(t, pf, context.Background(), req)
	now = now.Add(time.Second)
	second := runPreflight(t, pf, context.Background(), req)
	if !first.Ready || first.ResultDigest != second.ResultDigest {
		t.Fatalf("同输入同结论 digest 必须稳定: first=%+v second=%+v", first, second)
	}
}

func TestPreflightRejectsInvalidAssetRef(t *testing.T) {
	d := committedDraft(3)
	d.Snapshot["assets"] = []any{map[string]any{"id": "song1", "revision": float64(0)}}
	d.SnapshotHash = contentpkg.HashSnapshot(d.Snapshot)
	pf, _ := newDraftPreflight(&fakeDraftSource{draft: d}, allRealCaps)
	res := runPreflight(t, pf, context.Background(), PreflightRequest{Mode: ModePublicLive, DraftID: "d1", ExpectedRevision: 3})
	if res.Ready {
		t.Fatalf("缺 content_hash 的资产不得通过公开预检: %+v", res.Checks)
	}
}

func TestPreflightRejectsSameRevisionSnapshotMutation(t *testing.T) {
	d := committedDraft(3)
	pf, _ := newDraftPreflight(&fakeDraftSource{draft: d}, allRealCaps)
	res := runPreflight(t, pf, context.Background(), PreflightRequest{Mode: ModePublicLive, DraftID: "d1", ExpectedRevision: 3})
	if !res.Ready {
		t.Fatalf("初始草稿应 ready: %+v", res.Checks)
	}
	d.Snapshot["greeting"] = "changed"
	// 模拟持久化损坏：同 revision 改了 snapshot 但没有同步 hash。
	pf.SetDraftSource(&fakeDraftSource{draft: d})
	res = runPreflight(t, pf, context.Background(), PreflightRequest{Mode: ModePublicLive, DraftID: "d1", ExpectedRevision: 3})
	if res.Ready {
		t.Fatal("同 revision 的 snapshot/hash 不一致必须阻断")
	}
}

func TestPublicStartRequiresDigestAndRevalidatesDraft(t *testing.T) {
	ctx := context.Background()
	source := &fakeDraftSource{draft: committedDraft(3)}
	pf, gate := newDraftPreflight(source, allRealCaps)
	m := NewManagerWithStores(eventbus.New(64), NewMemorySink(64), NewFakeController(quietLogger()), nil, nil, nil)
	m.SetPreflight(pf)
	m.SetPushGate(gate, false)
	req := PreflightRequest{Mode: ModePublicLive, DraftID: "d1", ExpectedRevision: 3}
	res := runPreflight(t, pf, ctx, req)
	if !res.Ready {
		t.Fatalf("有效已提交草稿应 ready: %+v", res.Checks)
	}
	start := StartRequest{Mode: ModePublicLive, PreflightToken: res.PreflightToken, DraftID: "d1", ExpectedRevision: 3}
	if _, _, _, _, err := m.StartWithMode(ctx, "", "", "", start); !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("缺 result_digest 必须要求重新预检: %v", err)
	}
	start.ResultDigest = res.ResultDigest
	source.draft.Revision = 4
	if _, _, _, _, err := m.StartWithMode(ctx, "", "", "", start); !errors.Is(err, ErrPreflightRequired) {
		t.Fatalf("预检后 revision 变化必须拒绝: %v", err)
	}
	if snap := m.Snapshot(); snap.SessionID != "" {
		t.Fatalf("复核失败不得创建场次: %+v", snap)
	}
}

func TestServerModePolicyRejectsSpoofAndRequiresExplicitDev(t *testing.T) {
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		policy ModePolicy
	}{
		{name: "default_public", policy: ModePolicy{}},
		{name: "real_engine_still_public", policy: ModePolicy{AllowLocalPreview: true, EngineMode: "real"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pf := NewPreflight(nil, allFakeCaps, nil, nil)
			pf.SetModePolicy(tc.policy)
			_, err := pf.Run(ctx, PreflightRequest{Mode: ModeLocalPreview})
			if !errors.Is(err, ErrModeNotAllowed) {
				t.Fatalf("伪造 local-preview 必须 ErrModeNotAllowed: %v", err)
			}
			m := NewManagerWithStores(eventbus.New(64), NewMemorySink(64), NewFakeController(quietLogger()), nil, nil, nil)
			m.SetModePolicy(tc.policy)
			_, _, _, _, err = m.StartWithMode(ctx, "", "", "", StartRequest{Mode: ModeLocalPreview})
			if !errors.Is(err, ErrModeNotAllowed) {
				t.Fatalf("Start 伪造 local-preview 必须 ErrModeNotAllowed: %v", err)
			}
			if snap := m.Snapshot(); snap.SessionID != "" {
				t.Fatalf("mode 拒绝后不得创建场次: %+v", snap)
			}
		})
	}

	pf := NewPreflight(nil, allFakeCaps, nil, nil)
	pf.SetModePolicy(localPreviewPolicy())
	res, err := pf.Run(ctx, PreflightRequest{Mode: ModeLocalPreview})
	if err != nil || res.Mode != ModeLocalPreview {
		t.Fatalf("显式 allow + dev 才允许 local-preview: res=%+v err=%v", res, err)
	}
	if _, err := pf.Run(ctx, PreflightRequest{Mode: ModePublicLive}); !errors.Is(err, ErrModeNotAllowed) {
		t.Fatalf("服务端 dev 预览时客户端 public 也必须明确拒绝: %v", err)
	}
}

func TestPublicPreflightAndStartRequireDraft(t *testing.T) {
	ctx := context.Background()
	pf := NewPreflight(nil, allRealCaps, nil, func() bool { return true })
	if _, err := pf.Run(ctx, PreflightRequest{Mode: ModePublicLive}); !errors.Is(err, ErrDraftRequired) {
		t.Fatalf("public 空 draft 预检必须 ErrDraftRequired: %v", err)
	}
	m := NewManagerWithStores(eventbus.New(64), NewMemorySink(64), NewFakeController(quietLogger()), nil, nil, nil)
	if _, _, _, _, err := m.StartWithMode(ctx, "", "", "", StartRequest{Mode: ModePublicLive}); !errors.Is(err, ErrDraftRequired) {
		t.Fatalf("public 空 draft Start 必须 ErrDraftRequired: %v", err)
	}
}

func TestPreflightStoragePersistentTracksPostgresProbe(t *testing.T) {
	ctx := context.Background()
	current := capability.Real
	caps := func(ctx context.Context) capability.Matrix {
		mx := allFakeCaps(ctx)
		mx["storage_persistent"] = current
		return mx
	}
	pf := NewPreflight(nil, caps, nil, func() bool { return true })
	pf.SetDraftSource(&fakeDraftSource{draft: committedDraft(1)})

	storageCheck := func() Check {
		t.Helper()
		res := runPreflight(t, pf, ctx, PreflightRequest{Mode: ModePublicLive, DraftID: "d1", ExpectedRevision: 1})
		for _, check := range res.Checks {
			if check.Code == "storage_persistent" {
				return check
			}
		}
		t.Fatal("缺少 storage_persistent 检查")
		return Check{}
	}

	if check := storageCheck(); check.Status != CheckPass {
		t.Fatalf("PG 可达且 Ent 已安装时应 pass: %+v", check)
	}
	current = capability.Unknown
	if check := storageCheck(); check.Status != CheckFail || !strings.Contains(check.Detail, "postgres_unreachable") {
		t.Fatalf("PG 不可达时必须 fail 且原因可读: %+v", check)
	}
	current = capability.Real
	if check := storageCheck(); check.Status != CheckPass {
		t.Fatalf("PG 恢复后应回到 pass: %+v", check)
	}
}

func TestPublicStartRejectsCapabilityRegression(t *testing.T) {
	ctx := context.Background()
	source := &fakeDraftSource{draft: committedDraft(3)}
	current := capability.Real
	caps := func(context.Context) capability.Matrix {
		mx := allRealCaps(ctx)
		mx["action_sender"] = current
		return mx
	}
	pf, gate := newDraftPreflight(source, caps)
	m := NewManagerWithStores(eventbus.New(64), NewMemorySink(64), NewFakeController(quietLogger()), nil, nil, nil)
	m.SetPreflight(pf)
	m.SetPushGate(gate, false)
	res := runPreflight(t, pf, ctx, PreflightRequest{Mode: ModePublicLive, DraftID: "d1", ExpectedRevision: 3})
	current = capability.Fake
	_, _, _, _, err := m.StartWithMode(ctx, "", "", "", StartRequest{
		Mode: ModePublicLive, PreflightToken: res.PreflightToken, ResultDigest: res.ResultDigest,
		PersonaRevision: res.Revision, DraftID: "d1", ExpectedRevision: 3,
	})
	if err == nil {
		t.Fatal("预检后能力退化不得开播")
	}
}
