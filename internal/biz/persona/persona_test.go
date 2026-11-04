package persona

import (
	"context"
	"errors"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/chat"
	"github.com/teamwork/digital-human/internal/biz/settings"
	"github.com/teamwork/digital-human/internal/llm"
)

func mustSave(t *testing.T, s *Service, id, prompt string) Persona {
	t.Helper()
	p, err := s.Save(context.Background(), id, SaveInput{SystemPrompt: prompt})
	if err != nil {
		t.Fatalf("save %s: %v", id, err)
	}
	return p
}

// 蓝本 personas.save 语义：id 归一小写、name 缺省回退 id、prompt 必填、id 非法 400。
func TestMemoryCRUD(t *testing.T) {
	svc := NewService(NewMemoryStore())
	ctx := context.Background()

	p := mustSave(t, svc, "XiaoYa", "你是小雅")
	if p.ID != "xiaoya" || p.Name != "xiaoya" {
		t.Fatalf("id 归一/name 回退失败: %+v", p)
	}
	mustSave(t, svc, "ab", "你是阿北")

	items, active, err := svc.List(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].ID != "ab" || items[1].ID != "xiaoya" {
		t.Fatalf("list = %+v", items)
	}
	if active != "" {
		t.Fatalf("新建不应自动 active: %q", active)
	}

	if _, err := svc.Save(ctx, "bad id!", SaveInput{SystemPrompt: "x"}); !errors.Is(err, ErrInvalidID) {
		t.Fatalf("非法 id err = %v", err)
	}
	if _, err := svc.Save(ctx, "ok", SaveInput{SystemPrompt: "  "}); !errors.Is(err, ErrEmptyPrompt) {
		t.Fatalf("空 prompt err = %v", err)
	}
	if err := svc.Delete(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("删除不存在 err = %v", err)
	}
	if _, err := svc.Select(ctx, "ghost"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("切换不存在 err = %v", err)
	}
}

// select 排他置位；覆盖保存保留 is_active；删 active 409（蓝本 bridge.py:1135）。
func TestSelectDeleteGuards(t *testing.T) {
	svc := NewService(NewMemoryStore())
	ctx := context.Background()
	mustSave(t, svc, "a", "A")
	mustSave(t, svc, "b", "B")

	if _, err := svc.Select(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if _, active, _ := svc.List(ctx); active != "a" {
		t.Fatalf("active = %q", active)
	}
	if err := svc.Delete(ctx, "a"); !errors.Is(err, ErrActiveConflict) {
		t.Fatalf("删 active err = %v", err)
	}

	// 覆盖保存保留 is_active（蓝本文件语义下 active 存在 config，不被 save 覆盖）。
	if _, err := svc.Save(ctx, "a", SaveInput{SystemPrompt: "A2", Tagline: "新"}); err != nil {
		t.Fatal(err)
	}
	if got := svc.SystemPrompt(ctx); got != "A2" {
		t.Fatalf("覆盖后 active prompt = %q", got)
	}

	if _, err := svc.Select(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if err := svc.Delete(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	items, active, _ := svc.List(ctx)
	if len(items) != 1 || active != "b" {
		t.Fatalf("items=%+v active=%q", items, active)
	}
}

// storage 标识 + ProfileResolver 空值容忍：无 active、active prompt 为空（数据异常）均返回 ""。
func TestStorageAndResolverTolerant(t *testing.T) {
	svc := NewService(NewMemoryStore())
	if svc.Storage() != "memory" {
		t.Fatalf("storage = %q", svc.Storage())
	}
	if got := svc.SystemPrompt(context.Background()); got != "" {
		t.Fatalf("无 active 应空串, got %q", got)
	}
	// 直接写底层 store 构造空 prompt active（绕过 Save 校验）。
	mem := NewMemoryStore()
	if err := mem.Save(context.Background(), Persona{ID: "x", Slug: "x", Name: "x", IsActive: true}); err != nil {
		t.Fatal(err)
	}
	if got := NewService(mem).SystemPrompt(context.Background()); got != "" {
		t.Fatalf("空 prompt 应空串, got %q", got)
	}
}

// G04 联动（真实 chat.Manager）：人设作为 resolver 注入；对话产生记忆后 select 新的人设
// 并 ResetHistory，断言记忆真实清空且新 system prompt 来自新 active（G05 单一来源）。
func TestSelectThenChatReset(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	mustSave(t, svc, "a", "你是A")
	mustSave(t, svc, "b", "你是B")
	if _, err := svc.Select(ctx, "a"); err != nil {
		t.Fatal(err)
	}

	fs := &fakeStreamer{sentences: []string{"好。"}}
	m := chat.NewManager(fs, svc)
	if _, err := m.Chat(ctx, "s1", "在吗", func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	// system 来自 active 人设 a（v3 唯一真相源）。
	if fs.got[0][0].Role != "system" || fs.got[0][0].Content != "你是A" {
		t.Fatalf("messages = %+v", fs.got[0])
	}
	if n := m.ResetHistory("s1"); n != 1 {
		t.Fatalf("reset turns = %d, want 1", n)
	}
	if n := m.ResetHistory("s1"); n != 0 {
		t.Fatalf("二次 reset turns = %d, want 0", n)
	}

	// 切到 b 后 resolver 立即生效（无需重启）。
	if _, err := svc.Select(ctx, "b"); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Chat(ctx, "s1", "还在吗", func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	msgs := fs.got[1]
	if len(msgs) != 2 || msgs[0].Content != "你是B" {
		t.Fatalf("切换后 messages = %+v", msgs)
	}
}

type fakeStreamer struct {
	sentences []string
	got       [][]llm.Message
}

func (f *fakeStreamer) Stream(_ context.Context, msgs []llm.Message) (<-chan llm.Sentence, error) {
	f.got = append(f.got, msgs)
	ch := make(chan llm.Sentence, len(f.sentences))
	for _, s := range f.sentences {
		ch <- llm.Sentence{Text: s}
	}
	close(ch)
	return ch, nil
}

// TestSystemPromptSingleSource A23 人设单源：settings llm.system 改任何值都不进
// LLM 提示词；chat 系统提示词只来自档案；改档案 system_prompt 立即生效。
func TestSystemPromptSingleSource(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	mustSave(t, svc, "p1", "档案设定v1")
	if _, err := svc.Select(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	// 未装配第二来源时保持兼容：settings 不会隐式进入 prompt。
	st := settings.NewService(settings.NewMemoryStore())
	if err := st.Patch(ctx, "llm", map[string]any{"system": "后台设定-不得生效"}); err != nil {
		t.Fatal(err)
	}
	fs := &fakeStreamer{sentences: []string{"嗯"}}
	m := chat.NewManager(fs, svc)
	if _, err := m.Chat(ctx, "s1", "在吗", func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(fs.got) != 1 || fs.got[0][0].Role != "system" || fs.got[0][0].Content != "档案设定v1" {
		t.Fatalf("未装配第二来源时必须只来自档案: %+v", fs.got)
	}
	// 改档案 system_prompt → chat 立即生效。
	mustSave(t, svc, "p1", "档案设定v2")
	fs2 := &fakeStreamer{sentences: []string{"嗯"}}
	m2 := chat.NewManager(fs2, svc)
	if _, err := m2.Chat(ctx, "s2", "在吗", func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if len(fs2.got) != 1 || fs2.got[0][0].Content != "档案设定v2" {
		t.Fatalf("档案 prompt 修改必须生效: %+v", fs2.got)
	}
}

func TestMergeSystemPrompts(t *testing.T) {
	tests := []struct {
		name      string
		primary   string
		secondary string
		want      string
	}{
		{name: "双来源固定人设优先", primary: "人设规则", secondary: "全局规则", want: "人设规则\n\n全局规则"},
		{name: "仅人设", primary: " 人设规则\n", want: "人设规则"},
		{name: "人设空回退全局", secondary: " 全局规则\n", want: "全局规则"},
		{name: "修剪后相同去重", primary: " 相同规则\n", secondary: "相同规则", want: "相同规则"},
		{name: "全空不拼分隔符", primary: " \n", secondary: "\t", want: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MergeSystemPrompts(tt.primary, tt.secondary); got != tt.want {
				t.Fatalf("MergeSystemPrompts(%q, %q) = %q, want %q", tt.primary, tt.secondary, got, tt.want)
			}
		})
	}
}

func TestSystemPromptUsesConfiguredSecondarySource(t *testing.T) {
	ctx := context.Background()
	st := settings.NewService(settings.NewMemoryStore())
	if err := st.Patch(ctx, "llm", map[string]any{"system": "全局规则"}); err != nil {
		t.Fatal(err)
	}
	svc := NewService(NewMemoryStore())
	svc.SetPromptFallback(func(ctx context.Context) string {
		v, _ := st.Value(ctx, "llm", "system").(string)
		return v
	})
	if got := svc.SystemPrompt(ctx); got != "全局规则" {
		t.Fatalf("无 active 时应使用第二来源, got %q", got)
	}
	mustSave(t, svc, "p1", "人设规则")
	if _, err := svc.Select(ctx, "p1"); err != nil {
		t.Fatal(err)
	}
	if got := svc.SystemPrompt(ctx); got != "人设规则\n\n全局规则" {
		t.Fatalf("双来源合并 = %q", got)
	}
}
