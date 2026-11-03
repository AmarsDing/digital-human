package repo

import (
	"context"

	"github.com/google/uuid"

	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/ent/sessionevent"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

// EntEventSink 实现 live.EventSink（结构化接口，不 import biz）。
type EntEventSink struct {
	client *ent.Client
}

func NewEntEventSink(client *ent.Client) *EntEventSink {
	return &EntEventSink{client: client}
}

func (s *EntEventSink) Append(ctx context.Context, e eventbus.Event) error {
	_, err := s.client.SessionEvent.Create().
		SetID(uuid.NewString()).
		SetSessionID(e.SessionID).
		SetSeq(e.Seq).
		SetEventID(e.EventID).
		SetType(e.Type).
		SetTsMs(e.TsMs).
		SetEpoch(e.Epoch).
		SetRequestID(e.RequestID).
		SetPayload(e.Payload).
		Save(ctx)
	return err
}

func (s *EntEventSink) List(ctx context.Context, sessionID string, afterSeq int64, limit int) ([]eventbus.Event, error) {
	q := s.client.SessionEvent.Query().
		Where(sessionevent.SeqGT(afterSeq)).
		Order(ent.Asc(sessionevent.FieldSeq)).
		Limit(limit)
	if sessionID != "" {
		q.Where(sessionevent.SessionID(sessionID))
	}
	rows, err := q.All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]eventbus.Event, 0, len(rows))
	for _, r := range rows {
		out = append(out, eventbus.Event{
			Seq:       r.Seq,
			EventID:   r.EventID,
			SessionID: r.SessionID,
			Type:      r.Type,
			TsMs:      r.TsMs,
			Epoch:     r.Epoch,
			RequestID: r.RequestID,
			Payload:   r.Payload,
		})
	}
	return out, nil
}
