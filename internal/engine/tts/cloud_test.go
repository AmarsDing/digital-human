package tts

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func validCloudConfig() CloudConfig {
	return CloudConfig{
		Provider:   "dashscope",
		BaseURL:    "wss://example.test/api-ws/v1",
		Model:      "qwen3-tts-flash-realtime",
		APIKey:     "test-key",
		Voice:      "Cherry",
		SampleRate: 24000,
		Format:     "pcm",
	}
}

func TestCloudConfigMissingFieldIsSpecificAndUnconfigured(t *testing.T) {
	tests := []struct {
		name      string
		wantField string
		mutate    func(*CloudConfig)
	}{
		{name: "base URL", wantField: "base_url", mutate: func(c *CloudConfig) { c.BaseURL = "" }},
		{name: "model", wantField: "model", mutate: func(c *CloudConfig) { c.Model = "" }},
		{name: "sample rate", wantField: "sample_rate", mutate: func(c *CloudConfig) { c.SampleRate = 0 }},
		{name: "format", wantField: "format", mutate: func(c *CloudConfig) { c.Format = "" }},
		{name: "API key", wantField: "api_key", mutate: func(c *CloudConfig) { c.APIKey = "" }},
		{name: "voice", wantField: "voice", mutate: func(c *CloudConfig) { c.Voice = "" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validCloudConfig()
			tt.mutate(&cfg)
			if got := cfg.Capability(); got != CapabilityUnconfigured {
				t.Fatalf("Capability() = %q, want %q", got, CapabilityUnconfigured)
			}
			_, err := cfg.Normalize()
			if ErrorCode(err) != "cloud_tts_unconfigured" {
				t.Fatalf("ErrorCode() = %q, want cloud_tts_unconfigured; err=%v", ErrorCode(err), err)
			}
			if err == nil || !strings.Contains(err.Error(), tt.wantField) {
				t.Fatalf("Normalize() error = %v, want missing field %q", err, tt.wantField)
			}
		})
	}
}

func TestCloudConfigNormalizeDefaults(t *testing.T) {
	cfg := validCloudConfig()
	cfg.Provider = ""
	got, err := cfg.Normalize()
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if got.Provider != "dashscope" || got.SampleRate != 24000 || got.Format != "pcm" || got.Timeout <= 0 || got.ProbeCacheTTL <= 0 {
		t.Fatalf("Normalize() defaults = %+v", got)
	}
}

func TestCloudConfigRejectsInvalidConfiguration(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*CloudConfig)
	}{
		{name: "HTTP URL", mutate: func(c *CloudConfig) { c.BaseURL = "https://example.test/api-ws/v1" }},
		{name: "voice", mutate: func(c *CloudConfig) { c.Voice = "" }},
		{name: "sample rate", mutate: func(c *CloudConfig) { c.SampleRate = 12345 }},
		{name: "format", mutate: func(c *CloudConfig) { c.Format = "wav" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := validCloudConfig()
			tt.mutate(&cfg)
			if _, err := cfg.Normalize(); err == nil {
				t.Fatal("Normalize() error = nil, want invalid configuration")
			}
		})
	}
}

func TestProtocolForModel(t *testing.T) {
	tests := []struct {
		model string
		want  CloudProtocol
	}{
		{model: "qwen3-tts-flash-realtime", want: CloudProtocolQwen3Realtime},
		{model: "qwen-audio-3.1-tts-flash", want: CloudProtocolQwenAudio},
	}
	for _, tt := range tests {
		got, err := ProtocolForModel(tt.model)
		if err != nil || got != tt.want {
			t.Fatalf("ProtocolForModel(%q) = %q, %v; want %q", tt.model, got, err, tt.want)
		}
	}
	_, err := ProtocolForModel("unknown-model")
	if ErrorCode(err) != "cloud_tts_protocol_unimplemented" {
		t.Fatalf("unknown model ErrorCode() = %q; err=%v", ErrorCode(err), err)
	}
}

func TestCloudConfigResolveEncryptedAPIKey(t *testing.T) {
	cfg := validCloudConfig()
	cfg.APIKey = "enc:v1:" + base64.StdEncoding.EncodeToString([]byte("ciphertext"))
	got, err := cfg.resolveAPIKey(func(cipher []byte) ([]byte, error) {
		if string(cipher) != "ciphertext" {
			t.Fatalf("cipher = %q", cipher)
		}
		return []byte("resolved-test-key"), nil
	})
	if err != nil || got != "resolved-test-key" {
		t.Fatalf("resolveAPIKey() = %q, %v", got, err)
	}
}

func TestCloudConfigRejectsInvalidEncryptedAPIKey(t *testing.T) {
	cfg := validCloudConfig()
	cfg.APIKey = "enc:v1:not-base64"
	_, err := cfg.ResolveAPIKey()
	if err == nil || ErrorCode(err) != "cloud_tts_unconfigured" {
		t.Fatalf("ResolveAPIKey() error = %v, code %q", err, ErrorCode(err))
	}
}

func TestCloudConfigIdentityChangesWithKeyWithoutExposingIt(t *testing.T) {
	first := validCloudConfig()
	second := first
	second.APIKey = "another-test-key"
	a, err := first.Identity()
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.Identity()
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("Identity() did not change with API key")
	}
	if strings.Contains(a, first.APIKey) || strings.Contains(a, second.APIKey) {
		t.Fatal("Identity() exposed API key")
	}
}

func TestCloudConfigResolveEncryptedAPIKeyFailure(t *testing.T) {
	cfg := validCloudConfig()
	cfg.APIKey = "enc:v1:" + base64.StdEncoding.EncodeToString([]byte("ciphertext"))
	want := errors.New("decrypt failed")
	_, err := cfg.resolveAPIKey(func([]byte) ([]byte, error) { return nil, want })
	if !errors.Is(err, want) {
		t.Fatalf("resolveAPIKey() error = %v, want wrapped %v", err, want)
	}
}
