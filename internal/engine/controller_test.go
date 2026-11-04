package engine

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/engine/tts"
	"github.com/teamwork/digital-human/internal/engine/vidu"
	"github.com/teamwork/digital-human/pkg/audio"
)

// stubTTS 返回固定 wav（24k/s16le/mono），长度可调。
type stubTTS struct {
	wav []byte
	err error
}

func (s stubTTS) Synthesize(context.Context, tts.JobRequest) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.wav, nil
}

type streamingSynthesizerFunc func(context.Context, tts.JobRequest, func(context.Context, []byte) error) error

func (f streamingSynthesizerFunc) SynthesizeStream(ctx context.Context, req tts.JobRequest, emit func(context.Context, []byte) error) error {
	return f(ctx, req, emit)
}

// fakeWS 服务端按连接记录：type7/type10/二进制帧/ping 次数，可控 conn_init 行为。
type fakeWS struct {
	srv *httptest.Server
	url string

	mu     sync.Mutex
	conns  int
	inits  int
	type7  int
	type9  int
	type10 int
	bin    int
	ping   int

	authorization string

	type11        int             // type11 prompt_operation 帧数（G3-1）
	lastType11Raw json.RawMessage // 最近一帧 type11 的 payload

	notReadyBeforeAck int // 前 N 次 conn_init 回 NOT_READY
	closeAfterInit    int // 前 N 条连接在 init 成功后立即关闭
}

func newFakeWS(t *testing.T) *fakeWS {
	t.Helper()
	f := &fakeWS{}
	up := websocket.Upgrader{CheckOrigin: func(*http.Request) bool { return true }}
	f.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.authorization = r.Header.Get("Authorization")
		f.mu.Unlock()
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		f.mu.Lock()
		f.conns++
		connNo := f.conns
		f.mu.Unlock()
		c.SetPingHandler(func(data string) error {
			f.mu.Lock()
			f.ping++
			f.mu.Unlock()
			return c.WriteControl(websocket.PongMessage, []byte(data), time.Now().Add(time.Second))
		})
		for {
			mt, data, err := c.ReadMessage()
			if err != nil {
				return
			}
			if mt == websocket.BinaryMessage {
				f.mu.Lock()
				f.bin++
				f.mu.Unlock()
				continue
			}
			var s vidu.Signal
			if err := json.Unmarshal(data, &s); err != nil {
				continue
			}
			f.mu.Lock()
			switch s.Type {
			case vidu.TypeInterrupt:
				f.type7++
			case vidu.TypeInputText:
				f.type9++
			case vidu.TypeOutputText:
				f.type10++
			case vidu.TypePromptOperation:
				f.type11++
				f.lastType11Raw = append(json.RawMessage(nil), s.Payload...)
			}
			f.mu.Unlock()
			if s.Type != vidu.TypeConnInit {
				continue
			}
			f.mu.Lock()
			f.inits++
			n := f.inits
			notReady := f.notReadyBeforeAck > 0 && n <= f.notReadyBeforeAck
			closeAfter := f.closeAfterInit >= connNo
			f.mu.Unlock()
			if notReady {
				_ = c.WriteMessage(websocket.TextMessage, initAck(false))
				continue
			}
			_ = c.WriteMessage(websocket.TextMessage, initAck(true))
			if closeAfter {
				return // defer c.Close() 触发对端断连
			}
		}
	}))
	f.url = "ws" + strings.TrimPrefix(f.srv.URL, "http")
	t.Cleanup(f.srv.Close)
	return f
}

func initAck(ok bool) []byte {
	code := ""
	if !ok {
		code = "NOT_READY"
	}
	p, _ := json.Marshal(map[string]any{"conn_init_ack": map[string]any{
		"success": ok, "error_code": code, "error_msg": "",
	}})
	f, _ := json.Marshal(vidu.Signal{Type: vidu.TypeConnInitAck, Payload: p})
	return f
}

func (f *fakeWS) counts() (conns, inits, type7, type10, bin, ping int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.conns, f.inits, f.type7, f.type10, f.bin, f.ping
}

func (f *fakeWS) countType9() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.type9
}

func (f *fakeWS) authorizationHeader() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.authorization
}

// type11Stats 返回 type11 帧数与最近一帧 payload（G3-1 断言用）。
func (f *fakeWS) type11Stats() (int, json.RawMessage) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.type11, append(json.RawMessage(nil), f.lastType11Raw...)
}

// fakeViduHTTP 只应答建场接口。
func fakeViduHTTP(t *testing.T) *vidu.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"live":{"id":"live-1"}}`))
	}))
	t.Cleanup(srv.Close)
	return vidu.NewClient(vidu.Options{APIKey: "k", BaseURL: srv.URL})
}

func newTestController(t *testing.T, ws *fakeWS, ttss Synthesizer, tweak func(*ViduConfig)) *ViduController {
	t.Helper()
	cfg := ViduConfig{
		Client: fakeViduHTTP(t),
		WS: vidu.WSOptions{
			URL:              ws.url,
			PingInterval:     50 * time.Millisecond,
			PingTimeout:      5 * time.Second,
			ConnInitRetries:  5,
			ConnInitInterval: 10 * time.Millisecond,
		},
		TTS: ttss,
		Ownership: OwnershipConfig{
			Rounds:    2,
			RoundWait: 30 * time.Millisecond,
			Quiet:     5 * time.Millisecond,
		},
		// 测试节奏：缩短看门狗相关常量。
		WatchdogTick:    10 * time.Millisecond,
		StallIdleRecv:   time.Hour, // 默认不在本测试触发 stall
		StallMinWindow:  30 * time.Millisecond,
		StallMinSamples: 3,
		StallMaxRate:    0.55,
		MaxReconnects:   3,
		ReconnectMinGap: 5 * time.Millisecond,
		StallSuppress:   time.Millisecond,
		GuardInterval:   20 * time.Millisecond,
	}
	if tweak != nil {
		tweak(&cfg)
	}
	c, err := NewViduController(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func waitFor(t *testing.T, d time.Duration, fn func() bool, msg string) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timeout waiting: %s", msg)
}

type stubGreetingDecoder struct {
	pcm []byte
	err error
}

func (d stubGreetingDecoder) Decode(context.Context, string) ([]byte, error) { return d.pcm, d.err }
func (d stubGreetingDecoder) Name() string                                   { return "stub" }

func TestPlayGreetingUsesFileDecoderWithoutTTS(t *testing.T) {
	tests := []struct {
		name    string
		decoder stubGreetingDecoder
		wantErr bool
	}{
		{name: "合法文件直接喂流", decoder: stubGreetingDecoder{pcm: audio.TonePCM(120)}},
		{name: "损坏或不支持显式报错", decoder: stubGreetingDecoder{err: errors.New("decode failed")}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ws := newFakeWS(t)
			var ttsCalls int
			ctl := newTestController(t, ws, synthesizerFunc(func(context.Context, tts.JobRequest) ([]byte, error) {
				ttsCalls++
				return audio.WavBytes(audio.TonePCM(100)), nil
			}), func(cfg *ViduConfig) { cfg.GreetingDecoder = tt.decoder })
			if _, err := ctl.Start(context.Background(), live.StartOpts{SessionID: "s-greeting"}); err != nil {
				t.Fatal(err)
			}
			defer ctl.Stop(context.Background(), "test done")
			_, err := ctl.PlayGreeting(context.Background(), "greeting.wav")
			if (err != nil) != tt.wantErr {
				t.Fatalf("PlayGreeting err=%v, wantErr=%v", err, tt.wantErr)
			}
			if ttsCalls != 0 {
				t.Fatalf("文件开场白不得调用 TTS, calls=%d", ttsCalls)
			}
			_, _, _, type10, bin, _ := ws.counts()
			if type10 != 0 {
				t.Fatalf("文件开场白不得发送口播文本 type10=%d", type10)
			}
			if !tt.wantErr && bin == 0 {
				t.Fatal("合法音频文件必须真实喂流")
			}
		})
	}
}

func TestControllerStartSayFeedsPCM(t *testing.T) {
	ws := newFakeWS(t)
	pcm := audio.TonePCM(220) // 220ms ≈ 10560B，>2 个 100ms 分片
	ctl := newTestController(t, ws, stubTTS{wav: audio.WavBytes(pcm)}, nil)

	liveID, err := ctl.Start(context.Background(), live.StartOpts{SessionID: "s-1"})
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if liveID != "live-1" {
		t.Fatalf("live_id=%s", liveID)
	}
	defer ctl.Stop(context.Background(), "test done")

	id, err := ctl.Say(context.Background(), "你好", "say")
	if err != nil {
		t.Fatalf("say: %v", err)
	}
	if id == "" {
		t.Fatal("utterance id empty")
	}
	_, _, type7, type10, bin, _ := ws.counts()
	if type10 != 1 {
		t.Fatalf("type10=%d want 1", type10)
	}
	if bin < 2 {
		t.Fatalf("binary frames=%d want >=2 (220ms at 100ms chunk)", bin)
	}
	if type7 != 0 {
		t.Fatalf("type7=%d want 0（无抢占不该发中断）", type7)
	}
	st := ctl.RuntimeStatus(context.Background())
	if !st.FeedKnown || !st.WSKnown || st.WSState != "connected" {
		t.Fatalf("runtime status: %+v", st)
	}
}

func TestControllerSayStreamsFirstDeltaBeforeSynthesisCompletes(t *testing.T) {
	ws := newFakeWS(t)
	release := make(chan struct{})
	var fallbackCalls atomic.Int32
	fallback := synthesizerFunc(func(context.Context, tts.JobRequest) ([]byte, error) {
		fallbackCalls.Add(1)
		return nil, errors.New("non-streaming fallback must not be called")
	})
	stream := streamingSynthesizerFunc(func(ctx context.Context, _ tts.JobRequest, emit func(context.Context, []byte) error) error {
		if err := emit(ctx, audio.TonePCM(100)); err != nil {
			return err
		}
		<-release
		return nil
	})
	ctl := newTestController(t, ws, fallback, func(cfg *ViduConfig) { cfg.StreamTTS = stream })
	if _, err := ctl.Start(context.Background(), live.StartOpts{SessionID: "s-stream"}); err != nil {
		t.Fatal(err)
	}
	defer ctl.Stop(context.Background(), "done")

	said := make(chan error, 1)
	go func() {
		_, err := ctl.Say(context.Background(), "流式口播", "chat")
		said <- err
	}()
	waitFor(t, time.Second, func() bool {
		_, _, _, _, bin, _ := ws.counts()
		return bin >= 1
	}, "first PCM delta before streaming synthesis returns")
	if fallbackCalls.Load() != 0 {
		t.Fatal("configured streaming synthesizer was bypassed")
	}
	close(release)
	if err := <-said; err != nil {
		t.Fatalf("streamed Say: %v", err)
	}
}

func TestNewViduControllerRejectsMissingSynthesizers(t *testing.T) {
	_, err := NewViduController(ViduConfig{Client: fakeViduHTTP(t)})
	if err == nil {
		t.Fatal("controller without batch or streaming synthesizer must fail closed")
	}
}

func TestControllerPreemptCancelsStreamingUpstreamAndDropsLatePCM(t *testing.T) {
	ws := newFakeWS(t)
	firstStarted := make(chan struct{})
	firstCancelled := make(chan struct{})
	lateWrite := make(chan error, 1)
	var calls atomic.Int32
	stream := streamingSynthesizerFunc(func(ctx context.Context, _ tts.JobRequest, emit func(context.Context, []byte) error) error {
		if calls.Add(1) == 1 {
			if err := emit(ctx, audio.TonePCM(100)); err != nil {
				return err
			}
			close(firstStarted)
			<-ctx.Done()
			close(firstCancelled)
			lateWrite <- emit(context.Background(), audio.TonePCM(100))
			return ctx.Err()
		}
		return emit(ctx, audio.TonePCM(100))
	})
	ctl := newTestController(t, ws, nil, func(cfg *ViduConfig) { cfg.StreamTTS = stream })
	if _, err := ctl.Start(context.Background(), live.StartOpts{SessionID: "s-preempt-stream"}); err != nil {
		t.Fatal(err)
	}
	defer ctl.Stop(context.Background(), "done")

	firstDone := make(chan error, 1)
	go func() {
		_, err := ctl.Say(context.Background(), "第一句流式口播", "chat")
		firstDone <- err
	}()
	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first streaming Say did not emit")
	}
	if _, err := ctl.Say(context.Background(), "第二句抢占", "chat"); err != nil {
		t.Fatalf("preempting Say: %v", err)
	}
	select {
	case <-firstCancelled:
	case <-time.After(time.Second):
		t.Fatal("preemption did not cancel streaming upstream context")
	}
	if err := <-lateWrite; !errors.Is(err, context.Canceled) {
		t.Fatalf("late emit error = %v, want context.Canceled", err)
	}
	if err := <-firstDone; err != nil {
		t.Fatalf("preempted Say should finish as an interruption, got %v", err)
	}
	waitFor(t, time.Second, func() bool {
		_, _, _, _, bin, _ := ws.counts()
		return bin == 2
	}, "exactly one PCM chunk from each Say")
	_, _, _, _, bin, _ := ws.counts()
	if bin != 2 {
		t.Fatalf("PCM frames = %d, want 2; cancelled upstream fed late audio", bin)
	}
}

func TestControllerSayRespectsActiveSong(t *testing.T) {
	ws := newFakeWS(t)
	var ttsCalls atomic.Int32
	ctl := newTestController(t, ws, synthesizerFunc(func(context.Context, tts.JobRequest) ([]byte, error) {
		ttsCalls.Add(1)
		return audio.WavBytes(audio.TonePCM(100)), nil
	}), nil)
	if _, err := ctl.Start(context.Background(), live.StartOpts{SessionID: "s-song"}); err != nil {
		t.Fatal(err)
	}
	defer ctl.Stop(context.Background(), "test done")

	songDone := make(chan struct{})
	songCtx, cancelSong := context.WithCancel(context.Background())
	defer cancelSong()
	lease, err := ctl.own.Acquire(context.Background(), Request{Task: Task{
		ID: "song-test", Song: true, Cancel: cancelSong, Done: songDone,
	}})
	if err != nil {
		t.Fatal(err)
	}

	sayDone := make(chan error, 1)
	go func() {
		_, err := ctl.Say(context.Background(), "等待歌曲", "say")
		sayDone <- err
	}()
	select {
	case <-songCtx.Done():
		t.Fatal("普通口播抢占了歌曲喂流权")
	case err := <-sayDone:
		t.Fatalf("歌曲未结束时口播不应完成: %v", err)
	case <-time.After(50 * time.Millisecond):
	}
	if got := ttsCalls.Load(); got != 0 {
		t.Fatalf("等待歌曲时不应提前合成口播，TTS 调用=%d", got)
	}

	close(songDone)
	lease.Release()
	select {
	case err := <-sayDone:
		if err != nil {
			t.Fatalf("歌曲结束后口播失败: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("歌曲结束后口播未获得喂流权")
	}
	if got := ttsCalls.Load(); got != 1 {
		t.Fatalf("歌曲结束后应合成一次口播，TTS 调用=%d", got)
	}
}

func TestControllerSayPriorityCombinations(t *testing.T) {
	t.Run("song protects against typed chat", func(t *testing.T) {
		ws := newFakeWS(t)
		var ttsCalls atomic.Int32
		ctl := newTestController(t, ws, synthesizerFunc(func(context.Context, tts.JobRequest) ([]byte, error) {
			ttsCalls.Add(1)
			return audio.WavBytes(audio.TonePCM(100)), nil
		}), nil)
		if _, err := ctl.Start(context.Background(), live.StartOpts{SessionID: "s-priority-song"}); err != nil {
			t.Fatal(err)
		}
		defer ctl.Stop(context.Background(), "done")

		songDone := make(chan struct{})
		songCtx, cancelSong := context.WithCancel(context.Background())
		defer cancelSong()
		lease, err := ctl.own.Acquire(context.Background(), Request{Task: Task{
			ID: "song-priority", Song: true, Cancel: cancelSong, Done: songDone,
		}})
		if err != nil {
			t.Fatal(err)
		}
		waiting := make(chan error, 1)
		go func() {
			_, err := ctl.Say(context.Background(), "等待歌曲结束", "chat")
			waiting <- err
		}()
		select {
		case <-songCtx.Done():
			t.Fatal("打字聊天抢占了歌曲")
		case err := <-waiting:
			t.Fatalf("歌曲未结束时口播不应完成: %v", err)
		case <-time.After(50 * time.Millisecond):
		}
		if got := ttsCalls.Load(); got != 0 {
			t.Fatalf("等待歌曲时不应提前合成口播，TTS 调用=%d", got)
		}
		close(songDone)
		lease.Release()
		select {
		case err := <-waiting:
			if err != nil {
				t.Fatalf("歌曲结束后口播失败: %v", err)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("歌曲结束后口播未获得喂流权")
		}
	})

	for _, tc := range []struct {
		name         string
		holderMode   string
		incomingMode string
		wantPreempt  bool
	}{
		{name: "danmaku waits for typed chat", holderMode: "chat", incomingMode: "danmaku"},
		{name: "typed chat preempts danmaku", holderMode: "danmaku", incomingMode: "chat", wantPreempt: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ws := newFakeWS(t)
			firstStarted := make(chan struct{})
			firstCancelled := make(chan struct{})
			releaseFirst := make(chan struct{})
			var releaseOnce sync.Once
			release := func() { releaseOnce.Do(func() { close(releaseFirst) }) }
			defer release()
			var calls atomic.Int32
			stream := streamingSynthesizerFunc(func(ctx context.Context, _ tts.JobRequest, emit func(context.Context, []byte) error) error {
				if calls.Add(1) == 1 {
					if err := emit(ctx, audio.TonePCM(100)); err != nil {
						return err
					}
					close(firstStarted)
					select {
					case <-releaseFirst:
						return nil
					case <-ctx.Done():
						close(firstCancelled)
						return ctx.Err()
					}
				}
				return emit(ctx, audio.TonePCM(100))
			})
			ctl := newTestController(t, ws, nil, func(cfg *ViduConfig) { cfg.StreamTTS = stream })
			if _, err := ctl.Start(context.Background(), live.StartOpts{SessionID: "s-priority-chat"}); err != nil {
				t.Fatal(err)
			}
			defer ctl.Stop(context.Background(), "done")

			holderDone := make(chan error, 1)
			go func() {
				_, err := ctl.Say(context.Background(), "正在播出的口播", tc.holderMode)
				holderDone <- err
			}()
			select {
			case <-firstStarted:
			case <-time.After(time.Second):
				t.Fatal("首个口播未开始")
			}
			incomingDone := make(chan error, 1)
			go func() {
				_, err := ctl.Say(context.Background(), "随后到达的口播", tc.incomingMode)
				incomingDone <- err
			}()

			if tc.wantPreempt {
				select {
				case err := <-incomingDone:
					if err != nil {
						t.Fatalf("打字聊天抢占弹幕失败: %v", err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("打字聊天未抢占弹幕")
				}
				select {
				case <-firstCancelled:
				case <-time.After(time.Second):
					t.Fatal("弹幕未被取消")
				}
				if err := <-holderDone; err != nil {
					t.Fatalf("被抢占的弹幕应正常结束: %v", err)
				}
				return
			}

			select {
			case <-firstCancelled:
				t.Fatal("弹幕抢占了打字聊天")
			case err := <-incomingDone:
				t.Fatalf("打字聊天未结束时弹幕不应完成: %v", err)
			case <-time.After(50 * time.Millisecond):
			}
			if got := calls.Load(); got != 1 {
				t.Fatalf("弹幕等待聊天时不应提前合成，TTS 调用=%d", got)
			}
			release()
			for _, done := range []chan error{holderDone, incomingDone} {
				select {
				case err := <-done:
					if err != nil {
						t.Fatalf("聊天结束后口播失败: %v", err)
					}
				case <-time.After(2 * time.Second):
					t.Fatal("聊天结束后弹幕未获得喂流权")
				}
			}
		})
	}
}

func TestControllerStartAuthenticatesWebSocket(t *testing.T) {
	ws := newFakeWS(t)
	ctl := newTestController(t, ws, stubTTS{wav: audio.WavBytes(audio.TonePCM(100))}, nil)
	if _, err := ctl.Start(context.Background(), live.StartOpts{SessionID: "s-ws-auth"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer ctl.Stop(context.Background(), "done")
	if got := ws.authorizationHeader(); got != "Token k" {
		t.Fatalf("websocket Authorization=%q want Token k", got)
	}
}

func TestControllerConnInitNotReadyRetries(t *testing.T) {
	ws := newFakeWS(t)
	ws.notReadyBeforeAck = 2
	ctl := newTestController(t, ws, stubTTS{wav: audio.WavBytes(audio.TonePCM(100))}, nil)
	if _, err := ctl.Start(context.Background(), live.StartOpts{SessionID: "s-1"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer ctl.Stop(context.Background(), "done")
	conns, inits, _, _, _, _ := ws.counts()
	if conns != 1 || inits != 3 {
		t.Fatalf("conns=%d inits=%d want 1/3（NOT_READY 重试不重连）", conns, inits)
	}
}

func TestControllerHeartbeatPings(t *testing.T) {
	ws := newFakeWS(t)
	ctl := newTestController(t, ws, stubTTS{wav: audio.WavBytes(audio.TonePCM(100))}, nil)
	if _, err := ctl.Start(context.Background(), live.StartOpts{SessionID: "s-1"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer ctl.Stop(context.Background(), "done")
	// PingInterval=50ms：250ms 内应见 ≥3 个 ping（心跳节拍真实存在）。
	waitFor(t, 2*time.Second, func() bool {
		_, _, _, _, _, ping := ws.counts()
		return ping >= 3
	}, "client ping frames")
}

func TestControllerPreemptSendsType7Once(t *testing.T) {
	ws := newFakeWS(t)
	long := audio.WavBytes(audio.TonePCM(800))  // 800ms 长句，足够被抢占
	short := audio.WavBytes(audio.TonePCM(100)) // 100ms 短句
	var calls atomic.Int32
	ttsStub := synthesizerFunc(func(ctx context.Context, _ tts.JobRequest) ([]byte, error) {
		if calls.Add(1) == 1 {
			return long, nil
		}
		return short, nil
	})
	ctl := newTestController(t, ws, ttsStub, nil)
	if _, err := ctl.Start(context.Background(), live.StartOpts{SessionID: "s-1"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer ctl.Stop(context.Background(), "done")

	done1 := make(chan error, 1)
	go func() {
		done1 <- func() error { _, err := ctl.Say(context.Background(), "第一句长口播", "say"); return err }()
	}()
	// 等第一句确实在喂（服务器收到二进制帧）。
	waitFor(t, 2*time.Second, func() bool {
		_, _, _, _, bin, _ := ws.counts()
		return bin >= 2
	}, "first say feeding")
	if _, err := ctl.Say(context.Background(), "第二句抢占", "say"); err != nil {
		t.Fatalf("preempt say: %v", err)
	}
	<-done1
	_, _, type7, type10, _, _ := ws.counts()
	if type7 != 1 {
		t.Fatalf("type7=%d want 1（抢占必须单发）", type7)
	}
	if type10 != 2 {
		t.Fatalf("type10=%d want 2", type10)
	}
}

func TestControllerReconnectOnWSClose(t *testing.T) {
	ws := newFakeWS(t)
	ws.closeAfterInit = 1 // 第一条连接 init 后立即断开
	ctl := newTestController(t, ws, stubTTS{wav: audio.WavBytes(audio.TonePCM(100))}, nil)
	if _, err := ctl.Start(context.Background(), live.StartOpts{SessionID: "s-1"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer ctl.Stop(context.Background(), "done")
	waitFor(t, 3*time.Second, func() bool {
		conns, _, _, _, _, _ := ws.counts()
		st := ctl.RuntimeStatus(context.Background())
		return conns >= 2 && st.WSState == "connected" && st.WSReconnectTotal == 1
	}, "reconnect after ws close")
	// NOT_RESTART：phase 仍为 live（Manager 层无感知）。
	if got := ctl.phaseString(); got != "live" {
		t.Fatalf("phase=%s want live（引擎内部重连不得影响 Manager）", got)
	}
}

func TestControllerReconnectBudgetExhausted(t *testing.T) {
	ws := newFakeWS(t)
	ws.closeAfterInit = 99 // 每条连接 init 后立即断开
	ctl := newTestController(t, ws, stubTTS{wav: audio.WavBytes(audio.TonePCM(100))}, func(cfg *ViduConfig) {
		cfg.MaxReconnects = 3
		cfg.ReconnectMinGap = 5 * time.Millisecond
	})
	if _, err := ctl.Start(context.Background(), live.StartOpts{SessionID: "s-1"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer ctl.Stop(context.Background(), "done")
	// 重连上限 3：耗尽后本地终结为 error，不再无限重连。
	waitFor(t, 5*time.Second, func() bool {
		return ctl.phaseString() == "error"
	}, "reconnect budget exhausted")
	st := ctl.RuntimeStatus(context.Background())
	if st.WSReconnectTotal != 3 {
		t.Fatalf("reconnects=%d want 3", st.WSReconnectTotal)
	}
}

func TestControllerStallTriggersReconnect(t *testing.T) {
	ws := newFakeWS(t)
	ctl := newTestController(t, ws, stubTTS{wav: audio.WavBytes(audio.TonePCM(100))}, func(cfg *ViduConfig) {
		cfg.StallIdleRecv = 30 * time.Millisecond
		cfg.StallMinWindow = 30 * time.Millisecond
		cfg.StallMinSamples = 3
	})
	if _, err := ctl.Start(context.Background(), live.StartOpts{SessionID: "s-1"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer ctl.Stop(context.Background(), "done")
	// 不喂流：fed=0 → 速率 0 ≤ 0.55，idle_recv 持续增长 → 判定 stall 并重连。
	waitFor(t, 3*time.Second, func() bool {
		conns, _, _, _, _, _ := ws.counts()
		return conns >= 2
	}, "stall-triggered reconnect")
}

func TestControllerIdleTimeoutTerminates(t *testing.T) {
	ws := newFakeWS(t)
	ctl := newTestController(t, ws, stubTTS{wav: audio.WavBytes(audio.TonePCM(100))}, func(cfg *ViduConfig) {
		cfg.IdleTimeout = 60 * time.Millisecond
	})
	if _, err := ctl.Start(context.Background(), live.StartOpts{SessionID: "s-1"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer ctl.Stop(context.Background(), "done")
	waitFor(t, 3*time.Second, func() bool {
		return ctl.phaseString() == "error"
	}, "idle timeout terminates session locally")
}

func TestStallJudgeLogic(t *testing.T) {
	j := stallJudge{minWindow: 20 * time.Second, minSamples: 3, maxRate: 0.55}
	t0 := time.Now()
	// 采样不足：不判
	j.observe(t0, 0)
	if j.stall(100*time.Second, 75*time.Second) {
		t.Fatal("samples<3 must not stall")
	}
	// 低速喂流：24B/ms（0.5x）持续 30s → 判 stall
	for i := 1; i <= 4; i++ {
		j.observe(t0.Add(time.Duration(i)*10*time.Second), int64(i)*10*1000*24)
	}
	if !j.stall(75*time.Second, 75*time.Second) {
		t.Fatal("slow feed 0.5x over 40s window must stall")
	}
	// idle_recv 不足：不判
	if j.stall(10*time.Second, 75*time.Second) {
		t.Fatal("idle_recv<75s must not stall")
	}
	// 高速喂流：48B/ms（1x）→ 不判
	j.reset()
	for i := 0; i <= 4; i++ {
		j.observe(t0.Add(time.Duration(i)*10*time.Second), int64(i)*10*1000*48)
	}
	if j.stall(100*time.Second, 75*time.Second) {
		t.Fatal("full-rate feed must not stall")
	}
}

func TestAudioGuardRevokeSkipsSong(t *testing.T) {
	interrupts := 0
	own := NewOwnership(OwnershipConfig{
		RoundWait: 5 * time.Millisecond,
		Quiet:     time.Millisecond,
		Hooks: Hooks{
			Interrupt: func(context.Context) error { interrupts++; return nil },
			Speaking:  func() bool { return true },
			LastFeed:  func() time.Time { return time.Now().Add(-time.Hour) }, // 早已 stale
		},
	})
	ctx := context.Background()

	// 普通持有者 stale+hold 超时 → 收回。
	l1, err := own.Acquire(ctx, Request{Task: Task{ID: "say-1"}})
	if err != nil {
		t.Fatal(err)
	}
	// 直接篡改持有时刻以越过 hold 判定：用真实等待代价太高，改为把 stale/hold 设为 0 前的负值。
	ok, err := own.RevokeStale(ctx, 0, 0)
	if err != nil || !ok {
		t.Fatalf("revoke stale: ok=%v err=%v", ok, err)
	}
	if l1.Valid() {
		t.Fatal("stale lease must be invalid after revoke")
	}

	// 歌曲持有者豁免。
	l2, err := own.Acquire(ctx, Request{Task: Task{ID: "song-1", Song: true}})
	if err != nil {
		t.Fatal(err)
	}
	ok, err = own.RevokeStale(ctx, 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if ok {
		t.Fatal("song holder must be exempt from guard revoke")
	}
	if !l2.Valid() {
		t.Fatal("song lease must stay valid")
	}
	l2.Release()
	if interrupts != 1 {
		t.Fatalf("interrupts=%d want 1（只在确实在播的收回时发一次 type7）", interrupts)
	}
}

type synthesizerFunc func(ctx context.Context, req tts.JobRequest) ([]byte, error)

func (f synthesizerFunc) Synthesize(ctx context.Context, req tts.JobRequest) ([]byte, error) {
	return f(ctx, req)
}

// TestVoiceIDPerSessionIsolated voice_id 时序（v3 §3.4）：opts.VoiceID 在 Start
// 落入当前场状态；Say 合成读场值、空回落装配默认 cfg.Voice；两场各持各的 voice 不串。
func TestVoiceIDPerSessionIsolated(t *testing.T) {
	ws := newFakeWS(t)
	var mu sync.Mutex
	var voices []string
	ttsRec := synthesizerFunc(func(_ context.Context, req tts.JobRequest) ([]byte, error) {
		mu.Lock()
		voices = append(voices, req.VoiceID)
		mu.Unlock()
		return audio.WavBytes(audio.TonePCM(100)), nil
	})
	ctl := newTestController(t, ws, ttsRec, func(cfg *ViduConfig) { cfg.Voice = "default-voice" })

	// 场 1：场值 voice-1。
	if _, err := ctl.Start(context.Background(), live.StartOpts{SessionID: "s-1", VoiceID: "voice-1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ctl.Say(context.Background(), "第一句", "say"); err != nil {
		t.Fatal(err)
	}
	_ = ctl.Stop(context.Background(), "switch")
	// 场 2：场值 voice-2，与场 1 不串。
	if _, err := ctl.Start(context.Background(), live.StartOpts{SessionID: "s-2", VoiceID: "voice-2"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ctl.Say(context.Background(), "第二句", "say"); err != nil {
		t.Fatal(err)
	}
	_ = ctl.Stop(context.Background(), "switch")
	// 场 3：空场值 → 回落装配默认 cfg.Voice。
	if _, err := ctl.Start(context.Background(), live.StartOpts{SessionID: "s-3"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ctl.Say(context.Background(), "第三句", "say"); err != nil {
		t.Fatal(err)
	}
	_ = ctl.Stop(context.Background(), "done")

	mu.Lock()
	defer mu.Unlock()
	want := []string{"voice-1", "voice-2", "default-voice"}
	if len(voices) != len(want) {
		t.Fatalf("synthesize 次数=%d want %d (%v)", len(voices), len(want), voices)
	}
	for i := range want {
		if voices[i] != want[i] {
			t.Fatalf("voices=%v want %v（两场不同 voice 不得串场，空场值回落装配默认）", voices, want)
		}
	}
}

// TestStartBuildsCreateBodyFromOpts 建场 body 由 opts 构造（v3 §3.1）：
// model 默认 vidu-s2；image_uri 与 avatar_id 二选一且 URI 优先；可选 action 段；
// enable_recording 场级/装配任一成立即带；RefImageURIs 不进建场 body（M5 推送）。
func TestStartBuildsCreateBodyFromOpts(t *testing.T) {
	ws := newFakeWS(t)
	var mu sync.Mutex
	var gotBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		_ = json.NewDecoder(r.Body).Decode(&gotBody)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"live":{"id":"live-body"}}`))
	}))
	t.Cleanup(srv.Close)
	ctl := newTestController(t, ws, stubTTS{wav: audio.WavBytes(audio.TonePCM(100))}, func(cfg *ViduConfig) {
		cfg.Client = vidu.NewClient(vidu.Options{APIKey: "k", BaseURL: srv.URL})
	})
	_, err := ctl.Start(context.Background(), live.StartOpts{
		SessionID: "s-body", ViduAvatarID: "ava-9", ViduImageURI: "vidu://img-1",
		ActionLibraryID: "lib-1", RefImageURIs: []string{"vidu://r1"}, RecordingEnabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Stop(context.Background(), "done")

	mu.Lock()
	defer mu.Unlock()
	if gotBody["model"] != "vidu-s2" {
		t.Fatalf("默认 model: %v", gotBody)
	}
	if gotBody["image_uri"] != "vidu://img-1" {
		t.Fatalf("URI 优先: %v", gotBody)
	}
	if _, has := gotBody["avatar_id"]; has {
		t.Fatalf("image_uri 与 avatar_id 二选一: %v", gotBody)
	}
	act, _ := gotBody["action"].(map[string]any)
	if act["enabled"] != true || act["library_id"] != "lib-1" {
		t.Fatalf("action 段: %v", gotBody)
	}
	managed, _ := gotBody["managed_rtc"].(map[string]any)
	if managed == nil || managed["enable_recording"] != true {
		t.Fatalf("managed_rtc.enable_recording: %v", gotBody)
	}
	if _, has := gotBody["enable_recording"]; has {
		t.Fatalf("enable_recording 不得放在顶层: %v", gotBody)
	}
	if _, has := gotBody["session_id"]; has {
		t.Fatalf("session_id 不是官方建场字段: %v", gotBody)
	}
	if _, has := gotBody["ref_image_uris"]; has {
		t.Fatalf("参考图不进建场 body: %v", gotBody)
	}
}

func TestStartReadsNestedLiveID(t *testing.T) {
	ws := newFakeWS(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"live":{"id":"live-nested","status":"waiting"}}`))
	}))
	t.Cleanup(srv.Close)
	ctl := newTestController(t, ws, stubTTS{wav: audio.WavBytes(audio.TonePCM(100))}, func(cfg *ViduConfig) {
		cfg.Client = vidu.NewClient(vidu.Options{APIKey: "k", BaseURL: srv.URL})
	})
	liveID, err := ctl.Start(context.Background(), live.StartOpts{SessionID: "s-nested"})
	if err != nil {
		t.Fatal(err)
	}
	defer ctl.Stop(context.Background(), "done")
	if liveID != "live-nested" {
		t.Fatalf("live_id=%q want live-nested", liveID)
	}
}

// TestStartActionForbiddenDegradesOnce G3-2（v3 §3.3 时序）：带 action 建场被
// Vidu 403 拒绝 → 引擎去 action 重建一次并成功，ActionDegraded 如实上报。
func TestStartActionForbiddenDegradesOnce(t *testing.T) {
	ws := newFakeWS(t)
	var mu sync.Mutex
	var withAction, withoutAction int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		if _, ok := body["action"]; ok {
			withAction++
		} else {
			withoutAction++
		}
		mu.Unlock()
		if _, ok := body["action"]; ok {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":"ACTION_FORBIDDEN","message":"action not allowed"}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"live":{"id":"live-degraded"}}`))
	}))
	t.Cleanup(srv.Close)
	ctl := newTestController(t, ws, stubTTS{wav: audio.WavBytes(audio.TonePCM(100))}, func(cfg *ViduConfig) {
		cfg.Client = vidu.NewClient(vidu.Options{APIKey: "k", BaseURL: srv.URL})
	})

	liveID, err := ctl.Start(context.Background(), live.StartOpts{
		SessionID: "s-403", ViduImageURI: "vidu://img-1", ActionLibraryID: "lib-1",
	})
	if err != nil {
		t.Fatalf("降级后必须开播成功: %v", err)
	}
	defer ctl.Stop(context.Background(), "done")
	if liveID != "live-degraded" {
		t.Fatalf("live_id=%s", liveID)
	}
	if !ctl.ActionDegraded() {
		t.Fatal("ActionDegraded 必须为 true（去 action 重建成功）")
	}
	mu.Lock()
	defer mu.Unlock()
	if withAction != 1 || withoutAction != 1 {
		t.Fatalf("withAction=%d withoutAction=%d（降级必须恰好一次）", withAction, withoutAction)
	}
}

// TestStartActionForbiddenStillFails 去 action 重建仍 403 → 错误如实上抛，不得无限重建。
func TestStartActionForbiddenStillFails(t *testing.T) {
	ws := newFakeWS(t)
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"code":"ACTION_FORBIDDEN","message":"action not allowed"}`))
	}))
	t.Cleanup(srv.Close)
	ctl := newTestController(t, ws, stubTTS{wav: audio.WavBytes(audio.TonePCM(100))}, func(cfg *ViduConfig) {
		cfg.Client = vidu.NewClient(vidu.Options{APIKey: "k", BaseURL: srv.URL})
	})

	_, err := ctl.Start(context.Background(), live.StartOpts{
		SessionID: "s-403f", ViduImageURI: "vidu://img-1", ActionLibraryID: "lib-1",
	})
	var apiErr *vidu.APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusForbidden {
		t.Fatalf("err=%v want vidu 403 上抛", err)
	}
	if ctl.ActionDegraded() {
		t.Fatal("降级未成功不得置 ActionDegraded")
	}
	if calls != 2 {
		t.Fatalf("calls=%d want 2（首次 + 降级一次，不得无限重建）", calls)
	}
}

func TestSendCommandForwardsType9WithoutClaimingExecution(t *testing.T) {
	ws := newFakeWS(t)
	ctl := newTestController(t, ws, stubTTS{wav: audio.WavBytes(audio.TonePCM(100))}, nil)
	if err := ctl.SendCommand(context.Background(), "挥手"); !errors.Is(err, vidu.ErrNotReady) {
		t.Fatalf("未开播下发 err=%v want ErrNotReady", err)
	}
	if _, err := ctl.Start(context.Background(), live.StartOpts{SessionID: "s-action"}); err != nil {
		t.Fatal(err)
	}
	defer ctl.Stop(context.Background(), "done")
	if err := ctl.SendCommand(context.Background(), "挥手"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool { return ws.countType9() >= 1 }, "type9 action command")
	// SendCommand 成功仅表示 type9 已写入信令；协议无执行回执时不得生成“已执行”状态。
}

// TestPromptOperationForwardsType11 G3-1：控制器 type11 出口把参考图操作
// 如实转发到当前场信令；未开播返回 ErrNotReady。
func TestPromptOperationForwardsType11(t *testing.T) {
	ws := newFakeWS(t)
	ctl := newTestController(t, ws, stubTTS{wav: audio.WavBytes(audio.TonePCM(100))}, nil)

	// 未开播：如实报未就绪，不假装成功。
	if err := ctl.PromptOperation(context.Background(), "switch", []map[string]any{{"url": "vidu://r1"}}); !errors.Is(err, vidu.ErrNotReady) {
		t.Fatalf("未开播 err=%v want ErrNotReady", err)
	}

	if _, err := ctl.Start(context.Background(), live.StartOpts{SessionID: "s-p11"}); err != nil {
		t.Fatal(err)
	}
	defer ctl.Stop(context.Background(), "done")

	err := ctl.PromptOperation(context.Background(), "switch", []map[string]any{
		{"url": "vidu://r1"}, {"url": "vidu://r2"},
	})
	if err != nil {
		t.Fatal(err)
	}
	waitFor(t, 2*time.Second, func() bool {
		n, _ := ws.type11Stats()
		return n == 1
	}, "type11 frame")
	_, raw := ws.type11Stats()
	var payload struct {
		PromptOperation struct {
			OpType string           `json:"op_type"`
			Images []map[string]any `json:"images"`
		} `json:"prompt_operation"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("payload decode: %v", err)
	}
	if payload.PromptOperation.OpType != "switch" || len(payload.PromptOperation.Images) != 2 {
		t.Fatalf("type11 payload=%+v", payload.PromptOperation)
	}
}
