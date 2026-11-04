package media

import (
	"bytes"
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

// 本地可播证据必须使用真实音频文件、真实 FfmpegPlayer 与捕获的 PCM；
// 只证明播放器本地可播，不证明音频已送达 Vidu、云端或观众。
func TestLocalPlaybackProbeRealFileEvidence(t *testing.T) {
	dec, err := NewFfmpegDecoder()
	if err != nil {
		t.Skipf("ffmpeg 不在 PATH，跳过真实播放证据: %v", err)
	}
	src := filepath.Join(t.TempDir(), "real-tone.wav")
	raw := audio.WavBytes(audio.TonePCM(120))
	if err := os.WriteFile(src, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	sourcePCM, err := dec.Decode(context.Background(), src)
	if err != nil {
		t.Fatal(err)
	}

	evidence, err := ProbeLocalPlayback(context.Background(), dec, src)
	if err != nil {
		t.Fatalf("真实音频文件本地播放探测失败: %v", err)
	}
	if len(evidence.runs) != 2 || !bytes.Equal(evidence.runs[0], evidence.runs[1]) {
		t.Fatal("连续两次真实播放必须捕获到完全一致的 PCM")
	}
	played := evidence.runs[0]
	if len(played) == 0 || bytes.Equal(played, make([]byte, len(played))) {
		t.Fatal("真实播放输出不得为空或全零")
	}
	if bytes.Equal(played, raw) {
		t.Fatal("解码输出与源文件字节相等，疑似直通搬运")
	}
	toleranceBytes := int64(LocalPlaybackTolerance/time.Millisecond) * audio.BytesPerMS
	if diff := abs64(int64(len(played)) - int64(len(sourcePCM))); diff > toleranceBytes {
		t.Fatalf("播放样本数与源不一致: played=%d source=%d tolerance=%d bytes", len(played)/2, len(sourcePCM)/2, toleranceBytes/2)
	}
	if evidence.Samples != int64(len(played)/2) || evidence.Duration != time.Duration(audio.PCMDurationMS(played))*time.Millisecond {
		t.Fatalf("证据样本数/时长错误: %+v", evidence)
	}
}

func TestLocalPlaybackProbeRejectsAllZeroOutput(t *testing.T) {
	dec, err := NewFfmpegDecoder()
	if err != nil {
		t.Skipf("ffmpeg 不在 PATH，跳过全零负对照: %v", err)
	}
	src := filepath.Join(t.TempDir(), "silence.wav")
	if err := os.WriteFile(src, audio.WavBytes(audio.SilencePCM(120)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ProbeLocalPlayback(context.Background(), dec, src); err == nil || !strings.Contains(err.Error(), "全零") {
		t.Fatalf("全零输出必须判 fail，err=%v", err)
	}
}

func abs64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
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
