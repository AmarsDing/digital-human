package publish

// Kratos 生成的 StreamServiceHTTPServer 接口实现（薄适配层）。
// 主实施线在 internal/server/http.go 里 RegisterStreamServiceHTTPServer 即可。

import (
	"context"
	"sync"

	streamv1 "github.com/teamwork/digital-human/api/gen/go/stream/v1"
)

// StreamService 实现 streamv1.StreamServiceHTTPServer。
type StreamService struct {
	pub *Publisher

	mu   sync.Mutex
	idem map[string]string // idempotency_key -> action
}

// NewStreamService 构造。
func NewStreamService(pub *Publisher) *StreamService {
	return &StreamService{pub: pub, idem: map[string]string{}}
}

// SetTarget 实现接口。
func (s *StreamService) SetTarget(_ context.Context, req *streamv1.SetPushTargetRequest) (*streamv1.SetPushTargetReply, error) {
	// req.StreamKey 只进 TargetStore，不进任何日志
	fp, err := s.pub.SetTarget(Target{RTMPURL: req.GetRtmpUrl(), Key: req.GetStreamKey()})
	if err != nil {
		return &streamv1.SetPushTargetReply{Ok: false, Error: err.Error()}, nil
	}
	return &streamv1.SetPushTargetReply{Ok: true, KeyFingerprint8: fp}, nil
}

// GetPushStatus 实现接口。
func (s *StreamService) GetPushStatus(context.Context, *streamv1.GetPushStatusRequest) (*streamv1.GetPushStatusReply, error) {
	st := s.pub.Status()
	return &streamv1.GetPushStatusReply{
		State:              string(st.State),
		Platform:           st.Platform,
		TargetVerification: string(st.TargetVerification),
		BitrateKbps:        int32(st.BitrateKbps),
		Fps:                st.FPS,
		DroppedFrames:      st.DroppedFrames,
		KeyFingerprint8:    st.KeyFingerprint8,
		LastError:          st.LastError,
	}, nil
}

func (s *StreamService) dup(key, action string) bool {
	if key == "" {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.idem[key] == action {
		return true
	}
	s.idem[key] = action
	return false
}

// StartPush 实现接口（幂等）。
func (s *StreamService) StartPush(ctx context.Context, req *streamv1.StartPushRequest) (*streamv1.StartPushReply, error) {
	if s.dup(req.GetIdempotencyKey(), "start") {
		return &streamv1.StartPushReply{Ok: true, Reason: "duplicate_ignored"}, nil
	}
	if err := s.pub.Start(ctx); err != nil {
		return &streamv1.StartPushReply{Ok: false, Reason: err.Error()}, nil
	}
	return &streamv1.StartPushReply{Ok: true}, nil
}

// StopPush 实现接口（幂等）。
func (s *StreamService) StopPush(_ context.Context, req *streamv1.StopPushRequest) (*streamv1.StopPushReply, error) {
	if s.dup(req.GetIdempotencyKey(), "stop") {
		return &streamv1.StopPushReply{Ok: true, Reason: "duplicate_ignored"}, nil
	}
	if err := s.pub.Stop(); err != nil {
		return &streamv1.StopPushReply{Ok: false, Reason: err.Error()}, nil
	}
	return &streamv1.StopPushReply{Ok: true}, nil
}
