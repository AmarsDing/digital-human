package live

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/avatar"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

func quietLogger() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// TestStartOptsFromProfileResolver 档案经 ProfileResolver 进入开播参数（v3 §3.1/§8）：
// fake controller 断言收到的 opts 与档案一致；入参 greeting 优先于档案 greeting。
func TestStartOptsFromProfileResolver(t *testing.T) {
	ctx := context.Background()
	store := avatar.NewMemoryStore()
	if err := store.PutAsset(ctx, avatar.Asset{ID: "ref-1", SHA256: "s1", Kind: "ref", URI: "vidu://r1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Create(ctx, avatar.Avatar{
		ID: "p1", Slug: "s1", VoiceID: "voice-a", Greeting: "档案问候",
		ViduImageURI: "vidu://img", ActionLibraryID: "lib-1",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.AddRef(ctx, "p1", "ref-1", 0); err != nil {
		t.Fatal(err)
	}
	ctrl := NewFakeController(quietLogger())
	m := newPreviewManager(eventbus.New(64), NewMemorySink(64), ctrl, nil, nil, avatar.NewService(store))

	sid, phase, _, err := m.Start(ctx, "p1", "", "")
	if err != nil || phase != PhaseLive {
		t.Fatalf("start: phase=%s err=%v", phase, err)
	}
	opts := ctrl.LastOpts()
	if opts.SessionID != sid || opts.Model != "vidu-s2" {
		t.Fatalf("opts 基础字段: %+v", opts)
	}
	if opts.VoiceID != "voice-a" || opts.ViduImageURI != "vidu://img" || opts.ActionLibraryID != "lib-1" {
		t.Fatalf("档案字段未进 opts: %+v", opts)
	}
	if opts.Greeting != "档案问候" {
		t.Fatalf("空 greeting 应回落档案值: %+v", opts)
	}
	if len(opts.RefImageURIs) != 1 || opts.RefImageURIs[0] != "vidu://r1" {
		t.Fatalf("参考图未进 opts: %+v", opts)
	}
}

// TestStartOptsFallbackWithoutProfile 深化方案 v2 §7.6-3：M1 的「Resolve 失败回落旧路径」
// fail-open 已删除——档案不存在（幽灵 id）两种模式一律显式拒绝且不创建场次（无 session 残留）。
func TestStartOptsFallbackWithoutProfile(t *testing.T) {
	ctx := context.Background()
	store := avatar.NewMemoryStore() // 空档案库：Resolve 必 ErrNotFound
	ctrl := NewFakeController(quietLogger())
	m := newPreviewManager(eventbus.New(64), NewMemorySink(64), ctrl, nil, nil, avatar.NewService(store))

	sid, phase, _, err := m.Start(ctx, "ghost-persona", "", "旧路径问候")
	if !errors.Is(err, ErrResolveFailed) {
		t.Fatalf("Resolve 失败必须显式拒绝（ErrResolveFailed）: phase=%s err=%v", phase, err)
	}
	if sid != "" {
		t.Fatalf("拒绝后不得产生 session: sid=%q", sid)
	}
	if snap := m.Snapshot(); snap.Phase != PhaseIdle || snap.SessionID != "" {
		t.Fatalf("不得有场次残留: %+v", snap)
	}
}

// TestStartOptsWithoutResolver resolver 为 nil（旧装配）：仅按入参构造 opts。
func TestStartOptsWithoutResolver(t *testing.T) {
	ctrl := NewFakeController(quietLogger())
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), ctrl)
	if _, phase, _, err := m.Start(context.Background(), "p", "", "hi"); err != nil || phase != PhaseLive {
		t.Fatalf("start: phase=%s err=%v", phase, err)
	}
	opts := ctrl.LastOpts()
	if opts.Greeting != "hi" || opts.VoiceID != "" {
		t.Fatalf("nil resolver opts: %+v", opts)
	}
}

// TestStartOptsRecordingEnabledFromSettings F34 遗留接线：misc.record_enabled=开
// 的场在建场时带 StartOpts.RecordingEnabled=true（与 yaml 常开任一成立即录）。
func TestStartOptsRecordingEnabledFromSettings(t *testing.T) {
	ctrl := NewFakeController(quietLogger())
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), ctrl)
	m.SetRecordingFlagFn(func() bool { return true })
	if _, phase, _, err := m.Start(context.Background(), "p", "", ""); err != nil || phase != PhaseLive {
		t.Fatalf("start: phase=%s err=%v", phase, err)
	}
	if !ctrl.LastOpts().RecordingEnabled {
		t.Fatal("record_enabled=开 必须落入 StartOpts.RecordingEnabled")
	}
}

// TestStartOptsRecordingDefaultOff 未装配/关：StartOpts.RecordingEnabled 保持 false。
func TestStartOptsRecordingDefaultOff(t *testing.T) {
	ctrl := NewFakeController(quietLogger())
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), ctrl)
	m.SetRecordingFlagFn(func() bool { return false })
	if _, phase, _, err := m.Start(context.Background(), "p", "", ""); err != nil || phase != PhaseLive {
		t.Fatalf("start: phase=%s err=%v", phase, err)
	}
	if ctrl.LastOpts().RecordingEnabled {
		t.Fatal("record_enabled=关 不得开启录制")
	}
}
