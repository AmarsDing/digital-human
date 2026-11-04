package repo

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/teamwork/digital-human/internal/biz/song"
	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/ent/songrequest"
)

// EntSongRequestStore 是 song.RequestStore 的 Ent 持久化实现。
type EntSongRequestStore struct {
	client *ent.Client
	policy song.QueuePolicy
	now    func() time.Time

	enqueueMu sync.Mutex
}

func NewEntSongRequestStore(client *ent.Client, policy song.QueuePolicy, now func() time.Time) *EntSongRequestStore {
	if now == nil {
		now = time.Now
	}
	if policy.MaxQueue <= 0 {
		policy.MaxQueue = 10
	}
	if policy.TTL <= 0 {
		policy.TTL = 120 * time.Second
	}
	if policy.UserCooldown <= 0 {
		policy.UserCooldown = 120 * time.Second
	}
	return &EntSongRequestStore{client: client, policy: policy, now: now}
}

func (s *EntSongRequestStore) Enqueue(ctx context.Context, in song.Request) (song.Request, bool, error) {
	s.enqueueMu.Lock()
	defer s.enqueueMu.Unlock()
	if in.SourceEventRowID == "" {
		in.SourceEventRowID = uuid.NewString()
	}
	existing, err := s.client.SongRequest.Query().Where(
		songrequest.SessionID(in.SessionID),
		songrequest.SourceEventRowID(in.SourceEventRowID),
	).Only(ctx)
	if err == nil {
		return songRequestFromEnt(existing), true, nil
	}
	if !ent.IsNotFound(err) {
		return song.Request{}, false, err
	}
	if in.Status == "" {
		in.Status = song.RequestQueued
	}
	if in.Status != song.RequestQueued && in.Status != song.RequestPendingConfirm {
		return song.Request{}, false, song.ErrInvalidRequestTransition
	}

	now := s.now()
	rows, err := s.client.SongRequest.Query().Where(songrequest.SessionID(in.SessionID)).All(ctx)
	if err != nil {
		return song.Request{}, false, err
	}
	active := 0
	var latest int64
	for _, row := range rows {
		if row.Status == song.RequestQueued || row.Status == song.RequestPlaying {
			active++
		}
		if row.UserKey == in.UserKey {
			if row.Status == song.RequestQueued || row.Status == song.RequestPlaying || row.Status == song.RequestUnknown {
				return song.Request{}, false, song.ErrUserActive
			}
			if row.CreatedAtMs > latest {
				latest = row.CreatedAtMs
			}
		}
	}
	if active >= s.policy.MaxQueue {
		return song.Request{}, false, song.ErrQueueFull
	}
	if latest > 0 && now.Sub(time.UnixMilli(latest)) < s.policy.UserCooldown {
		return song.Request{}, false, song.ErrUserCooldown
	}

	nextSeq := int64(1)
	last, err := s.client.SongRequest.Query().Order(ent.Desc(songrequest.FieldQueueSeq)).First(ctx)
	if err == nil {
		nextSeq = last.QueueSeq + 1
	} else if !ent.IsNotFound(err) {
		return song.Request{}, false, err
	}
	in.ID = uuid.NewString()
	in.QueueSeq = nextSeq
	in.Position = 0
	if in.Status == song.RequestQueued {
		in.Position = active + 1
	}
	in.Version = 1
	in.ReconcileStatus = "none"
	in.CreatedAtMs = now.UnixMilli()
	if in.ExpiresAtMs <= 0 {
		in.ExpiresAtMs = now.Add(s.policy.TTL).UnixMilli()
	}
	row, err := s.client.SongRequest.Create().
		SetID(in.ID).
		SetSessionID(in.SessionID).
		SetSourceEventRowID(in.SourceEventRowID).
		SetDecisionID(in.DecisionID).
		SetEffectIdempotencyKey(in.EffectIdempotencyKey).
		SetUserKey(in.UserKey).
		SetFile(in.File).
		SetScript(in.Script).
		SetStatus(in.Status).
		SetQueueSeq(in.QueueSeq).
		SetPosition(in.Position).
		SetVersion(in.Version).
		SetPlayID(in.PlayID).
		SetAttempt(in.Attempt).
		SetLastError(in.LastError).
		SetReconcileStatus(in.ReconcileStatus).
		SetCreatedAtMs(in.CreatedAtMs).
		SetExpiresAtMs(in.ExpiresAtMs).
		SetStartedAtMs(in.StartedAtMs).
		SetTerminalAtMs(in.TerminalAtMs).
		SetTerminalReason(in.TerminalReason).
		Save(ctx)
	if err != nil {
		if isUniqueViolation(err) {
			existing, queryErr := s.client.SongRequest.Query().Where(
				songrequest.SessionID(in.SessionID),
				songrequest.SourceEventRowID(in.SourceEventRowID),
			).Only(ctx)
			if queryErr == nil {
				return songRequestFromEnt(existing), true, nil
			}
		}
		return song.Request{}, false, err
	}
	return songRequestFromEnt(row), false, nil
}

func (s *EntSongRequestStore) Get(ctx context.Context, id string) (song.Request, bool, error) {
	row, err := s.client.SongRequest.Get(ctx, id)
	if ent.IsNotFound(err) {
		return song.Request{}, false, nil
	}
	if err != nil {
		return song.Request{}, false, err
	}
	return songRequestFromEnt(row), true, nil
}

func (s *EntSongRequestStore) List(ctx context.Context) ([]song.Request, error) {
	rows, err := s.client.SongRequest.Query().
		Order(ent.Asc(songrequest.FieldQueueSeq), ent.Asc(songrequest.FieldID)).
		All(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]song.Request, 0, len(rows))
	position := 0
	for _, row := range rows {
		req := songRequestFromEnt(row)
		if req.Status == song.RequestQueued {
			position++
			req.Position = position
		} else {
			req.Position = 0
		}
		out = append(out, req)
	}
	return out, nil
}

func (s *EntSongRequestStore) ClaimNext(ctx context.Context, playID string) (song.Request, bool, error) {
	nowMs := s.now().UnixMilli()
	_, err := s.client.SongRequest.Update().
		Where(
			songrequest.Status(song.RequestQueued),
			songrequest.ExpiresAtMsGT(0),
			songrequest.ExpiresAtMsLTE(nowMs),
		).
		SetStatus(song.RequestExpired).
		SetPosition(0).
		SetTerminalAtMs(nowMs).
		SetTerminalReason("expired").
		AddVersion(1).
		Save(ctx)
	if err != nil {
		return song.Request{}, false, err
	}
	for {
		row, err := s.client.SongRequest.Query().
			Where(songrequest.Status(song.RequestQueued)).
			Order(ent.Asc(songrequest.FieldQueueSeq), ent.Asc(songrequest.FieldID)).
			First(ctx)
		if ent.IsNotFound(err) {
			return song.Request{}, false, nil
		}
		if err != nil {
			return song.Request{}, false, err
		}
		n, err := s.client.SongRequest.Update().
			Where(
				songrequest.ID(row.ID),
				songrequest.Status(song.RequestQueued),
				songrequest.Version(row.Version),
			).
			SetStatus(song.RequestPlaying).
			SetPosition(0).
			SetPlayID(playID).
			SetStartedAtMs(nowMs).
			AddAttempt(1).
			AddVersion(1).
			Save(ctx)
		if err != nil {
			return song.Request{}, false, err
		}
		if n == 0 {
			continue
		}
		req := songRequestFromEnt(row)
		req.Status = song.RequestPlaying
		req.Position = 0
		req.Version++
		req.PlayID = playID
		req.Attempt++
		req.StartedAtMs = nowMs
		return req, true, nil
	}
}

func (s *EntSongRequestStore) Complete(ctx context.Context, id, playID string, playErr error) (bool, error) {
	nowMs := s.now().UnixMilli()
	update := s.client.SongRequest.Update().
		Where(songrequest.ID(id), songrequest.Status(song.RequestPlaying), songrequest.PlayID(playID)).
		SetTerminalAtMs(nowMs).
		AddVersion(1)
	if playErr == nil {
		update.SetStatus(song.RequestDone).SetTerminalReason("completed")
	} else if errors.Is(playErr, song.ErrStopped) {
		update.SetStatus(song.RequestSkipped).SetTerminalReason("stopped").SetLastError(playErr.Error())
	} else {
		update.SetStatus(song.RequestFailed).SetTerminalReason("play_failed").SetLastError(playErr.Error())
	}
	n, err := update.Save(ctx)
	return n > 0, err
}

func (s *EntSongRequestStore) RequeueNotStarted(ctx context.Context, id, playID string, cause error) (bool, error) {
	n, err := s.client.SongRequest.Update().
		Where(songrequest.ID(id), songrequest.Status(song.RequestPlaying), songrequest.PlayID(playID)).
		SetStatus(song.RequestQueued).
		SetPlayID("").
		SetLastError(cause.Error()).
		AddVersion(1).
		Save(ctx)
	return n > 0, err
}

func (s *EntSongRequestStore) FailNotStarted(ctx context.Context, id, playID string, cause error) (bool, error) {
	n, err := s.client.SongRequest.Update().
		Where(songrequest.ID(id), songrequest.Status(song.RequestPlaying), songrequest.PlayID(playID)).
		SetStatus(song.RequestFailed).
		SetLastError(cause.Error()).
		SetTerminalReason("start_failed").
		SetTerminalAtMs(s.now().UnixMilli()).
		AddVersion(1).
		Save(ctx)
	return n > 0, err
}

func (s *EntSongRequestStore) RecoverPlaying(ctx context.Context) error {
	_, err := s.client.SongRequest.Update().
		Where(songrequest.Status(song.RequestPlaying)).
		SetStatus(song.RequestUnknown).
		SetReconcileStatus("pending").
		SetLastError("playing_recovery_unknown").
		AddVersion(1).
		Save(ctx)
	return err
}

func (s *EntSongRequestStore) ResolveUnknown(ctx context.Context, id string, action song.RequestReconcileAction) error {
	update := s.client.SongRequest.Update().
		Where(songrequest.ID(id), songrequest.Status(song.RequestUnknown)).
		SetReconcileStatus("resolved").
		AddVersion(1)
	switch action {
	case song.RequestReconcileDone:
		update.SetStatus(song.RequestDone).
			SetTerminalReason("manual_completed").
			SetTerminalAtMs(s.now().UnixMilli())
	case song.RequestReconcileFailed:
		update.SetStatus(song.RequestFailed).
			SetTerminalReason("manual_failed").
			SetTerminalAtMs(s.now().UnixMilli())
	case song.RequestReconcileRequeue:
		update.SetStatus(song.RequestQueued).
			SetPlayID("").
			SetLastError("").
			SetTerminalAtMs(0).
			SetTerminalReason("manual_requeued")
	default:
		return song.ErrUnknownState
	}
	n, err := update.Save(ctx)
	if err != nil {
		return err
	}
	if n == 0 {
		return song.ErrUnknownState
	}
	return nil
}

func (s *EntSongRequestStore) ChangeStatus(ctx context.Context, id, action string) error {
	var update *ent.SongRequestUpdate
	switch action {
	case song.RequestActionApprove:
		update = s.client.SongRequest.Update().
			Where(songrequest.ID(id), songrequest.Status(song.RequestPendingConfirm)).
			SetStatus(song.RequestQueued)
	case song.RequestActionReject:
		update = s.client.SongRequest.Update().
			Where(songrequest.ID(id), songrequest.Status(song.RequestPendingConfirm)).
			SetStatus(song.RequestRejected).
			SetPosition(0).
			SetTerminalAtMs(s.now().UnixMilli()).
			SetTerminalReason("rejected")
	case song.RequestActionCancel:
		update = s.client.SongRequest.Update().
			Where(
				songrequest.ID(id),
				songrequest.Or(
					songrequest.Status(song.RequestPendingConfirm),
					songrequest.Status(song.RequestQueued),
				),
			).
			SetStatus(song.RequestCancelled).
			SetPosition(0).
			SetTerminalAtMs(s.now().UnixMilli()).
			SetTerminalReason("cancelled")
	default:
		_, ok, err := s.Get(ctx, id)
		if err != nil {
			return err
		}
		if !ok {
			return song.ErrRequestNotFound
		}
		return song.ErrInvalidRequestTransition
	}
	n, err := update.AddVersion(1).Save(ctx)
	if err != nil {
		return err
	}
	if n > 0 {
		return nil
	}
	_, ok, err := s.Get(ctx, id)
	if err != nil {
		return err
	}
	if !ok {
		return song.ErrRequestNotFound
	}
	return song.ErrInvalidRequestTransition
}

func songRequestFromEnt(row *ent.SongRequest) song.Request {
	return song.Request{
		ID: row.ID, SessionID: row.SessionID, SourceEventRowID: row.SourceEventRowID,
		DecisionID: row.DecisionID, EffectIdempotencyKey: row.EffectIdempotencyKey,
		UserKey: row.UserKey, File: row.File, Script: row.Script, Status: row.Status,
		QueueSeq: row.QueueSeq, Position: row.Position, Version: row.Version,
		PlayID: row.PlayID, Attempt: row.Attempt, LastError: row.LastError,
		ReconcileStatus: row.ReconcileStatus, CreatedAtMs: row.CreatedAtMs,
		ExpiresAtMs: row.ExpiresAtMs, StartedAtMs: row.StartedAtMs,
		TerminalAtMs: row.TerminalAtMs, TerminalReason: row.TerminalReason,
	}
}

// SwitchableSongRequestStore 启动时使用内存队列，PG 就绪后切换为 Ent。
// 热切不回迁切换前的内存请求；持久能力以当前 Ent 实现为准。
type SwitchableSongRequestStore struct {
	mu     sync.RWMutex
	mem    song.RequestStore
	db     song.RequestStore
	policy song.QueuePolicy
	now    func() time.Time
}

func NewSwitchableSongRequestStore(mem song.RequestStore, policy song.QueuePolicy, now func() time.Time) *SwitchableSongRequestStore {
	if now == nil {
		now = time.Now
	}
	return &SwitchableSongRequestStore{mem: mem, policy: policy, now: now}
}

func (s *SwitchableSongRequestStore) SetEnt(client *ent.Client) {
	s.mu.Lock()
	s.db = NewEntSongRequestStore(client, s.policy, s.now)
	s.mu.Unlock()
}

func (s *SwitchableSongRequestStore) current() song.RequestStore {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.db != nil {
		return s.db
	}
	return s.mem
}

func (s *SwitchableSongRequestStore) Enqueue(ctx context.Context, in song.Request) (song.Request, bool, error) {
	return s.current().Enqueue(ctx, in)
}
func (s *SwitchableSongRequestStore) ClaimNext(ctx context.Context, playID string) (song.Request, bool, error) {
	return s.current().ClaimNext(ctx, playID)
}
func (s *SwitchableSongRequestStore) Complete(ctx context.Context, id, playID string, playErr error) (bool, error) {
	return s.current().Complete(ctx, id, playID, playErr)
}
func (s *SwitchableSongRequestStore) RequeueNotStarted(ctx context.Context, id, playID string, cause error) (bool, error) {
	return s.current().RequeueNotStarted(ctx, id, playID, cause)
}
func (s *SwitchableSongRequestStore) FailNotStarted(ctx context.Context, id, playID string, cause error) (bool, error) {
	return s.current().FailNotStarted(ctx, id, playID, cause)
}
func (s *SwitchableSongRequestStore) RecoverPlaying(ctx context.Context) error {
	return s.current().RecoverPlaying(ctx)
}
func (s *SwitchableSongRequestStore) Get(ctx context.Context, id string) (song.Request, bool, error) {
	return s.current().Get(ctx, id)
}
func (s *SwitchableSongRequestStore) List(ctx context.Context) ([]song.Request, error) {
	return s.current().List(ctx)
}
func (s *SwitchableSongRequestStore) ResolveUnknown(ctx context.Context, id string, action song.RequestReconcileAction) error {
	return s.current().ResolveUnknown(ctx, id, action)
}
func (s *SwitchableSongRequestStore) ChangeStatus(ctx context.Context, id, action string) error {
	return s.current().ChangeStatus(ctx, id, action)
}

var _ song.RequestStore = (*EntSongRequestStore)(nil)
var _ song.RequestStore = (*SwitchableSongRequestStore)(nil)
