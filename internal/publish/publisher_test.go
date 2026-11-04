package publish

// Publisher 端到端测试：假 ffmpeg（helper 进程，见 helper_test.go）+ 假 RTMP 服务器。
// 验证：状态机流转、重连退避、key 不进 argv/日志、指标解析。

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	streamv1 "github.com/teamwork/digital-human/api/gen/go/stream/v1"
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
	if got := waitState(t, p, StatePushing, 10*time.Second).TargetVerification; got != TargetLocal {
		t.Fatalf("RTMP 发布回执后应记录本地协议验证，got=%q", got)
	}
	// helper 每轮都有首媒体，退出后重连；成功会话不会累计到 MaxRetries。
	deadline := time.Now().Add(10 * time.Second)
	for {
		srv.mu.Lock()
		connections := srv.connCount
		srv.mu.Unlock()
		if connections >= 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("重连未发生：connCount=%d\nlogs:\n%s", connections, logs.all())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if err := p.Stop(); err != nil {
		t.Fatal(err)
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
	st := p.Status()
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
	// stopped 后可再次 Start（幂等重开）；本轮有首媒体，断线不会耗尽连续失败次数。
	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitState(t, p, StatePushing, 10*time.Second)
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	p.Wait()
}

func TestPublisherWaitsForFirstMediaBeforePushing(t *testing.T) {
	srv := newFakeRTMPServer(t)
	defer srv.close()
	t.Setenv("HELPER_MEDIA_DELAY", "250ms")
	logs := &syncedLog{}
	p := helperPublisher(t, srv, filepath.Join(t.TempDir(), "argv.txt"), logs)
	if _, err := p.SetTarget(Target{RTMPURL: srv.url(), Key: testStreamKey}); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = p.Stop()
		p.Wait()
	}()

	deadline := time.Now().Add(3 * time.Second)
	for {
		srv.mu.Lock()
		gotMeta := srv.gotMeta > 0
		srv.mu.Unlock()
		if gotMeta {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("等待 RTMP metadata 超时")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if st := p.Status(); st.State != StateStarting {
		t.Fatalf("首媒体前必须保持 starting，got=%s", st.State)
	}
	waitState(t, p, StatePushing, 3*time.Second)
}

func TestFirstMediaTimeoutBecomesDegraded(t *testing.T) {
	srv := newFakeRTMPServer(t)
	defer srv.close()
	t.Setenv("HELPER_NO_MEDIA", "1")
	logs := &syncedLog{}
	p := helperPublisher(t, srv, filepath.Join(t.TempDir(), "argv.txt"), logs)
	p.cfg.FirstMediaTimeout = 50 * time.Millisecond
	p.cfg.BackoffInit = 5 * time.Second
	p.cfg.BackoffMax = 5 * time.Second
	events := make(chan Event, 16)
	p.m.onEvent = func(e Event) { events <- e }
	if _, err := p.SetTarget(Target{RTMPURL: srv.url(), Key: testStreamKey}); err != nil {
		t.Fatal(err)
	}
	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	defer func() {
		_ = p.Stop()
		p.Wait()
	}()

	var sawPushing bool
	timer := time.NewTimer(3 * time.Second)
	defer timer.Stop()
	for {
		select {
		case e := <-events:
			if e.State == StatePushing {
				sawPushing = true
			}
			if e.State != StateDegraded {
				continue
			}
			if sawPushing {
				t.Fatal("无媒体会话不得进入 pushing")
			}
			if !strings.Contains(e.Reason, "publish_first_media_timeout") ||
				!strings.Contains(e.Reason, "未收到媒体") {
				t.Fatalf("degraded 原因不可读或不精确: %q", e.Reason)
			}
			return
		case <-timer.C:
			t.Fatalf("未等到首媒体超时 degraded；state=%s last=%q logs:\n%s",
				p.Status().State, p.Status().LastError, logs.all())
		}
	}
}

func TestRetryAfterEstablishedSessionStartsAtOne(t *testing.T) {
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
	defer func() {
		_ = p.Stop()
		p.Wait()
	}()

	deadline := time.Now().Add(5 * time.Second)
	for strings.Count(logs.all(), "后第 1 次重连") < 2 {
		if time.Now().After(deadline) {
			t.Fatalf("成功会话后的重试未从 1 开始:\n%s", logs.all())
		}
		time.Sleep(10 * time.Millisecond)
	}
	if strings.Contains(logs.all(), "后第 2 次重连") {
		t.Fatalf("成功会话不应累计连续失败次数:\n%s", logs.all())
	}
}

func TestTargetVerificationLifecycle(t *testing.T) {
	store := &MemTargetStore{}
	p := NewPublisher(Config{}, store)
	p.m.setVerification(TargetDouyin)
	if _, err := p.SetTarget(Target{RTMPURL: "rtmp://127.0.0.1/live", Key: "first"}); err != nil {
		t.Fatal(err)
	}
	if got := p.Status().TargetVerification; got != TargetUnknown {
		t.Fatalf("更换目标后必须清除验证状态，got=%q", got)
	}

	p.m.setVerification(TargetDouyin)
	if err := p.m.transit(StateStarting, ""); err != nil {
		t.Fatal(err)
	}
	if err := p.m.transit(StatePushing, ""); err != nil {
		t.Fatal(err)
	}
	_, err := p.SetTarget(Target{RTMPURL: "rtmp://127.0.0.1/other", Key: "second"})
	if ErrorCode(err) != "publish_target_active" {
		t.Fatalf("活动会话更换目标应精确拒绝，err=%v code=%q", err, ErrorCode(err))
	}
	got, _ := store.Load()
	if got.RTMPURL != "rtmp://127.0.0.1/live" || got.Key != "first" {
		t.Fatalf("拒绝更换目标时不得覆盖原目标: %+v", got)
	}
}

func TestStartClearsTargetVerification(t *testing.T) {
	store := &MemTargetStore{}
	if err := store.Save(Target{RTMPURL: "rtmp://127.0.0.1/live", Key: "secret"}); err != nil {
		t.Fatal(err)
	}
	p := NewPublisher(Config{}, store)
	started := make(chan struct{})
	release := make(chan struct{})
	p.dialRTMP = func(string, string, time.Duration) (rtmpConn, error) {
		close(started)
		<-release
		return nil, errors.New("dial failed")
	}
	p.m.setVerification(TargetDouyin)
	if err := p.Start(t.Context()); err != nil {
		t.Fatal(err)
	}
	<-started
	if got := p.Status().TargetVerification; got != TargetUnknown {
		t.Fatalf("重新 Start 必须清除旧验证状态，got=%q", got)
	}
	close(release)
	if err := p.Stop(); err != nil {
		t.Fatal(err)
	}
	p.Wait()
}

func TestStreamServiceStatusIncludesTargetVerification(t *testing.T) {
	p := NewPublisher(Config{}, &MemTargetStore{})
	p.m.setVerification(TargetLocal)
	reply, err := NewStreamService(p).GetPushStatus(t.Context(), &streamv1.GetPushStatusRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if reply.TargetVerification != string(TargetLocal) {
		t.Fatalf("target_verification=%q, want=%q", reply.TargetVerification, TargetLocal)
	}
}

type staticPushGate struct{ st Status }

func (g staticPushGate) Status() Status { return g.st }

func TestCheckStartLiveRequiresDouyinVerification(t *testing.T) {
	tests := []struct {
		name         string
		verification TargetVerification
		allow        bool
		wantCode     string
	}{
		{name: "本地协议成功不解锁", verification: TargetLocal, wantCode: "publish_target_unverified"},
		{name: "本地预览开关不解锁未验证推流", verification: TargetLocal, allow: true, wantCode: "publish_target_unverified"},
		{name: "未识别域名不解锁", verification: TargetUnknown, wantCode: "publish_target_unverified"},
		{name: "真实抖音回执解锁", verification: TargetDouyin},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := staticPushGate{st: Status{State: StatePushing, TargetVerification: tt.verification}}
			err := CheckStartLive(g, tt.allow)
			if tt.wantCode == "" && err != nil {
				t.Fatal(err)
			}
			if tt.wantCode != "" && ErrorCode(err) != tt.wantCode {
				t.Fatalf("err=%v code=%q", err, ErrorCode(err))
			}
		})
	}
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
