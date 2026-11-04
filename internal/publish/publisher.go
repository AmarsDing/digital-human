package publish

// Publisher：编排 采集源→ffmpeg→FLV→RTMP 全链路，状态机+指数退避重连。
// 密钥卫生：stream key 只经 TargetStore（env/内存）进入 DialRTMP；
// ffmpeg argv 无 key；日志回调接收的均为脱敏文本。

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Target 推流目标。Key 为敏感值：不序列化到日志/yaml。
type Target struct {
	Platform string `json:"platform"`
	RTMPURL  string `json:"rtmp_url"`
	Key      string `json:"-"`
}

// Fingerprint8 返回 key 的 sha256 前 8 位十六进制（用于 UI 回显"已配置"）。
func (t Target) Fingerprint8() string {
	if t.Key == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(t.Key))
	return hex.EncodeToString(sum[:])[:8]
}

// TargetStore 密钥/目标存取。env 实现只读；内存实现进程级。
type TargetStore interface {
	Load() (Target, bool)
	Save(t Target) error
}

// EnvTargetStore 从环境变量读取（只读）：DH_RTMP_URL / DH_STREAM_KEY。
type EnvTargetStore struct {
	Lookup func(string) string
}

// Load 实现 TargetStore。
func (e EnvTargetStore) Load() (Target, bool) {
	get := e.Lookup
	if get == nil {
		get = os.Getenv
	}
	u, k := get("DH_RTMP_URL"), get("DH_STREAM_KEY")
	if u == "" || k == "" {
		return Target{}, false
	}
	return Target{Platform: "douyin", RTMPURL: u, Key: k}, true
}

// Save 实现 TargetStore（env 只读，返回错误）。
func (e EnvTargetStore) Save(Target) error {
	return errors.New("publish: env 目标存储只读，请改环境变量后重启")
}

// MemTargetStore 进程内目标存储（重启即失；持久化加密存储为主实施线接线项）。
type MemTargetStore struct {
	mu sync.Mutex
	t  Target
	ok bool
}

// Load 实现 TargetStore。
func (m *MemTargetStore) Load() (Target, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.t, m.ok
}

// Save 实现 TargetStore。
func (m *MemTargetStore) Save(t Target) error {
	if t.RTMPURL == "" || t.Key == "" {
		return errors.New("publish: rtmp_url 与 stream_key 均必填")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.t, m.ok = t, true
	return nil
}

// chainStore：先 mem 后 env。
type chainStore struct {
	mem *MemTargetStore
	env EnvTargetStore
}

func (c *chainStore) Load() (Target, bool) {
	if t, ok := c.mem.Load(); ok {
		return t, true
	}
	return c.env.Load()
}

func (c *chainStore) Save(t Target) error { return c.mem.Save(t) }

// Config Publisher 配置。
type Config struct {
	Platform          string
	Source            SourceKind
	Stage             CaptureConfig
	Encoder           EncoderConfig
	FFmpegBin         string   // 默认 ffmpeg；测试注入 helper
	FFmpegPrepend     []string // 测试注入 helper 参数
	ConnectTimeout    time.Duration
	FirstMediaTimeout time.Duration
	// 退避：初始 2s，倍增，上限 30s；连续 MaxRetries 次失败转 rejected 之外的终态停止。
	BackoffInit time.Duration
	BackoffMax  time.Duration
	MaxRetries  int
	// NoFrameTimeout：stage 源超过该时长无帧视为白屏/断流 → degraded。
	NoFrameTimeout time.Duration
	// AudioTimeout：收到过 RTC PCM 后，持续无新帧则显式降级；未收到前不据此声称已通。
	AudioTimeout time.Duration
	// Logf 日志回调（调用方保证审计安全；本包保证调用处不传入 key）。
	Logf func(format string, args ...any)
	// OnEvent 状态事件回调（主实施线接 eventbus）。
	OnEvent func(Event)
}

func (c *Config) withDefaults() {
	if c.Platform == "" {
		c.Platform = "douyin"
	}
	if c.ConnectTimeout == 0 {
		c.ConnectTimeout = 10 * time.Second
	}
	if c.FirstMediaTimeout <= 0 {
		c.FirstMediaTimeout = 10 * time.Second
	}
	if c.BackoffInit <= 0 {
		c.BackoffInit = 2 * time.Second
	}
	if c.BackoffMax <= 0 {
		c.BackoffMax = 30 * time.Second
	}
	if c.MaxRetries <= 0 {
		c.MaxRetries = 8
	}
	if c.NoFrameTimeout <= 0 {
		c.NoFrameTimeout = 8 * time.Second
	}
	if c.AudioTimeout <= 0 {
		c.AudioTimeout = 8 * time.Second
	}
}

// Publisher 推流执行器。
type Publisher struct {
	cfg   Config
	store TargetStore
	m     *machine

	mu     sync.Mutex
	cancel context.CancelFunc
	runID  int64
	wg     sync.WaitGroup

	// dialRTMP / startStageCapture 为可替换接缝（测试注入 fake）。
	dialRTMP          func(rawURL, key string, timeout time.Duration) (rtmpConn, error)
	startStageCapture func(context.Context, CaptureConfig) (*StageCapture, error)
	pcm               *PCMBridge
}

// rtmpConn 抽象 RTMPClient 便于测试替换。
type rtmpConn interface {
	SendMeta(width, height, fps int) error
	SendTag(t Tag) error
	Close() error
}

// NewPublisher 构造。
func NewPublisher(cfg Config, store TargetStore) *Publisher {
	cfg.withDefaults()
	if store == nil {
		store = &chainStore{mem: &MemTargetStore{}, env: EnvTargetStore{}}
	}
	p := &Publisher{cfg: cfg, store: store, pcm: NewPCMBridge(), startStageCapture: StartStageCapture}
	p.m = newMachine(cfg.Platform, cfg.OnEvent)
	p.dialRTMP = func(u, k string, t time.Duration) (rtmpConn, error) {
		return DialRTMP(u, k, t)
	}
	return p
}

// logf 安全日志出口。
func (p *Publisher) logf(format string, args ...any) {
	if p.cfg.Logf != nil {
		p.cfg.Logf(format, args...)
	}
}

// Status 返回快照。
func (p *Publisher) Status() Status { return p.m.snapshot() }

// PCMBridge 是公开采集页的唯一音频入口；统计不构成云端送达证据。
func (p *Publisher) PCMBridge() *PCMBridge { return p.pcm }

// SetTarget 更新推流目标（写入内存层；env 已配置时报错提示改 env）。
func (p *Publisher) SetTarget(t Target) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	switch p.m.snapshot().State {
	case StateStarting, StatePushing, StateDegraded:
		return "", &Error{Code: "publish_target_active", Message: "推流活动中，请先停止推流再更换目标"}
	}
	t.Platform = p.cfg.Platform
	t, err := normalizeTarget(t)
	if err != nil {
		return "", err
	}
	if err := p.store.Save(t); err != nil {
		return "", err
	}
	fp := t.Fingerprint8()
	p.m.setFingerprint(fp)
	p.m.setVerification(TargetUnknown)
	return fp, nil
}

// Start 启动推流（幂等：pushing/starting 中重复调用返回 nil）。
func (p *Publisher) Start(ctx context.Context) error {
	p.mu.Lock()
	st := p.m.snapshot().State
	if st == StatePushing || st == StateStarting {
		p.mu.Unlock()
		return nil
	}
	t, ok := p.store.Load()
	if !ok {
		p.mu.Unlock()
		return errors.New("publish: 未配置推流目标（设置 DH_RTMP_URL/DH_STREAM_KEY 或面板保存）")
	}
	t, err := normalizeTarget(t)
	if err != nil {
		p.mu.Unlock()
		return err
	}
	p.m.setVerification(TargetUnknown)
	if err := p.m.transit(StateStarting, ""); err != nil {
		p.mu.Unlock()
		return err
	}
	runCtx, cancel := context.WithCancel(ctx)
	p.cancel = cancel
	p.runID++
	id := p.runID
	p.m.setFingerprint(t.Fingerprint8())
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		p.runLoop(runCtx, id, t)
	}()
	p.mu.Unlock()
	return nil
}

// Stop 停止推流（幂等）。
func (p *Publisher) Stop() error {
	p.mu.Lock()
	cancel := p.cancel
	p.cancel = nil
	p.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	st := p.m.snapshot().State
	if st == StateIdle || st == StateStopped {
		return nil
	}
	return p.m.transit(StateStopped, "")
}

// Wait 等待运行循环退出（测试用）。
func (p *Publisher) Wait() { p.wg.Wait() }

func (p *Publisher) runLoop(ctx context.Context, id int64, t Target) {
	backoff := p.cfg.BackoffInit
	retries := 0
	for {
		if ctx.Err() != nil {
			return
		}
		err := p.runOnce(ctx, id, t)
		if ctx.Err() != nil {
			return
		}
		established := p.m.snapshot().State == StatePushing
		if errors.Is(err, ErrPublishRejected) {
			p.logf("publish: 推流被拒（key 失效？）: %v", scrubErr(err, t))
			_ = p.m.transit(StateRejected, "publish_rejected")
			return
		}
		if established {
			retries = 0
			backoff = p.cfg.BackoffInit
		}
		retries++
		if retries > p.cfg.MaxRetries {
			p.logf("publish: 超过最大重试 %d 次，停止", p.cfg.MaxRetries)
			_ = p.m.transit(StateStopped, "max_retries_exceeded")
			return
		}
		_ = p.m.transit(StateDegraded, fmt.Sprintf("reconnecting: %v", scrubErr(err, t)))
		p.logf("publish: %v，%s 后第 %d 次重连", scrubErr(err, t), backoff, retries)
		select {
		case <-ctx.Done():
			return
		case <-time.After(backoff):
		}
		_ = p.m.transit(StateStarting, "")
		backoff *= 2
		if backoff > p.cfg.BackoffMax {
			backoff = p.cfg.BackoffMax
		}
	}
}

// runOnce 单次推流会话：RTMP 连接 → ffmpeg → FLV 转发。返回即表示需要重连。
func (p *Publisher) runOnce(ctx context.Context, id int64, t Target) error {
	rc, err := p.dialRTMP(t.RTMPURL, t.Key, p.cfg.ConnectTimeout)
	if err != nil {
		return err
	}
	// DialRTMP 返回即已收到 NetStream.Publish.Start；验证等级只在此单点写入。
	p.m.setVerification(classifyTarget(t.RTMPURL))
	defer rc.Close()

	d := &FFmpegDriver{Bin: p.cfg.FFmpegBin, PrependArgs: p.cfg.FFmpegPrepend}
	var audioTimeout <-chan error
	if p.cfg.Source == SourceStage {
		bridgeCtx, cancel := context.WithCancel(ctx)
		defer cancel()
		audioInput, err := p.pcm.Start(bridgeCtx)
		if err != nil {
			return err
		}
		defer p.pcm.Close()
		d.AudioInput = audioInput
		audioTimeout = p.watchPCM(bridgeCtx)
	}
	if err := d.Start(p.cfg.Source, p.cfg.Encoder); err != nil {
		return err
	}
	defer d.Kill()
	p.logf("publish: ffmpeg 已启动（argv 脱敏形状: %s）",
		strings.Join(d.safeArgs(p.cfg.Source, p.cfg.Encoder), " "))

	// 采集源喂帧
	var capCancel context.CancelFunc
	if p.cfg.Source == SourceStage {
		capCtx, c := context.WithCancel(ctx)
		capCancel = c
		sc, err := p.startStageCapture(capCtx, p.cfg.Stage)
		if err != nil {
			d.Kill()
			return err
		}
		defer sc.Close()
		go p.feedFrames(capCtx, sc, d)
	}
	defer func() {
		if capCancel != nil {
			capCancel()
		}
	}()

	// 指标
	done := make(chan struct{})
	defer close(done)
	go d.WatchProgress(func(m Metrics) { p.m.setMetrics(m) },
		func(line string) { p.logf("publish: ffmpeg: %s", line) })

	// ffmpeg 进程监控
	waitErr := make(chan error, 1)
	go func() { waitErr <- d.Wait() }()

	ec := p.cfg.Encoder.withDefaults()
	if err := rc.SendMeta(ec.Width, ec.Height, ec.FPS); err != nil {
		return err
	}

	// FLV → RTMP 转发；首个音频/视频 tag 成功后才允许进入 pushing。
	firstMedia := make(chan struct{})
	forwardErr := make(chan error, 1)
	go func() {
		br := bufio.NewReader(d.Stdout())
		if err := ReadFLVHeader(br); err != nil {
			forwardErr <- err
			return
		}
		mediaSent := false
		for {
			tag, err := ReadFLVTag(br)
			if err != nil {
				if err == io.EOF {
					forwardErr <- errors.New("publish: ffmpeg 输出结束")
					return
				}
				forwardErr <- err
				return
			}
			if err := rc.SendTag(tag); err != nil {
				forwardErr <- err
				return
			}
			if !mediaSent && (tag.Type == 8 || tag.Type == 9) {
				mediaSent = true
				close(firstMedia)
			}
		}
	}()

	firstMediaTimer := time.NewTimer(p.cfg.FirstMediaTimeout)
	defer firstMediaTimer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-waitErr:
		return fmt.Errorf("publish: ffmpeg 退出: %v", err)
	case err := <-forwardErr:
		return err
	case err := <-audioTimeout:
		return err
	case <-firstMediaTimer.C:
		return &Error{Code: "publish_first_media_timeout", Message: "推流已连接，但未收到媒体数据，请检查采集源和编码器"}
	case <-firstMedia:
	}
	if err := p.m.transit(StatePushing, ""); err != nil {
		return err
	}
	p.logf("publish: 推流中（platform=%s %dx%d@%d）", p.cfg.Platform, ec.Width, ec.Height, ec.FPS)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-waitErr:
		return fmt.Errorf("publish: ffmpeg 退出: %v", err)
	case err := <-forwardErr:
		return err
	case err := <-audioTimeout:
		return err
	}
}

// watchPCM 只在本次连接收到过真实 WS PCM 后启用超时；未收到时不制造“音频已通”假象。
func (p *Publisher) watchPCM(ctx context.Context) <-chan error {
	result := make(chan error, 1)
	go func() {
		ticker := time.NewTicker(20 * time.Millisecond)
		defer ticker.Stop()
		last := p.pcm.Stats().LastPCMAt
		seen := false
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-ticker.C:
				current := p.pcm.Stats().LastPCMAt
				if current.After(last) {
					last = current
					seen = true
				}
				if seen && now.Sub(last) >= p.cfg.AudioTimeout {
					result <- &Error{Code: "publish_audio_pcm_timeout", Message: "RTC PCM 已中断，请检查公开采集页与 Vidu 远端音轨"}
					return
				}
			}
		}
	}()
	return result
}

// feedFrames 把采集帧喂给 ffmpeg stdin；NoFrameTimeout 无帧则杀 ffmpeg 触发重连。
func (p *Publisher) feedFrames(ctx context.Context, sc *StageCapture, d *FFmpegDriver) {
	timer := time.NewTimer(p.cfg.NoFrameTimeout)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case frame, ok := <-sc.Frames():
			if !ok {
				return
			}
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(p.cfg.NoFrameTimeout)
			if w := d.Stdin(); w != nil {
				if _, err := w.Write(frame); err != nil {
					return
				}
			}
		case <-timer.C:
			p.logf("publish: stage 页超过 %s 无帧（白屏/断流），重启采集", p.cfg.NoFrameTimeout)
			d.Kill()
			return
		}
	}
}

// scrubErr 确保错误文本不含 key（防御性：RTMP 服务器错误描述可能回显）。
func scrubErr(err error, t Target) error {
	if err == nil || t.Key == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), t.Key, "***"))
}
