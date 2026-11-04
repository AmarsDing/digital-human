package main

import (
	"context"
	"errors"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/capability"
	"github.com/teamwork/digital-human/internal/biz/choreo"
	"github.com/teamwork/digital-human/internal/biz/koubo"
	"github.com/teamwork/digital-human/internal/conf"
)

func kouboCapabilityStore() *choreo.MemoryStore {
	return choreo.NewMemoryStore(choreo.Config{Groups: []choreo.Group{{
		ID: "g1", Name: "欢迎", Steps: []choreo.Step{{Cmd: "挥手"}},
	}}})
}

func TestKouboActionGenSelectionUsesRulesWithoutLLM(t *testing.T) {
	store := kouboCapabilityStore()
	if got := provideKouboActionGen(store, nil, false); got == nil {
		t.Fatal("无 LLM 时必须装配规则生成器")
	} else if _, ok := got.(*koubo.RuleActionGen); !ok {
		t.Fatalf("无 LLM 时装配类型=%T，不得走 fake 或 LLM", got)
	}
	if got := provideKouboActionGen(store, nil, true); got == nil {
		t.Fatal("配置 LLM 时必须装配 LLM 生成器")
	} else if _, ok := got.(*koubo.LLMActionGen); !ok {
		t.Fatalf("配置 LLM 后装配类型=%T", got)
	}
}

func TestKouboActionCapabilityStates(t *testing.T) {
	cfg := &conf.Config{}
	store := kouboCapabilityStore()
	rt := &panelRuntime{}
	mx := provideCapabilities(cfg, rt, nil, nil, nil)(context.Background())
	if got := mx.Get("koubo_actiongen"); got != capability.Unconfigured {
		t.Fatalf("未装配生成器必须 unconfigured, got %s", got)
	}

	rt.actionGen = koubo.FakeActionGen{}
	mx = provideCapabilities(cfg, rt, nil, nil, nil)(context.Background())
	if got := mx.Get("koubo_actiongen"); got != capability.Fake {
		t.Fatalf("显式 fake 必须 fake, got %s", got)
	}

	rt.actionGen = koubo.NewRuleActionGen(store)
	mx = provideCapabilities(cfg, rt, nil, nil, nil)(context.Background())
	if got := mx.Get("koubo_actiongen"); got != capability.Real {
		t.Fatalf("确定性规则实现必须 real, got %s", got)
	}

	rt.actionGen = koubo.NewLLMActionGen(store, nil)
	rt.actionGenProbe = func(context.Context) error { return nil }
	mx = provideCapabilities(cfg, rt, nil, nil, nil)(context.Background())
	if got := mx.Get("koubo_actiongen"); got != capability.Real {
		t.Fatalf("LLM 探测成功必须 real, got %s", got)
	}
	rt.actionGenProbe = func(context.Context) error { return errors.New("llm down") }
	mx = provideCapabilities(cfg, rt, nil, nil, nil)(context.Background())
	if got := mx.Get("koubo_actiongen"); got != capability.Unknown {
		t.Fatalf("LLM 不可达必须 unknown, got %s", got)
	}
}
