// ffmpeg 真实解码器/播放器。fail-closed：ffmpeg 不在 PATH 时构造即报
// ErrFFmpegUnavailable，绝不降级成 fake；本机（验收环境）ffmpeg 不在 PATH，
// 因此所有真实播放路径都会明确报不可用。
package media

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/teamwork/digital-human/pkg/audio"
)

// ErrFFmpegUnavailable 表示 ffmpeg 不在 PATH，真实解码/播放不可用。
var ErrFFmpegUnavailable = errors.New("media: ffmpeg 不在 PATH，真实解码/播放不可用")

// FfmpegAvailable 报告 PATH 中是否有 ffmpeg（供 /healthz 能力位使用）。
func FfmpegAvailable() bool {
	_, err := exec.LookPath("ffmpeg")
	return err == nil
}

// FfmpegDecoder 用 ffmpeg 把任意音频文件解码为 24k/s16le/mono PCM。
type FfmpegDecoder struct {
	bin string
}

// NewFfmpegDecoder 解析 ffmpeg 可执行文件；不在 PATH 时返回 ErrFFmpegUnavailable。
func NewFfmpegDecoder() (*FfmpegDecoder, error) {
	bin, err := exec.LookPath("ffmpeg")
	if err != nil {
		return nil, ErrFFmpegUnavailable
	}
	return &FfmpegDecoder{bin: bin}, nil
}

func (d *FfmpegDecoder) Name() string { return "ffmpeg" }

// Decode 流式执行 ffmpeg -i <in> -f s16le -ac 1 -ar 24000 pipe:1 并收集 PCM。
// 蓝本 decode.py 同一口径；ctx 取消即杀进程，绝不挂死。
func (d *FfmpegDecoder) Decode(ctx context.Context, path string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, d.bin,
		"-hide_banner", "-loglevel", "error",
		"-i", path,
		"-f", "s16le", "-ac", "1", "-ar", "24000",
		"pipe:1")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("media: ffmpeg 启动失败: %w", err)
	}
	var buf bytes.Buffer
	_, copyErr := buf.ReadFrom(stdout) // 流式读，避免子进程写满管道死锁
	waitErr := cmd.Wait()
	if copyErr != nil {
		return nil, fmt.Errorf("media: ffmpeg 读流失败 %s: %w", path, copyErr)
	}
	if waitErr != nil {
		return nil, fmt.Errorf("media: ffmpeg 解码失败 %s: %w (%s)", path, waitErr, tail(stderr.String(), 200))
	}
	if buf.Len() == 0 {
		return nil, fmt.Errorf("media: ffmpeg 解码为空 %s", path)
	}
	return buf.Bytes(), nil
}

func tail(s string, n int) string {
	if len(s) > n {
		return s[len(s)-n:]
	}
	return s
}

// FeedSink 是播放器的喂流出口：按 48B/ms 节奏接收 PCM 分片（由 ViduController 提供）。
type FeedSink func(ctx context.Context, pcm []byte) error

// FfmpegPlayer = FfmpegDecoder 解码 + 真实时钟节奏写 FeedSink。
type FfmpegPlayer struct {
	dec  *FfmpegDecoder
	sink FeedSink

	mu     sync.Mutex
	cancel context.CancelFunc
	active bool
}

// NewFfmpegPlayer dec/sink 任一缺失即失败（fail-closed）。
func NewFfmpegPlayer(dec *FfmpegDecoder, sink FeedSink) (*FfmpegPlayer, error) {
	if dec == nil {
		return nil, ErrFFmpegUnavailable
	}
	if sink == nil {
		return nil, errors.New("media: ffmpeg player 需要 FeedSink")
	}
	return &FfmpegPlayer{dec: dec, sink: sink}, nil
}

func (p *FfmpegPlayer) Name() string { return "ffmpeg-vidu" }

// Play 解码整文件后按 100ms×4800B 分片、48B/ms 真实时钟节奏喂给 FeedSink；
// ctx 取消或 Stop 即停，不跳帧不补发。
func (p *FfmpegPlayer) Play(ctx context.Context, filePath, script string) error {
	pcm, err := p.dec.Decode(ctx, filePath)
	if err != nil {
		return err
	}
	return p.PlayPCM(ctx, pcm, script)
}

// PlayPCM 按 100ms×4800B 分片喂出调用方已解码的 PCM。
func (p *FfmpegPlayer) PlayPCM(ctx context.Context, pcm []byte, script string) error {
	pctx, cancel := context.WithCancel(ctx)
	p.mu.Lock()
	if p.active {
		p.mu.Unlock()
		cancel()
		return errors.New("media: ffmpeg player 已有播放进行中")
	}
	p.active = true
	p.cancel = cancel
	p.mu.Unlock()
	defer func() {
		cancel()
		p.mu.Lock()
		p.active = false
		p.cancel = nil
		p.mu.Unlock()
	}()

	pacer := audio.NewPacer(nil, audio.DefaultChunkMS)
	t0 := time.Now()
	var played int64
	for _, chunk := range audio.SplitPCM(pcm, pacer.ChunkBytes()) {
		if err := pctx.Err(); err != nil {
			return err
		}
		if err := p.sink(pctx, chunk); err != nil {
			return err
		}
		played += audio.MSForBytes(len(chunk))
		pacer.Wait(t0, played)
	}
	return nil
}

// Stop 提前结束当前播放；无播放时幂等。
func (p *FfmpegPlayer) Stop(context.Context) error {
	p.mu.Lock()
	cancel := p.cancel
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// LocalPlaybackTolerance 是解码基准与两次播放捕获之间允许的样本数偏差：
// 24kHz/s16le/mono 下 50ms = 1200 samples = 2400 bytes。
const LocalPlaybackTolerance = 50 * time.Millisecond

// LocalPlaybackEvidence 只证明真实音频文件经过真实 FfmpegPlayer 后本地可播，
// 不证明音频已送达 Vidu、云端、公开推流或观众。
type LocalPlaybackEvidence struct {
	Samples  int64
	Duration time.Duration
	runs     [2][]byte
}

// ProbeLocalPlayback 对同一真实音频文件连续播放两次并捕获输出 PCM。
// 非全零、样本数/时长接近独立解码基准、输出不等于源文件、两次结果一致，
// 四项全部成立才返回证据；任一不满足都 fail-closed。
func ProbeLocalPlayback(ctx context.Context, dec *FfmpegDecoder, path string) (LocalPlaybackEvidence, error) {
	if dec == nil {
		return LocalPlaybackEvidence{}, errors.New("media: 本地播放证据需要真实 ffmpeg decoder")
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return LocalPlaybackEvidence{}, fmt.Errorf("media: 读取播放证据源文件失败: %w", err)
	}
	sourcePCM, err := dec.Decode(ctx, path)
	if err != nil {
		return LocalPlaybackEvidence{}, fmt.Errorf("media: 解码播放证据基准失败: %w", err)
	}

	var evidence LocalPlaybackEvidence
	for run := 0; run < 2; run++ {
		var captured []byte
		player, err := NewFfmpegPlayer(dec, func(_ context.Context, pcm []byte) error {
			captured = append(captured, pcm...)
			return nil
		})
		if err != nil {
			return LocalPlaybackEvidence{}, err
		}
		if err := player.Play(ctx, path, ""); err != nil {
			return LocalPlaybackEvidence{}, fmt.Errorf("media: 第 %d 次本地真实播放失败: %w", run+1, err)
		}
		if len(captured) == 0 || bytes.Equal(captured, make([]byte, len(captured))) {
			return LocalPlaybackEvidence{}, fmt.Errorf("media: 第 %d 次本地播放输出为空或全零", run+1)
		}
		if bytes.Equal(captured, raw) {
			return LocalPlaybackEvidence{}, errors.New("media: 播放输出与源文件完全相等，疑似直通而非真实解码")
		}
		toleranceBytes := int64(LocalPlaybackTolerance/time.Millisecond) * audio.BytesPerMS
		diff := int64(len(captured)) - int64(len(sourcePCM))
		if diff < 0 {
			diff = -diff
		}
		if diff > toleranceBytes {
			return LocalPlaybackEvidence{}, fmt.Errorf("media: 第 %d 次播放样本数与源不一致: got=%d want=%d tolerance=%d bytes", run+1, len(captured)/2, len(sourcePCM)/2, toleranceBytes/2)
		}
		if run == 1 && !bytes.Equal(evidence.runs[0], captured) {
			return LocalPlaybackEvidence{}, errors.New("media: 连续两次本地播放 PCM 不一致")
		}
		evidence.runs[run] = captured
	}
	evidence.Samples = int64(len(evidence.runs[0]) / audio.BytesPerSample)
	evidence.Duration = time.Duration(audio.PCMDurationMS(evidence.runs[0])) * time.Millisecond
	return evidence, nil
}

// UnavailableDecoder 在 real 模式 ffmpeg 缺失时装配：启动不崩，
// 但任何解码请求都返回明确不可用错误。
type UnavailableDecoder struct{ Err error }

func (u UnavailableDecoder) Name() string { return "ffmpeg-unavailable" }

// Decode 永远返回 ffmpeg 不可用错误。
func (u UnavailableDecoder) Decode(context.Context, string) ([]byte, error) {
	if u.Err != nil {
		return nil, u.Err
	}
	return nil, ErrFFmpegUnavailable
}

// UnavailablePlayer 同理承接播放路径：Stop 幂等，Play 明确报错。
type UnavailablePlayer struct{ Err error }

func (u UnavailablePlayer) Name() string { return "ffmpeg-unavailable" }

// Play 永远返回 ffmpeg 不可用错误。
func (u UnavailablePlayer) Play(context.Context, string, string) error {
	if u.Err != nil {
		return u.Err
	}
	return ErrFFmpegUnavailable
}

// Stop 幂等成功。
func (u UnavailablePlayer) Stop(context.Context) error { return nil }
