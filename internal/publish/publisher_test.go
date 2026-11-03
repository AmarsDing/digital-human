package publish

// Publisher 端到端测试：假 ffmpeg（helper 进程，见 helper_test.go）+ 假 RTMP 服务器。
// 验证：状态机流转、重连退避、key 不进 argv/日志、指标解析。

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func helperPublisher(t *testing.T, srv *fakeRTMPServer, dump string, logs *syncedLog) *Publisher {
	t.Helper()
	t.Setenv("GO_WANT_HELPER", "1")
	t.Setenv("HELPER_ARGVDUMP", dump)
	return NewPublisher(Config{
		Platform:       "douyin",
		Source:         SourceSynthetic,
		FFmpegBin:      os.Args[0],
		FFmpegPrepend:  []string{"fake-ffmpeg"}, // 不被解析，仅作标记
		ConnectTimeout: 2 * time.Second,
		BackoffInit:    50 * time.Millisecond,
		BackoffMax:     100 * time.Millisecond,
		MaxRetries:     2,
		Logf: func(f string, a ...any) {
			logs.add(fmt.Sprintf(f, a...))
		},
	}, &MemTargetStore{})
}

type syncedLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *syncedLog) add(s string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, s)
}

func (l *syncedLog) all() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return strings.Join(l.lines, "\n")
}

func waitState(t *testing.T, p *Publisher, want State, timeout time.Duration) Status {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		st := p.Status()
		if st.State == want {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("等待状态 %s 超时，当前 %s（last=%s）", want, p.Status().State, p.Status().LastError)
	return Status{}
}

const testStreamKey = "sk_live_SUPERSECRET_9f8e7d"

func TestPublisherEndToEnd(t *testing.T) {
	srv := newFakeRTMPServer(t)
	defer srv.close()
	dump := filepath.Join(t.TempDir(), "argv.txt")
	logs := &syncedLog{}
	p := helperPublisher(t, srv, dump, logs)

	fp, err := p.SetTarget(Target{RTMPURL: srv.url(), Key: testStreamKey})
	if err != nil {
		t.Fatal(err)
	}
	if len(fp) != 8 {
		t.Fatalf("fingerprint: %q", fp)
	}

	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	// helper 每次跑 200ms 后退出 → 触发重连；MaxRetries=2 → 最终 stopped
	st := waitState(t, p, StateStopped, 15*time.Second)
	if st.LastError != "max_retries_exceeded" {
		t.Fatalf("LastError: %q", st.LastError)
	}
	p.Wait()

	// 1) 假 RTMP 服务器收到正确的 key 与 tag
	srv.mu.Lock()
	if srv.gotKey != testStreamKey {
		t.Fatalf("server key: %q", srv.gotKey)
	}
	if srv.gotVideo == 0 {
		t.Fatalf("server 未收到视频 tag（video=%d meta=%d audio=%d conn=%d）\nlogs:\n%s",
			srv.gotVideo, srv.gotMeta, srv.gotAudio, srv.connCount, logs.all())
	}
	if srv.connCount < 2 {
		t.Fatalf("重连未发生：connCount=%d", srv.connCount)
	}
	srv.mu.Unlock()

	// 2) ffmpeg argv 不含 key（核心红线）
	argvDump, err := os.ReadFile(dump)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(argvDump), testStreamKey) {
		t.Fatal("argv 中出现 stream key！")
	}
	if strings.Contains(string(argvDump), "rtmp://") {
		t.Fatal("argv 中出现推流地址！")
	}

	// 3) 日志不含 key
	if strings.Contains(logs.all(), testStreamKey) {
		t.Fatal("日志中出现 stream key！")
	}

	// 4) 指标被解析
	st = p.Status()
	if st.FPS == 0 || st.BitrateKbps == 0 || st.DroppedFrames != 3 {
		t.Fatalf("metrics: %+v", st)
	}
	if st.KeyFingerprint8 != fp {
		t.Fatalf("fingerprint: %q vs %q", st.KeyFingerprint8, fp)
	}
}

func TestPublisherRejectedIsTerminal(t *testing.T) {
	srv := newFakeRTMPServer(t)
	defer srv.close()
	srv.denyKey = testStreamKey
	logs := &syncedLog{}
	p := helperPublisher(t, srv, filepath.Join(t.TempDir(), "argv.txt"), logs)
	if _, err := p.SetTarget(Target{RTMPURL: srv.url(), Key: testStreamKey}); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	st := waitState(t, p, StateRejected, 10*time.Second)
	if st.LastError != "publish_rejected" {
		t.Fatalf("LastError: %q", st.LastError)
	}
	p.Wait()
	// rejected 不重连
	srv.mu.Lock()
	defer srv.mu.Unlock()
	if srv.connCount != 1 {
		t.Fatalf("rejected 后不应重连：connCount=%d", srv.connCount)
	}
}

func TestPublisherStartWithoutTarget(t *testing.T) {
	p := NewPublisher(Config{}, &MemTargetStore{})
	if err := p.Start(t.Context()); err == nil {
		t.Fatal("未配置目标应报错")
	}
}

func TestPublisherStartIdempotent(t *testing.T) {
	srv := newFakeRTMPServer(t)
	defer srv.close()
	logs := &syncedLog{}
	p := helperPublisher(t, srv, filepath.Join(t.TempDir(), "argv.txt"), logs)
	if _, err := p.SetTarget(Target{RTMPURL: srv.url(), Key: testStreamKey}); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(t.Context()); err != nil {
		t.Fatalf("重复 Start 应幂等: %v", err)
	}
	_ = p.Stop()
	p.Wait()
	if st := p.Status(); st.State != StateStopped {
		t.Fatalf("want stopped, got %s", st.State)
	}
	// stopped 后可再次 Start（幂等重开）
	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitState(t, p, StateStopped, 15*time.Second)
	p.Wait()
}

func TestCheckStartLive(t *testing.T) {
	p := NewPublisher(Config{}, &MemTargetStore{})
	// idle 拒绝
	if err := CheckStartLive(p, false); err == nil ||
		!strings.Contains(err.Error(), "开始推流") {
		t.Fatalf("idle: %v", err)
	}
	// 显式开关放行（本地预览）
	if err := CheckStartLive(p, true); err != nil {
		t.Fatalf("allowUnpushed: %v", err)
	}
	// nil gate 拒绝
	if err := CheckStartLive(nil, false); err == nil {
		t.Fatal("nil gate 应拒绝")
	}
}
