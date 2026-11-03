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

// runFakeFFmpeg 假 ffmpeg：落 argv → stdout 写最小 FLV → stderr 写 -progress → 退出。
func runFakeFFmpeg() {
	if f := os.Getenv("HELPER_ARGVDUMP"); f != "" {
		_ = os.WriteFile(f, []byte(strings.Join(os.Args, "\n")), 0o600)
	}
	flv := buildFLV(
		Tag{Type: 18, Timestamp: 0, Data: []byte("meta")},
		Tag{Type: 9, Timestamp: 33, Data: []byte{0x17, 0x00}},
	)
	_, _ = os.Stdout.Write(flv)
	fmt.Fprintln(os.Stderr, "fps=29.97")
	fmt.Fprintln(os.Stderr, "bitrate=1498.5kbits/s")
	fmt.Fprintln(os.Stderr, "drop_frames=3")
	fmt.Fprintln(os.Stderr, "progress=continue")
	// 短暂驻留让 Publisher 读完
	time.Sleep(200 * time.Millisecond)
}
