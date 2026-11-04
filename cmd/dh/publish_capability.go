package main

import (
	"github.com/teamwork/digital-human/internal/biz/capability"
	"github.com/teamwork/digital-human/internal/publish"
)

func publishCapabilityStatus(st publish.Status) capability.Status {
	if st.State != publish.StatePushing {
		return capability.Unknown
	}
	switch st.TargetVerification {
	case publish.TargetLocal:
		return capability.Fake
	case publish.TargetDouyin:
		return capability.Real
	default:
		return capability.Unknown
	}
}
