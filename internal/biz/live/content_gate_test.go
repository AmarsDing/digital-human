package live

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/contentgate"
	"github.com/teamwork/digital-human/pkg/eventbus"
	"github.com/teamwork/digital-human/pkg/eventcontract"
)

func newLiveContentGate(t *testing.T) *contentgate.Gate {
	t.Helper()
	path := filepath.Join(t.TempDir(), "content_gate.yaml")
	rules := `layers:
  political:
    decision: block
    words: [政治违禁词]
  vulgar:
    decision: block
    words: [低俗词]
`
	if err := os.WriteFile(path, []byte(rules), 0o600); err != nil {
		t.Fatal(err)
	}
	return contentgate.New(contentgate.Options{
		Enabled: true, RulesPath: path, CircuitBreakerThreshold: 3,
	})
}

func newLiveContentGateManager(t *testing.T) (*Manager, *sayRecorder, *MemorySink) {
	t.Helper()
	ctrl := &sayRecorder{FakeController: NewFakeController(quietLogger())}
	sink := NewMemorySink(128)
	m := newPreviewBasicManager(eventbus.New(128), sink, ctrl)
	m.SetCaptionSplitFn(func() bool { return false })
	m.SetContentGate(newLiveContentGate(t))
	if _, phase, _, err := m.Start(context.Background(), "p", "", ""); err != nil || phase != PhaseLive {
		t.Fatalf("start: phase=%s err=%v", phase, err)
	}
	return m, ctrl, sink
}

func transcriptEventTexts(sink *MemorySink, eventType string) []string {
	var out []string
	for _, event := range sink.All() {
		if event.Type != eventType {
			continue
		}
		text, _ := event.Payload[eventcontract.FieldText].(string)
		out = append(out, text)
	}
	return out
}

func assertOriginalAbsentFromTranscripts(t *testing.T, sink *MemorySink, original string) {
	t.Helper()
	for _, event := range sink.All() {
		if !strings.HasPrefix(event.Type, "transcript.") {
			continue
		}
		text, _ := event.Payload[eventcontract.FieldText].(string)
		if strings.Contains(text, original) {
			t.Fatalf("原文不得进入字幕: event=%s text=%q original=%q", event.Type, text, original)
		}
	}
}

func TestContentGateVulgarSpeaksOnlyStaticTemplate(t *testing.T) {
	m, ctrl, sink := newLiveContentGateManager(t)
	const original = "这是低俗词"
	if _, err := m.Say(context.Background(), original, "chat"); err != nil {
		t.Fatal(err)
	}
	said := ctrl.said()
	if len(said) != 1 || said[0] != contentgate.SafeSpokenTemplate {
		t.Fatalf("必须对静态模板发生一次合成: %v", said)
	}
	if said[0] == original || strings.Contains(said[0], original) {
		t.Fatalf("不得对原文合成: %v", said)
	}
	for _, eventType := range []string{eventcontract.TranscriptPartial, eventcontract.TranscriptFinal} {
		texts := transcriptEventTexts(sink, eventType)
		if len(texts) != 1 || texts[0] != contentgate.SafeSpokenTemplate {
			t.Fatalf("%s 必须只包含安全模板: %v", eventType, texts)
		}
	}
	assertOriginalAbsentFromTranscripts(t, sink, original)
	if got := m.Snapshot().Transcript; len(got) != 1 || got[0] != contentgate.SafeSpokenTemplate {
		t.Fatalf("场次字幕只能保留安全模板: %v", got)
	}
}

func TestContentGatePoliticalIsSilentAndSwitchesTopic(t *testing.T) {
	m, ctrl, sink := newLiveContentGateManager(t)
	const original = "出现政治违禁词"
	id, err := m.Say(context.Background(), original, "chat")
	if err != nil {
		t.Fatal(err)
	}
	if id != "" || len(ctrl.said()) != 0 {
		t.Fatalf("政治敏感内容不得合成: id=%q said=%v", id, ctrl.said())
	}
	assertOriginalAbsentFromTranscripts(t, sink, original)
	for _, eventType := range []string{eventcontract.TranscriptPartial, eventcontract.TranscriptFinal} {
		texts := transcriptEventTexts(sink, eventType)
		if len(texts) != 1 || texts[0] != contentgate.SafeSilentNotice {
			t.Fatalf("静默拦截必须上屏静态提示: %s %v", eventType, texts)
		}
	}
	resets := 0
	for _, event := range sink.All() {
		if event.Type == "llm.reset" {
			resets++
		}
	}
	if resets != 1 {
		t.Fatalf("静默拦截必须且只切换一次话题上下文: %d", resets)
	}
}

func TestContentGateFourthConsecutiveBlockBecomesSilent(t *testing.T) {
	m, ctrl, sink := newLiveContentGateManager(t)
	for i := 0; i < 4; i++ {
		if _, err := m.Say(context.Background(), "低俗词", "chat"); err != nil {
			t.Fatal(err)
		}
	}
	said := ctrl.said()
	if len(said) != 3 {
		t.Fatalf("前三次口播模板，第四次必须静默: %v", said)
	}
	for _, text := range said {
		if text != contentgate.SafeSpokenTemplate {
			t.Fatalf("前三次只能合成模板: %v", said)
		}
	}
	finals := transcriptEventTexts(sink, eventcontract.TranscriptFinal)
	if len(finals) != 4 || finals[3] != contentgate.SafeSilentNotice {
		t.Fatalf("第四次应只上屏静默提示: %v", finals)
	}
	assertOriginalAbsentFromTranscripts(t, sink, "低俗词")
}

func TestContentGateAllowsNormalTextUnchanged(t *testing.T) {
	m, ctrl, _ := newLiveContentGateManager(t)
	const original = "欢迎收看今天的普通商品介绍"
	if _, err := m.Say(context.Background(), original, "chat"); err != nil {
		t.Fatal(err)
	}
	if said := ctrl.said(); len(said) != 1 || said[0] != original {
		t.Fatalf("正常文本必须原样合成: %v", said)
	}
}

func TestContentGateCircuitBreakerResetsOnNewSession(t *testing.T) {
	m, ctrl, _ := newLiveContentGateManager(t)
	for i := 0; i < 3; i++ {
		if _, err := m.Say(context.Background(), "低俗词", "chat"); err != nil {
			t.Fatal(err)
		}
	}
	if _, _, err := m.Stop(context.Background(), "restart"); err != nil {
		t.Fatal(err)
	}
	if _, phase, _, err := m.Start(context.Background(), "p", "", ""); err != nil || phase != PhaseLive {
		t.Fatalf("restart: phase=%s err=%v", phase, err)
	}
	if _, err := m.Say(context.Background(), "低俗词", "chat"); err != nil {
		t.Fatal(err)
	}
	if said := ctrl.said(); len(said) != 4 || said[3] != contentgate.SafeSpokenTemplate {
		t.Fatalf("新场次必须重置熔断: %v", said)
	}
}
