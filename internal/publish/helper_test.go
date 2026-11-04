package publish

// 假 ffmpeg helper：TestMain 在 flag.Parse 之前拦截，
// 避免 go test 的 flag 包对 ffmpeg 参数报"flag provided but not defined"。

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

// TestMain 包级入口：helper 模式下充当假 ffmpeg，否则正常跑测试。
func TestMain(m *testing.M) {
	if os.Getenv("GO_WANT_HELPER") == "1" {
		runFakeFFmpeg()
		os.Exit(0)
	}
	os.Exit(m.Run())
}

var helperAVCConfig = []byte{
	0x17, 0x00, 0x00, 0x00, 0x00,
	0x01, 0x42, 0xc0, 0x28, 0xff, 0xe1, 0x00, 0x19,
	0x67, 0x42, 0xc0, 0x28, 0xd9, 0x00, 0x78, 0x02, 0x27, 0xe5,
	0x84, 0x00, 0x00, 0x03, 0x00, 0x04, 0x00, 0x00, 0x03, 0x00,
	0xf0, 0x3c, 0x60, 0xc9, 0x20, 0x01, 0x00, 0x04, 0x08, 0x06, 0x07, 0x08,
}

// runFakeFFmpeg 假 ffmpeg：落 argv → stdout 写最小 FLV → stderr 写 -progress → 退出。
func runFakeFFmpeg() {
	if f := os.Getenv("HELPER_ARGVDUMP"); f != "" {
		_ = os.WriteFile(f, []byte(strings.Join(os.Args, "\n")), 0o600)
	}
	if raw := os.Getenv("HELPER_MEDIA_DELAY"); raw != "" {
		delay, err := time.ParseDuration(raw)
		if err == nil {
			time.Sleep(delay)
		}
	}
	var tags []Tag
	if os.Getenv("HELPER_NO_MEDIA") != "1" {
		tags = append(tags, Tag{Type: 9, Timestamp: 33, Data: helperAVCConfig})
	}
	_, _ = os.Stdout.Write(buildFLV(tags...))
	fmt.Fprintln(os.Stderr, "fps=29.97")
	fmt.Fprintln(os.Stderr, "bitrate=1498.5kbits/s")
	fmt.Fprintln(os.Stderr, "drop_frames=3")
	fmt.Fprintln(os.Stderr, "progress=continue")
	// 短暂驻留让 Publisher 读完；无媒体模式给首媒体超时留出明确窗口。
	hold := 300 * time.Millisecond
	if raw := os.Getenv("HELPER_HOLD"); raw != "" {
		if configured, err := time.ParseDuration(raw); err == nil {
			hold = configured
		}
	}
	time.Sleep(hold)
}
