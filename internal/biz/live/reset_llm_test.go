package live

import (
	"context"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/chat"
	"github.com/teamwork/digital-human/internal/llm"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

type llmFakeStreamer struct{ sentences []string }

func (f *llmFakeStreamer) Stream(_ context.Context, _ []llm.Message) (<-chan llm.Sentence, error) {
	ch := make(chan llm.Sentence, len(f.sentences))
	for _, s := range f.sentences {
		ch <- llm.Sentence{Text: s}
	}
	close(ch)
	return ch, nil
}

// G07：ResetLlm 真实清 chat 记忆并返回清除轮数（蓝本 bridge.py:536 → llm.reset_history）。
func TestResetLlmClearsChatMemory(t *testing.T) {
	ctx := context.Background()
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), &recController{})
	chatMgr := chat.NewManager(&llmFakeStreamer{sentences: []string{"好。"}}, nil)
	m.SetLlmMemory(chatMgr)

	// 无场次：无记忆可清，如实返回 0。
	if n := m.ResetLlm(ctx); n != 0 {
		t.Fatalf("idle reset = %d, want 0", n)
	}

	sid, phase, _, err := m.Start(ctx, "p", "a", "")
	if err != nil || phase != PhaseLive {
		t.Fatalf("start: phase=%s err=%v", phase, err)
	}
	// 模拟弹幕与操作员口播分别以场次 id 和 operator key 各攒 2 轮记忆。
	sink := func(context.Context, string) error { return nil }
	for i := 0; i < 2; i++ {
		if _, err := chatMgr.Chat(ctx, sid, "hi", sink); err != nil {
			t.Fatal(err)
		}
		if _, err := chatMgr.Chat(ctx, sid+":operator", "voice", sink); err != nil {
			t.Fatal(err)
		}
	}
	if n := m.ResetLlm(ctx); n != 4 {
		t.Fatalf("reset turns = %d, want 4", n)
	}
	for _, key := range []string{sid, sid + ":operator"} {
		if n := chatMgr.ResetHistory(key); n != 0 {
			t.Fatalf("记忆 %q 未清空, 残留 %d 轮", key, n)
		}
	}
}

// 未装配记忆端口时如实返回 0（不假装清过）。
func TestResetLlmWithoutMemory(t *testing.T) {
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), &recController{})
	if _, _, _, err := m.Start(context.Background(), "p", "a", ""); err != nil {
		t.Fatal(err)
	}
	if n := m.ResetLlm(context.Background()); n != 0 {
		t.Fatalf("reset = %d, want 0", n)
	}
}
