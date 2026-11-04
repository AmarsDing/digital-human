package main

import (
	"testing"

	"github.com/teamwork/digital-human/internal/biz/capability"
	"github.com/teamwork/digital-human/internal/publish"
)

func TestPublishCapabilityStatus(t *testing.T) {
	tests := []struct {
		name string
		st   publish.Status
		want capability.Status
	}{
		{name: "idle未知", st: publish.Status{State: publish.StateIdle, TargetVerification: publish.TargetDouyin}, want: capability.Unknown},
		{name: "连接中未知", st: publish.Status{State: publish.StateStarting, TargetVerification: publish.TargetDouyin}, want: capability.Unknown},
		{name: "本地仅协议验证", st: publish.Status{State: publish.StatePushing, TargetVerification: publish.TargetLocal}, want: capability.Fake},
		{name: "抖音真实验证", st: publish.Status{State: publish.StatePushing, TargetVerification: publish.TargetDouyin}, want: capability.Real},
		{name: "未识别域名", st: publish.Status{State: publish.StatePushing, TargetVerification: publish.TargetUnknown}, want: capability.Unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := publishCapabilityStatus(tt.st); got != tt.want {
				t.Fatalf("got=%s want=%s", got, tt.want)
			}
		})
	}
}
