package recording

import (
	"context"
	"errors"

	"github.com/teamwork/digital-human/internal/engine/vidu"
)

// ViduClient 把 vidu.Client.GetRecording 适配成 recording.Client：
// 404/不支持 → Unsupported=true（上层据此报 recording_not_enabled）。
type ViduClient struct {
	C *vidu.Client
}

// RecordingStatus 查询并归一化 Vidu 录制状态。
func (a ViduClient) RecordingStatus(ctx context.Context, liveID string) (Status, error) {
	resp, err := a.C.GetRecording(ctx, liveID)
	if err != nil {
		var apiErr *vidu.APIError
		if errors.As(err, &apiErr) && apiErr.Status == 404 {
			return Status{Unsupported: true}, nil
		}
		return Status{}, err
	}
	return MapViduState(resp), nil
}
