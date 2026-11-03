package repo

import (
	"context"
	"sync"
	"time"

	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/ent/livesession"
)

// EntSessionStore 实现 live.SessionStore：存在则 Update，否则 Create。
type EntSessionStore struct {
	client *ent.Client
}

func NewEntSessionStore(client *ent.Client) *EntSessionStore {
	return &EntSessionStore{client: client}
}

func (s *EntSessionStore) Save(ctx context.Context, r live.SessionRecord) error {
	exists, err := s.client.LiveSession.Query().Where(livesession.ID(r.ID)).Exist(ctx)
	if err != nil {
		return err
	}
	if exists {
		_, err := s.client.LiveSession.UpdateOneID(r.ID).
			SetNillableLiveID(nilIfEmpty(r.LiveID)).
			SetPhase(string(r.Phase)).
			SetMode(r.Mode).
			SetPersonaID(r.PersonaID).
			SetAvatarID(r.AvatarID).
			SetStartedAtMs(r.StartedAtMs).
			SetEndedAtMs(r.EndedAtMs).
			SetDurationS(r.DurationS).
			SetRecordingState(r.RecordingState).
			SetErrorCode(r.ErrorCode).
			SetUpdatedAtMs(r.UpdatedAtMs).
			Save(ctx)
		return err
	}
	_, err = s.client.LiveSession.Create().
		SetID(r.ID).
		SetNillableLiveID(nilIfEmpty(r.LiveID)).
		SetPhase(string(r.Phase)).
		SetMode(r.Mode).
		SetPersonaID(r.PersonaID).
		SetAvatarID(r.AvatarID).
		SetStartedAtMs(r.StartedAtMs).
		SetEndedAtMs(r.EndedAtMs).
		SetDurationS(r.DurationS).
		SetRecordingState(r.RecordingState).
		SetErrorCode(r.ErrorCode).
		SetUpdatedAtMs(r.UpdatedAtMs).
		Save(ctx)
	return err
}

func (s *EntSessionStore) ListStale(ctx context.Context, cutoffMs int64) ([]live.SessionRecord, error) {
	rows, err := s.client.LiveSession.Query().
		Where(
			livesession.PhaseIn(string(live.PhaseStarting), string(live.PhaseLive), string(live.PhaseStopping)),
			livesession.UpdatedAtMsLT(cutoffMs),
		).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]live.SessionRecord, 0, len(rows))
	for _, r := range rows {
		liveID := ""
		if r.LiveID != nil {
			liveID = *r.LiveID
		}
		out = append(out, live.SessionRecord{
			ID:             r.ID,
			LiveID:         liveID,
			Phase:          live.Phase(r.Phase),
			Mode:           r.Mode,
			PersonaID:      r.PersonaID,
			AvatarID:       r.AvatarID,
			StartedAtMs:    r.StartedAtMs,
			EndedAtMs:      r.EndedAtMs,
			DurationS:      r.DurationS,
			RecordingState: r.RecordingState,
			ErrorCode:      r.ErrorCode,
			UpdatedAtMs:    r.UpdatedAtMs,
		})
	}
	return out, nil
}

func (s *EntSessionStore) MarkCrashed(ctx context.Context, id string) error {
	err := s.client.LiveSession.UpdateOneID(id).
		SetPhase(string(live.PhaseError)).
		SetErrorCode("crash_recovered").
		SetEndedAtMs(time.Now().UnixMilli()).
		SetUpdatedAtMs(time.Now().UnixMilli()).
		Exec(ctx)
	if ent.IsNotFound(err) {
		return nil
	}
	return err
}

func (s *EntSessionStore) List(ctx context.Context, limit, offset int, phase string) ([]live.SessionRecord, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	q := s.client.LiveSession.Query()
	if phase != "" {
		q = q.Where(livesession.Phase(phase))
	}
	rows, err := q.Order(ent.Desc(livesession.FieldStartedAtMs)).Offset(offset).Limit(limit).All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]live.SessionRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, entSessionRecord(r))
	}
	return out, nil
}

func (s *EntSessionStore) Count(ctx context.Context, phase string) (int, error) {
	q := s.client.LiveSession.Query()
	if phase != "" {
		q = q.Where(livesession.Phase(phase))
	}
	return q.Count(ctx)
}

func (s *EntSessionStore) GetRecord(ctx context.Context, id string) (live.SessionRecord, bool, error) {
	r, err := s.client.LiveSession.Get(ctx, id)
	if ent.IsNotFound(err) {
		return live.SessionRecord{}, false, nil
	}
	if err != nil {
		return live.SessionRecord{}, false, err
	}
	return entSessionRecord(r), true, nil
}

func entSessionRecord(r *ent.LiveSession) live.SessionRecord {
	liveID := ""
	if r.LiveID != nil {
		liveID = *r.LiveID
	}
	return live.SessionRecord{
		ID:             r.ID,
		LiveID:         liveID,
		Phase:          live.Phase(r.Phase),
		Mode:           r.Mode,
		PersonaID:      r.PersonaID,
		AvatarID:       r.AvatarID,
		StartedAtMs:    r.StartedAtMs,
		EndedAtMs:      r.EndedAtMs,
		DurationS:      r.DurationS,
		RecordingState: r.RecordingState,
		ErrorCode:      r.ErrorCode,
		UpdatedAtMs:    r.UpdatedAtMs,
	}
}

// nilIfEmpty 让空的 live_id 以 NULL 落库，避免与唯一索引冲突（PG 唯一索引允许多个 NULL）。
func nilIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

// SwitchableSessionStore 启动时用内存实现；PG 运行期就绪后原子切换为 Ent。
type SwitchableSessionStore struct {
	mu  sync.RWMutex
	mem live.SessionStore
	db  live.SessionStore
}

func NewSwitchableSessionStore(mem live.SessionStore) *SwitchableSessionStore {
	return &SwitchableSessionStore{mem: mem}
}

func (s *SwitchableSessionStore) SetEnt(client *ent.Client) {
	s.mu.Lock()
	s.db = NewEntSessionStore(client)
	s.mu.Unlock()
}

func (s *SwitchableSessionStore) current() live.SessionStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db != nil {
		return s.db
	}
	return s.mem
}

func (s *SwitchableSessionStore) Save(ctx context.Context, r live.SessionRecord) error {
	if c := s.current(); c != nil {
		return c.Save(ctx, r)
	}
	return nil
}

func (s *SwitchableSessionStore) ListStale(ctx context.Context, cutoffMs int64) ([]live.SessionRecord, error) {
	if c := s.current(); c != nil {
		return c.ListStale(ctx, cutoffMs)
	}
	return nil, nil
}

func (s *SwitchableSessionStore) MarkCrashed(ctx context.Context, id string) error {
	if c := s.current(); c != nil {
		return c.MarkCrashed(ctx, id)
	}
	return nil
}

func (s *SwitchableSessionStore) List(ctx context.Context, limit, offset int, phase string) ([]live.SessionRecord, error) {
	if c := s.current(); c != nil {
		return c.List(ctx, limit, offset, phase)
	}
	return nil, nil
}

func (s *SwitchableSessionStore) Count(ctx context.Context, phase string) (int, error) {
	if c := s.current(); c != nil {
		return c.Count(ctx, phase)
	}
	return 0, nil
}

func (s *SwitchableSessionStore) GetRecord(ctx context.Context, id string) (live.SessionRecord, bool, error) {
	if c := s.current(); c != nil {
		return c.GetRecord(ctx, id)
	}
	return live.SessionRecord{}, false, nil
}
