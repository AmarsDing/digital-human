package live

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/teamwork/digital-human/pkg/eventbus"
)

// sayRecorder 记录每次 Say 的文本，用于断言逐句/整段投喂（F19/G16）。
type sayRecorder struct {
	*FakeController
	mu    sync.Mutex
	texts []string
}

func (r *sayRecorder) Say(_ context.Context, text, _ string) (string, error) {
	r.mu.Lock()
	r.texts = append(r.texts, text)
	n := len(r.texts)
	r.mu.Unlock()
	return "u-" + strconv.Itoa(n), nil
}

func (r *sayRecorder) said() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.texts...)
}

// TestSaySplitsSentencesWhenCaptionEnabled F19/G16：captions.output_sentences=开
// → 整段文本按蓝本分句规则逐句投喂 TTS（每句一次 ctrl.Say），返回首句 utterance id。
func TestSaySplitsSentencesWhenCaptionEnabled(t *testing.T) {
	ctrl := &sayRecorder{FakeController: NewFakeController(quietLogger())}
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), ctrl)
	m.SetCaptionSplitFn(func() bool { return true })
	if _, phase, _, err := m.Start(context.Background(), "p", "", ""); err != nil || phase != PhaseLive {
		t.Fatalf("start: phase=%s err=%v", phase, err)
	}
	id, err := m.Say(context.Background(), "大家好，欢迎来到直播间。今天播三个小时！", "chat")
	if err != nil {
		t.Fatal(err)
	}
	got := ctrl.said()
	if len(got) != 2 || got[0] != "大家好，欢迎来到直播间。" || got[1] != "今天播三个小时！" {
		t.Fatalf("逐句投喂: %v", got)
	}
	if id != "u-1" {
		t.Fatalf("返回首句 id: %s", id)
	}
}

// TestSayWholeTextWhenCaptionDisabled F19/G16：captions.output_sentences=关
// → 整段一次投喂，不切句。
func TestSayWholeTextWhenCaptionDisabled(t *testing.T) {
	ctrl := &sayRecorder{FakeController: NewFakeController(quietLogger())}
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), ctrl)
	m.SetCaptionSplitFn(func() bool { return false })
	if _, phase, _, err := m.Start(context.Background(), "p", "", ""); err != nil || phase != PhaseLive {
		t.Fatalf("start: phase=%s err=%v", phase, err)
	}
	if _, err := m.Say(context.Background(), "大家好，欢迎来到直播间。今天播三个小时！", "chat"); err != nil {
		t.Fatal(err)
	}
	got := ctrl.said()
	if len(got) != 1 || got[0] != "大家好，欢迎来到直播间。今天播三个小时！" {
		t.Fatalf("整段投喂: %v", got)
	}
}

func TestSayCanDisableTranscriptPersistencePerTurn(t *testing.T) {
	ctrl := &sayRecorder{FakeController: NewFakeController(quietLogger())}
	sink := NewMemorySink(64)
	m := newPreviewBasicManager(eventbus.New(64), sink, ctrl)
	m.SetCaptionSplitFn(func() bool { return false })
	if _, phase, _, err := m.Start(context.Background(), "p", "", ""); err != nil || phase != PhaseLive {
		t.Fatalf("start: phase=%s err=%v", phase, err)
	}
	ctx := WithTranscriptPersistence(context.Background(), false)
	if _, err := m.Say(ctx, "不保留这句话", "chat"); err != nil {
		t.Fatal(err)
	}
	if got := m.Snapshot().Transcript; len(got) != 0 {
		t.Fatalf("transcript should not be retained: %v", got)
	}
	for _, event := range sink.All() {
		if strings.HasPrefix(event.Type, "transcript.") {
			t.Fatalf("transcript event should not be published: %+v", event)
		}
	}
}

// TestSaySplitDefaultsOnWithoutFn 未装配开关读取口（nil）按产品默认 true 逐句。
func TestSaySplitDefaultsToSentenceWithoutFn(t *testing.T) {
	ctrl := &sayRecorder{FakeController: NewFakeController(quietLogger())}
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), ctrl)
	if _, phase, _, err := m.Start(context.Background(), "p", "", ""); err != nil || phase != PhaseLive {
		t.Fatalf("start: phase=%s err=%v", phase, err)
	}
	if _, err := m.Say(context.Background(), "第一句。第二句。", "chat"); err != nil {
		t.Fatal(err)
	}
	if got := ctrl.said(); len(got) != 2 {
		t.Fatalf("nil fn 应按默认逐句: %v", got)
	}
}

// TestSaySplitNoPunctuationStaysWhole 无句读文本即使开关为开也保持单句。
func TestSaySplitNoPunctuationStaysWhole(t *testing.T) {
	ctrl := &sayRecorder{FakeController: NewFakeController(quietLogger())}
	m := newPreviewBasicManager(eventbus.New(64), NewMemorySink(64), ctrl)
	m.SetCaptionSplitFn(func() bool { return true })
	if _, phase, _, err := m.Start(context.Background(), "p", "", ""); err != nil || phase != PhaseLive {
		t.Fatalf("start: phase=%s err=%v", phase, err)
	}
	if _, err := m.Say(context.Background(), "没有标点的整段话", "chat"); err != nil {
		t.Fatal(err)
	}
	if got := ctrl.said(); len(got) != 1 || got[0] != "没有标点的整段话" {
		t.Fatalf("无句读应保持整段: %v", got)
	}
}
