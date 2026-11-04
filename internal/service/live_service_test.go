package service

import (
	"fmt"
	"testing"

	kerrors "github.com/go-kratos/kratos/v2/errors"
	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/publish"
)

// TestMapLiveErrorAvatarExpired 到期链路归一码：ErrAvatarExpired → 400 avatar_expired，
// 与前端 client.ts 的 avatar_expired 归一码对齐（v3 §3.3 失败分支）。
func TestMapLiveErrorAvatarExpired(t *testing.T) {
	err := mapLiveError(fmt.Errorf("precheck: %w", live.ErrAvatarExpired))
	se := kerrors.FromError(err)
	if se.Code != 400 || se.Reason != "avatar_expired" {
		t.Fatalf("code=%d reason=%q, want 400/avatar_expired", se.Code, se.Reason)
	}
	// 未包装哨兵同样命中。
	se = kerrors.FromError(mapLiveError(live.ErrAvatarExpired))
	if se.Code != 400 || se.Reason != "avatar_expired" {
		t.Fatalf("code=%d reason=%q, want 400/avatar_expired", se.Code, se.Reason)
	}
}

func TestMapLiveErrorPublishTargetUnverified(t *testing.T) {
	err := fmt.Errorf("start: %w", &publish.Error{
		Code: "publish_target_unverified", Message: "需先完成一次真实抖音推流验证",
	})
	se := kerrors.FromError(mapLiveError(err))
	if se.Code != 409 || se.Reason != "publish_target_unverified" {
		t.Fatalf("code=%d reason=%q, want 409/publish_target_unverified", se.Code, se.Reason)
	}
}
