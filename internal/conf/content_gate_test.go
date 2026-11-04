package conf

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestRepositoryConfigExplicitlyEnablesContentGate(t *testing.T) {
	raw, err := os.ReadFile("../../configs/config.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		ContentGate *ContentGate `yaml:"content_gate"`
	}
	if err := yaml.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	if file.ContentGate == nil || !file.ContentGate.Enabled {
		t.Fatalf("生产配置必须显式默认启用内容终检: %+v", file.ContentGate)
	}
	if file.ContentGate.RulesPath != "configs/content_gate.yaml" || file.ContentGate.CircuitBreakerThreshold != 3 {
		t.Fatalf("内容终检默认配置不符: %+v", file.ContentGate)
	}
}
