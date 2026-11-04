// Package song 实现歌曲列表/上传/点播/停止与 song_playing 事件。
//
// 蓝本事实：歌曲即 `/api/tts/files` 里的音频；点播走 `/api/say/file`（engine.say_file）：
// 未开播拒绝、先解码、歌词优先 lrc.json > 同名字幕 txt > 传入脚本、播放中回写 song_playing。
// 解码走 ffmpeg 端口（FakeDecoder），喂流走 Vidu Player 端口（FakePlayer）。
package song

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/teamwork/digital-human/internal/biz/capability"
	"github.com/teamwork/digital-human/internal/biz/media"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

var (
	ErrNotLive   = errors.New("未开播")
	ErrNotFound  = errors.New("歌曲不存在")
	ErrBusy      = errors.New("正在播放其他歌曲")
	ErrStopped   = errors.New("歌曲已停止")
	ErrEmptyName = errors.New("file_name 为空")
)

type State struct {
	Source       capability.Status `json:"source"`
	File         string            `json:"file"`
	Script       string            `json:"script"`
	Playing      bool              `json:"song_playing"`
	Epoch        int64             `json:"epoch"`
	StartedAtMs  int64             `json:"started_at_ms,omitempty"`
	CaptionLines []string          `json:"caption_lines"`
	LyricsSource string            `json:"lyrics_source"`
	Decoder      string            `json:"decoder"`
}

type Service struct {
	lib    *media.Library
	dec    media.Decoder
	player media.Player
	bus    *eventbus.Bus
	isLive func() bool

	dispatchMu sync.Mutex
	mu         sync.Mutex
	state      State
	cancel     context.CancelFunc
	epoch      int64

	cacheDigest [sha256.Size]byte
	cachePCM    []byte
}

func NewService(lib *media.Library, dec media.Decoder, player media.Player, bus *eventbus.Bus, isLive func() bool) *Service {
	if isLive == nil {
		isLive = func() bool { return true }
	}
	return &Service{lib: lib, dec: dec, player: player, bus: bus, isLive: isLive}
}

func (s *Service) SetLive(fn func() bool) {
	s.mu.Lock()
	s.isLive = fn
	s.mu.Unlock()
}

func (s *Service) List() ([]media.File, error) { return s.lib.List() }

func (s *Service) Upload(name string, r io.Reader) (media.File, error) {
	return s.lib.SaveUpload(name, r)
}

func (s *Service) Delete(name string) error { return s.lib.Delete(name) }

func (s *Service) Status() State {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.snapshot()
}

func (s *Service) snapshot() State {
	out := s.state
	out.Source = playbackSource(s.dec, s.player)
	out.CaptionLines = append([]string(nil), s.state.CaptionLines...)
	return out
}

func playbackSource(dec media.Decoder, player media.Player) capability.Status {
	decoder := decoderSource(dec)
	source := playerSource(player)
	if decoder == capability.Unconfigured || source == capability.Unconfigured {
		return capability.Unconfigured
	}
	if decoder == capability.Unknown || source == capability.Unknown {
		return capability.Unknown
	}
	if decoder == capability.Fake || source == capability.Fake {
		return capability.Fake
	}
	return capability.Real
}

func decoderSource(dec media.Decoder) capability.Status {
	switch dec.(type) {
	case nil, media.UnavailableDecoder:
		return capability.Unconfigured
	case media.FakeDecoder:
		return capability.Fake
	case *media.FfmpegDecoder:
		return capability.Real
	default:
		return capability.Unknown
	}
}

func playerSource(player media.Player) capability.Status {
	switch player.(type) {
	case nil, media.UnavailablePlayer:
		return capability.Unconfigured
	case *media.FakePlayer:
		return capability.Fake
	case *media.FfmpegPlayer:
		return capability.Real
	default:
		return capability.Unknown
	}
}

// Play 保留手动入口，但播放中拒绝抢占；自动点歌只能由 Scheduler 调用同一内部原语。
func (s *Service) Play(ctx context.Context, name, script string) (State, error) {
	return s.start(ctx, name, script, nil)
}

func (s *Service) start(ctx context.Context, name, script string, onDone func(error)) (State, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return State{}, ErrEmptyName
	}
	s.mu.Lock()
	live := s.isLive
	busy := s.state.Playing || s.cancel != nil
	s.mu.Unlock()
	if live == nil || !live() {
		return State{}, ErrNotLive
	}
	if busy {
		return State{}, ErrBusy
	}
	path, _, err := s.lib.Path(name)
	if err != nil {
		return State{}, ErrNotFound
	}
	pcm, err := s.decodePCM(ctx, path)
	if err != nil {
		return State{}, err
	}
	lines, source := readLyrics(path, script)

	s.mu.Lock()
	if s.state.Playing || s.cancel != nil {
		s.mu.Unlock()
		return State{}, ErrBusy
	}
	s.epoch++
	epoch := s.epoch
	pctx, cancel := context.WithCancel(ctx)
	s.cancel = cancel
	s.state = State{
		File: name, Script: script, Playing: true, Epoch: epoch,
		StartedAtMs: time.Now().UnixMilli(), CaptionLines: lines,
		LyricsSource: source, Decoder: s.dec.Name(),
	}
	out := s.snapshot()
	s.mu.Unlock()

	_ = s.player.Stop(context.WithoutCancel(ctx))
	s.publish(ctx, "song.playing", map[string]any{
		"file": name, "script": script, "epoch": epoch, "song_playing": true,
		"caption_lines": lines, "lyrics_source": source,
	})
	go s.run(pctx, epoch, path, script, pcm, onDone)
	return out, nil
}

// decodePCM 以文件内容 SHA-256 为缓存键；只保留最近一项，避免 PCM 无界增长。
func (s *Service) decodePCM(ctx context.Context, path string) ([]byte, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	digest := sha256.Sum256(raw)
	s.mu.Lock()
	if s.cachePCM != nil && s.cacheDigest == digest {
		pcm := s.cachePCM
		s.mu.Unlock()
		return pcm, nil
	}
	s.mu.Unlock()

	pcm, err := s.dec.Decode(ctx, path)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.cacheDigest = digest
	s.cachePCM = pcm
	s.mu.Unlock()
	return pcm, nil
}

func (s *Service) run(ctx context.Context, epoch int64, path, script string, pcm []byte, onDone func(error)) {
	var err error
	if player, ok := s.player.(media.PCMPlayer); ok {
		err = player.PlayPCM(ctx, pcm, script)
	} else {
		err = s.player.Play(ctx, path, script)
	}
	s.mu.Lock()
	current := s.epoch == epoch
	if current {
		s.state.Playing = false
		s.cancel = nil
	}
	s.mu.Unlock()
	if onDone != nil {
		if !current && err == nil {
			err = ErrStopped
		}
		onDone(err)
	}
	if !current {
		return
	}
	if err != nil {
		s.publish(context.WithoutCancel(ctx), "song.error", map[string]any{"file": path, "error": err.Error()})
		return
	}
	s.publish(context.WithoutCancel(ctx), "song.ended", map[string]any{"epoch": epoch, "song_playing": false})
}

// Stop 停止点播并清空 song_playing。
func (s *Service) Stop(ctx context.Context) (State, error) {
	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}
	s.epoch++
	s.state.Playing = false
	out := s.snapshot()
	s.mu.Unlock()
	_ = s.player.Stop(ctx)
	s.publish(ctx, "song.stopped", map[string]any{"song_playing": false, "file": out.File})
	return out, nil
}

func (s *Service) publish(ctx context.Context, typ string, payload map[string]any) {
	if s.bus != nil {
		// 场次/epoch 由 Manager 绑定的 scope 统一填充，换场次不串号。
		s.bus.PublishScoped(ctx, typ, payload)
	}
}

// readLyrics 优先级与蓝本一致：lrc.json > script > 同名 txt；都没有则空字幕。
func readLyrics(audioPath, script string) ([]string, string) {
	stem := strings.TrimSuffix(audioPath, filepath.Ext(audioPath))
	if raw, err := os.ReadFile(stem + ".lrc.json"); err == nil {
		if lines := parseLRC(raw); len(lines) > 0 {
			return lines, "lrc"
		}
	}
	if script != "" {
		return splitScript(script), "script"
	}
	if raw, err := os.ReadFile(stem + ".txt"); err == nil {
		return splitScript(string(raw)), "txt"
	}
	return []string{}, "none"
}

func parseLRC(raw []byte) []string {
	var arr []string
	if json.Unmarshal(raw, &arr) == nil && len(arr) > 0 {
		return arr
	}
	var obj struct {
		Lines []json.RawMessage `json:"lines"`
	}
	if json.Unmarshal(raw, &obj) != nil {
		return nil
	}
	out := make([]string, 0, len(obj.Lines))
	for _, l := range obj.Lines {
		var s string
		if json.Unmarshal(l, &s) == nil {
			out = append(out, s)
			continue
		}
		var item struct {
			Text string `json:"text"`
		}
		if json.Unmarshal(l, &item) == nil && item.Text != "" {
			out = append(out, item.Text)
		}
	}
	return out
}

func splitScript(text string) []string {
	text = strings.ReplaceAll(text, "\r\n", "\n")
	raw := strings.Split(text, "\n")
	out := []string{}
	for _, line := range raw {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		runes := []rune(line)
		for len(runes) > 32 {
			out = append(out, string(runes[:32]))
			runes = runes[32:]
		}
		if len(runes) > 0 {
			out = append(out, string(runes))
		}
	}
	return out
}
