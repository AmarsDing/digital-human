package koubo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/media"
	"github.com/teamwork/digital-human/pkg/audio"
)

type synthCall struct {
	text  string
	voice string
	speed float64
}

type probeSynth struct {
	mu     sync.Mutex
	active int
	max    int
	delay  time.Duration
	calls  []synthCall
}

func (p *probeSynth) Synth(ctx context.Context, text, voice string) ([]byte, error) {
	return p.synth(ctx, text, voice, 0)
}

func (p *probeSynth) SynthWithParams(ctx context.Context, text, voice string, speed float64) ([]byte, error) {
	return p.synth(ctx, text, voice, speed)
}

func (p *probeSynth) synth(ctx context.Context, text, voice string, speed float64) ([]byte, error) {
	p.mu.Lock()
	p.active++
	p.calls = append(p.calls, synthCall{text: text, voice: voice, speed: speed})
	if p.active > p.max {
		p.max = p.active
	}
	p.mu.Unlock()
	defer func() {
		p.mu.Lock()
		p.active--
		p.mu.Unlock()
	}()
	if p.delay > 0 {
		select {
		case <-ctx.Done():
		case <-time.After(p.delay):
		}
	}
	return audio.SilencePCM(160), nil
}

func (p *probeSynth) Calls() []synthCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]synthCall(nil), p.calls...)
}

func (p *probeSynth) MaxConcurrent() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.max
}

func newKo(t *testing.T) (*Service, *probeSynth) {
	t.Helper()
	dir := t.TempDir()
	synth := &probeSynth{delay: 20 * time.Millisecond}
	player := media.NewFakePlayer()
	player.SetDelay(0)
	svc := NewService(NewMemoryStore(defaultState()), synth, player, FakeActionGen{Fixed: "点头"}, dir, func() bool { return true })
	return svc, synth
}

func TestSplitKeepsTokens(t *testing.T) {
	text := "请看 https://example.com/a?b=1 这个链接。然后我们开始口播测试，记得不要切断英文单词。"
	const url = "https://example.com/a?b=1"
	parts := SplitText(text, 20)
	if len(parts) < 2 {
		t.Fatalf("parts=%v", parts)
	}
	for _, p := range parts {
		if strings.Contains(p, "example.com") && !strings.Contains(p, url) {
			t.Fatalf("url split across segments: %q", p)
		}
		if strings.Contains(p, "英文单") && !strings.Contains(p, "英文单词") {
			t.Fatalf("word split: %q", p)
		}
	}
}

func TestKouboWorkflow(t *testing.T) {
	svc, _ := newKo(t)
	ctx := context.Background()
	st, err := svc.Split("第一句。第二句。第三句。", 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Segments) != 2 {
		t.Fatalf("segments=%d", len(st.Segments))
	}
	id := st.Segments[0].ID
	st, err = svc.TTS(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Segments[0].Takes) != 1 || st.Segments[0].Selected == nil {
		t.Fatalf("takes=%+v", st.Segments[0])
	}
	if err := svc.Play(ctx, id); err != nil {
		t.Fatal(err)
	}
	st, err = svc.Action(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if st.Segments[0].Action == "" {
		t.Fatal("action empty")
	}
	job, err := svc.TTSAll(ctx, true)
	if err != nil {
		t.Fatal(err)
	}
	if job.Total != 1 {
		t.Fatalf("job=%+v", job)
	}
	if err := svc.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	st = svc.State()
	if len(st.Segments[1].Takes) != 1 {
		t.Fatalf("segment2 takes=%+v", st.Segments[1])
	}
	if _, err := svc.Stop(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestPlayRequiresLive(t *testing.T) {
	svc, _ := newKo(t)
	svc.SetLive(func() bool { return false })
	ctx := context.Background()
	st, _ := svc.Split("一句话。", 20)
	if err := svc.Play(ctx, st.Segments[0].ID); err != ErrNotLive {
		t.Fatalf("err=%v", err)
	}
}

func TestTTSIsSerial(t *testing.T) {
	svc, synth := newKo(t)
	ctx := context.Background()
	st, err := svc.Split("甲句。乙句。", 20)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for _, seg := range st.Segments {
		id := seg.ID
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := svc.TTS(ctx, id); err != nil {
				t.Errorf("tts %s: %v", id, err)
			}
		}()
	}
	wg.Wait()
	if synth.MaxConcurrent() != 1 {
		t.Fatalf("max concurrent=%d", synth.MaxConcurrent())
	}
}

func TestTTSCacheIsKeyedByTextVoiceAndSpeed(t *testing.T) {
	svc, synth := newKo(t)
	ctx := context.Background()
	voice, speed := "voice-a", 1.0
	svc.SetSynthParamsFn(func() (string, float64) { return voice, speed })
	st, err := svc.Split("相同文本。", 20)
	if err != nil {
		t.Fatal(err)
	}
	id := st.Segments[0].ID

	if _, err := svc.TTS(ctx, id); err != nil {
		t.Fatal(err)
	}
	second, err := svc.TTS(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(synth.Calls()); got != 1 {
		t.Fatalf("同文本同参数合成次数=%d want 1", got)
	}
	if len(second.Segments[0].Takes) != 2 || second.Segments[0].Takes[0].File != second.Segments[0].Takes[1].File {
		t.Fatalf("同文本同参数未复用 take 文件: %+v", second.Segments[0].Takes)
	}
	call := synth.Calls()[0]
	if call.text != "相同文本。" || call.voice != "voice-a" || call.speed != 1.0 {
		t.Fatalf("合成参数未完整传递: %+v", call)
	}

	voice = "voice-b"
	third, err := svc.TTS(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(synth.Calls()); got != 2 {
		t.Fatalf("音色变化后合成次数=%d want 2", got)
	}
	if third.Segments[0].Takes[2].File == second.Segments[0].Takes[1].File {
		t.Fatal("音色变化后不得复用旧音频")
	}

	speed = 1.25
	fourth, err := svc.TTS(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	if got := len(synth.Calls()); got != 3 {
		t.Fatalf("语速变化后合成次数=%d want 3", got)
	}
	if fourth.Segments[0].Takes[3].File == third.Segments[0].Takes[2].File {
		t.Fatal("语速变化后不得复用旧音频")
	}

	if _, err := svc.UpdateSegment(id, "不同文本。"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TTS(ctx, id); err != nil {
		t.Fatal(err)
	}
	if got := len(synth.Calls()); got != 4 {
		t.Fatalf("文本变化后合成次数=%d want 4", got)
	}
}

func TestFileStorePersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "project.json")
	store := NewFileStore(path)
	svc := NewService(store, &probeSynth{}, media.NewFakePlayer(), FakeActionGen{}, t.TempDir(), func() bool { return true })
	if _, err := svc.Split("测试持久化。", 20); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	svc2 := NewService(NewFileStore(path), &probeSynth{}, media.NewFakePlayer(), FakeActionGen{}, t.TempDir(), func() bool { return true })
	if st := svc2.State(); len(st.Segments) != 1 {
		t.Fatalf("segments=%d", len(st.Segments))
	}
}

func TestAudioPathGuard(t *testing.T) {
	svc, _ := newKo(t)
	if _, err := svc.AudioPath("../secret.wav"); err == nil {
		t.Fatal("traversal must fail")
	}
}

func TestStickyStopRegression(t *testing.T) {
	svc, _ := newKo(t)
	ctx := context.Background()
	if _, err := svc.Split("第一句。第二句。", 5); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TTSAll(ctx, false); err != nil {
		t.Fatal(err)
	}
	if err := svc.WaitIdle(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.PlayAll(ctx, 1); err != nil {
		t.Fatal(err)
	}
	if st := svc.State(); st.Playing || st.PlayIdx != nil {
		t.Fatalf("播放结束后 Playing 标志未清回: %+v", st.PlayIdx)
	}
	if _, err := svc.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if !svc.Stopped() {
		t.Fatal("stop 标志未置位")
	}
	// sticky-stop 回归：再次连续播报必须复位 stop 并真实播放。
	n, err := svc.PlayAll(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("sticky stop: 第二次连续播报 0 段")
	}
}

func TestSplitSegmentUniqueIDs(t *testing.T) {
	svc, _ := newKo(t)
	// 语义结论（protectRE 复刻蓝本 _PROTECT：英文单词/URL/数字不可切断）：
	// "abcdefghij" 这类整段单 token 文本在内部任何位置都不可切，SafePos 会把
	// 切点挪到 token 边界导致半段为空，返回 ErrInvalidSplit 是诚实且正确的。
	// 因此这里用空格分隔的词组作为可切分输入，专门验证重复切分时的 ID 唯一性。
	if _, err := svc.Split("ab cd ef", 100); err != nil {
		t.Fatal(err)
	}
	// pos=4 落在词 "cd" 内部，SafePos 挪到词尾 5：left="ab cd" right="ef"。
	st, err := svc.SplitSegment("s1", 4)
	if err != nil {
		t.Fatal(err)
	}
	st, err = svc.SplitSegment("s1", 2)
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for _, seg := range st.Segments {
		if seen[seg.ID] {
			t.Fatalf("segment id 冲突: %s", seg.ID)
		}
		seen[seg.ID] = true
	}
	if !seen["s1b"] || !seen["s1b2"] {
		t.Fatalf("切分 ID 未按序递增: %v", seen)
	}
}

// 锁定保护规则语义：整段是单个不可切 token 时，任何内部切点都必须报
// ErrInvalidSplit，而不是把英文单词拦腰切断。
func TestSplitSegmentRejectsUnbreakableToken(t *testing.T) {
	svc, _ := newKo(t)
	if _, err := svc.Split("abcdefghij", 100); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.SplitSegment("s1", 4); !errors.Is(err, ErrInvalidSplit) {
		t.Fatalf("want ErrInvalidSplit, got %v", err)
	}
}
