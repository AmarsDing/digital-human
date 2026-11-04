package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/action"
	"github.com/teamwork/digital-human/internal/biz/capability"
	"github.com/teamwork/digital-human/internal/conf"
)

type realActionSenderStub struct{}

func (realActionSenderStub) SendCommand(context.Context, string) error { return nil }

type realActionRemoteStub struct{ action.Remote }

func TestActionCapabilityThreeStates(t *testing.T) {
	cfg := &conf.Config{}
	cfg.Engine.Mode = "real"
	rt := &panelRuntime{
		sender:      realActionSenderStub{},
		remote:      realActionRemoteStub{},
		actionProbe: func(context.Context) error { return nil },
	}
	mx := provideCapabilities(cfg, rt, nil, nil, nil)(context.Background())
	if got := mx.Get("action_sender"); got != capability.Real {
		t.Fatalf("真实出口和可达探测必须 real, got %s", got)
	}
	if got := mx.Get("action_remote"); got != capability.Real {
		t.Fatalf("真实动作库和可达探测必须 real, got %s", got)
	}

	rt.actionProbe = func(context.Context) error { return errors.New("dial tcp: down") }
	mx = provideCapabilities(cfg, rt, nil, nil, nil)(context.Background())
	if got := mx.Get("action_sender"); got != capability.Unknown {
		t.Fatalf("Vidu 不可达必须 unknown, got %s", got)
	}
	if got := mx.Get("action_remote"); got != capability.Unknown {
		t.Fatalf("动作库不可达必须 unknown, got %s", got)
	}

	rt.sender = nil
	rt.remote = nil
	rt.actionProbe = nil
	mx = provideCapabilities(cfg, rt, nil, nil, nil)(context.Background())
	if got := mx.Get("action_sender"); got != capability.Unconfigured {
		t.Fatalf("未装配发送器必须 unconfigured, got %s", got)
	}
	if got := mx.Get("action_remote"); got != capability.Unconfigured {
		t.Fatalf("未装配动作库必须 unconfigured, got %s", got)
	}
}

func TestCapabilityProbeCachesSuccessAndFailure(t *testing.T) {
	calls := 0
	probe := newCachedCapabilityProbe(5*time.Second, func(context.Context) error {
		calls++
		return errors.New("down")
	})
	now := time.Unix(100, 0)
	probe.now = func() time.Time { return now }
	if probe.check(context.Background()) == nil || probe.check(context.Background()) == nil {
		t.Fatal("失败必须如实返回")
	}
	if calls != 1 {
		t.Fatalf("5 秒内重复探测 calls=%d want 1", calls)
	}
	now = now.Add(6 * time.Second)
	_ = probe.check(context.Background())
	if calls != 2 {
		t.Fatalf("缓存过期后 calls=%d want 2", calls)
	}
}

func TestActionCapabilityDevExplicitlyFake(t *testing.T) {
	cfg := &conf.Config{}
	cfg.Engine.Mode = "dev"
	rt := &panelRuntime{sender: action.NewFakeSender(), remote: action.NewMemoryRemote()}
	mx := provideCapabilities(cfg, rt, nil, nil, nil)(context.Background())
	if got := mx.Get("action_sender"); got != capability.Fake {
		t.Fatalf("dev fake sender 必须如实 fake, got %s", got)
	}
	if got := mx.Get("action_remote"); got != capability.Fake {
		t.Fatalf("dev memory remote 必须如实 fake, got %s", got)
	}
}
