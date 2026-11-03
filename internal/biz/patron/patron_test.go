package patron

import (
	"context"
	"strings"
	"sync"
	"testing"
)

type fakeSettings struct{ groups map[string]map[string]any }

func (f fakeSettings) Value(_ context.Context, group, key string) any {
	if g, ok := f.groups[group]; ok {
		return g[key]
	}
	return nil
}

type fakeSpeaker struct {
	mu   sync.Mutex
	said []string
}

func (s *fakeSpeaker) Say(_ context.Context, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.said = append(s.said, text)
	return nil
}

func (s *fakeSpeaker) lines() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.said...)
}

func memberOn(extra map[string]any) fakeSettings {
	g := map[string]any{
		"enabled": true, "forward_joins": true, "once_per_session": true,
		"welcome_template": "诶，{nick}来啦，欢迎欢迎。",
	}
	for k, v := range extra {
		g[k] = v
	}
	return fakeSettings{groups: map[string]map[string]any{"member": g}}
}

// F29：名单 CRUD（nick 去重、trim、空 nick 丢弃），IDs 分 nicks/uids，IsPatron 双键命中。
func TestService_SaveListIDs(t *testing.T) {
	svc := NewService(nil, memberOn(nil), nil, nil)
	ctx := context.Background()
	n, err := svc.Save(ctx, []Entry{
		{Nick: " 大哥 ", UID: "u1", Alias: "哥", Note: "榜一", Since: "2026-11-03"},
		{Nick: "大哥"}, // 重复 nick 丢弃
		{Nick: "", UID: "x"}, {Nick: "二姐"},
	})
	if err != nil || n != 2 {
		t.Fatalf("save n=%d err=%v want 2", n, err)
	}
	nicks, uids, err := svc.IDs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(nicks) != 2 || nicks[0] != "大哥" || nicks[1] != "二姐" {
		t.Fatalf("nicks=%v", nicks)
	}
	if len(uids) != 1 || uids[0] != "u1" {
		t.Fatalf("uids=%v", uids)
	}
	if !svc.IsPatron(ctx, "大哥") || !svc.IsPatron(ctx, "u1") || svc.IsPatron(ctx, "路人") {
		t.Fatal("IsPatron 命中异常")
	}
}

// G24：进场欢迎（名单内 + 开播）→ 模板渲染 Say；once_per_session 每场一次；
// 非贵宾/未开播不欢迎；新场次重置。
func TestService_JoinWelcome(t *testing.T) {
	sp := &fakeSpeaker{}
	svc := NewService(nil, memberOn(nil), sp, nil)
	ctx := context.Background()
	if _, err := svc.Save(ctx, []Entry{{Nick: "大哥", UID: "u1"}}); err != nil {
		t.Fatal(err)
	}
	// 未开播：不欢迎。
	if _, ok := svc.OnJoin(ctx, "大哥", "", ""); ok {
		t.Fatal("未开播不应欢迎")
	}
	svc.OnSessionStart(ctx, "s1")
	// uid 命中解析为名单 nick 欢迎。
	line, ok := svc.OnJoin(ctx, "", "u1", "")
	if !ok || !strings.Contains(line, "大哥") {
		t.Fatalf("uid 命中应解析 nick 欢迎: line=%q ok=%v", line, ok)
	}
	// 同人换 nick 进场：once_per_session 按名单 nick 去重，不再重复欢迎。
	if _, ok := svc.OnJoin(ctx, "大哥", "", ""); ok {
		t.Fatal("once_per_session 应拦截（uid/nick 同人）")
	}
	// 非贵宾不欢迎。
	if _, ok := svc.OnJoin(ctx, "路人", "", ""); ok {
		t.Fatal("非贵宾不应欢迎")
	}
	if got := svc.State(ctx)["welcomed"]; got != 1 {
		t.Fatalf("welcomed=%v want 1", got)
	}
	// 新场次重置。
	svc.OnSessionStop(ctx, "s1")
	svc.OnSessionStart(ctx, "s2")
	if _, ok := svc.OnJoin(ctx, "大哥", "", ""); !ok {
		t.Fatal("新场次应可再次欢迎")
	}
	svc.OnSessionStop(ctx, "s2")
	if len(sp.lines()) != 2 {
		t.Fatalf("said=%v want 2", sp.lines())
	}
}

// G24：forward_joins 关闭或 member.enabled 关闭时 join 不欢迎；
// /welcome 强制口（不查名单）仍受 once_per_session 与开播闸约束。
func TestService_WelcomeForceAndSwitches(t *testing.T) {
	sp := &fakeSpeaker{}
	svc := NewService(nil, memberOn(map[string]any{"forward_joins": false}), sp, nil)
	ctx := context.Background()
	_, _ = svc.Save(ctx, []Entry{{Nick: "大哥"}})
	svc.OnSessionStart(ctx, "s1")
	defer svc.OnSessionStop(ctx, "s1")
	if _, ok := svc.OnJoin(ctx, "大哥", "", ""); ok {
		t.Fatal("forward_joins=false 不应欢迎")
	}
	// 强制欢迎（不要求在名单内）。
	line, ok := svc.Welcome(ctx, "路人", "")
	if !ok || !strings.Contains(line, "路人") {
		t.Fatalf("force welcome line=%q ok=%v", line, ok)
	}
	if _, ok := svc.Welcome(ctx, "路人", ""); ok {
		t.Fatal("once_per_session 对强制口同样生效（对齐蓝本）")
	}
}

// 模板不含 nick 时回退兜底（蓝本 welcome_patron 语义：欢迎语必须含名字）。
func TestService_WelcomeTemplateFallback(t *testing.T) {
	sp := &fakeSpeaker{}
	svc := NewService(nil, memberOn(map[string]any{"welcome_template": "欢迎光临！"}), sp, nil)
	ctx := context.Background()
	svc.OnSessionStart(ctx, "s1")
	defer svc.OnSessionStop(ctx, "s1")
	line, ok := svc.Welcome(ctx, "大哥", "")
	if !ok || !strings.Contains(line, "大哥") {
		t.Fatalf("兜底应含 nick: %q", line)
	}
}
