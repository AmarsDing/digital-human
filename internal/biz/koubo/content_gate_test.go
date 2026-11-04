package koubo

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/contentgate"
)

type contentGatePlayerCall struct {
	path   string
	script string
}

type contentGatePlayer struct {
	mu    sync.Mutex
	calls []contentGatePlayerCall
}

func (p *contentGatePlayer) Play(_ context.Context, path, script string) error {
	p.mu.Lock()
	p.calls = append(p.calls, contentGatePlayerCall{path: path, script: script})
	p.mu.Unlock()
	return nil
}
func (p *contentGatePlayer) Stop(context.Context) error { return nil }
func (p *contentGatePlayer) Name() string               { return "content-gate-player" }
func (p *contentGatePlayer) Calls() []contentGatePlayerCall {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]contentGatePlayerCall(nil), p.calls...)
}

type contentGateDecisionHandler struct {
	mu        sync.Mutex
	decisions []contentgate.Decision
}

func (h *contentGateDecisionHandler) HandleContentDecision(_ context.Context, d contentgate.Decision) error {
	h.mu.Lock()
	h.decisions = append(h.decisions, d)
	h.mu.Unlock()
	return nil
}
func (h *contentGateDecisionHandler) Decisions() []contentgate.Decision {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]contentgate.Decision(nil), h.decisions...)
}

func newKouboContentGate(t *testing.T) *contentgate.Gate {
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
	return contentgate.New(contentgate.Options{Enabled: true, RulesPath: path, CircuitBreakerThreshold: 3})
}

func newKouboContentGateService(t *testing.T, store Store) (*Service, *probeSynth, *contentGatePlayer, *contentGateDecisionHandler, string) {
	t.Helper()
	dir := t.TempDir()
	synth := &probeSynth{}
	player := &contentGatePlayer{}
	handler := &contentGateDecisionHandler{}
	svc := NewService(store, synth, player, FakeActionGen{}, dir, func() bool { return true })
	svc.SetContentGate(newKouboContentGate(t), handler, func() string { return "s1" })
	return svc, synth, player, handler, dir
}

func newKouboCachedService(t *testing.T, text string) (*Service, *probeSynth, *contentGatePlayer, *contentGateDecisionHandler, string) {
	t.Helper()
	selected := 0
	store := NewMemoryStore(State{MaxChars: 120, Segments: []Segment{{
		ID: "s1", Text: text, Takes: []Take{{File: "cached.wav"}}, Selected: &selected,
	}}})
	svc, synth, player, handler, dir := newKouboContentGateService(t, store)
	if err := os.WriteFile(filepath.Join(dir, "cached.wav"), []byte("original cached audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	return svc, synth, player, handler, dir
}

func TestContentGateTTSSynthesizesOnlyStaticTemplate(t *testing.T) {
	svc, synth, _, _, _ := newKouboContentGateService(t, NewMemoryStore(defaultState()))
	const original = "这是低俗词"
	st, err := svc.Split(original, 20)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := svc.TTS(context.Background(), st.Segments[0].ID); err != nil {
		t.Fatal(err)
	}
	calls := synth.Calls()
	if len(calls) != 1 || calls[0].text != contentgate.SafeSpokenTemplate {
		t.Fatalf("必须对模板精确合成: %+v", calls)
	}
	if calls[0].text == original || strings.Contains(calls[0].text, original) {
		t.Fatalf("不得对原文合成: %+v", calls)
	}
}

func TestContentGateSilentTTSNeverSynthesizesOriginal(t *testing.T) {
	svc, synth, _, _, _ := newKouboContentGateService(t, NewMemoryStore(defaultState()))
	st, _ := svc.Split("政治违禁词", 20)
	if _, err := svc.TTS(context.Background(), st.Segments[0].ID); !errors.Is(err, ErrContentBlocked) {
		t.Fatalf("静默内容不得生成普通 take: %v", err)
	}
	if calls := synth.Calls(); len(calls) != 0 {
		t.Fatalf("静默内容不得发生合成: %+v", calls)
	}
}

func TestContentGateCachedPlayReplacesOriginalAudioWithTemplate(t *testing.T) {
	const original = "低俗词"
	svc, synth, player, _, _ := newKouboCachedService(t, original)
	if err := svc.Play(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	calls := synth.Calls()
	if len(calls) != 1 || calls[0].text != contentgate.SafeSpokenTemplate {
		t.Fatalf("缓存命中后仍须真实合成模板: %+v", calls)
	}
	played := player.Calls()
	if len(played) != 1 || played[0].script != contentgate.SafeSpokenTemplate || filepath.Base(played[0].path) == "cached.wav" {
		t.Fatalf("不得播放原缓存音频或原文字幕: %+v", played)
	}
	if strings.Contains(played[0].script, original) {
		t.Fatalf("原文不得进入 Player: %+v", played)
	}
}

func TestContentGatePlayAllRechecksCachedAudio(t *testing.T) {
	svc, synth, player, _, _ := newKouboCachedService(t, "低俗词")
	played, err := svc.PlayAll(context.Background(), 1)
	if err != nil || played != 1 {
		t.Fatalf("连续播报安全模板: played=%d err=%v", played, err)
	}
	if calls := synth.Calls(); len(calls) != 1 || calls[0].text != contentgate.SafeSpokenTemplate {
		t.Fatalf("连续播报只能合成模板: %+v", calls)
	}
	if calls := player.Calls(); len(calls) != 1 || calls[0].script != contentgate.SafeSpokenTemplate || filepath.Base(calls[0].path) == "cached.wav" {
		t.Fatalf("连续播报不得旁路原缓存: %+v", calls)
	}
}

func TestContentGateSilentCachedPlayIsSuppressed(t *testing.T) {
	svc, synth, player, handler, _ := newKouboCachedService(t, "政治违禁词")
	if err := svc.Play(context.Background(), "s1"); err != nil {
		t.Fatal(err)
	}
	if len(synth.Calls()) != 0 || len(player.Calls()) != 0 {
		t.Fatalf("静默缓存不得合成或播放: synth=%+v player=%+v", synth.Calls(), player.Calls())
	}
	decisions := handler.Decisions()
	if len(decisions) != 1 || decisions[0].Disposition != contentgate.DispositionSilent || decisions[0].Text != contentgate.SafeSilentNotice {
		t.Fatalf("静默分支必须交给安全提示与话题切换处理器: %+v", decisions)
	}
}
