package song

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/capability"
	"github.com/teamwork/digital-human/internal/biz/media"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

func TestPlaybackSourceUsesSharedEnum(t *testing.T) {
	lib := media.NewLibrary(t.TempDir())
	for _, tc := range []struct {
		name   string
		dec    media.Decoder
		player media.Player
		want   capability.Status
	}{
		{name: "real", dec: &media.FfmpegDecoder{}, player: &media.FfmpegPlayer{}, want: capability.Real},
		{name: "fake", dec: media.FakeDecoder{}, player: media.NewFakePlayer(), want: capability.Fake},
		{name: "unconfigured", dec: media.UnavailableDecoder{}, player: media.UnavailablePlayer{}, want: capability.Unconfigured},
		{name: "unknown", dec: media.FakeDecoder{}, player: newGatePlayer(), want: capability.Unknown},
	} {
		t.Run(tc.name, func(t *testing.T) {
			svc := NewService(lib, tc.dec, tc.player, nil, nil)
			if got := svc.Status().Source; got != tc.want {
				t.Fatalf("source=%q want %q", got, tc.want)
			}
		})
	}
}

func TestPlayAndStop(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "demo.wav"), []byte("123456"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "demo.lrc.json"), []byte(`["第一行","第二行"]`), 0o644); err != nil {
		t.Fatal(err)
	}
	lib := media.NewLibrary(dir)
	player := media.NewFakePlayer()
	player.SetDelay(40 * time.Millisecond)
	svc := NewService(lib, media.FakeDecoder{MS: 100}, player, nil, func() bool { return true })

	st, err := svc.Play(context.Background(), "demo.wav", "")
	if err != nil {
		t.Fatal(err)
	}
	if !st.Playing || st.LyricsSource != "lrc" || len(st.CaptionLines) != 2 {
		t.Fatalf("state=%+v", st)
	}
	if !svc.Status().Playing {
		t.Fatal("status should be playing")
	}
	if _, err := svc.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}
	if svc.Status().Playing {
		t.Fatal("stop failed")
	}
}

func TestPlayDoesNotPreemptCurrentSong(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"a.wav", "b.wav"} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	player := media.NewFakePlayer()
	player.SetDelay(200 * time.Millisecond)
	svc := NewService(media.NewLibrary(dir), media.FakeDecoder{}, player, nil, func() bool { return true })
	first, err := svc.Play(context.Background(), "a.wav", "")
	if err != nil {
		t.Fatal(err)
	}
	defer svc.Stop(context.Background())
	if _, err := svc.Play(context.Background(), "b.wav", ""); err != ErrBusy {
		t.Fatalf("播放中不得抢占: %v", err)
	}
	if got := svc.Status(); !got.Playing || got.File != first.File || got.Epoch != first.Epoch {
		t.Fatalf("当前曲状态被第二次 Play 改写: %+v", got)
	}
}

func TestPlayRequiresLiveAndFile(t *testing.T) {
	dir := t.TempDir()
	lib := media.NewLibrary(dir)
	svc := NewService(lib, media.FakeDecoder{}, media.NewFakePlayer(), nil, func() bool { return false })
	if _, err := svc.Play(context.Background(), "x.wav", ""); err != ErrNotLive {
		t.Fatalf("err=%v", err)
	}
	svc.SetLive(func() bool { return true })
	if _, err := svc.Play(context.Background(), "x.wav", ""); err != ErrNotFound {
		t.Fatalf("err=%v", err)
	}
}

func TestScriptFallback(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "s.wav"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := NewService(media.NewLibrary(dir), media.FakeDecoder{}, media.NewFakePlayer(), nil, func() bool { return true })
	st, err := svc.Play(context.Background(), "s.wav", "这是一句歌词。")
	if err != nil {
		t.Fatal(err)
	}
	if st.LyricsSource != "script" || len(st.CaptionLines) == 0 {
		t.Fatalf("state=%+v", st)
	}
	_, _ = svc.Stop(context.Background())
}

func TestListAndDelete(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	svc := NewService(media.NewLibrary(dir), media.FakeDecoder{}, media.NewFakePlayer(), nil, func() bool { return true })
	files, err := svc.List()
	if err != nil || len(files) != 1 {
		t.Fatalf("files=%+v err=%v", files, err)
	}
	if err := svc.Delete("a.mp3"); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.List(); err != nil {
		t.Fatal(err)
	}
}

type countingDecoder struct {
	calls atomic.Int32
	pcm   []byte
}

func (d *countingDecoder) Decode(context.Context, string) ([]byte, error) {
	d.calls.Add(1)
	return append([]byte(nil), d.pcm...), nil
}

func (d *countingDecoder) Name() string { return "counting" }

func newPCMService(t *testing.T, player media.Player) (*Service, *countingDecoder, string) {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "demo.wav")
	if err := os.WriteFile(path, []byte("original"), 0o644); err != nil {
		t.Fatal(err)
	}
	dec := &countingDecoder{pcm: []byte{1, 2, 3, 4}}
	svc := NewService(media.NewLibrary(dir), dec, player, nil, func() bool { return true })
	return svc, dec, path
}

func playAndWait(t *testing.T, svc *Service) {
	t.Helper()
	if _, err := svc.Play(context.Background(), "demo.wav", ""); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for svc.Status().Playing && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if svc.Status().Playing {
		t.Fatal("播放未结束")
	}
}

func TestDecodedPCMCacheReusesUnchangedFile(t *testing.T) {
	player := media.NewFakePlayer()
	player.SetDelay(0)
	svc, dec, _ := newPCMService(t, player)
	playAndWait(t, svc)
	playAndWait(t, svc)
	if got := dec.calls.Load(); got != 1 {
		t.Fatalf("相同文件重复播放应复用 SHA-256 PCM 缓存，解码次数=%d want 1", got)
	}
}

func TestDecodedPCMCacheInvalidatesChangedFile(t *testing.T) {
	player := media.NewFakePlayer()
	player.SetDelay(0)
	svc, dec, path := newPCMService(t, player)
	playAndWait(t, svc)
	if err := os.WriteFile(path, []byte("changed"), 0o644); err != nil {
		t.Fatal(err)
	}
	playAndWait(t, svc)
	if got := dec.calls.Load(); got != 2 {
		t.Fatalf("文件内容变化后必须重新解码，解码次数=%d want 2", got)
	}
}

type pcmCapturePlayer struct {
	pcm  chan []byte
	path chan string
}

func newPCMCapturePlayer() *pcmCapturePlayer {
	return &pcmCapturePlayer{pcm: make(chan []byte, 1), path: make(chan string, 1)}
}

func (p *pcmCapturePlayer) Play(_ context.Context, path, _ string) error {
	p.path <- filepath.Base(path)
	return nil
}

func (p *pcmCapturePlayer) PlayPCM(_ context.Context, pcm []byte, _ string) error {
	p.pcm <- append([]byte(nil), pcm...)
	return nil
}

func (p *pcmCapturePlayer) Stop(context.Context) error { return nil }
func (p *pcmCapturePlayer) Name() string               { return "pcm-capture" }

func TestPlayPassesDecodedPCMToPlayer(t *testing.T) {
	player := newPCMCapturePlayer()
	svc, dec, _ := newPCMService(t, player)
	if _, err := svc.Play(context.Background(), "demo.wav", ""); err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-player.pcm:
		if !bytes.Equal(got, dec.pcm) {
			t.Fatalf("播放器 PCM=%v want %v", got, dec.pcm)
		}
	case path := <-player.path:
		t.Fatalf("服务层已解码，不应再按路径播放 %s", path)
	case <-time.After(2 * time.Second):
		t.Fatal("播放器未收到 PCM")
	}
}

func TestNaturalEndPublishesOnlySongEnded(t *testing.T) {
	player := media.NewFakePlayer()
	player.SetDelay(0)
	svc, _, _ := newPCMService(t, player)
	bus := eventbus.New(16)
	svc.bus = bus
	events, cancel := bus.Subscribe(0)
	defer cancel()
	playAndWait(t, svc)

	var types []string
	for {
		select {
		case event := <-events:
			types = append(types, event.Type)
			if event.Type == "song.ended" {
				if len(types) != 2 || types[0] != "song.playing" {
					t.Fatalf("自然结束事件序列=%v", types)
				}
				return
			}
		case <-time.After(2 * time.Second):
			t.Fatalf("未收到 song.ended，事件=%v", types)
		}
	}
}

func TestStopPublishesOnlySongStopped(t *testing.T) {
	player := media.NewFakePlayer()
	player.SetDelay(time.Hour)
	svc, _, _ := newPCMService(t, player)
	bus := eventbus.New(16)
	svc.bus = bus
	events, cancel := bus.Subscribe(0)
	defer cancel()
	if _, err := svc.Play(context.Background(), "demo.wav", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := svc.Stop(context.Background()); err != nil {
		t.Fatal(err)
	}

	var types []string
	deadline := time.After(50 * time.Millisecond)
	for {
		select {
		case event := <-events:
			types = append(types, event.Type)
		case <-deadline:
			if len(types) != 2 || types[0] != "song.playing" || types[1] != "song.stopped" {
				t.Fatalf("主动停止事件序列=%v", types)
			}
			return
		}
	}
}
