package publish

import (
	"strings"
	"testing"
)

func TestStageFFmpegMapsJPEGAndRemotePCMInputs(t *testing.T) {
	d := &FFmpegDriver{AudioInput: "tcp://127.0.0.1:43123"}
	args := d.buildArgs(SourceStage, EncoderConfig{})
	joined := strings.Join(args, " ")

	for _, want := range []string{
		"-f image2pipe -framerate 30 -analyzeduration 0 -probesize 32 -vcodec mjpeg -i pipe:0",
		"-f s16le -ar 48000 -ac 1 -i tcp://127.0.0.1:43123",
		"-map 0:v -map 1:a",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("SourceStage argv 缺少 %q:\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "anullsrc") {
		t.Fatalf("公开音轨不得再由静音发生器替代: %s", joined)
	}
	if strings.Contains(joined, "rtmp://") || strings.Contains(joined, "sk_live") {
		t.Fatalf("argv 不得包含推流地址或 key: %s", joined)
	}
}

func TestStageFFmpegRequiresRemotePCMBridge(t *testing.T) {
	d := &FFmpegDriver{}
	if err := d.validateSource(SourceStage); err == nil {
		t.Fatal("SourceStage 缺少 PCM bridge 地址时必须 fail-closed")
	}
}
