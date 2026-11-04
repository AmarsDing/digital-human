package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/teamwork/digital-human/internal/biz/capability"
	"github.com/teamwork/digital-human/internal/biz/contentgate"
	"github.com/teamwork/digital-human/internal/conf"
)

func TestContentGateCapabilityStates(t *testing.T) {
	validRules := filepath.Join(t.TempDir(), "valid.yaml")
	if err := os.WriteFile(validRules, []byte("layers:\n  vulgar:\n    decision: block\n    words: [低俗词]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		opts contentgate.Options
		want capability.Status
	}{
		{name: "loaded", opts: contentgate.Options{Enabled: true, RulesPath: validRules, CircuitBreakerThreshold: 3}, want: capability.Real},
		{name: "disabled", opts: contentgate.Options{Enabled: false, CircuitBreakerThreshold: 3}, want: capability.Unconfigured},
		{name: "load error", opts: contentgate.Options{Enabled: true, RulesPath: filepath.Join(t.TempDir(), "missing.yaml"), CircuitBreakerThreshold: 3}, want: capability.Unknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := &panelRuntime{contentGate: contentgate.New(tt.opts)}
			mx := provideCapabilities(&conf.Config{}, rt, nil, nil, nil)(context.Background())
			if got := mx["content_gate"]; got != tt.want {
				t.Fatalf("content_gate=%s, want %s", got, tt.want)
			}
		})
	}
}
