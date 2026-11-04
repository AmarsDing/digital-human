package tts

import (
	"context"
	"sync"
	"time"
)

// CapabilityState 是云 TTS 的三态能力；real 只能由真实非空音频建立。
type CapabilityState string

const (
	CapabilityUnconfigured CapabilityState = "unconfigured"
	CapabilityUnknown      CapabilityState = "unknown"
	CapabilityReal         CapabilityState = "real"
)

// ProbeFunc 返回本次真实合成得到的有效 PCM 字节数。
type ProbeFunc func(context.Context) (int, error)

// CapabilityCache 按模型配置身份缓存短期能力，任何新失败都会清除旧 real。
type CapabilityCache struct {
	mu       sync.Mutex
	ttl      time.Duration
	now      func() time.Time
	identity string
	state    CapabilityState
	expires  time.Time
}

func NewCapabilityCache(ttl time.Duration) *CapabilityCache {
	if ttl <= 0 {
		ttl = 30 * time.Second
	}
	return &CapabilityCache{ttl: ttl, now: time.Now, state: CapabilityUnknown}
}

// State 返回当前未过期状态；新身份或过期 real 不会被当作可用能力。
func (c *CapabilityCache) State(identity string) CapabilityState {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.identity != identity || c.identity == "" {
		return CapabilityUnknown
	}
	if c.state == CapabilityReal && !c.now().Before(c.expires) {
		return CapabilityUnknown
	}
	return c.state
}

// Capability 在缓存过期后执行真实探测；失败或无音频均落为 unknown。
func (c *CapabilityCache) Capability(ctx context.Context, identity string, probe ProbeFunc) CapabilityState {
	c.mu.Lock()
	if c.identity == identity && c.identity != "" && c.now().Before(c.expires) {
		state := c.state
		c.mu.Unlock()
		return state
	}
	c.identity = identity
	c.state = CapabilityUnknown
	c.expires = time.Time{}
	c.mu.Unlock()

	audioBytes, err := probe(ctx)
	if err != nil || audioBytes <= 0 {
		c.RecordFailure(identity)
		return CapabilityUnknown
	}
	c.RecordSuccess(identity, audioBytes)
	return CapabilityReal
}

func (c *CapabilityCache) RecordSuccess(identity string, audioBytes int) {
	if audioBytes <= 0 {
		c.RecordFailure(identity)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.identity = identity
	c.state = CapabilityReal
	c.expires = c.now().Add(c.ttl)
}

func (c *CapabilityCache) RecordFailure(identity string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.identity = identity
	c.state = CapabilityUnknown
	c.expires = c.now().Add(c.ttl)
}
