package media

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/teamwork/digital-human/pkg/audio"
)

// 本机（验收环境）ffmpeg 不在 PATH：构造必须明确失败，绝不降级 fake。
func TestFfmpegDecoderUnavailableWhenMissing(t *testing.T) {
	if FfmpegAvailable() {
		t.Skip("本机 PATH 有 ffmpeg，缺省路径不适用")
	}
	_, err := NewFfmpegDecoder()
	if !errors.Is(err, ErrFFmpegUnavailable) {
		t.Fatalf("err=%v want ErrFFmpegUnavailable", err)
	}
	if !strings.Contains(err.Error(), "ffmpeg 不在 PATH") {
		t.Fatalf("错误信息须明确指出 ffmpeg 不在 PATH: %v", err)
	}
}

// 有 ffmpeg 时做一条真实解码冒烟：生成 100ms wav → 解出 24k/s16le/mono PCM。
func TestFfmpegDecoderRealSmoke(t *testing.T) {
	dec, err := NewFfmpegDecoder()
	if err != nil {
		t.Skipf("ffmpeg 不在 PATH，跳过真实解码冒烟（待装了 ffmpeg 的真机）: %v", err)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "in.wav")
	if err := os.WriteFile(src, audio.WavBytes(audio.TonePCM(100)), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	pcm, err := dec.Decode(ctx, src)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	ms := audio.PCMDurationMS(pcm)
	if ms < 80 || ms > 160 {
		t.Fatalf("decoded %dms, want ~100ms", ms)
	}
}

// UnavailableDecoder/UnavailablePlayer 必须明确报错（real 模式 ffmpeg 缺失时的装配路径）。
func TestUnavailableDecoderPlayerFailClosed(t *testing.T) {
	d := UnavailableDecoder{}
	if _, err := d.Decode(context.Background(), "x.mp3"); !errors.Is(err, ErrFFmpegUnavailable) {
		t.Fatalf("decoder err=%v", err)
	}
	p := UnavailablePlayer{}
	if err := p.Play(context.Background(), "x.mp3", ""); !errors.Is(err, ErrFFmpegUnavailable) {
		t.Fatalf("player err=%v", err)
	}
	if err := p.Stop(context.Background()); err != nil {
		t.Fatalf("stop must be idempotent: %v", err)
	}
}

// FfmpegPlayer 节奏：100ms×4800B 分片按真实时钟喂给 FeedSink，总耗时应≈PCM 时长；
// ctx 取消即停。
func TestFfmpegPlayerPacing(t *testing.T) {
	if !FfmpegAvailable() {
		t.Skip("ffmpeg 不在 PATH，播放器真实链路待真机")
	}
	dec, err := NewFfmpegDecoder()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "in.wav")
	if err := os.WriteFile(src, audio.WavBytes(audio.TonePCM(300)), 0o644); err != nil {
		t.Fatal(err)
	}
	var fedMu sync.Mutex
	var fedBytes int64
	sink := FeedSink(func(ctx context.Context, pcm []byte) error {
		fedMu.Lock()
		fedBytes += int64(len(pcm))
		fedMu.Unlock()
		return nil
	})
	p, err := NewFfmpegPlayer(dec, sink)
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Now()
	if err := p.Play(context.Background(), src, ""); err != nil {
		t.Fatalf("play: %v", err)
	}
	elapsed := time.Since(t0)
	fedMu.Lock()
	defer fedMu.Unlock()
	if fedBytes == 0 {
		t.Fatal("no pcm fed")
	}
	if elapsed < 250*time.Millisecond {
		t.Fatalf("pacing too fast: %s for ~300ms audio", elapsed)
	}
}
