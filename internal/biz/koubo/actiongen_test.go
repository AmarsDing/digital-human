package koubo

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/choreo"
	"github.com/teamwork/digital-human/internal/llm"
)

func actionTestStore() *choreo.MemoryStore {
	return choreo.NewMemoryStore(choreo.Config{Groups: []choreo.Group{
		{ID: "welcome", Name: "欢迎观众", Order: "order", Steps: []choreo.Step{{Cmd: "挥手"}, {Cmd: "微笑"}}},
		{ID: "thanks", Name: "感谢礼物", Order: "order", Steps: []choreo.Step{{Cmd: "鞠躬"}, {Cmd: "比心"}}},
	}})
}

func TestRuleActionGenUsesTextAndChoreoDeterministically(t *testing.T) {
	gen := NewRuleActionGen(actionTestStore())
	var first string
	for i := 0; i < 3; i++ {
		got, err := gen.Generate(context.Background(), "谢谢大家送来的礼物")
		if err != nil {
			t.Fatal(err)
		}
		if got != "鞠躬 -> 比心" {
			t.Fatalf("口播文本未映射到感谢动作组: %q", got)
		}
		if i > 0 && got != first {
			t.Fatalf("同一输入不得随机变化: first=%q got=%q", first, got)
		}
		first = got
	}
	got, err := gen.Generate(context.Background(), "没有命中关键词的普通口播")
	if err != nil {
		t.Fatal(err)
	}
	if got != "挥手 -> 微笑" {
		t.Fatalf("未命中时应稳定使用首个动作组: %q", got)
	}
}

func TestRuleActionGenWithoutChoreoFailsClosed(t *testing.T) {
	gen := NewRuleActionGen(choreo.NewMemoryStore(choreo.Config{}))
	if _, err := gen.Generate(context.Background(), "口播"); err == nil {
		t.Fatal("choreo 没有动作时不得伪造默认动作")
	}
}

type actionStreamStub struct {
	text string
	err  error
	msgs [][]llm.Message
}

func (s *actionStreamStub) Stream(_ context.Context, msgs []llm.Message) (<-chan llm.Sentence, error) {
	s.msgs = append(s.msgs, append([]llm.Message(nil), msgs...))
	if s.err != nil {
		return nil, s.err
	}
	ch := make(chan llm.Sentence, 2)
	ch <- llm.Sentence{Text: s.text}
	close(ch)
	return ch, nil
}

func TestLLMActionGenAcceptsOnlyChoreoCommands(t *testing.T) {
	streamer := &actionStreamStub{text: "鞠躬 -> 比心"}
	gen := NewLLMActionGen(actionTestStore(), streamer)
	got, err := gen.Generate(context.Background(), "感谢大家支持")
	if err != nil {
		t.Fatal(err)
	}
	if got != "鞠躬 -> 比心" {
		t.Fatalf("LLM 动作计划=%q", got)
	}
	if len(streamer.msgs) != 1 {
		t.Fatalf("配置 LLM 后必须实际调用 LLM: %d", len(streamer.msgs))
	}
	prompt := streamer.msgs[0][0].Content + streamer.msgs[0][1].Content
	if !strings.Contains(prompt, "感谢大家支持") || !strings.Contains(prompt, "鞠躬") {
		t.Fatalf("提示词缺少口播文本或 choreo 动作: %q", prompt)
	}
}

func TestLLMActionGenRejectsUnknownCommandAndStreamFailure(t *testing.T) {
	gen := NewLLMActionGen(actionTestStore(), &actionStreamStub{text: "凭空跳跃"})
	if _, err := gen.Generate(context.Background(), "跳一下"); err == nil {
		t.Fatal("LLM 返回 choreo 外动作必须失败")
	}

	streamErr := errors.New("llm unreachable")
	gen = NewLLMActionGen(actionTestStore(), &actionStreamStub{err: streamErr})
	if _, err := gen.Generate(context.Background(), "感谢大家"); !errors.Is(err, streamErr) {
		t.Fatalf("LLM 失败不得回退伪装成功: %v", err)
	}
}
