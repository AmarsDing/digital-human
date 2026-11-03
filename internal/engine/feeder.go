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

// Feed 喂完一轮语音。t0 每轮独立（静默期不喂流，不能用全局连续流假设）。
// stop 为 true（被打断/已停播）时在下一帧前退出。
func (f *Feeder) Feed(ctx context.Context, sender PCMSender, pcm []byte, stop func() bool) (FeedResult, error) {
	var res FeedResult
	if len(pcm) == 0 {
		return res, nil
	}
	t0 := f.pacer.Clock().Now()
	playedMS := int64(0)

	for _, chunk := range audio.SplitPCM(pcm, f.pacer.ChunkBytes()) {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		if stop != nil && stop() {
			return res, ErrFeedInterrupted
		}

		writeCtx, cancel := context.WithTimeout(ctx, f.write)
		err := sender.SendPCM(writeCtx, chunk)
		cancel()
		if err != nil {
			// 单帧失败终止本轮，不重发（蓝本 engine.py:1737-1740）。
			return res, err
		}

		res.Frames++
		res.Bytes += len(chunk)
		playedMS += audio.MSForBytes(len(chunk))

		delay := f.pacer.Wait(t0, playedMS)
		class := audio.Classify(delay)
		switch class {
		case audio.LagLate:
			res.Late++
		case audio.LagWarn:
			res.Late++
			if f.warn.ShouldWarn(f.pacer.Clock().Now(), delay) {
				res.Warn++
				if f.cfg.OnWarn != nil {
					f.cfg.OnWarn(-delay)
				}
			}
		}
		if delay < 0 {
			if lag := -delay; lag > res.MaxLag {
				res.MaxLag = lag
			}
			if f.cfg.OnLate != nil {
				f.cfg.OnLate(delay)
			}
		}
	}
	res.Drift = f.pacer.Clock().Now().Sub(audio.Deadline(t0, playedMS))
	return res, nil
}
