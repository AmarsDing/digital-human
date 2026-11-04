package tts

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCapabilityCacheRealOnlyAfterNonEmptyAudio(t *testing.T) {
	cache := NewCapabilityCache(time.Minute)
	cache.RecordSuccess("qwen3", 0)
	if got := cache.State("qwen3"); got != CapabilityUnknown {
		t.Fatalf("State() after empty audio = %q, want unknown", got)
	}
	cache.RecordSuccess("qwen3", 4800)
	if got := cache.State("qwen3"); got != CapabilityReal {
		t.Fatalf("State() after real audio = %q, want real", got)
	}
}

func TestCapabilityCacheFailureClearsPreviousRealAfterTTL(t *testing.T) {
	now := time.Unix(100, 0)
	cache := NewCapabilityCache(time.Minute)
	cache.now = func() time.Time { return now }
	cache.RecordSuccess("qwen3", 4800)

	now = now.Add(2 * time.Minute)
	probeErr := errors.New("model unavailable")
	got := cache.Capability(context.Background(), "qwen3", func(context.Context) (int, error) {
		return 0, probeErr
	})
	if got != CapabilityUnknown {
		t.Fatalf("Capability() = %q, want unknown after failed probe", got)
	}
	if got := cache.State("qwen3"); got != CapabilityUnknown {
		t.Fatalf("State() = %q, old real was retained", got)
	}
}

func TestCapabilityCacheDoesNotProbeWithinTTL(t *testing.T) {
	cache := NewCapabilityCache(time.Minute)
	cache.RecordSuccess("qwen3", 4800)
	called := false
	got := cache.Capability(context.Background(), "qwen3", func(context.Context) (int, error) {
		called = true
		return 0, errors.New("must not run")
	})
	if got != CapabilityReal || called {
		t.Fatalf("Capability() = %q, probe called = %v", got, called)
	}
}

func TestCapabilityCacheIdentityChangeDropsReal(t *testing.T) {
	cache := NewCapabilityCache(time.Minute)
	cache.RecordSuccess("qwen3", 4800)
	if got := cache.State("qwen-audio"); got != CapabilityUnknown {
		t.Fatalf("State(new identity) = %q, want unknown", got)
	}
	got := cache.Capability(context.Background(), "qwen-audio", func(context.Context) (int, error) {
		return 0, errors.New("not implemented")
	})
	if got != CapabilityUnknown {
		t.Fatalf("Capability(new identity) = %q, want unknown", got)
	}
}

func TestCapabilityCacheProbeNeedsAudioAndSuccess(t *testing.T) {
	cache := NewCapabilityCache(time.Minute)
	got := cache.Capability(context.Background(), "qwen3", func(context.Context) (int, error) {
		return 0, nil
	})
	if got != CapabilityUnknown {
		t.Fatalf("Capability() with empty audio = %q, want unknown", got)
	}
	cache.RecordFailure("qwen3")
	got = cache.Capability(context.Background(), "qwen3", func(context.Context) (int, error) {
		return 4800, nil
	})
	if got != CapabilityUnknown {
		t.Fatalf("Capability() used cached failure = %q, want unknown", got)
	}
}
