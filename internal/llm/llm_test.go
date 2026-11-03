package llm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// collect 读完整条流，返回文本句列表与终止错误（无终止错误为 nil）。
func collect(ch <-chan Sentence) ([]string, error) {
	var out []string
	for s := range ch {
		if s.Err != nil {
			return out, s.Err
		}
		out = append(out, s.Text)
	}
	return out, nil
}

// sseHandler 把 pieces 逐条写成 SSE delta，最后发 [DONE]。
func sseHandler(pieces []string) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fl, _ := w.(http.Flusher)
		for _, p := range pieces {
			payload, _ := json.Marshal(map[string]any{
				"choices": []any{map[string]any{"delta": map[string]any{"content": p}}},
			})
			fmt.Fprintf(w, "data: %s\n\n", payload)
			if fl != nil {
				fl.Flush()
			}
		}
		fmt.Fprint(w, "data: [DONE]\n\n")
		if fl != nil {
			fl.Flush()
		}
	}
}

func cfgFor(srv *httptest.Server, model string, fallbacks ...string) Config {
	return Config{
		BaseURL:        srv.URL,
		APIKey:         "sk-test-key",
		Model:          model,
		FallbackModels: fallbacks,
		Temperature:    0.7,
		HeaderTimeout:  2 * time.Second,
	}
}

// 逐句产出：强停顿立即分句，弱停顿需攒够 soft_min（蓝本 SentenceStream 语义）。
func TestStreamSentencesPunctuationBoundaries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(sseHandler(
		[]string{"你好。", "谢谢，", "不客气！"},
	)))
	defer srv.Close()
	c := NewStaticClient(cfgFor(srv, "deepseek-chat"))
	ch, err := c.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := collect(ch)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"你好。", "谢谢，不客气！"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("sentences = %q, want %q", got, want)
	}
}

// 弱停顿：逗号前积累不足 10 字不切，够 10 字才切（蓝本 soft_min=10）。
func TestStreamSoftCutNeedsMinChars(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(sseHandler(
		[]string{"一二三四五六七八九十，", "还有下文。"},
	)))
	defer srv.Close()
	c := NewStaticClient(cfgFor(srv, "m"))
	ch, _ := c.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}})
	got, err := collect(ch)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"一二三四五六七八九十，", "还有下文。"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("sentences = %q, want %q", got, want)
	}
}

// 无标点长串兜底：超过 hard_max(45) 强制放出，flush 收尾吐出尾巴。
func TestStreamHardMaxFallbackAndFlush(t *testing.T) {
	long := strings.Repeat("啊", 50)
	srv := httptest.NewServer(http.HandlerFunc(sseHandler([]string{long, "收尾"})))
	defer srv.Close()
	c := NewStaticClient(cfgFor(srv, "m"))
	ch, _ := c.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}})
	got, err := collect(ch)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("sentences = %d, want 2: %q", len(got), got)
	}
	if n := len([]rune(got[0])); n != 45 {
		t.Fatalf("hard-max piece = %d runes, want 45", n)
	}
	if got[1] != "啊啊啊啊啊收尾" {
		t.Fatalf("flush tail = %q", got[1])
	}
}

// 请求形态：POST /chat/completions、Bearer 头、model/stream/temperature 字段。
func TestStreamRequestShape(t *testing.T) {
	var gotAuth, gotPath string
	var gotBody chatRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		sseHandler([]string{"好。"})(w, r)
	}))
	defer srv.Close()
	c := NewStaticClient(cfgFor(srv, "deepseek-chat"))
	ch, err := c.Stream(context.Background(), []Message{
		{Role: "system", Content: "s"}, {Role: "user", Content: "u"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := collect(ch); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/chat/completions" || gotAuth != "Bearer sk-test-key" {
		t.Fatalf("path=%q auth=%q", gotPath, gotAuth)
	}
	if gotBody.Model != "deepseek-chat" || !gotBody.Stream || gotBody.Temperature != 0.7 {
		t.Fatalf("body = %+v", gotBody)
	}
	if len(gotBody.Messages) != 2 || gotBody.Messages[0].Role != "system" {
		t.Fatalf("messages = %+v", gotBody.Messages)
	}
}

// 重试降级（蓝本 llm.py:214-279 / G06）：主模型 503 重试 2 次后降级 fallback 成功。
func TestStreamRetryThenFallback(t *testing.T) {
	var models []string
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body chatRequest
		_ = json.NewDecoder(r.Body).Decode(&body)
		models = append(models, body.Model)
		atomic.AddInt64(&calls, 1)
		if body.Model == "primary" {
			w.WriteHeader(http.StatusServiceUnavailable)
			fmt.Fprint(w, `{"error":"Service is too busy"}`)
			return
		}
		sseHandler([]string{"降级成功。"})(w, r)
	}))
	defer srv.Close()

	c := NewStaticClient(cfgFor(srv, "primary", "fb1"))
	c.backoff = func(context.Context, time.Duration) bool { return true }
	ch, _ := c.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}})
	got, err := collect(ch)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "降级成功。" {
		t.Fatalf("sentences = %q", got)
	}
	want := []string{"primary", "primary", "fb1"}
	if strings.Join(models, ",") != strings.Join(want, ",") {
		t.Fatalf("model sequence = %v, want %v", models, want)
	}
}

// 全部失败：主模型 + fallback 各 2 次都 503 → 终止错误含 lastErr 状态码。
func TestStreamAllModelsFail(t *testing.T) {
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&calls, 1)
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, "busy")
	}))
	defer srv.Close()
	c := NewStaticClient(cfgFor(srv, "primary", "fb1", "fb2"))
	c.backoff = func(context.Context, time.Duration) bool { return true }
	ch, _ := c.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}})
	got, err := collect(ch)
	if err == nil {
		t.Fatal("全部失败必须返回错误")
	}
	if len(got) != 0 {
		t.Fatalf("不应产出句子: %q", got)
	}
	var he *httpError
	if !errors.As(err, &he) || he.status != 503 {
		t.Fatalf("err 应含 lastErr(503): %v", err)
	}
	if calls != 6 { // (1 主 + 2 fallback) × 2 次
		t.Fatalf("calls = %d, want 6", calls)
	}
}

// 非可重试错误（400）立即失败，不重试不降级。
func TestStreamNonRetryableFailsFast(t *testing.T) {
	var calls int64
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		atomic.AddInt64(&calls, 1)
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":"bad request"}`)
	}))
	defer srv.Close()
	c := NewStaticClient(cfgFor(srv, "primary", "fb1"))
	c.backoff = func(context.Context, time.Duration) bool { return true }
	ch, _ := c.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}})
	_, err := collect(ch)
	var he *httpError
	if !errors.As(err, &he) || he.status != 400 {
		t.Fatalf("err = %v, want http 400", err)
	}
	if calls != 1 {
		t.Fatalf("calls = %d, want 1（不重试）", calls)
	}
}

// fail-closed：未配置 api_key 同步返回 ErrLLMNotConfigured。
func TestStreamNotConfigured(t *testing.T) {
	c := NewStaticClient(Config{Model: "m"})
	if _, err := c.Stream(context.Background(), []Message{{Role: "user", Content: "hi"}}); !errors.Is(err, ErrLLMNotConfigured) {
		t.Fatalf("err = %v, want ErrLLMNotConfigured", err)
	}
}

// ctx 取消传播：服务端挂起，取消 ctx 后通道以 ctx.Err 终止并关闭。
func TestStreamContextCancel(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if fl, ok := w.(http.Flusher); ok {
			fl.Flush()
		}
		<-r.Context().Done()
	}))
	defer srv.Close()
	c := NewStaticClient(cfgFor(srv, "m"))
	ctx, cancel := context.WithCancel(context.Background())
	ch, err := c.Stream(ctx, []Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	cancel()
	_, err = collect(ch)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

// 分句器单测：软切回退到空格（蓝本 llm.py:74-83 兜底路径）。
func TestSentenceSplitterFallbackToSpace(t *testing.T) {
	sp := NewSentenceSplitter()
	// 46 字无标点，但第 10 字后有一个空格 → 回退到空格切。
	in := strings.Repeat("字", 10) + " " + strings.Repeat("字", 40)
	out := sp.Feed(in)
	if len(out) != 1 || out[0] != strings.Repeat("字", 10) {
		t.Fatalf("fallback piece = %q", out)
	}
	if tail := sp.Flush(); tail != strings.Repeat("字", 40) {
		t.Fatalf("flush = %q", tail)
	}
}
