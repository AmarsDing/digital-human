package service

import (
	"context"

	livev1 "github.com/teamwork/digital-human/api/gen/go/live/v1"
	"github.com/teamwork/digital-human/internal/biz/danmaku"
)

type DanmakuService struct {
	svc *danmaku.Service
}

func NewDanmakuService(svc *danmaku.Service) *DanmakuService { return &DanmakuService{svc: svc} }

// Ingest 永远返回成功响应体（HTTP 200），由 {ok,reason} 表达拒绝原因。
func (s *DanmakuService) Ingest(ctx context.Context, req *livev1.IngestRequest) (*livev1.IngestReply, error) {
	ok, reason := s.svc.Ingest(ctx, req.GetKind(), req.GetUserId(), req.GetUserName(), req.GetText(), req.GetDiamond(), req.GetSource(), req.GetVip())
	return &livev1.IngestReply{Ok: ok, Reason: reason}, nil
}

func (s *DanmakuService) SetMode(_ context.Context, req *livev1.SetModeRequest) (*livev1.SetModeReply, error) {
	changed := s.svc.SetMode(danmaku.Mode(req.GetMode()))
	return &livev1.SetModeReply{Changed: changed, Mode: string(s.svc.Mode())}, nil
}

func (s *DanmakuService) Confirm(_ context.Context, req *livev1.ConfirmRequest) (*livev1.ConfirmReply, error) {
	if !s.svc.Confirm(req.GetPendingId()) {
		return &livev1.ConfirmReply{Ok: false, Reason: "not_found"}, nil
	}
	return &livev1.ConfirmReply{Ok: true}, nil
}

func (s *DanmakuService) Dismiss(_ context.Context, req *livev1.DismissRequest) (*livev1.DismissReply, error) {
	return &livev1.DismissReply{Ok: s.svc.Dismiss(req.GetPendingId())}, nil
}

func (s *DanmakuService) ListPending(_ context.Context, _ *livev1.ListPendingRequest) (*livev1.ListPendingReply, error) {
	rows := s.svc.ListPending()
	out := make([]*livev1.PendingItem, 0, len(rows))
	for _, r := range rows {
		out = append(out, &livev1.PendingItem{Id: r.ID, UserName: r.UserName, Text: r.Text, CreatedAtMs: r.CreatedAtMs})
	}
	return &livev1.ListPendingReply{Items: out}, nil
}
