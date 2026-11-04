package service

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/go-kratos/kratos/v2/transport"
	livev1 "github.com/teamwork/digital-human/api/gen/go/live/v1"
	"github.com/teamwork/digital-human/internal/biz/action"
	"github.com/teamwork/digital-human/internal/biz/danmaku"
	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/biz/rtc"
	"github.com/teamwork/digital-human/internal/publish"
)

// LiveService 只做参数映射，业务全部在 biz/live；动作指令额外经过 action.Scheduler
// （优先级/冷却/队列）并计入命令面板 Top10（蓝本 dm_router.count）。
type LiveService struct {
	m     *live.Manager
	sched *action.Scheduler
	dm    *danmaku.Service
	rtc   *rtc.Issuer
}

func NewLiveService(m *live.Manager, sched *action.Scheduler, dm *danmaku.Service, issuer *rtc.Issuer) *LiveService {
	return &LiveService{m: m, sched: sched, dm: dm, rtc: issuer}
}

func (s *LiveService) StartLive(ctx context.Context, req *livev1.StartLiveRequest) (*livev1.StartLiveReply, error) {
	key, err := idempotencyKey(ctx, req.GetIdempotencyKey())
	if err != nil {
		return nil, err
	}
	// 深化方案 v2 §7.6 严格 Start：mode/preflight_token 透传给 Manager 在 opMu 临界区复核。
	sessionID, phase, liveID, mode, err := s.m.StartIdempotentWithMode(ctx, key, req.GetPersonaId(), req.GetAvatarId(), req.GetGreeting(), live.StartRequest{
		Mode:             live.Mode(req.GetMode()),
		PreflightToken:   req.GetPreflightToken(),
		ResultDigest:     req.GetResultDigest(),
		PersonaRevision:  req.GetRevision(),
		DraftID:          req.GetDraftId(),
		ExpectedRevision: req.GetExpectedRevision(),
	})
	if err != nil {
		return nil, mapLiveError(err)
	}
	if s.dm != nil {
		s.dm.BeginSession(sessionID)
	}
	return &livev1.StartLiveReply{SessionId: sessionID, Phase: string(phase), LiveId: liveID, Mode: string(mode)}, nil
}

func (s *LiveService) StopLive(ctx context.Context, req *livev1.StopLiveRequest) (*livev1.StopLiveReply, error) {
	key, err := idempotencyKey(ctx, req.GetIdempotencyKey())
	if err != nil {
		return nil, err
	}
	ok, note, err := s.m.StopIdempotent(ctx, key, "manual")
	if err != nil {
		return nil, mapLiveError(err)
	}
	return &livev1.StopLiveReply{Phase: string(live.PhaseEnded), Ok: ok, Note: note, RecordingState: s.m.RecordingState()}, nil
}

func (s *LiveService) GetLiveStatus(ctx context.Context, req *livev1.GetLiveStatusRequest) (*livev1.GetLiveStatusReply, error) {
	snap, err := s.m.SnapshotFor(req.GetSessionId(), int(req.GetTranscriptSince()))
	if err != nil {
		return nil, mapLiveError(err)
	}
	rt := s.m.RuntimeStatus(ctx)
	ws := s.m.WSStatusFor(snap.SessionID)
	// feed 仅在真实引擎实现 StatusProvider 且 Known=true 时才给出 owner；
	// 否则 owner_kind=unknown，绝不伪造 true。
	feed := &livev1.FeedStatus{OwnerKind: "unknown"}
	if rt.FeedKnown {
		feed = &livev1.FeedStatus{
			Epoch: rt.FeedEpoch, LagMs: rt.FeedLagMs,
			Owner: rt.FeedOwner, OwnerKind: rt.FeedKind,
		}
	}
	rtc := &livev1.RtcStatus{Mode: "unconfigured", Provider: "none"}
	if rt.RTCKnown {
		rtc = &livev1.RtcStatus{
			Mode: rt.RTCMode, Provider: rt.RTCProvider,
			TokenExpireAt: rt.RTCTokenExpireAt,
		}
	} else if s.rtc != nil {
		mode, provider := s.rtc.Status()
		rtc = &livev1.RtcStatus{Mode: mode, Provider: provider}
	}
	dm := &livev1.DanmakuStatus{}
	if s.dm != nil {
		sid := req.GetSessionId()
		if sid == "" {
			sid = snap.SessionID
		}
		if pending, dropped, t2, ok := s.dm.StatsFor(sid); ok {
			dm = &livev1.DanmakuStatus{Pending: int32(pending), DroppedTotal: dropped, T2SkippedTotal: t2}
		}
	}
	return &livev1.GetLiveStatusReply{
		SessionId:    snap.SessionID,
		Phase:        string(snap.Phase),
		LiveId:       snap.LiveID,
		StartedAtMs:  snap.StartedAtMs,
		DurationS:    snap.DurationS,
		EventSeq:     snap.EventSeq,
		Transcripts:  snap.Transcript,
		ErrorCode:    snap.ErrorCode,
		ErrorMessage: snap.ErrorMsg,
		Feed:         feed,
		Ws: &livev1.WsStatus{
			State: ws.WSState, ReconnectTotal: ws.WSReconnectTotal, LastPongMs: ws.WSPongMs,
		},
		Danmaku: dm,
		Rtc:     rtc,
		// F34/N2：录播运行态随状态轮询下发（蓝本 stage 轮询 /api/record 语义）。
		Record: &livev1.RecordStatus{
			Enabled: s.m.RecordingEnabled(),
			State:   s.m.RecordingState(),
			Url:     s.m.RecordingURL(),
		},
	}, nil
}

// GetRtcToken 签发/刷新 RTC 凭据。未配置时返回 200 + mode=unconfigured/ok=false，
// 让前端明确展示“未配置”，而不是假装正在等待连接。
func (s *LiveService) GetRtcToken(_ context.Context, req *livev1.GetRtcTokenRequest) (*livev1.GetRtcTokenReply, error) {
	snap, err := s.m.SnapshotFor(req.GetSessionId(), 0)
	if err != nil {
		return nil, mapLiveError(err)
	}
	if s.rtc == nil || snap.SessionID == "" {
		return &livev1.GetRtcTokenReply{
			Mode: "unconfigured", Provider: "none", Ok: false, Reason: "rtc_not_configured",
		}, nil
	}
	t, err := s.rtc.Issue(snap.SessionID)
	if err != nil {
		return nil, mapLiveError(err)
	}
	return &livev1.GetRtcTokenReply{
		Mode: t.Mode, Provider: t.Provider, Token: t.Token,
		ExpiresAt: t.ExpiresAt, RoomId: t.RoomID, UserId: t.UserID,
		Ok: t.OK, Reason: t.Reason,
	}, nil
}

// WSConnected / WSDisconnected 把真实 WS 连接态写入 Manager，供 GetLiveStatus 读取。
func (s *LiveService) WSConnected(sessionID string)    { s.m.WSConnected(sessionID) }
func (s *LiveService) WSDisconnected(sessionID string) { s.m.WSDisconnected(sessionID) }

// idempotencyKey 读取 Idempotency-Key 头；若 body 也带 key，则两者必须一致。
func idempotencyKey(ctx context.Context, bodyKey string) (string, error) {
	var header string
	if tr, ok := transport.FromServerContext(ctx); ok && tr != nil {
		header = strings.TrimSpace(tr.RequestHeader().Get("Idempotency-Key"))
	}
	bodyKey = strings.TrimSpace(bodyKey)
	if header != "" && bodyKey != "" && header != bodyKey {
		return "", kerrors.New(400, "idempotency_key_mismatch", "Idempotency-Key 头与 body.idempotency_key 不一致")
	}
	if header != "" {
		return header, nil
	}
	return bodyKey, nil
}

func mapLiveError(err error) error {
	switch {
	case errors.Is(err, live.ErrEngineUnavailable):
		return kerrors.New(503, "engine_unavailable", err.Error())
	case errors.Is(err, live.ErrAvatarExpired):
		// v3 §3.3 预检失败分支：reason 与前端 client.ts/StagePage 归一码对齐（avatar_expired）。
		return kerrors.New(400, "avatar_expired", "档案形象未同步或 Vidu 资产已过期，请重新同步后开播")
	case errors.Is(err, live.ErrSessionMismatch):
		return kerrors.New(404, "session_not_found", err.Error())
	case errors.Is(err, live.ErrModeNotAllowed):
		return kerrors.New(409, "mode_not_allowed", err.Error())
	case errors.Is(err, live.ErrDraftRequired):
		return kerrors.New(409, "draft_required", err.Error())
	case errors.Is(err, live.ErrPreflightExpired):
		return kerrors.New(428, "preflight_expired", err.Error())
	case errors.Is(err, live.ErrProfileChanged):
		return kerrors.New(409, "profile_changed", err.Error())
	case errors.Is(err, live.ErrPreflightRequired):
		// §7.6：public-live 缺/错/过期 preflight_token → 428 前置要求（先走唯一预检）。
		return kerrors.New(428, "preflight_required", err.Error())
	case errors.Is(err, live.ErrResolveFailed):
		// §7.6-3：Resolve 失败/档案删除不回落旧路径，显式 409（不创建场次）。
		return kerrors.New(409, "avatar_resolve_failed", err.Error())
	case errors.Is(err, live.ErrCapabilityBlocked):
		return kerrors.New(409, "capability_blocked", err.Error())
	case errors.Is(err, live.ErrPushGateRequired):
		return kerrors.New(409, "push_gate_required", err.Error())
	case publish.ErrorCode(err) == "publish_target_unverified":
		return kerrors.New(409, "publish_target_unverified", err.Error())
	case errors.Is(err, live.ErrPreviewWhilePushing):
		return kerrors.New(409, "local_preview_while_pushing", err.Error())
	default:
		return err
	}
}

func (s *LiveService) Say(ctx context.Context, req *livev1.SayRequest) (*livev1.SayReply, error) {
	id, err := s.m.Say(ctx, req.GetText(), req.GetMode())
	if err != nil {
		return nil, err
	}
	return &livev1.SayReply{UtteranceId: id}, nil
}

func (s *LiveService) SendCommand(ctx context.Context, req *livev1.SendCommandRequest) (*livev1.SendCommandReply, error) {
	if req.GetText() == "" {
		return &livev1.SendCommandReply{Accepted: false, Reason: "empty"}, nil
	}
	if s.sched == nil {
		s.m.Interrupt(ctx)
		return &livev1.SendCommandReply{Accepted: true}, nil
	}
	if s.dm != nil {
		s.dm.NoteCommand(req.GetText(), time.Now())
	}
	res, err := s.sched.Command(ctx, req.GetText(), "manual", "")
	if err != nil {
		return &livev1.SendCommandReply{Accepted: false, Reason: err.Error()}, nil
	}
	reason := ""
	if res.Deferred {
		reason = "queued"
	}
	if res.Dropped != "" {
		reason = "queued;dropped:" + res.Dropped
	}
	return &livev1.SendCommandReply{Accepted: true, Reason: reason}, nil
}

func (s *LiveService) Interrupt(ctx context.Context, _ *livev1.InterruptRequest) (*livev1.InterruptReply, error) {
	s.m.Interrupt(ctx)
	return &livev1.InterruptReply{Ok: true}, nil
}

func (s *LiveService) ClearTranscript(ctx context.Context, _ *livev1.ClearTranscriptRequest) (*livev1.ClearTranscriptReply, error) {
	n := s.m.ClearTranscript(ctx)
	return &livev1.ClearTranscriptReply{Ok: true, Cleared: int32(n)}, nil
}

// StopActions 中断当前动作语音。装配了调度器时由 sched.Interrupt 统一负责
// （停当前 + 清队列 + 单次 action.stopped 事件）；否则退回 Manager 发事件。
func (s *LiveService) StopActions(ctx context.Context, _ *livev1.StopActionsRequest) (*livev1.StopActionsReply, error) {
	cleared := 0
	if s.sched != nil {
		cleared = s.sched.Interrupt(ctx)
	} else {
		s.m.StopActions(ctx)
	}
	return &livev1.StopActionsReply{Ok: true, Cleared: int32(cleared)}, nil
}

// ClearActions 只清动作队列，不影响当前播放中的动作。
func (s *LiveService) ClearActions(ctx context.Context, _ *livev1.ClearActionsRequest) (*livev1.ClearActionsReply, error) {
	cleared := 0
	if s.sched != nil {
		cleared = s.sched.Clear(ctx)
	} else {
		s.m.ClearActions(ctx)
	}
	return &livev1.ClearActionsReply{Ok: true, Cleared: int32(cleared)}, nil
}

func (s *LiveService) ResetLlm(ctx context.Context, _ *livev1.ResetLlmRequest) (*livev1.ResetLlmReply, error) {
	return &livev1.ResetLlmReply{ClearedTurns: int32(s.m.ResetLlm(ctx))}, nil
}

func (s *LiveService) ListEvents(ctx context.Context, req *livev1.ListEventsRequest) (*livev1.ListEventsReply, error) {
	rows, err := s.m.ListEvents(ctx, req.GetSessionId(), req.GetAfterSeq(), int(req.GetLimit()))
	if err != nil {
		return nil, err
	}
	out := make([]*livev1.LiveEvent, 0, len(rows))
	for _, e := range rows {
		payload, _ := json.Marshal(e.Payload)
		out = append(out, &livev1.LiveEvent{
			Seq: e.Seq, EventId: e.EventID, SessionId: e.SessionID,
			Type: e.Type, TsMs: e.TsMs, Epoch: e.Epoch,
			PayloadJson: payload,
			RequestId:   e.RequestID,
		})
	}
	return &livev1.ListEventsReply{Events: out}, nil
}
