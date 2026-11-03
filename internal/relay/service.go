package relay

// Kratos 生成的 CollectorServiceHTTPServer 接口实现（薄适配层）。

import (
	"context"
	"strings"
	"time"

	collectorv1 "github.com/teamwork/digital-human/api/gen/go/collector/v1"
)

// CollectorService 实现 collectorv1.CollectorServiceHTTPServer。
type CollectorService struct {
	tracker *Tracker
	store   *RoomConfigStore
}

// NewCollectorService 构造。
func NewCollectorService(t *Tracker, s *RoomConfigStore) *CollectorService {
	return &CollectorService{tracker: t, store: s}
}

// SetCollector 实现接口。
func (c *CollectorService) SetCollector(_ context.Context, req *collectorv1.SetCollectorRequest) (*collectorv1.SetCollectorReply, error) {
	room := strings.TrimSpace(req.GetRoomId())
	if room == "" {
		return &collectorv1.SetCollectorReply{Ok: false, Error: "room_id 必填"}, nil
	}
	c.store.rc.mu.Lock()
	c.store.rc.roomID = room
	c.store.rc.relayMode = req.GetRelayMode()
	c.store.rc.mu.Unlock()
	return &collectorv1.SetCollectorReply{Ok: true}, nil
}

// GetCollectorStatus 实现接口。
func (c *CollectorService) GetCollectorStatus(context.Context, *collectorv1.GetCollectorStatusRequest) (*collectorv1.GetCollectorStatusReply, error) {
	c.tracker.mu.Lock()
	defer c.tracker.mu.Unlock()
	st := c.tracker.state
	if st != "off" && time.Since(c.tracker.lastBeatAt) > 35*time.Second {
		st = "stopped"
	}
	return &collectorv1.GetCollectorStatusReply{
		State:      st,
		RoomId:     c.tracker.roomID,
		LastMsgMs:  c.tracker.lastMsgMs,
		MsgsPerMin: int64(c.tracker.msgsPerMin),
	}, nil
}
