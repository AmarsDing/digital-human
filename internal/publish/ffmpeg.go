package publish

// ffmpeg 进程驱动：argv 永不包含 stream key（FLV 经 stdout 管道交给内嵌
// RTMP 客户端）；指标经 -progress 解析。编码默认 libx264 软编；
// h264_nvenc 需显式开启（与 OmniVoice 抢显存，见 docs）。

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strconv"
	"strings"
)

// ErrFFmpegUnavailable 表示 ffmpeg 不在 PATH（fail-closed）。
var ErrFFmpegUnavailable = errors.New("publish: ffmpeg 不在 PATH，推流不可用")

// SourceKind 采集源类型。
type SourceKind string

const (
	// SourceSynthetic 内置测试图+静音/正弦（lavfi），用于无浏览器环境下验证管线。
	SourceSynthetic SourceKind = "synthetic"
	// SourceStage 无头浏览器抓 stage 页帧（JPEG 经 stdin image2pipe 喂入）。
	SourceStage SourceKind = "stage"
)

// EncoderConfig 编码器配置。
type EncoderConfig struct {
	// Codec: "libx264"（默认）或 "h264_nvenc"（需显式开启，显存与 OmniVoice 竞争）。
	Codec string
	// Width/Height/FPS 输出参数；竖屏默认 720x1280@30。
	Width, Height, FPS int
	// VideoBitrateKbps / AudioBitrateKbps 输出码率。
	VideoBitrateKbps int
	AudioBitrateKbps int
}

func (c EncoderConfig) withDefaults() EncoderConfig {
	if c.Codec == "" {
		c.Codec = "libx264"
	}
	if c.Width == 0 {
		c.Width = 720
	}
	if c.Height == 0 {
		c.Height = 1280
	}
	if c.FPS == 0 {
		c.FPS = 30
	}
	if c.VideoBitrateKbps == 0 {
		c.VideoBitrateKbps = 1500
	}
	if c.AudioBitrateKbps == 0 {
		c.AudioBitrateKbps = 128
	}
	return c
}

// FFmpegDriver 管理一个 ffmpeg 子进程。
type FFmpegDriver struct {
	// Bin 默认 "ffmpeg"；测试可替换为假 ffmpeg（helper 进程）。
	Bin string
	// PrependArgs 插入在 Bin 与真实参数之间（测试用 helper 进程注入）。
	PrependArgs []string
	// AudioInput 是 SourceStage 的 RTC 远端 PCM loopback 地址；禁止传原始 TTS/歌曲 PCM。
	AudioInput string

	stdin  io.WriteCloser // SourceStage 时喂 JPEG 帧
	stdout io.ReadCloser  // FLV 输出
	stderr io.ReadCloser  // -progress 与错误日志
	cmd    *exec.Cmd
}

// buildArgs 构造 argv。保证：argv 中绝不出现 stream key / 完整推流地址。
func (d *FFmpegDriver) buildArgs(kind SourceKind, ec EncoderConfig) []string {
	ec = ec.withDefaults()
	args := []string{"-hide_banner", "-loglevel", "error"}
	var mapArgs []string
	switch kind {
	case SourceSynthetic:
		args = append(args, "-nostdin",
			"-f", "lavfi", "-i",
			fmt.Sprintf("testsrc2=size=%dx%d:rate=%d", ec.Width, ec.Height, ec.FPS),
			"-f", "lavfi", "-i", "sine=frequency=440:sample_rate=44100")
		mapArgs = []string{"-map", "0:v", "-map", "1:a"}
	case SourceStage:
		// 输入 0 保留 JPEG 舞台；输入 1 只接浏览器 RTC 远端音轨的 PCM bridge。
		// 不混入原始 TTS/歌曲 PCM，避免本地双声；未实证 Vidu 下行前仍是 audio_delivery=unknown。
		args = append(args,
			"-f", "image2pipe", "-framerate", strconv.Itoa(ec.FPS),
			"-analyzeduration", "0", "-probesize", "32", "-vcodec", "mjpeg", "-i", "pipe:0",
			"-f", "s16le", "-ar", "48000", "-ac", "1", "-i", d.AudioInput)
		mapArgs = []string{"-map", "0:v", "-map", "1:a"}
	}
	// 统一缩放到输出分辨率（分辨率变化由 scale+pad 吸收），软编默认。
	vf := fmt.Sprintf("scale=%d:%d:force_original_aspect_ratio=decrease,"+
		"pad=%d:%d:(ow-iw)/2:(oh-ih)/2,setsar=1", ec.Width, ec.Height, ec.Width, ec.Height)
	args = append(args,
		mapArgs[0], mapArgs[1], mapArgs[2], mapArgs[3],
		"-c:v", ec.Codec, "-preset", "veryfast", "-tune", "zerolatency",
		"-b:v", fmt.Sprintf("%dk", ec.VideoBitrateKbps),
		"-maxrate", fmt.Sprintf("%dk", ec.VideoBitrateKbps*12/10),
		"-bufsize", fmt.Sprintf("%dk", ec.VideoBitrateKbps*2),
		"-g", strconv.Itoa(ec.FPS*2),
		"-c:a", "aac", "-b:a", fmt.Sprintf("%dk", ec.AudioBitrateKbps),
		"-vf", vf,
		"-progress", "pipe:2",
		"-f", "flv", "pipe:1")
	return args
}

// validateSource 在启动前固定输入合同，避免 PCM bridge 缺失时退回静音源。
func (d *FFmpegDriver) validateSource(kind SourceKind) error {
	if kind == SourceStage && d.AudioInput == "" {
		return errors.New("publish: SourceStage 缺少 RTC PCM bridge 地址")
	}
	return nil
}

// Start 启动 ffmpeg。kind=SourceStage 时调用方经 Stdin() 喂 JPEG 帧。
func (d *FFmpegDriver) Start(kind SourceKind, ec EncoderConfig) error {
	if err := d.validateSource(kind); err != nil {
		return err
	}
	bin := d.Bin
	if bin == "" {
		bin = "ffmpeg"
	}
	resolved, err := exec.LookPath(bin)
	if err != nil {
		return ErrFFmpegUnavailable
	}
	argv := append(append([]string{}, d.PrependArgs...), d.buildArgs(kind, ec)...)
	// 注意：d.cmd 不在此保留原始 argv 之外的任何密钥材料——argv 不含 key（设计红线）。
	d.cmd = exec.Command(resolved, argv...)
	if kind == SourceStage {
		d.stdin, err = d.cmd.StdinPipe()
		if err != nil {
			return err
		}
	} else {
		d.cmd.Stdin = nil
	}
	d.stdout, err = d.cmd.StdoutPipe()
	if err != nil {
		return err
	}
	d.stderr, err = d.cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := d.cmd.Start(); err != nil {
		return fmt.Errorf("publish: ffmpeg 启动失败: %w", err)
	}
	return nil
}

// Stdin 返回帧喂入管道（仅 SourceStage）。
func (d *FFmpegDriver) Stdin() io.Writer { return d.stdin }

// Stdout 返回 FLV 输出流。
func (d *FFmpegDriver) Stdout() io.Reader { return d.stdout }

// Wait 等待进程退出。
func (d *FFmpegDriver) Wait() error { return d.cmd.Wait() }

// Kill 强制终止。
func (d *FFmpegDriver) Kill() {
	if d.cmd != nil && d.cmd.Process != nil {
		_ = d.cmd.Process.Kill()
	}
}

// WatchProgress 解析 stderr 上的 -progress 输出，回调指标；stderr 关闭后返回。
// 非 progress 行（错误日志）经 logErr 回调（调用方负责再脱敏检查）。
func (d *FFmpegDriver) WatchProgress(onMetrics func(Metrics), logErr func(string)) {
	sc := bufio.NewScanner(d.stderr)
	sc.Buffer(make([]byte, 0, 64<<10), 1<<20)
	var m Metrics
	var have bool
	flush := func() {
		if have {
			onMetrics(m)
			have = false
		}
	}
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			if line != "" && logErr != nil {
				logErr(line)
			}
			continue
		}
		switch k {
		case "fps":
			m.FPS, _ = strconv.ParseFloat(v, 64)
			have = true
		case "bitrate":
			v = strings.TrimSuffix(strings.TrimSpace(v), "kbits/s")
			f, _ := strconv.ParseFloat(v, 64)
			m.BitrateKbps = int(f + 0.5)
			have = true
		case "drop_frames":
			m.DroppedFrames, _ = strconv.ParseInt(v, 10, 64)
			have = true
		case "progress":
			flush()
		}
	}
	flush()
}

// safeArgs 返回可安全记日志的 argv 形状（当前设计 argv 本无 key，仍统一走此出口，
// 防止未来改动意外把密钥带进日志）。
func (d *FFmpegDriver) safeArgs(kind SourceKind, ec EncoderConfig) []string {
	return d.buildArgs(kind, ec)
}
