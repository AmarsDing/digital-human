package repo

import (
	"context"
	"sync"

	"github.com/google/uuid"

	"github.com/teamwork/digital-human/internal/biz/danmaku"
	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/ent/danmakuevent"
	"github.com/teamwork/digital-human/internal/data/ent/interactiondecision"
)

// EntDanmakuSink 把弹幕事件写 danmakuevent 表（风控与复盘，30 天保留口径）。
type EntDanmakuSink struct {
	client *ent.Client
}

func NewEntDanmakuSink(client *ent.Client) *EntDanmakuSink {
	return &EntDanmakuSink{client: client}
}

func (s *EntDanmakuSink) Record(ctx context.Context, e danmaku.Event) error {
	_, err := s.RecordEvent(ctx, e)
	return err
}

func (s *EntDanmakuSink) RecordEvent(ctx context.Context, e danmaku.Event) (bool, error) {
	id := e.ID
	if id == "" {
		id = uuid.NewString()
	}
	create := s.client.DanmakuEvent.Create().
		SetID(id).
		SetSessionID(e.SessionID).
		SetKind(e.Kind).
		SetUserID(e.UserID).
		SetUserName(e.UserName).
		SetText(e.Text).
		SetDiamond(e.Diamond).
		SetSource(e.Source).
		SetDecision(e.Decision).
		SetOccurredAtMs(e.OccurredAtMs).
		SetReceivedAtMs(e.ReceivedAtMs).
		SetExpiresAtMs(e.ExpiresAtMs).
		SetCreatedAtMs(e.CreatedAtMs)
	if e.SourceEventID != "" {
		create.SetSourceEventID(e.SourceEventID)
	}
	_, err := create.Save(ctx)
	if err == nil {
		return false, nil
	}
	if !isUniqueViolation(err) || e.SourceEventID == "" {
		return false, err
	}
	exists, queryErr := s.client.DanmakuEvent.Query().
		Where(danmakuevent.Source(e.Source), danmakuevent.SourceEventID(e.SourceEventID)).
		Exist(ctx)
	if queryErr != nil {
		return false, queryErr
	}
	if !exists {
		return false, err
	}
	return true, nil
}

// ListOrphanRuleChats 返回已落原始行但尚无 Decision 的规则聊天，按到达顺序供启动扫描补建。
func (s *EntDanmakuSink) ListOrphanRuleChats(ctx context.Context) ([]danmaku.Event, error) {
	rows, err := s.client.DanmakuEvent.Query().
		Where(danmakuevent.Kind("chat"), danmakuevent.Decision("rules")).
		Order(ent.Asc(danmakuevent.FieldCreatedAtMs), ent.Asc(danmakuevent.FieldID)).All(ctx)
	if err != nil {
		return nil, err
	}
	decisions, err := s.client.InteractionDecision.Query().
		Where(interactiondecision.SourceEventRowIDNEQ("")).All(ctx)
	if err != nil {
		return nil, err
	}
	decided := make(map[string]struct{}, len(decisions))
	for _, row := range decisions {
		decided[row.SourceEventRowID] = struct{}{}
	}
	out := make([]danmaku.Event, 0)
	for _, row := range rows {
		if _, ok := decided[row.ID]; ok {
			continue
		}
		out = append(out, danmaku.Event{
			ID: row.ID, SessionID: row.SessionID, Kind: row.Kind,
			UserID: row.UserID, UserName: row.UserName, Text: row.Text,
			Diamond: row.Diamond, Source: row.Source, SourceEventID: row.SourceEventID,
			Decision: row.Decision, OccurredAtMs: row.OccurredAtMs,
			ReceivedAtMs: row.ReceivedAtMs, ExpiresAtMs: row.ExpiresAtMs, CreatedAtMs: row.CreatedAtMs,
		})
	}
	return out, nil
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

func (s *SwitchableDanmakuSink) current() danmaku.EventSink {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db != nil {
		return s.db
	}
	return s.mem
}

func (s *SwitchableDanmakuSink) Record(ctx context.Context, e danmaku.Event) error {
	if c := s.current(); c != nil {
		return c.Record(ctx, e)
	}
	return nil
}

func (s *SwitchableDanmakuSink) RecordEvent(ctx context.Context, e danmaku.Event) (bool, error) {
	c := s.current()
	if c == nil {
		return false, nil
	}
	if sink, ok := c.(danmaku.IdempotentEventSink); ok {
		return sink.RecordEvent(ctx, e)
	}
	return false, c.Record(ctx, e)
}
