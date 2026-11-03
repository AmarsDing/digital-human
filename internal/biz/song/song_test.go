package song

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/media"
)

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
