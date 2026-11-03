package chat

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/teamwork/digital-human/internal/llm"
)

// fakeStreamer fake LLM provider：按预设逐句吐出，可携带终止错误；记录收到的 messages。
type fakeStreamer struct {
	sentences []string
	err       error
	got       [][]llm.Message
}

func (f *fakeStreamer) Stream(_ context.Context, msgs []llm.Message) (<-chan llm.Sentence, error) {
	f.got = append(f.got, append([]llm.Message(nil), msgs...))
	ch := make(chan llm.Sentence, len(f.sentences)+1)
	for _, s := range f.sentences {
		ch <- llm.Sentence{Text: s}
	}
	if f.err != nil {
		ch <- llm.Sentence{Err: f.err}
	}
	close(ch)
	return ch, nil
}

type fakeResolver struct{ prompt string }

func (r fakeResolver) SystemPrompt(context.Context) string { return r.prompt }

func collect() (SentenceSink, *[]string) {
	got := &[]string{}
	return func(_ context.Context, text string) error {
		*got = append(*got, text)
		return nil
	}, got
}

// merge_for_tts：短句攒到首块阈值 14 字才发 TTS；尾块 flush 兜底（蓝本 llm.py:99）。
func TestChatMergeForTTS(t *testing.T) {
	fs := &fakeStreamer{sentences: []string{"你好。", "今天天气", "真不错呢。"}}
	m := NewManager(fs, nil)
	sink, got := collect()
	reply, err := m.Chat(context.Background(), "k", "在吗", sink)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "你好。今天天气真不错呢。" {
		t.Fatalf("reply = %q", reply)
	}
	if len(*got) != 1 || (*got)[0] != "你好。今天天气真不错呢。" {
		t.Fatalf("tts chunks = %q", *got)
	}
}

// 首块攒够 14 字立即出（不拖慢开口）；之后每块攒 30 字（蓝本 first_min=14/min_chars=30）。
func TestChatMergeTwoTierThreshold(t *testing.T) {
	s1 := strings.Repeat("一", 13) + "。" // 14 字 → 首块立即出
	s2 := strings.Repeat("二", 10) + "。" // 11 字
	s3 := strings.Repeat("三", 19) + "。" // 累计 31 字 ≥30 → 第二块出
	s4 := strings.Repeat("尾", 5)        // 不足 30 → flush
	fs := &fakeStreamer{sentences: []string{s1, s2, s3, s4}}
	m := NewManager(fs, nil)
	sink, got := collect()
	if _, err := m.Chat(context.Background(), "k", "hi", sink); err != nil {
		t.Fatal(err)
	}
	want := []string{s1, s2 + s3, s4}
	if strings.Join(*got, "|") != strings.Join(want, "|") {
		t.Fatalf("tts chunks = %q, want %q", *got, want)
	}
}

// v3 约束：system prompt 只经 ProfileResolver；nil 时无 system 消息。
func TestChatSystemPromptFromResolverOnly(t *testing.T) {
	fs := &fakeStreamer{sentences: []string{"好。"}}
	m := NewManager(fs, fakeResolver{prompt: "你是小雅"})
	sink, _ := collect()
	if _, err := m.Chat(context.Background(), "k", "hi", sink); err != nil {
		t.Fatal(err)
	}
	msgs := fs.got[0]
	if len(msgs) != 2 || msgs[0].Role != "system" || msgs[0].Content != "你是小雅" {
		t.Fatalf("messages = %+v", msgs)
	}

	fs2 := &fakeStreamer{sentences: []string{"好。"}}
	m2 := NewManager(fs2, nil)
	sink2, _ := collect()
	if _, err := m2.Chat(context.Background(), "k", "hi", sink2); err != nil {
		t.Fatal(err)
	}
	if fs2.got[0][0].Role == "system" {
		t.Fatalf("nil resolver 不应注入 system: %+v", fs2.got[0])
	}
}

// 会话记忆：按 key 独立；窗口按轮截断；历史随请求带给 LLM。
func TestChatMemoryWindowAndIsolation(t *testing.T) {
	fs := &fakeStreamer{sentences: []string{"答。"}}
	m := NewManager(fs, nil, WithMaxHistoryTurns(2))
	sink, _ := collect()
	ctx := context.Background()
	for _, q := range []string{"问1", "问2", "问3", "问4"} {
		if _, err := m.Chat(ctx, "a", q, sink); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := m.Chat(ctx, "b", "别的会话", sink); err != nil {
		t.Fatal(err)
	}
	// 会话 a 第 4 轮只带最近 2 轮历史（4 条）+ 本轮 user。
	msgsA := fs.got[3]
	if len(msgsA) != 5 || msgsA[0].Content != "问2" || msgsA[4].Content != "问4" {
		t.Fatalf("会话 a 窗口错误: %+v", msgsA)
	}
	// 会话 b 独立：无历史。
	msgsB := fs.got[4]
	if len(msgsB) != 1 || msgsB[0].Content != "别的会话" {
		t.Fatalf("会话 b 应独立: %+v", msgsB)
	}
}

// ResetHistory 返回真实清除轮数（G07 接线语义）。
func TestResetHistoryReturnsTurns(t *testing.T) {
	fs := &fakeStreamer{sentences: []string{"答。"}}
	m := NewManager(fs, nil)
	sink, _ := collect()
	ctx := context.Background()
	_, _ = m.Chat(ctx, "k", "问1", sink)
	_, _ = m.Chat(ctx, "k", "问2", sink)
	if n := m.ResetHistory("k"); n != 2 {
		t.Fatalf("cleared = %d, want 2", n)
	}
	if n := m.ResetHistory("k"); n != 0 {
		t.Fatalf("重复清空应为 0, got %d", n)
	}
	if n := m.ResetHistory("不存在"); n != 0 {
		t.Fatalf("未知 key 应为 0, got %d", n)
	}
	// 清空后下一轮不带旧历史。
	_, _ = m.Chat(ctx, "k", "问3", sink)
	if msgs := fs.got[len(fs.got)-1]; len(msgs) != 1 {
		t.Fatalf("清空后仍带历史: %+v", msgs)
	}
}

// 流式失败：错误透传且不写入记忆。
func TestChatStreamErrorNoHistory(t *testing.T) {
	fs := &fakeStreamer{sentences: []string{"半句"}, err: errors.New("llm: 全部模型失败")}
	m := NewManager(fs, nil)
	sink, got := collect()
	if _, err := m.Chat(context.Background(), "k", "hi", sink); err == nil {
		t.Fatal("必须返回流式错误")
	}
	if len(*got) != 0 { // "半句" 不足首块阈值，flush 被错误打断
		t.Fatalf("失败不应产出 TTS: %q", *got)
	}
	if n := m.ResetHistory("k"); n != 0 {
		t.Fatalf("失败轮不应写入记忆, got %d", n)
	}
}

// sink 失败：错误透传、不写入记忆、且抽干通道不阻塞。
func TestChatSinkError(t *testing.T) {
	fs := &fakeStreamer{sentences: []string{strings.Repeat("一", 13) + "。", "二。"}}
	m := NewManager(fs, nil)
	boom := errors.New("tts down")
	sink := func(context.Context, string) error { return boom }
	if _, err := m.Chat(context.Background(), "k", "hi", sink); !errors.Is(err, boom) {
		t.Fatalf("err = %v, want %v", err, boom)
	}
	if n := m.ResetHistory("k"); n != 0 {
		t.Fatalf("sink 失败轮不应写入记忆, got %d", n)
	}
}

// strip_think：思维链不写入记忆（蓝本 llm.py:280）。
func TestChatStripThinkInHistory(t *testing.T) {
	fs := &fakeStreamer{sentences: []string{"<think>推理</think>正式答复。"}}
	m := NewManager(fs, nil)
	sink, _ := collect()
	reply, err := m.Chat(context.Background(), "k", "hi", sink)
	if err != nil {
		t.Fatal(err)
	}
	if reply != "正式答复。" {
		t.Fatalf("reply = %q", reply)
	}
}
