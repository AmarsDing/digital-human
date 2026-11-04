package publish

// 真实协议互验测试（非默认跑）：本包 RTMP 客户端 → 真实 MediaMTX 接收端。
// helper 只产生 FLV 协议样本，不代表真实编码或真实平台验证；真实 ffmpeg 用例单独门控。
// 需要环境变量（缺一即 skip）：
//   DH_INTEROP=1            总开关
//   DH_FFMPEG_BIN           仅真实 ffmpeg 用例需要的 ffmpeg.exe 绝对路径
//   DH_MEDIAMTX_BIN         mediamtx.exe 绝对路径
//   DH_MEDIAMTX_CONF        mediamtx.yml 绝对路径
// 断言：连接成功、MediaMTX 侧读到流（tracks/bytesReceived）、本地验证等级、
// 公开开播阻断；真实 ffmpeg 用例另验指标、argv 无 key、断流重连。

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

const interopKey = "interop-key-9f3b7c21" // 测试专用假 key，非真实凭据

func interopEnv(t *testing.T) (ff, mtx, conf string) {
	t.Helper()
	if os.Getenv("DH_INTEROP") != "1" {
		t.Skip("DH_INTEROP!=1，跳过真实互验")
	}
	ff = os.Getenv("DH_FFMPEG_BIN")
	mtx = os.Getenv("DH_MEDIAMTX_BIN")
	conf = os.Getenv("DH_MEDIAMTX_CONF")
	if ff == "" || mtx == "" || conf == "" {
		t.Skip("缺少 DH_FFMPEG_BIN / DH_MEDIAMTX_BIN / DH_MEDIAMTX_CONF")
	}
	return
}

type mtxProcess struct {
	cmd *exec.Cmd
}

func startMediaMTX(t *testing.T, bin, conf string) *mtxProcess {
	t.Helper()
	cmd := exec.Command(bin, conf)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动 MediaMTX 失败: %v", err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get("http://127.0.0.1:9997/v3/paths/list")
		if err == nil {
			resp.Body.Close()
			return &mtxProcess{cmd: cmd}
		}
		time.Sleep(200 * time.Millisecond)
	}
	_ = cmd.Process.Kill()
	t.Fatal("MediaMTX API 10s 内未就绪")
	return nil
}

func (m *mtxProcess) stop() {
	if m.cmd != nil && m.cmd.Process != nil {
		_ = m.cmd.Process.Kill()
		_ = m.cmd.Wait()
	}
}

// mtxPath 返回指定 path 的 MediaMTX 侧观测（存在性、tracks、已收字节）。
func mtxPath(name string) (ready bool, tracks []string, bytesRecv int64, err error) {
	resp, err := http.Get("http://127.0.0.1:9997/v3/paths/list")
	if err != nil {
		return false, nil, 0, err
	}
	defer resp.Body.Close()
	var out struct {
		Items []struct {
			Name          string   `json:"name"`
			Ready         bool     `json:"ready"`
			Tracks        []string `json:"tracks"`
			BytesReceived int64    `json:"bytesReceived"`
		} `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false, nil, 0, err
	}
	for _, it := range out.Items {
		if it.Name == name {
			return it.Ready, it.Tracks, it.BytesReceived, nil
		}
	}
	return false, nil, 0, fmt.Errorf("path %q 不存在", name)
}

// waitState 复用 publisher_test.go 中的实现。

// ffmpegArgv 取当前所有 ffmpeg 进程命令行（WMI）。
func ffmpegArgv(t *testing.T) []string {
	t.Helper()
	out, err := exec.Command("powershell", "-NoProfile", "-Command",
		`(Get-CimInstance Win32_Process -Filter "Name='ffmpeg.exe'" | ForEach-Object { $_.CommandLine }) -join [char]10`).Output()
	if err != nil {
		t.Fatalf("读取 ffmpeg 命令行失败: %v", err)
	}
	var lines []string
	for _, l := range strings.Split(string(out), "\n") {
		if l = strings.TrimSpace(l); l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// TestInteropRTMPMediaMTX 只验证真实 RTMP 服务端互操作，不经过 ffmpeg。
// 用于无 ffmpeg 环境：握手/发布/收流/断连均为真实网络行为。
func TestInteropRTMPMediaMTX(t *testing.T) {
	if os.Getenv("DH_INTEROP") != "1" {
		t.Skip("DH_INTEROP!=1，跳过真实互验")
	}
	mtx, conf := os.Getenv("DH_MEDIAMTX_BIN"), os.Getenv("DH_MEDIAMTX_CONF")
	if mtx == "" || conf == "" {
		t.Skip("缺少 DH_MEDIAMTX_BIN / DH_MEDIAMTX_CONF")
	}

	server := startMediaMTX(t, mtx, conf)
	defer server.stop()

	// 本地协议互验使用的公开 path 名，不是真实推流凭据。
	const pathKey = "protocol-interop"
	client, err := DialRTMP("rtmp://127.0.0.1:1935/live", pathKey, 5*time.Second)
	if err != nil {
		t.Fatalf("RTMP 握手/发布失败: %v", err)
	}
	t.Logf("RTMP 握手、connect、createStream、publish 均成功 streamID=%d inChunk=%d outChunk=%d",
		client.streamID, client.inChunk, client.chunkSize)

	if err := client.SendMeta(720, 1280, 30); err != nil {
		t.Fatal(err)
	}
	// SPS/PPS 取自 MediaMTX 内置默认参数集，避免手工样本失真。
	avcConfig := []byte{
		0x17, 0x00, 0x00, 0x00, 0x00,
		0x01, 0x42, 0xc0, 0x28, 0xff, 0xe1, 0x00, 0x19,
		0x67, 0x42, 0xc0, 0x28, 0xd9, 0x00, 0x78, 0x02, 0x27, 0xe5,
		0x84, 0x00, 0x00, 0x03, 0x00, 0x04, 0x00, 0x00, 0x03, 0x00,
		0xf0, 0x3c, 0x60, 0xc9, 0x20, 0x01, 0x00, 0x04, 0x08, 0x06, 0x07, 0x08,
	}
	if err := client.SendTag(Tag{Type: 9, Data: avcConfig}); err != nil {
		t.Fatal(err)
	}
	if err := client.SendTag(Tag{Type: 8, Data: []byte{0xaf, 0x00, 0x12, 0x10}}); err != nil {
		t.Fatal(err)
	}
	// gortmplib 需要观察到至少 2 秒 DTS 跨度才完成轨道识别。
	for i := uint32(0); i <= 5; i++ {
		ts := i * 500
		// 5009 字节 video tag 跨越客户端 4096 字节 chunk 边界，覆盖真实大帧续片。
		video := make([]byte, 5009)
		copy(video, []byte{0x17, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x13, 0x88, 0x65})
		if err := client.SendTag(Tag{Type: 9, Timestamp: ts, Data: video}); err != nil {
			t.Fatal(err)
		}
		audio := []byte{0xaf, 0x01, 0x21, 0x10, 0x04, 0x60, 0x8c, 0x1c}
		if err := client.SendTag(Tag{Type: 8, Timestamp: ts, Data: audio}); err != nil {
			t.Fatal(err)
		}
	}

	resp, err := http.Get("http://127.0.0.1:9997/v3/rtmpconns/list")
	if err != nil {
		t.Fatal(err)
	}
	var connStats json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&connStats); err != nil {
		resp.Body.Close()
		t.Fatal(err)
	}
	resp.Body.Close()
	t.Logf("MediaMTX RTMP 连接统计: %s", connStats)

	wantPath := "live/" + pathKey
	var b1 int64
	var lastReady bool
	var lastTracks []string
	var lastBytes int64
	var lastErr error
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		lastReady, lastTracks, lastBytes, lastErr = mtxPath(wantPath)
		if lastErr == nil && lastReady && len(lastTracks) == 2 && lastBytes > 0 {
			b1 = lastBytes
			t.Logf("MediaMTX 已注册 path ready=%v tracks=%v bytesReceived=%d", lastReady, lastTracks, lastBytes)
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if b1 == 0 {
		t.Fatalf("MediaMTX 未观测到双轨就绪 path %q: ready=%v tracks=%v bytesReceived=%d err=%v",
			wantPath, lastReady, lastTracks, lastBytes, lastErr)
	}

	if err := client.Close(); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		ready, _, _, err := mtxPath(wantPath)
		if err != nil || !ready {
			t.Logf("RTMP 断连后 MediaMTX path 已撤销 ready=%v err=%v", ready, err)
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("RTMP 断连后 MediaMTX path 仍为 ready")
}

func TestInteropPublisherLocalVerification(t *testing.T) {
	if os.Getenv("DH_INTEROP") != "1" {
		t.Skip("DH_INTEROP!=1，跳过真实互验")
	}
	mtx, conf := os.Getenv("DH_MEDIAMTX_BIN"), os.Getenv("DH_MEDIAMTX_CONF")
	if mtx == "" || conf == "" {
		t.Skip("缺少 DH_MEDIAMTX_BIN / DH_MEDIAMTX_CONF")
	}
	t.Setenv("GO_WANT_HELPER", "1")
	t.Setenv("HELPER_HOLD", "3s")

	server := startMediaMTX(t, mtx, conf)
	defer server.stop()
	p := NewPublisher(Config{
		Source:            SourceSynthetic,
		FFmpegBin:         os.Args[0],
		FFmpegPrepend:     []string{"fake-ffmpeg"},
		FirstMediaTimeout: 2 * time.Second,
		BackoffInit:       5 * time.Second,
		Logf:              func(f string, a ...any) { t.Logf(f, a...) },
	}, &MemTargetStore{})
	if _, err := p.SetTarget(Target{RTMPURL: "rtmp://127.0.0.1:1935/live", Key: interopKey}); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.Stop(); p.Wait() }()

	st := waitState(t, p, StatePushing, 10*time.Second)
	if st.TargetVerification != TargetLocal {
		t.Fatalf("本地 MediaMTX 验证等级=%q, want=%q", st.TargetVerification, TargetLocal)
	}
	err := CheckStartLive(p, false)
	if ErrorCode(err) != "publish_target_unverified" {
		t.Fatalf("本地协议成功不得解锁公开开播: err=%v code=%q", err, ErrorCode(err))
	}
	t.Logf("真实 MediaMTX 已进入 %s，target_verification=%s；公开开播精确阻断: %v",
		st.State, st.TargetVerification, err)
}

func TestInteropRealFFmpegMediaMTX(t *testing.T) {
	ff, mtx, conf := interopEnv(t)

	server := startMediaMTX(t, mtx, conf)
	defer server.stop()

	store := &MemTargetStore{}
	if err := store.Save(Target{RTMPURL: "rtmp://127.0.0.1:1935/live", Key: interopKey}); err != nil {
		t.Fatal(err)
	}
	p := NewPublisher(Config{
		Source:      SourceSynthetic,
		FFmpegBin:   ff,
		BackoffInit: 1 * time.Second,
		BackoffMax:  4 * time.Second,
		MaxRetries:  10,
		Logf:        func(f string, a ...any) { t.Logf(f, a...) },
	}, store)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := p.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer func() { _ = p.Stop(); p.Wait() }()

	// 1) 连接成功并进入 pushing
	st := waitState(t, p, StatePushing, 45*time.Second)
	t.Logf("状态=%s key指纹=%s", st.State, st.KeyFingerprint8)

	// 2) MediaMTX 侧读到流：tracks + 持续增长的 bytesReceived
	wantPath := "live/" + interopKey
	var tracks []string
	var b1 int64
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		ready, tr, br, err := mtxPath(wantPath)
		if err == nil && ready && br > 0 {
			tracks, b1 = tr, br
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if tracks == nil {
		t.Fatalf("MediaMTX 未观测到就绪的 path %q", wantPath)
	}
	time.Sleep(3 * time.Second)
	_, _, b2, err := mtxPath(wantPath)
	if err != nil {
		t.Fatal(err)
	}
	if b2 <= b1 {
		t.Fatalf("bytesReceived 未增长（%d → %d），无实际数据到达", b1, b2)
	}
	t.Logf("MediaMTX tracks=%v bytesReceived %d→%d", tracks, b1, b2)

	// 3) 指标采样（真实 ffmpeg -progress）
	deadline = time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		st = p.Status()
		if st.FPS > 0 && st.BitrateKbps > 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if st.FPS <= 0 || st.BitrateKbps <= 0 {
		t.Fatalf("指标采样失败: %+v", st)
	}
	t.Logf("指标: fps=%.1f bitrate=%dkbps dropped=%d", st.FPS, st.BitrateKbps, st.DroppedFrames)

	// 4) argv 无 key 取证（真实进程命令行）
	argv := ffmpegArgv(t)
	if len(argv) == 0 {
		t.Fatal("未找到运行中的 ffmpeg 进程")
	}
	for _, a := range argv {
		if strings.Contains(a, interopKey) || strings.Contains(a, "rtmp://") {
			t.Fatalf("ffmpeg argv 泄漏敏感信息: %s", a)
		}
	}
	t.Logf("ffmpeg argv（实际命令行，无 key）: %s", argv[0])

	// 5) 断流 → degraded → 重连成功
	server.stop()
	waitState(t, p, StateDegraded, 20*time.Second)
	t.Log("断流后进入 degraded（符合预期）")

	server = startMediaMTX(t, mtx, conf)
	defer server.stop()
	st = waitState(t, p, StatePushing, 30*time.Second)
	t.Logf("重连成功，状态回到 %s", st.State)

	ready, _, b3, err := mtxPath(wantPath)
	if err != nil || !ready || b3 <= 0 {
		t.Fatalf("重连后 MediaMTX 未恢复收流: ready=%v bytes=%d err=%v", ready, b3, err)
	}
	t.Logf("重连后 MediaMTX 恢复收流 bytesReceived=%d", b3)
}
