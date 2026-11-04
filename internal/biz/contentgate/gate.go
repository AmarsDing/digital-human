// Package contentgate 在文本离开业务边界前执行统一的内容终检。
package contentgate

import (
	"context"
	"os"
	"regexp"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type State string

const (
	StateAllow  State = "allow"
	StateReview State = "review"
	StateBlock  State = "block"
)

type Disposition string

const (
	DispositionAllow         Disposition = "allow"
	DispositionSpeakTemplate Disposition = "speak_template"
	DispositionSilent        Disposition = "silent"
)

const (
	CapabilityReal         = "real"
	CapabilityUnconfigured = "unconfigured"
	CapabilityUnknown      = "unknown"

	// SafeSpokenTemplate 是静态常量，禁止拼接原文、关键词或原文长度特征。
	SafeSpokenTemplate = "这个话题我就不展开了，我们聊点别的。"
	SafeSilentNotice   = "当前话题已切换，我们聊点别的。"
)

var layerOrder = []string{"political", "contraband", "vulgar", "harassment", "medical", "financial"}

type Input struct {
	Outlet    string
	SessionID string
	Text      string
}

type Decision struct {
	State       State
	Category    string
	Disposition Disposition
	Text        string
	SwitchTopic bool
}

// DecisionHandler 在非直播出站点命中时复用直播的静态提示、真实模板口播与话题切换。
type DecisionHandler interface {
	HandleContentDecision(ctx context.Context, decision Decision) error
}

// AuditRecord 只包含终检元数据，结构上不携带原文、命中词或安全模板。
type AuditRecord struct {
	Outlet      string
	Category    string
	Disposition Disposition
}

// DecisionRecorder 由审计服务实现；记录失败不打断直播处置。
type DecisionRecorder interface {
	RecordContentDecision(ctx context.Context, record AuditRecord) error
}

type Options struct {
	Enabled                 bool
	RulesPath               string
	AllowReview             bool
	CircuitBreakerThreshold int
	Recorder                DecisionRecorder
}

type ruleFile struct {
	Layers map[string]ruleConfig `yaml:"layers"`
}

type ruleConfig struct {
	Decision string   `yaml:"decision"`
	Words    []string `yaml:"words"`
	Patterns []string `yaml:"patterns"`
}

type layer struct {
	name     string
	decision State
	words    []string
	patterns []*regexp.Regexp
}

type Gate struct {
	mu          sync.Mutex
	enabled     bool
	allowReview bool
	threshold   int
	capability  string
	layers      []layer
	strikes     map[string]int
	recorder    DecisionRecorder
}

// New 总是返回可用 Gate；加载或编译失败时保留实例并对后续文本保守静默。
func New(opts Options) *Gate {
	threshold := opts.CircuitBreakerThreshold
	if threshold <= 0 {
		threshold = 3
	}
	g := &Gate{
		enabled:     opts.Enabled,
		allowReview: opts.AllowReview,
		threshold:   threshold,
		capability:  CapabilityUnconfigured,
		strikes:     make(map[string]int),
		recorder:    opts.Recorder,
	}
	if !opts.Enabled {
		return g
	}
	g.capability = CapabilityUnknown
	raw, err := os.ReadFile(opts.RulesPath)
	if err != nil {
		return g
	}
	var file ruleFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return g
	}
	for _, name := range layerOrder {
		cfg, ok := file.Layers[name]
		if !ok {
			continue
		}
		decision := State(cfg.Decision)
		if decision != StateBlock && decision != StateReview {
			return g
		}
		compiled := layer{name: name, decision: decision, words: cfg.Words}
		for _, pattern := range cfg.Patterns {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return g
			}
			compiled.patterns = append(compiled.patterns, re)
		}
		g.layers = append(g.layers, compiled)
	}
	g.capability = CapabilityReal
	return g
}

func (g *Gate) Capability() string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.capability
}

// Check 只返回原文或静态安全文本，不返回命中词，也不把原文写入判定元数据。
func (g *Gate) Check(ctx context.Context, in Input) Decision {
	g.mu.Lock()
	decision := g.checkLocked(in)
	recorder := g.recorder
	g.mu.Unlock()

	if decision.Disposition != DispositionAllow && recorder != nil {
		_ = recorder.RecordContentDecision(ctx, AuditRecord{
			Outlet: in.Outlet, Category: decision.Category, Disposition: decision.Disposition,
		})
	}
	return decision
}

func (g *Gate) checkLocked(in Input) Decision {
	if !g.enabled {
		return Decision{State: StateAllow, Disposition: DispositionAllow, Text: in.Text}
	}
	if g.capability != CapabilityReal {
		return silentDecision("gate_error")
	}
	matched := g.match(in.Text)
	if matched == nil {
		g.resetSessionLocked(in.SessionID)
		return Decision{State: StateAllow, Disposition: DispositionAllow, Text: in.Text}
	}
	if matched.decision == StateReview && g.allowReview {
		g.resetSessionLocked(in.SessionID)
		return Decision{State: StateReview, Category: matched.name, Disposition: DispositionAllow, Text: in.Text}
	}

	if in.SessionID != "" {
		g.strikes[in.SessionID]++
	}
	disposition := DispositionSpeakTemplate
	if matched.name == "political" || matched.name == "contraband" {
		disposition = DispositionSilent
	}
	if in.SessionID != "" && g.strikes[in.SessionID] > g.threshold {
		disposition = DispositionSilent
	}
	if disposition == DispositionSilent {
		return Decision{
			State: matched.decision, Category: matched.name, Disposition: DispositionSilent,
			Text: SafeSilentNotice, SwitchTopic: true,
		}
	}
	return Decision{
		State: matched.decision, Category: matched.name, Disposition: DispositionSpeakTemplate,
		Text: SafeSpokenTemplate,
	}
}

func (g *Gate) ResetSession(sessionID string) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.resetSessionLocked(sessionID)
}

func (g *Gate) resetSessionLocked(sessionID string) {
	delete(g.strikes, sessionID)
}

func (g *Gate) match(text string) *layer {
	lower := strings.ToLower(text)
	for i := range g.layers {
		candidate := &g.layers[i]
		for _, word := range candidate.words {
			if word != "" && strings.Contains(lower, strings.ToLower(word)) {
				return candidate
			}
		}
		for _, pattern := range candidate.patterns {
			if pattern.MatchString(text) {
				return candidate
			}
		}
	}
	return nil
}

func silentDecision(category string) Decision {
	return Decision{
		State: StateBlock, Category: category, Disposition: DispositionSilent,
		Text: SafeSilentNotice, SwitchTopic: true,
	}
}
