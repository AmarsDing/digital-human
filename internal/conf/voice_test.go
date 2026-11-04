package conf

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestVoiceDefaultsFreezeStageOneDecisions(t *testing.T) {
	cfg, err := Load("")
	if err != nil {
		t.Fatal(err)
	}
	v := cfg.Voice
	if !v.ASR.AllowLocalSidecar || v.ASR.AllowCloud || v.ASR.ModelLicenseAcknowledged {
		t.Fatalf("ASR trust defaults = %+v", v.ASR)
	}
	if v.ASR.Provider != "sherpa" || v.ASR.PythonBin != "python" || v.ASR.Threads != 2 {
		t.Fatalf("ASR provider defaults = %+v", v.ASR)
	}
	if v.RetainAudio || !v.PersistText || !v.AllowContinuous || v.InterruptSongs ||
		v.AllowCrossOwnerPreempt || !v.CoordinateTextChat {
		t.Fatalf("voice policy defaults = %+v", v)
	}
	if v.ASRTimeout != 5*time.Second || v.LLMTimeout != 45*time.Second ||
		v.TurnTimeout != 90*time.Second || v.MaxAudio != 20*time.Second ||
		v.PartialInterval != 200*time.Millisecond || v.BargeIn != 250*time.Millisecond {
		t.Fatalf("voice timing defaults = %+v", v)
	}
	if v.VAD.Silence != 600*time.Millisecond || v.VAD.MinSpeech != 300*time.Millisecond ||
		v.VAD.MinStartSpeech != 120*time.Millisecond || v.VAD.AbsoluteDBFS != -45 {
		t.Fatalf("VAD defaults = %+v", v.VAD)
	}
}

func TestVoiceYAMLOverridesPolicies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "voice.yaml")
	raw := []byte(`voice:
  asr:
    provider: sherpa
    allow_local_sidecar: false
    allow_cloud: true
    model_license_acknowledged: true
    threads: 4
  retain_audio: false
  persist_text: false
  allow_continuous: false
  interrupt_songs: true
  allow_cross_owner_preempt: true
  coordinate_text_chat: false
  llm_timeout: 12s
  barge_in: 300ms
  vad:
    silence: 800ms
`)
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	v := cfg.Voice
	if v.ASR.AllowLocalSidecar || !v.ASR.AllowCloud || !v.ASR.ModelLicenseAcknowledged || v.ASR.Threads != 4 {
		t.Fatalf("ASR overrides = %+v", v.ASR)
	}
	if v.PersistText || v.AllowContinuous || !v.InterruptSongs || !v.AllowCrossOwnerPreempt || v.CoordinateTextChat {
		t.Fatalf("policy overrides = %+v", v)
	}
	if v.LLMTimeout != 12*time.Second || v.BargeIn != 300*time.Millisecond || v.VAD.Silence != 800*time.Millisecond {
		t.Fatalf("timing overrides = %+v", v)
	}
}

func TestVoiceRejectsUnsupportedOrInvalidConfiguration(t *testing.T) {
	for name, mutate := range map[string]func(*Config){
		"audio retention is not silently ignored": func(c *Config) { c.Voice.RetainAudio = true },
		"unknown ASR provider":                    func(c *Config) { c.Voice.ASR.Provider = "unknown" },
		"non-positive LLM timeout":                func(c *Config) { c.Voice.LLMTimeout = 0 },
		"non-positive VAD silence":                func(c *Config) { c.Voice.VAD.Silence = 0 },
	} {
		t.Run(name, func(t *testing.T) {
			cfg := defaults()
			mutate(cfg)
			if err := cfg.Validate(); err == nil {
				t.Fatal("Validate() error = nil")
			}
		})
	}
}
