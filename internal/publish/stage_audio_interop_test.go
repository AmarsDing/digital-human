package publish

import (
	"bufio"
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

var volumePattern = regexp.MustCompile(`(mean_volume|max_volume):\s*(-?[0-9.]+) dB`)

// TestInteropStageAudioPositiveAndZeroControl 验证真实桥接与编码，不验证 Vidu 下行或抖音观众端。
// 合成 PCM 仅是本地互验夹具；同一响度门禁必须接受 440Hz 正弦并拒绝全零负对照。
func TestInteropStageAudioPositiveAndZeroControl(t *testing.T) {
	ff, mtx, conf := interopEnv(t)
	for _, tc := range []struct {
		name   string
		key    string
		silent bool
	}{
		{name: "440Hz 合成 PCM 正例", key: "stage-audio-tone"},
		{name: "全零 PCM 负对照", key: "stage-audio-zero", silent: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			mean, max := runStageAudioInterop(t, ff, mtx, conf, tc.key, tc.silent)
			audible := mean > -55 && max > -30
			if tc.silent && audible {
				t.Fatalf("全零负对照被响度门禁误判为可听: mean=%.2f max=%.2f", mean, max)
			}
			if !tc.silent && !audible {
				t.Fatalf("合成 PCM 未达到响度门禁 mean>-55dB max>-30dB: mean=%.2f max=%.2f", mean, max)
			}
			t.Logf("STAGE_AUDIO_VOLUME mode=%s mean_volume=%.2f dB max_volume=%.2f dB audible=%v",
				tc.key, mean, max, audible)
		})
	}
}

func runStageAudioInterop(t *testing.T, ff, mtx, conf, key string, silent bool) (float64, float64) {
	t.Helper()
	server := startMediaMTX(t, mtx, conf)
	defer server.stop()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bridge := NewPCMBridge()
	audioInput, err := bridge.Start(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Close()
	d := &FFmpegDriver{Bin: ff, AudioInput: audioInput}
	t.Logf("PCM loopback=%s", audioInput)
	ec := EncoderConfig{Width: 320, Height: 568, FPS: 15, VideoBitrateKbps: 256, AudioBitrateKbps: 64}
	if err := d.Start(SourceStage, ec); err != nil {
		t.Fatal(err)
	}
	go d.WatchProgress(func(m Metrics) { t.Logf("ffmpeg metrics: %+v", m) }, func(line string) { t.Logf("ffmpeg stderr: %s", line) })
	waitErr := make(chan error, 1)
	go func() { waitErr <- d.Wait(); close(waitErr) }()
	defer func() { d.Kill(); <-waitErr }()
	client, err := DialRTMP("rtmp://127.0.0.1:1935/live", key, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.SendMeta(ec.Width, ec.Height, ec.FPS); err != nil {
		t.Fatal(err)
	}

	forwardErr := make(chan error, 1)
	go func() {
		br := bufio.NewReader(d.Stdout())
		if err := ReadFLVHeader(br); err != nil {
			forwardErr <- err
			return
		}
		for {
			tag, err := ReadFLVTag(br)
			if err != nil {
				forwardErr <- err
				return
			}
			if err := client.SendTag(tag); err != nil {
				forwardErr <- err
				return
			}
		}
	}()
	go feedInteropJPEG(ctx, d, ec)
	collector, err := bridge.Attach("stage-audio-interop", 1)
	if err != nil {
		t.Fatal(err)
	}
	defer bridge.Detach(collector)
	pcmErr := make(chan error, 1)
	go feedInteropPCM(ctx, collector, bridge, silent, pcmErr)

	wantPath := "live/" + key
	deadline := time.Now().Add(20 * time.Second)
	for {
		ready, tracks, bytesReceived, pathErr := mtxPath(wantPath)
		hasAudio := hasTrack(tracks, "aac") || hasTrack(tracks, "mpeg-4 audio") // MediaMTX 将 AAC 显示为 MPEG-4 Audio。
		if pathErr == nil && ready && bytesReceived > 0 && hasTrack(tracks, "h264") && hasAudio {
			t.Logf("STAGE_AUDIO_TRACKS path=%s tracks=%v bytesReceived=%d", wantPath, tracks, bytesReceived)
			break
		}
		select {
		case err := <-forwardErr:
			t.Fatalf("FLV/RTMP 转发失败: %v", err)
		case err := <-waitErr:
			t.Fatalf("FFmpeg 提前退出: %v", err)
		case err := <-pcmErr:
			bridge.mu.Lock()
			connected, queued, stats := bridge.conn != nil, len(bridge.frames), bridge.stats
			bridge.mu.Unlock()
			t.Fatalf("PCM 注入失败: %v connected=%v queued=%d stats=%+v", err, connected, queued, stats)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("MediaMTX 未观测到 H264/AAC 双轨: ready=%v tracks=%v bytes=%d err=%v", ready, tracks, bytesReceived, pathErr)
		}
		time.Sleep(200 * time.Millisecond)
	}

	probe := os.Getenv("DH_FFPROBE_BIN")
	if probe == "" {
		probe = filepath.Join(filepath.Dir(ff), "ffprobe")
		if runtime.GOOS == "windows" {
			probe += ".exe"
		}
	}
	probeCtx, probeCancel := context.WithTimeout(ctx, 20*time.Second)
	defer probeCancel()
	streamURL := "rtmp://127.0.0.1:1935/" + wantPath
	probeOutput, err := exec.CommandContext(probeCtx, probe,
		"-v", "error", "-rtmp_live", "live", "-i", streamURL,
		"-select_streams", "a:0", "-show_entries", "stream=codec_name,sample_rate,channels",
		"-of", "default=noprint_wrappers=1").CombinedOutput()
	if err != nil {
		t.Fatalf("ffprobe 音频流信息读取失败: %v\n%s", err, probeOutput)
	}
	t.Logf("ffprobe 音频流信息:\n%s", probeOutput)
	// ffprobe 不执行音频滤镜；用同版本 ffmpeg 解码实时流并运行 volumedetect。
	output, err := exec.CommandContext(probeCtx, ff,
		"-v", "info", "-rtmp_live", "live", "-i", streamURL,
		"-map", "0:a:0", "-t", "3", "-af", "volumedetect", "-f", "null", "-").CombinedOutput()
	if err != nil {
		t.Fatalf("ffmpeg volumedetect 失败: %v\n%s", err, output)
	}
	mean, max, ok := parseVolume(output)
	if !ok {
		t.Fatalf("ffmpeg 未给出 mean/max volume:\n%s", output)
	}
	t.Logf("ffmpeg volumedetect 原始输出:\n%s", output)
	return mean, max
}

func feedInteropJPEG(ctx context.Context, d *FFmpegDriver, ec EncoderConfig) {
	img := image.NewRGBA(image.Rect(0, 0, ec.Width, ec.Height))
	for y := 0; y < ec.Height; y++ {
		for x := 0; x < ec.Width; x++ {
			img.Set(x, y, color.RGBA{R: 24, G: 48, B: 72, A: 255})
		}
	}
	var frame bytes.Buffer
	_ = jpeg.Encode(&frame, img, &jpeg.Options{Quality: 80})
	ticker := time.NewTicker(time.Second / time.Duration(ec.FPS))
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := d.Stdin().Write(frame.Bytes()); err != nil {
				return
			}
		}
	}
}

func feedInteropPCM(ctx context.Context, collector *PCMCollector, bridge *PCMBridge, silent bool, result chan<- error) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	var sequence uint32
	var phase float64
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pcm := make([]byte, PCMFrameBytes)
			if !silent {
				for i := 0; i < PCMFrameBytes/2; i++ {
					sample := int16(math.Sin(phase) * 0.2 * math.MaxInt16)
					pcm[2*i] = byte(sample)
					pcm[2*i+1] = byte(uint16(sample) >> 8)
					phase += 2 * math.Pi * 440 / 48000
				}
			}
			if err := bridge.Write(collector, sequence, pcm); err != nil {
				result <- err
				return
			}
			sequence++
		}
	}
}

func hasTrack(tracks []string, want string) bool {
	for _, track := range tracks {
		if strings.EqualFold(track, want) {
			return true
		}
	}
	return false
}

func parseVolume(output []byte) (float64, float64, bool) {
	values := map[string]float64{}
	for _, match := range volumePattern.FindAllSubmatch(output, -1) {
		value, err := strconv.ParseFloat(string(match[2]), 64)
		if err == nil {
			values[string(match[1])] = value
		}
	}
	mean, meanOK := values["mean_volume"]
	max, maxOK := values["max_volume"]
	if !meanOK || !maxOK {
		return 0, 0, false
	}
	return mean, max, true
}
