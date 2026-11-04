package engine

import (
	"context"
	"errors"
	"time"

	"github.com/teamwork/digital-human/pkg/audio"
)

// 单帧写超时（蓝本 engine.py:1736 wait_for(send, 3.0)）。
const DefaultWriteTimeout = 3 * time.Second

var (
	ErrFeedInterrupted = errors.New("engine: feed interrupted")
	ErrFeedStopped     = errors.New("engine: feed stopped")
)

// PCMSender 是 Vidu WS 音频帧端口（单写者由信令层保证）。
type PCMSender interface {
	SendPCM(ctx context.Context, pcm []byte) error
}

type FeedConfig struct {
	ChunkMS      int
	WriteTimeout time.Duration
	Clock        audio.Clock
	Coarse       time.Duration
	WarnWindow   time.Duration
	OnLate       func(delay time.Duration)
	OnWarn       func(lag time.Duration)
}

type FeedResult struct {
	Frames int
	Bytes  int
	Late   int
	Warn   int
	MaxLag time.Duration
	Drift  time.Duration
}

// Feeder 按 48B/ms 绝对时基喂 PCM；迟到只记录、绝不跳帧或补发。
type Feeder struct {
	pacer *audio.Pacer
	write time.Duration
	warn  *audio.LagWarner
	cfg   FeedConfig
}

func NewFeeder(cfg FeedConfig) *Feeder {
	if cfg.ChunkMS <= 0 {
		cfg.ChunkMS = audio.DefaultChunkMS
	}
	if cfg.WriteTimeout <= 0 {
		cfg.WriteTimeout = DefaultWriteTimeout
	}
	if cfg.WarnWindow <= 0 {
		cfg.WarnWindow = 10 * time.Second
	}
	p := audio.NewPacer(cfg.Clock, cfg.ChunkMS)
	if cfg.Coarse > 0 {
		p = p.WithCoarseWindow(cfg.Coarse)
	}
	return &Feeder{pacer: p, write: cfg.WriteTimeout, warn: audio.NewLagWarner(cfg.WarnWindow), cfg: cfg}
}

// feedSession 在一轮语音内共享绝对时基，只暂存不足一个完整块的尾部。
type feedSession struct {
	feeder   *Feeder
	ctx      context.Context
	sender   PCMSender
	stop     func() bool
	t0       time.Time
	playedMS int64
	pending  []byte
	res      FeedResult
	done     bool
}

func (f *Feeder) newFeedSession(ctx context.Context, sender PCMSender, stop func() bool) *feedSession {
	return &feedSession{feeder: f, ctx: ctx, sender: sender, stop: stop, t0: f.pacer.Clock().Now()}
}

// Write 立即发送所有完整块，网络分片边界不影响 PCM 分块和喂流节奏。
func (s *feedSession) Write(pcm []byte) error {
	if err := s.ctx.Err(); err != nil {
		s.Discard()
		return err
	}
	if s.done {
		return ErrFeedStopped
	}
	chunkBytes := s.feeder.pacer.ChunkBytes()
	if len(s.pending) > 0 {
		need := chunkBytes - len(s.pending)
		if len(pcm) < need {
			s.pending = append(s.pending, pcm...)
			return nil
		}
		s.pending = append(s.pending, pcm[:need]...)
		pcm = pcm[need:]
		if err := s.send(s.pending); err != nil {
			s.Discard()
			return err
		}
		s.pending = nil
	}
	for len(pcm) >= chunkBytes {
		if err := s.send(pcm[:chunkBytes]); err != nil {
			s.Discard()
			return err
		}
		pcm = pcm[chunkBytes:]
	}
	if len(pcm) > 0 {
		s.pending = append([]byte(nil), pcm...)
	}
	return nil
}

// Finish 在正常结束时发送尾帧；取消或写失败时尾部已由 Discard 丢弃。
func (s *feedSession) Finish() (FeedResult, error) {
	if err := s.ctx.Err(); err != nil {
		s.Discard()
		return s.res, err
	}
	if s.done {
		return s.res, ErrFeedStopped
	}
	if len(s.pending) > 0 {
		if err := s.send(s.pending); err != nil {
			s.Discard()
			return s.res, err
		}
	}
	s.pending = nil
	s.done = true
	if s.res.Bytes > 0 {
		s.res.Drift = s.feeder.pacer.Clock().Now().Sub(audio.Deadline(s.t0, s.playedMS))
	}
	return s.res, nil
}

func (s *feedSession) Discard() {
	s.pending = nil
	s.done = true
}

func (s *feedSession) send(chunk []byte) error {
	if err := s.ctx.Err(); err != nil {
		return err
	}
	if s.stop != nil && s.stop() {
		return ErrFeedInterrupted
	}
	writeCtx, cancel := context.WithTimeout(s.ctx, s.feeder.write)
	err := s.sender.SendPCM(writeCtx, chunk)
	cancel()
	if err != nil {
		// 单帧失败终止本轮，不重发（蓝本 engine.py:1737-1740）。
		return err
	}

	s.res.Frames++
	s.res.Bytes += len(chunk)
	s.playedMS += audio.MSForBytes(len(chunk))
	delay := s.feeder.pacer.Wait(s.t0, s.playedMS)
	class := audio.Classify(delay)
	switch class {
	case audio.LagLate:
		s.res.Late++
	case audio.LagWarn:
		s.res.Late++
		if s.feeder.warn.ShouldWarn(s.feeder.pacer.Clock().Now(), delay) {
			s.res.Warn++
			if s.feeder.cfg.OnWarn != nil {
				s.feeder.cfg.OnWarn(-delay)
			}
		}
	}
	if delay < 0 {
		if lag := -delay; lag > s.res.MaxLag {
			s.res.MaxLag = lag
		}
		if s.feeder.cfg.OnLate != nil {
			s.feeder.cfg.OnLate(delay)
		}
	}
	return nil
}

// Feed 喂完一轮语音；旧整段入口复用同一个会话实现。
func (f *Feeder) Feed(ctx context.Context, sender PCMSender, pcm []byte, stop func() bool) (FeedResult, error) {
	if len(pcm) == 0 {
		return FeedResult{}, nil
	}
	session := f.newFeedSession(ctx, sender, stop)
	if err := session.Write(pcm); err != nil {
		return session.res, err
	}
	return session.Finish()
}
