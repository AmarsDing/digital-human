package conf

import (
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/engine/tts"
)

func TestConfigValidateCloudTTSFailClosed(t *testing.T) {
	cfg := defaults()
	cfg.Engine.Mode = "real"
	cfg.Engine.CloudTTS.Provider = "dashscope"
	if err := cfg.Validate(); tts.ErrorCode(err) != "cloud_tts_unconfigured" {
		t.Fatalf("Validate() error = %v, code = %q", err, tts.ErrorCode(err))
	}
}

func TestConfigValidateCloudTTSValid(t *testing.T) {
	cfg := defaults()
	cfg.Engine.Mode = "real"
	cfg.Engine.CloudTTS = tts.CloudConfig{
		Provider: "dashscope",
		BaseURL:  "wss://example.test/api-ws/v1",
		Model:    "qwen3-tts-flash-realtime",
		APIKey:   "test-key",
		Voice:    "Cherry",
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
	if cfg.Engine.CloudTTS.SampleRate != 24000 || cfg.Engine.CloudTTS.Timeout <= 0 || cfg.Engine.CloudTTS.ProbeCacheTTL <= 0 {
		t.Fatalf("cloud defaults not applied: %+v", cfg.Engine.CloudTTS)
	}
}

func TestLoadCloudTTSEnvironment(t *testing.T) {
	t.Setenv("DH_CLOUD_TTS_PROVIDER", "dashscope")
	t.Setenv("DH_CLOUD_TTS_BASE_URL", "wss://example.test/api-ws/v1")
	t.Setenv("DH_CLOUD_TTS_MODEL", "qwen3-tts-flash-realtime")
	t.Setenv("DH_CLOUD_TTS_API_KEY", "test-key")
	t.Setenv("DH_CLOUD_TTS_VOICE", "Cherry")
	t.Setenv("DH_CLOUD_TTS_SAMPLE_RATE", "24000")
	t.Setenv("DH_CLOUD_TTS_TIMEOUT", "12s")
	t.Setenv("DH_CLOUD_TTS_PROBE_CACHE_TTL", "9s")
	cfg, err := Load("")
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	got := cfg.Engine.CloudTTS
	if got.Provider != "dashscope" || got.BaseURL != "wss://example.test/api-ws/v1" || got.Model != "qwen3-tts-flash-realtime" || got.APIKey != "test-key" || got.Voice != "Cherry" {
		t.Fatalf("cloud environment not loaded: %+v", got)
	}
	if got.SampleRate != 24000 || got.Timeout != 12*time.Second || got.ProbeCacheTTL != 9*time.Second {
		t.Fatalf("cloud numeric environment not loaded: %+v", got)
	}
}
