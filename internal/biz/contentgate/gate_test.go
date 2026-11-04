package contentgate

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeRules(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "content_gate.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func newTestGate(t *testing.T, rules string, mutate func(*Options)) *Gate {
	t.Helper()
	opts := Options{
		Enabled:                 true,
		RulesPath:               writeRules(t, rules),
		CircuitBreakerThreshold: 3,
	}
	if mutate != nil {
		mutate(&opts)
	}
	return New(opts)
}

func TestGateLayeredDispositions(t *testing.T) {
	rules := `layers:
  political:
    decision: block
    words: [政治违禁词]
  contraband:
    decision: block
    words: [违禁品词]
  vulgar:
    decision: block
    words: [低俗词]
  harassment:
    decision: block
    words: [攻击词]
  medical:
    decision: block
    words: [包治百病]
  financial:
    decision: block
    patterns: ['保本收益[0-9]+%']
`
	tests := []struct {
		name        string
		text        string
		state       State
		category    string
		disposition Disposition
		wantText    string
		switchTopic bool
	}{
		{name: "allow", text: "今天介绍普通商品", state: StateAllow, disposition: DispositionAllow, wantText: "今天介绍普通商品"},
		{name: "vulgar", text: "这是低俗词", state: StateBlock, category: "vulgar", disposition: DispositionSpeakTemplate, wantText: SafeSpokenTemplate},
		{name: "harassment", text: "这是攻击词", state: StateBlock, category: "harassment", disposition: DispositionSpeakTemplate, wantText: SafeSpokenTemplate},
		{name: "medical", text: "这个药包治百病", state: StateBlock, category: "medical", disposition: DispositionSpeakTemplate, wantText: SafeSpokenTemplate},
		{name: "financial regex", text: "承诺保本收益99%", state: StateBlock, category: "financial", disposition: DispositionSpeakTemplate, wantText: SafeSpokenTemplate},
		{name: "political", text: "出现政治违禁词", state: StateBlock, category: "political", disposition: DispositionSilent, wantText: SafeSilentNotice, switchTopic: true},
		{name: "contraband", text: "出现违禁品词", state: StateBlock, category: "contraband", disposition: DispositionSilent, wantText: SafeSilentNotice, switchTopic: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := newTestGate(t, rules, nil)
			got := gate.Check(context.Background(), Input{Outlet: "live", SessionID: "s1", Text: tt.text})
			if got.State != tt.state || got.Category != tt.category || got.Disposition != tt.disposition {
				t.Fatalf("判定不符: got %+v", got)
			}
			if got.Text != tt.wantText || got.SwitchTopic != tt.switchTopic {
				t.Fatalf("处置不符: got %+v, want text=%q switch=%v", got, tt.wantText, tt.switchTopic)
			}
			if tt.disposition == DispositionSpeakTemplate {
				if got.Text == tt.text || strings.Contains(got.Text, tt.text) {
					t.Fatalf("安全模板不得等于或拼接原文: input=%q output=%q", tt.text, got.Text)
				}
			}
		})
	}
}

func TestGateReviewDefaultsToTemplateAndExplicitAllow(t *testing.T) {
	rules := `layers:
  financial:
    decision: review
    words: [高收益]
`
	const original = "这个产品高收益"
	gate := newTestGate(t, rules, nil)
	got := gate.Check(context.Background(), Input{Outlet: "live", SessionID: "s1", Text: original})
	if got.State != StateReview || got.Disposition != DispositionSpeakTemplate || got.Text != SafeSpokenTemplate {
		t.Fatalf("review 默认必须口播静态模板: %+v", got)
	}
	if got.Text == original {
		t.Fatal("review 不得合成原文")
	}

	gate = newTestGate(t, rules, func(opts *Options) { opts.AllowReview = true })
	got = gate.Check(context.Background(), Input{Outlet: "live", SessionID: "s1", Text: original})
	if got.State != StateReview || got.Disposition != DispositionAllow || got.Text != original {
		t.Fatalf("仅显式 allow_review 才能放行: %+v", got)
	}
}

func TestGateCircuitBreakerAndSessionReset(t *testing.T) {
	rules := `layers:
  vulgar:
    decision: block
    words: [低俗词]
`
	gate := newTestGate(t, rules, nil)
	check := func(session, text string) Decision {
		return gate.Check(context.Background(), Input{Outlet: "live", SessionID: session, Text: text})
	}
	for i := 1; i <= 3; i++ {
		if got := check("s1", "低俗词"); got.Disposition != DispositionSpeakTemplate {
			t.Fatalf("第 %d 次拦截仍应口播模板: %+v", i, got)
		}
	}
	if got := check("s1", "低俗词"); got.Disposition != DispositionSilent || got.Text != SafeSilentNotice {
		t.Fatalf("超过阈值后必须静默: %+v", got)
	}
	if got := check("s2", "低俗词"); got.Disposition != DispositionSpeakTemplate {
		t.Fatalf("熔断不得串场: %+v", got)
	}
	gate.ResetSession("s1")
	if got := check("s1", "低俗词"); got.Disposition != DispositionSpeakTemplate {
		t.Fatalf("ResetSession 必须清除熔断: %+v", got)
	}

	check("s3", "低俗词")
	check("s3", "低俗词")
	if got := check("s3", "正常内容"); got.Disposition != DispositionAllow {
		t.Fatalf("正常内容应放行: %+v", got)
	}
	if got := check("s3", "低俗词"); got.Disposition != DispositionSpeakTemplate {
		t.Fatalf("正常内容必须重置连续拦截计数: %+v", got)
	}
}

func TestGateFailuresAreClosedButProcessStaysUsable(t *testing.T) {
	tests := []struct {
		name string
		opts Options
	}{
		{name: "missing file", opts: Options{Enabled: true, RulesPath: filepath.Join(t.TempDir(), "missing.yaml"), CircuitBreakerThreshold: 3}},
		{name: "invalid regex", opts: Options{Enabled: true, RulesPath: writeRules(t, "layers:\n  vulgar:\n    decision: block\n    patterns: ['[']\n"), CircuitBreakerThreshold: 3}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := New(tt.opts)
			if gate.Capability() != CapabilityUnknown {
				t.Fatalf("异常能力状态必须为 unknown: %s", gate.Capability())
			}
			const original = "普通文本也不能因异常原样放行"
			got := gate.Check(context.Background(), Input{Outlet: "live", SessionID: "s1", Text: original})
			if got.State != StateBlock || got.Disposition != DispositionSilent || got.Text != SafeSilentNotice {
				t.Fatalf("加载异常必须 fail-closed 且静默: %+v", got)
			}
			if got.Text == original {
				t.Fatal("异常分支不得返回原文")
			}
		})
	}
}

func TestGateCapabilityAndDisabled(t *testing.T) {
	gate := newTestGate(t, "layers:\n  vulgar:\n    decision: block\n    words: [低俗词]\n", nil)
	if gate.Capability() != CapabilityReal {
		t.Fatalf("正常加载应为 real: %s", gate.Capability())
	}
	disabled := New(Options{Enabled: false})
	if disabled.Capability() != CapabilityUnconfigured {
		t.Fatalf("禁用时应为 unconfigured: %s", disabled.Capability())
	}
	const original = "任何文本"
	if got := disabled.Check(context.Background(), Input{Text: original}); got.Disposition != DispositionAllow || got.Text != original {
		t.Fatalf("显式禁用才允许绕过: %+v", got)
	}
}
