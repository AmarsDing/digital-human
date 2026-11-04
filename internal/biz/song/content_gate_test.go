package song

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/contentgate"
	"github.com/teamwork/digital-human/internal/biz/media"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

type songContentGatePlayer struct {
	mu      sync.Mutex
	scripts []string
}

func (p *songContentGatePlayer) Play(_ context.Context, _, script string) error {
	p.mu.Lock()
	p.scripts = append(p.scripts, script)
	p.mu.Unlock()
	return nil
}
func (p *songContentGatePlayer) PlayPCM(_ context.Context, _ []byte, script string) error {
	return p.Play(context.Background(), "", script)
}
func (p *songContentGatePlayer) Stop(context.Context) error { return nil }
func (p *songContentGatePlayer) Name() string               { return "song-content-gate-player" }
func (p *songContentGatePlayer) Scripts() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.scripts...)
}

type songDecisionHandler struct {
	mu        sync.Mutex
	decisions []contentgate.Decision
}

func (h *songDecisionHandler) HandleContentDecision(_ context.Context, d contentgate.Decision) error {
	h.mu.Lock()
	h.decisions = append(h.decisions, d)
	h.mu.Unlock()
	return nil
}

func newSongContentGateService(t *testing.T) (*Service, *songContentGatePlayer, *songDecisionHandler, *eventbus.Bus, string) {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "低俗词.wav"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "normal.wav"), []byte("audio"), 0o600); err != nil {
		t.Fatal(err)
	}
	rulesPath := filepath.Join(t.TempDir(), "content_gate.yaml")
	rules := `layers:
  political:
    decision: block
    words: [政治违禁词]
  vulgar:
    decision: block
    words: [低俗词]
`
	if err := os.WriteFile(rulesPath, []byte(rules), 0o600); err != nil {
		t.Fatal(err)
	}
	gate := contentgate.New(contentgate.Options{Enabled: true, RulesPath: rulesPath, CircuitBreakerThreshold: 3})
	player := &songContentGatePlayer{}
	handler := &songDecisionHandler{}
	bus := eventbus.New(16)
	svc := NewService(media.NewLibrary(dir), media.FakeDecoder{}, player, bus, func() bool { return true })
	svc.SetContentGate(gate, handler, func() string { return "s1" })
	return svc, player, handler, bus, dir
}

func assertNoSongPlayingEvent(t *testing.T, events <-chan eventbus.Event) {
	t.Helper()
	select {
	case event := <-events:
		if event.Type == "song.playing" {
			t.Fatalf("拦截内容不得发布 song.playing: %+v", event)
		}
	default:
	}
}

func TestContentGateBlockedSongTitleOnlyEmitsTemplateDecision(t *testing.T) {
	svc, player, handler, bus, _ := newSongContentGateService(t)
	events, cancel := bus.Subscribe(0)
	defer cancel()
	const original = "低俗词.wav"
	_, err := svc.Play(context.Background(), original, "普通歌词")
	if !errors.Is(err, ErrContentBlocked) {
		t.Fatalf("原歌曲必须被拒绝: %v", err)
	}
	if got := player.Scripts(); len(got) != 0 {
		t.Fatalf("违禁标题不得进入 Player: %v", got)
	}
	if svc.Status().Playing {
		t.Fatal("拦截歌曲不得进入 playing 状态")
	}
	assertNoSongPlayingEvent(t, events)
	if len(handler.decisions) != 1 || handler.decisions[0].Text != contentgate.SafeSpokenTemplate || handler.decisions[0].Text == original {
		t.Fatalf("普通可拒绝歌曲只能播报静态模板: %+v", handler.decisions)
	}
}

func TestContentGatePoliticalSongIsSilent(t *testing.T) {
	svc, player, handler, bus, _ := newSongContentGateService(t)
	events, cancel := bus.Subscribe(0)
	defer cancel()
	const original = "政治违禁词"
	_, err := svc.Play(context.Background(), "normal.wav", original)
	if !errors.Is(err, ErrContentBlocked) {
		t.Fatalf("政治敏感歌曲必须被拒绝: %v", err)
	}
	if got := player.Scripts(); len(got) != 0 {
		t.Fatalf("政治敏感原文不得进入 Player: %v", got)
	}
	assertNoSongPlayingEvent(t, events)
	if len(handler.decisions) != 1 || handler.decisions[0].Disposition != contentgate.DispositionSilent || handler.decisions[0].Text != contentgate.SafeSilentNotice {
		t.Fatalf("政治敏感歌曲只能静默提示并切换话题: %+v", handler.decisions)
	}
}

func TestContentGateNormalSongStillPlays(t *testing.T) {
	svc, player, handler, _, _ := newSongContentGateService(t)
	if _, err := svc.Play(context.Background(), "normal.wav", "普通歌词"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for len(player.Scripts()) == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if got := player.Scripts(); len(got) != 1 || got[0] != "普通歌词" {
		t.Fatalf("正常歌曲不得被改写: %v", got)
	}
	if len(handler.decisions) != 0 {
		t.Fatalf("正常歌曲不得进入安全处置: %+v", handler.decisions)
	}
}
