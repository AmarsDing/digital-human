// Package tts 提供音色、试听、素材文件与合成队列状态。
//
// 蓝本事实：bridge.py `tts_voices/tts_set_voice/tts_preview/files/upload/import-folder/delete`；
// 合成器是 OmniVoice 边车（未接入）。这里以 Synth 端口 + FakeSynth 承接，
// 音色选择写入 settings.voice.voice_id（与蓝本 appconf.patch({"tts":{"voice":...}}) 等价）。
package tts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/teamwork/digital-human/internal/biz/media"
	"github.com/teamwork/digital-human/internal/biz/settings"
	"github.com/teamwork/digital-human/pkg/audio"
)

var (
	ErrVoiceNotFound = errors.New("音色不存在")
	ErrEmptyText     = errors.New("试听文本为空")
	ErrQueueFull     = errors.New("合成队列已满")
	ErrUnavailable   = errors.New("tts_unavailable")
)

// Voice 对齐蓝本 voices 列表项：name/label/kind。
type Voice struct {
	Name  string `json:"name"`
	Label string `json:"label"`
	Kind  string `json:"kind,omitempty"`
}

// Synth 是 TTS 合成端口；真实实现调用 OmniVoice 边车。
type Synth interface {
	// Synth 返回 24k s16le mono PCM。
	Synth(ctx context.Context, text, voice string) ([]byte, error)
	Name() string
}

// FakeSynth 生成固定时长的音测试 PCM；Delay 用于验证串行锁与队列状态。
type FakeSynth struct {
	Delay time.Duration
	MS    int64

	mu     sync.Mutex
	active int
	seq    int64
}

func NewFakeSynth() *FakeSynth { return &FakeSynth{MS: 160} }

func (s *FakeSynth) Name() string { return "fake-omnivoice" }

func (s *FakeSynth) Synth(ctx context.Context, _ string, _ string) ([]byte, error) {
	s.mu.Lock()
	s.active++
	s.seq++
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.active--
		s.mu.Unlock()
	}()
	if s.Delay > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(s.Delay):
		}
	}
	ms := s.MS
	if ms <= 0 {
		ms = 160
	}
	return audio.SilencePCM(ms), nil
}

// Snapshot 返回当前并发合成数与累计次数，供测试断言。
func (s *FakeSynth) Snapshot() (active int, calls int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.active, s.seq
}

// UnavailableSynth real 模式缺 OmniVoice 地址时的 fail-closed 合成器：
// 绝不静默产出假音频，合成请求一律返回明确错误。
type UnavailableSynth struct{ Reason string }

// NewUnavailableSynth 返回一个永远失败的 Synth，错误信息含原因。
func NewUnavailableSynth(reason string) UnavailableSynth { return UnavailableSynth{Reason: reason} }

// Name 返回合成器标识。
func (u UnavailableSynth) Name() string { return "unavailable" }

// Synth 永远失败：real 模式下没有 OmniVoice 边车就明确报错。
func (u UnavailableSynth) Synth(context.Context, string, string) ([]byte, error) {
	reason := u.Reason
	if reason == "" {
		reason = "omnivoice_addr_not_configured"
	}
	return nil, fmt.Errorf("%w: %s", ErrUnavailable, reason)
}

// Queue 对齐蓝本队列状态：deep<=8，active 为正在合成数。
type Queue struct {
	Depth     int   `json:"depth"`
	Max       int   `json:"max"`
	Active    int   `json:"active"`
	Calls     int64 `json:"calls"`
	UpdatedAt int64 `json:"updated_at_ms"`
}

const maxQueueDepth = 8

type Service struct {
	lib      *media.Library
	synth    Synth
	settings *settings.Service
	voices   []Voice

	qmu      sync.Mutex
	queued   int
	lastSync int64
}

func NewService(lib *media.Library, synth Synth, st *settings.Service) *Service {
	return &Service{
		lib: lib, synth: synth, settings: st,
		voices: []Voice{
			{Name: "dh-warm-f", Label: "暖阳（中文女声）", Kind: "preset"},
			{Name: "dh-bright-f", Label: "清越（中文女声）", Kind: "preset"},
			{Name: "dh-en-f", Label: "Ember（English）", Kind: "preset"},
		},
		lastSync: time.Now().UnixMilli(),
	}
}

// Voices 返回当前音色与可选列表；current 来自设置组 voice.voice_id。
func (s *Service) Voices(ctx context.Context) (current, base string, list []Voice) {
	if s.settings != nil {
		if v, ok := s.settings.Value(ctx, "voice", "voice_id").(string); ok {
			current = v
		}
	}
	if current == "" {
		current = s.voices[0].Name
	}
	return current, s.synth.Name() + "://local", append([]Voice(nil), s.voices...)
}

func (s *Service) SetVoice(ctx context.Context, name string) error {
	if !s.hasVoice(name) {
		return ErrVoiceNotFound
	}
	if s.settings == nil {
		return errors.New("设置服务未就绪")
	}
	return s.settings.Patch(ctx, "voice", map[string]any{"voice_id": name})
}

func (s *Service) hasVoice(name string) bool {
	for _, v := range s.voices {
		if v.Name == name {
			return true
		}
	}
	return false
}

// Preview 合成试听并返回 WAV 字节；voice 为空时用当前音色，非法音色回退当前（蓝本语义）。
func (s *Service) Preview(ctx context.Context, text, voice string) ([]byte, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		text = "你好，欢迎体验数字人的声音。"
	}
	if voice == "" || !s.hasVoice(voice) {
		cur, _, _ := s.Voices(ctx)
		voice = cur
	}
	s.qmu.Lock()
	if s.queued >= maxQueueDepth {
		s.qmu.Unlock()
		return nil, ErrQueueFull
	}
	s.queued++
	s.qmu.Unlock()
	defer func() {
		s.qmu.Lock()
		s.queued--
		s.lastSync = time.Now().UnixMilli()
		s.qmu.Unlock()
	}()
	pcm, err := s.synth.Synth(ctx, text, voice)
	if err != nil {
		return nil, err
	}
	return audio.WavBytes(pcm), nil
}

func (s *Service) Queue() Queue {
	s.qmu.Lock()
	defer s.qmu.Unlock()
	active, calls := 0, int64(0)
	if f, ok := s.synth.(*FakeSynth); ok {
		active, calls = f.Snapshot()
	}
	return Queue{Depth: s.queued, Max: maxQueueDepth, Active: active, Calls: calls, UpdatedAt: s.lastSync}
}

func (s *Service) Files() (string, []media.File, error) {
	files, err := s.lib.List()
	return s.lib.Dir(), files, err
}

// Source 返回合成器来源，严格使用 real/fake/unconfigured/unknown 四值。
func (s *Service) Source() string {
	switch synth := s.synth.(type) {
	case nil:
		return "unconfigured"
	case *FakeSynth:
		return "fake"
	case UnavailableSynth:
		return "unconfigured"
	case interface{ Source() string }:
		if synth.Source() == "real" {
			return "real"
		}
	}
	return "unknown"
}

// LibDir 返回素材库目录，供服务端做 import-folder 白名单校验（不外泄给客户端）。
func (s *Service) LibDir() string { return s.lib.Dir() }

func (s *Service) Upload(name string, r io.Reader) (media.File, error) {
	return s.lib.SaveUpload(name, r)
}

func (s *Service) ImportFolder(dir string) (media.ImportResult, error) {
	return s.lib.ImportFolder(dir)
}

func (s *Service) Delete(name string) error { return s.lib.Delete(name) }

// SortedVoices 供测试稳定比较。
func (s *Service) SortedVoices() []Voice {
	out := append([]Voice(nil), s.voices...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
