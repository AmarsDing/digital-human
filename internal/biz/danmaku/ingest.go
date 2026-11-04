package danmaku

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"time"

	"github.com/google/uuid"
)

// Envelope 是采集层传入的原始事件；SourceEventID 原样保留 relay 幂等键。
type Envelope struct {
	Kind          string
	UserID        string
	UserName      string
	Text          string
	Diamond       int64
	Source        string
	SourceEventID string
	VIP           bool
	OccurredAtMs  int64
	ReceivedAtMs  int64
	ExpiresAtMs   int64
}

// IngestResult 返回已提交的原始事件行及去重结果。
type IngestResult struct {
	OK        bool
	Duplicate bool
	Reason    string
	Event     Event
}

// RuleTap 是 chat 规则入口；原始事件提交成功后才调用。
type RuleTap func(ctx context.Context, kind string, ev TapEvent) error

func (s *Service) SetRuleTap(fn RuleTap) {
	s.mu.Lock()
	s.ruleTap = fn
	s.mu.Unlock()
}

// IngestEvent 先提交原始事件，再更新消费队列并发布 Tap。
func (s *Service) IngestEvent(ctx context.Context, in Envelope) IngestResult {
	in.Text = sanitizeText(in.Text)
	if in.ReceivedAtMs <= 0 {
		in.ReceivedAtMs = time.Now().UnixMilli()
	}
	id := uuid.NewString()
	sourceKey := ""
	if in.SourceEventID != "" {
		sourceKey = in.Source + "\x00" + in.SourceEventID
		sum := sha256.Sum256([]byte(sourceKey))
		id = "evt-" + hex.EncodeToString(sum[:16])
	}

	s.mu.Lock()
	if existing, ok := s.seen[sourceKey]; sourceKey != "" && ok {
		s.mu.Unlock()
		result := IngestResult{OK: true, Duplicate: true, Event: existing}
		switch existing.Decision {
		case "blocked":
			result.OK, result.Reason = false, "unknown_kind"
		case "dropped":
			result.OK, result.Reason = false, "off"
		}
		return result
	}

	event := Event{
		ID: id, SessionID: s.sessionID, Kind: in.Kind,
		UserID: in.UserID, UserName: in.UserName, Text: in.Text,
		Diamond: in.Diamond, Source: in.Source, SourceEventID: in.SourceEventID,
		OccurredAtMs: in.OccurredAtMs, ReceivedAtMs: in.ReceivedAtMs,
		ExpiresAtMs: in.ExpiresAtMs, CreatedAtMs: in.ReceivedAtMs,
		Decision: "t0",
	}
	accepted := true
	reason := ""
	if in.Kind != "chat" && in.Kind != "gift" && in.Kind != "join" {
		accepted, reason, event.Decision = false, "unknown_kind", "blocked"
	} else if s.mode == ModeOff {
		accepted, reason, event.Decision = false, "off", "dropped"
	} else if in.Kind == "chat" && s.ruleTap != nil {
		event.Decision = "rules"
	} else if s.mode == ModeConfirm && in.Kind == "chat" {
		event.Decision = "confirm"
	}

	if s.sink != nil {
		var duplicate bool
		var err error
		if sink, ok := s.sink.(IdempotentEventSink); ok && sourceKey != "" {
			duplicate, err = sink.RecordEvent(ctx, event)
		} else {
			err = s.sink.Record(ctx, event)
		}
		if err != nil {
			s.mu.Unlock()
			return IngestResult{Reason: "persistence_failed", Event: event}
		}
		if duplicate {
			if s.seen == nil {
				s.seen = map[string]Event{}
			}
			s.seen[sourceKey] = event
			s.mu.Unlock()
			return IngestResult{OK: true, Duplicate: true, Event: event}
		}
	}
	if sourceKey != "" {
		if s.seen == nil {
			s.seen = map[string]Event{}
		}
		s.seen[sourceKey] = event
	}
	if !accepted {
		if reason == "off" {
			s.dropped++
		}
		s.mu.Unlock()
		return IngestResult{Reason: reason, Event: event}
	}

	if in.VIP {
		if in.UserName != "" {
			s.vipUsers[in.UserName] = in.ReceivedAtMs
		}
		if in.UserID != "" {
			s.vipUsers[in.UserID] = in.ReceivedAtMs
		}
	}
	routeRules := in.Kind == "chat" && s.ruleTap != nil
	if !routeRules {
		if in.VIP {
			if len(s.vipq) >= maxVIPQueue {
				s.vipq = s.vipq[1:]
				s.dropped++
			}
			s.vipq = append(s.vipq, ConsumeItem{UserName: in.UserName, Text: in.Text, Kind: in.Kind, VIP: true})
		} else {
			if len(s.recent) == MaxRecentDanmaku {
				s.recent = s.recent[1:]
			}
			s.recent = append(s.recent, ConsumeItem{UserName: in.UserName, Text: in.Text, Kind: in.Kind})
		}
		if s.mode == ModeConfirm && in.Kind == "chat" {
			if len(s.pend) >= maxPending {
				s.pend = s.pend[1:]
				s.dropped++
			}
			s.pend = append(s.pend, Pending{ID: uuid.NewString(), UserName: in.UserName, Text: in.Text, CreatedAtMs: in.ReceivedAtMs})
		}
	}
	kindTap, ruleTap := s.kindTap, s.ruleTap
	s.mu.Unlock()

	tap := TapEvent{
		EventRowID: event.ID, SourceEventID: event.SourceEventID,
		UserID: event.UserID, UserName: event.UserName, Text: event.Text,
		Diamond: event.Diamond, VIP: in.VIP, OccurredAtMs: event.OccurredAtMs,
		ReceivedAtMs: event.ReceivedAtMs, ExpiresAtMs: event.ExpiresAtMs,
	}
	if kindTap != nil && (in.Kind == "gift" || in.Kind == "join") {
		kindTap(in.Kind, tap)
	}
	if ruleTap != nil && in.Kind == "chat" {
		if err := ruleTap(ctx, in.Kind, tap); err != nil {
			return IngestResult{Reason: "rule_failed", Event: event}
		}
	}
	return IngestResult{OK: true, Event: event}
}
