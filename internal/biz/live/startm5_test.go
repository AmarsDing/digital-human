package live

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/avatar"
	"github.com/teamwork/digital-human/internal/biz/refs"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

// sayFailController TTS 注入失败，验证 greeting 降级（v3 §3.2 失败分支）。
type sayFailController struct {
	*FakeController
}

func (c *sayFailController) Say(context.Context, string, string) (string, error) {
	return "", errors.New("tts down")
}

type greetingController struct {
	*FakeController
	sayCalls  int
	playCalls int
	playFile  string
	playErr   error
}

func (c *greetingController) Say(context.Context, string, string) (string, error) {
	c.sayCalls++
	return "say", nil
}

func (c *greetingController) PlayGreeting(_ context.Context, file string) (string, error) {
	c.playCalls++
	c.playFile = file
	if c.playErr != nil {
		return "", c.playErr
	}
	return "greeting", nil
}

func hasEvent(sink *MemorySink, typ string) bool {
	for _, e := range sink.All() {
		if e.Type == typ {
			return true
		}
	}
	return false
}

// newRefFixture 已同步档案（ViduImageURI 非空）+ 两张已同步参考图（乱序绑定验保序）
// + 一张未同步参考图（URI 空，验跳过告警）。
func newRefFixture(t *testing.T) *avatar.Service {
	t.Helper()
	ctx := context.Background()
	store := avatar.NewMemoryStore()
	for _, a := range []avatar.Asset{
		{ID: "ref-1", SHA256: "s1", Kind: "ref", URI: "vidu://r1"},
		{ID: "ref-2", SHA256: "s2", Kind: "ref", URI: "vidu://r2"},
		{ID: "ref-3", SHA256: "s3", Kind: "ref"}, // 未同步：URI 空
	} {
		if err := store.PutAsset(ctx, a); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Create(ctx, avatar.Avatar{
		ID: "p1", Slug: "s1", Name: "小雅", VoiceID: "voice-a", Greeting: "档案问候",
		ViduImageURI: "vidu://img", ActionLibraryID: "lib-1",
	}); err != nil {
		t.Fatal(err)
	}
	// 乱序绑定：ref-2(ord=1) 先于 ref-1(ord=0)，解析必须按 ord 升序。
	if err := store.AddRef(ctx, "p1", "ref-2", 1); err != nil {
		t.Fatal(err)
	}
	if err := store.AddRef(ctx, "p1", "ref-1", 0); err != nil {
		t.Fatal(err)
	}
	if err := store.AddRef(ctx, "p1", "ref-3", 2); err != nil {
		t.Fatal(err)
	}
	return avatar.NewService(store)
}

// TestStartPushesRefsInOrderAndGreetsOnce A14+：ctrl.Start 成功后把档案参考图保序推给
// refs.Switch；greeting 非空 → Say 恰好一次；未同步 ref 跳过并发 refs_push_skipped。
func TestStartPushesRefsInOrderAndGreetsOnce(t *testing.T) {
	ctx := context.Background()
	av := newRefFixture(t)
	bus := eventbus.New(64)
	sink := NewMemorySink(64)
	ctrl := &recController{}
	m := newPreviewManager(bus, sink, ctrl, nil, nil, av)
	sig := refs.NewFakeSignaler()
	m.SetRefSwitcher(refs.NewService(sig, bus))

	if _, phase, _, err := m.Start(ctx, "p1", "", ""); err != nil || phase != PhaseLive {
		t.Fatalf("start: phase=%s err=%v", phase, err)
	}
	// Switch 恰好一次，图片按 ord 保序，未同步（URI 空）的不进列表。
	calls := sig.Calls()
	if len(calls) != 1 || calls[0].Op != "switch" {
		t.Fatalf("switch calls=%+v", calls)
	}
	imgs := calls[0].Images
	if len(imgs) != 2 || imgs[0].URL != "vidu://r1" || imgs[1].URL != "vidu://r2" {
		t.Fatalf("参考图必须保序 [r1 r2]: %+v", imgs)
	}
	// greeting 非空 → Say 恰好一次。
	if ctrl.said != 1 {
		t.Fatalf("greeting Say 次数=%d, want 1", ctrl.said)
	}
	// 未同步 ref 跳过告警。
	if !hasEvent(sink, "refs_push_skipped") {
		t.Fatal("缺 refs_push_skipped 事件")
	}
}

// TestStartRefsPushFailureDegrades A14+ 失败分支：switch 失败只发 refs_push_failed，
// 开播照常成功（v3 §3.3 写死：不算开播失败）。
func TestStartRefsPushFailureDegrades(t *testing.T) {
	ctx := context.Background()
	av := newRefFixture(t)
	bus := eventbus.New(64)
	sink := NewMemorySink(64)
	m := newPreviewManager(bus, sink, &recController{}, nil, nil, av)
	sig := refs.NewFakeSignaler()
	sig.SetFail(errors.New("type14 rejected"))
	m.SetRefSwitcher(refs.NewService(sig, bus))

	if _, phase, _, err := m.Start(ctx, "p1", "", ""); err != nil || phase != PhaseLive {
		t.Fatalf("refs 推送失败不得影响开播: phase=%s err=%v", phase, err)
	}
	if !hasEvent(sink, "refs_push_failed") {
		t.Fatal("缺 refs_push_failed 事件")
	}
}

// TestStartGreetingFailureDegrades A14+ 失败分支：greeting TTS 失败发 greeting_failed，
// 开播照常成功。
func TestStartGreetingFailureDegrades(t *testing.T) {
	ctx := context.Background()
	av := newRefFixture(t)
	sink := NewMemorySink(64)
	ctrl := &sayFailController{NewFakeController(quietLogger())}
	m := newPreviewManager(eventbus.New(64), sink, ctrl, nil, nil, av)

	if _, phase, _, err := m.Start(ctx, "p1", "", ""); err != nil || phase != PhaseLive {
		t.Fatalf("greeting 失败不得影响开播: phase=%s err=%v", phase, err)
	}
	if !hasEvent(sink, "greeting_failed") {
		t.Fatal("缺 greeting_failed 事件")
	}
}

func TestStartGreetingAudioFilePreferredAndFailClosed(t *testing.T) {
	tests := []struct {
		name    string
		playErr error
	}{
		{name: "文件优先且不调用TTS"},
		{name: "文件错误显式上报且不回退TTS", playErr: errors.New("unsupported audio")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			sink := NewMemorySink(64)
			ctrl := &greetingController{FakeController: NewFakeController(quietLogger()), playErr: tt.playErr}
			m := newPreviewManager(eventbus.New(64), sink, ctrl, nil, nil, newRefFixture(t))
			m.SetGreetingAudioFileFn(func() string { return " greeting.wav " })

			if _, phase, _, err := m.Start(ctx, "p1", "", ""); err != nil || phase != PhaseLive {
				t.Fatalf("start: phase=%s err=%v", phase, err)
			}
			if ctrl.playCalls != 1 || ctrl.playFile != "greeting.wav" {
				t.Fatalf("文件播放 calls=%d file=%q", ctrl.playCalls, ctrl.playFile)
			}
			if ctrl.sayCalls != 0 {
				t.Fatalf("配置音频文件后 TTS Say 必须 0 次, got %d", ctrl.sayCalls)
			}
			if got := hasEvent(sink, "greeting_failed"); got != (tt.playErr != nil) {
				t.Fatalf("greeting_failed presence=%v, want %v", got, tt.playErr != nil)
			}
		})
	}
}

func TestStartGreetingAudioWithoutPlayerDoesNotFallBackToTTS(t *testing.T) {
	ctx := context.Background()
	sink := NewMemorySink(64)
	ctrl := &recController{}
	m := newPreviewManager(eventbus.New(64), sink, ctrl, nil, nil, newRefFixture(t))
	m.SetGreetingAudioFileFn(func() string { return "greeting.mp3" })
	if _, phase, _, err := m.Start(ctx, "p1", "", ""); err != nil || phase != PhaseLive {
		t.Fatalf("start: phase=%s err=%v", phase, err)
	}
	if ctrl.said != 0 {
		t.Fatalf("缺少文件播放器时不得回退 TTS, Say=%d", ctrl.said)
	}
	if !hasEvent(sink, "greeting_failed") {
		t.Fatal("缺少文件播放器必须显式上报 greeting_failed")
	}
}

// TestStartPrecheckAvatarNotSynced A18 前置/到期链路：档案未同步 Vidu →
// ErrAvatarExpired，且不得停当前在播场次、不得触达引擎（预检在 stop 之前，v3 §4）。
func TestStartPrecheckAvatarNotSynced(t *testing.T) {
	ctx := context.Background()
	store := avatar.NewMemoryStore()
	// 无 ViduImageURI/ViduAvatarID/ViduSync.State → ViduSynced=false。
	if err := store.Create(ctx, avatar.Avatar{ID: "p1", Slug: "s1", Name: "未同步", VoiceID: "v"}); err != nil {
		t.Fatal(err)
	}
	ctrl := &recController{}
	m := newPreviewManager(eventbus.New(64), NewMemorySink(64), ctrl, nil, nil, avatar.NewService(store))
	// 先起一场无档案旧路径直播（personaID 空跳过预检）。
	if _, phase, _, err := m.Start(ctx, "", "", "旧路径"); err != nil || phase != PhaseLive {
		t.Fatalf("旧路径开播: phase=%s err=%v", phase, err)
	}
	// 带未同步档案开播：预检失败，当前场保持 live，未 stop、未再触达引擎。
	_, phase, _, err := m.Start(ctx, "p1", "", "")
	if !errors.Is(err, ErrAvatarExpired) {
		t.Fatalf("err=%v, want ErrAvatarExpired", err)
	}
	if phase != PhaseLive {
		t.Fatalf("当前场次必须保持 live: %s", phase)
	}
	if len(ctrl.stops) != 0 {
		t.Fatalf("预检失败不得停当前场: stops=%v", ctrl.stops)
	}
	if ctrl.started != 1 {
		t.Fatalf("预检失败不得触达引擎: started=%d", ctrl.started)
	}
}

// TestStartPrecheckImageExpired 到期链路：已同步但主形象 ssupload URI 已过期 →
// ErrAvatarExpired，引擎零触达。
func TestStartPrecheckImageExpired(t *testing.T) {
	ctx := context.Background()
	store := avatar.NewMemoryStore()
	if err := store.Create(ctx, avatar.Avatar{
		ID: "p1", Slug: "s1", Name: "过期", VoiceID: "v", ViduImageURI: "vidu://img",
		ImageAsset: avatar.Asset{ID: "img-1", Kind: "image", URI: "vidu://img",
			ExpiresAtMs: time.Now().Add(-time.Hour).UnixMilli()},
	}); err != nil {
		t.Fatal(err)
	}
	ctrl := &recController{}
	m := newPreviewManager(eventbus.New(64), NewMemorySink(64), ctrl, nil, nil, avatar.NewService(store))
	if _, _, _, err := m.Start(ctx, "p1", "", ""); !errors.Is(err, ErrAvatarExpired) {
		t.Fatalf("err=%v, want ErrAvatarExpired", err)
	}
	if ctrl.started != 0 {
		t.Fatalf("预检失败不得触达引擎: started=%d", ctrl.started)
	}
}

// TestStartSnapshotFrozenAcrossRevisions A18：档A v1 开播 → 改档 → 再开播；
// 两场 StartOpts 不同，且第一场快照冻结旧配置（v3 §7.3 快照契约）。
func TestStartSnapshotFrozenAcrossRevisions(t *testing.T) {
	ctx := context.Background()
	store := avatar.NewMemoryStore()
	if err := store.Create(ctx, avatar.Avatar{
		ID: "p1", Slug: "s1", Name: "小雅", VoiceID: "voice-a", Greeting: "第一版问候",
		ViduImageURI: "vidu://img", ActionLibraryID: "lib-1",
	}); err != nil {
		t.Fatal(err)
	}
	rec := &recStore{}
	ctrl := NewFakeController(quietLogger())
	m := newPreviewManager(eventbus.New(64), NewMemorySink(64), ctrl, rec, nil, avatar.NewService(store))

	if _, phase, _, err := m.Start(ctx, "p1", "", ""); err != nil || phase != PhaseLive {
		t.Fatalf("first start: phase=%s err=%v", phase, err)
	}
	first := ctrl.LastOpts()
	if first.Greeting != "第一版问候" || first.VoiceID != "voice-a" {
		t.Fatalf("第一场 opts: %+v", first)
	}
	snap, ok := rec.snapshotAt(PhaseLive)
	if !ok || snap["greeting"] != "第一版问候" || snap["persona_revision"] != int64(1) {
		t.Fatalf("第一场快照必须冻结 v1: ok=%v snap=%+v", ok, snap)
	}
	if _, _, err := m.Stop(ctx, "test"); err != nil {
		t.Fatal(err)
	}

	// 改档（CAS base=1 → revision=2）。
	a, ok, err := store.Get(ctx, "p1")
	if err != nil || !ok {
		t.Fatalf("get: ok=%v err=%v", ok, err)
	}
	a.Greeting = "第二版问候"
	a.VoiceID = "voice-b"
	if err := store.UpdateWithRevision(ctx, a, 1); err != nil {
		t.Fatal(err)
	}
	if _, phase, _, err := m.Start(ctx, "p1", "", ""); err != nil || phase != PhaseLive {
		t.Fatalf("second start: phase=%s err=%v", phase, err)
	}
	second := ctrl.LastOpts()
	if second.Greeting != "第二版问候" || second.VoiceID != "voice-b" {
		t.Fatalf("再开播必须用新档: %+v", second)
	}
	// 第一场快照仍是旧配置（snapshotAt 取第一条 live 记录）。
	snap, _ = rec.snapshotAt(PhaseLive)
	if snap["greeting"] != "第一版问候" || snap["persona_revision"] != int64(1) {
		t.Fatalf("第一场快照必须保持冻结: %+v", snap)
	}
}

// degradeController 建场发生动作库 403 降级（引擎已去 action 重建成功）。
type degradeController struct {
	*recController
}

func (c *degradeController) ActionDegraded() bool { return true }

// TestStartActionLibraryDegradedEvent G3-2（v3 §3.3）：引擎动作库 403 降级成功 →
// Manager 发 action_library_degraded 告警事件（带 library_id），开播照常成功。
func TestStartActionLibraryDegradedEvent(t *testing.T) {
	ctx := context.Background()
	av := newRefFixture(t)
	bus := eventbus.New(64)
	sink := NewMemorySink(64)
	m := newPreviewManager(bus, sink, &degradeController{&recController{}}, nil, nil, av)

	if _, phase, _, err := m.Start(ctx, "p1", "", ""); err != nil || phase != PhaseLive {
		t.Fatalf("降级开播必须成功: phase=%s err=%v", phase, err)
	}
	found := false
	for _, e := range sink.All() {
		if e.Type == "action_library_degraded" {
			found = true
			if e.Payload["action_library_id"] != "lib-1" {
				t.Fatalf("事件 payload=%+v", e.Payload)
			}
		}
	}
	if !found {
		t.Fatal("缺 action_library_degraded 事件")
	}
}

// TestStartNoDegradeNoEvent 正常开播（引擎无降级）不得发 action_library_degraded。
func TestStartNoDegradeNoEvent(t *testing.T) {
	ctx := context.Background()
	av := newRefFixture(t)
	sink := NewMemorySink(64)
	m := newPreviewManager(eventbus.New(64), sink, &recController{}, nil, nil, av)

	if _, phase, _, err := m.Start(ctx, "p1", "", ""); err != nil || phase != PhaseLive {
		t.Fatalf("start: phase=%s err=%v", phase, err)
	}
	if hasEvent(sink, "action_library_degraded") {
		t.Fatal("未降级不得发 action_library_degraded")
	}
}
