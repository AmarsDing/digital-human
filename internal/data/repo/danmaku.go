package repo

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/teamwork/digital-human/internal/biz/danmaku"
	"github.com/teamwork/digital-human/internal/data/ent"
)

// EntDanmakuSink 把弹幕事件写 danmakuevent 表（风控与复盘，30 天保留口径）。
type EntDanmakuSink struct {
	client *ent.Client
}

func NewEntDanmakuSink(client *ent.Client) *EntDanmakuSink {
	return &EntDanmakuSink{client: client}
}

func (s *EntDanmakuSink) Record(ctx context.Context, e danmaku.Event) error {
	_, err := s.client.DanmakuEvent.Create().
		SetID(uuid.NewString()).
		SetSessionID(e.SessionID).
		SetKind(e.Kind).
		SetUserID(e.UserID).
		SetUserName(e.UserName).
		SetText(e.Text).
		SetDiamond(e.Diamond).
		SetSource(e.Source).
		SetDecision(e.Decision).
		SetCreatedAtMs(e.CreatedAtMs).
		Save(ctx)
	return err
}

// SwitchableDanmakuSink 启动时用内存环形缓冲兜底；PG 运行期就绪后原子切换为 Ent。
// 内存降级只保留最近若干条供排查，不提供复盘查询（重启即丢）。
type SwitchableDanmakuSink struct {
	mu  sync.RWMutex
	mem danmaku.EventSink
	db  danmaku.EventSink
}

func NewSwitchableDanmakuSink(mem danmaku.EventSink) *SwitchableDanmakuSink {
	return &SwitchableDanmakuSink{mem: mem}
}

func (s *SwitchableDanmakuSink) SetEnt(client *ent.Client) {
	s.mu.Lock()
	s.db = NewEntDanmakuSink(client)
	s.mu.Unlock()
}

func (s *SwitchableDanmakuSink) Record(ctx context.Context, e danmaku.Event) error {
	s.mu.RLock()
	c := s.mem
	if s.db != nil {
		c = s.db
	}
	s.mu.RUnlock()
	if c == nil {
		return nil
	}
	return c.Record(ctx, e)
}
