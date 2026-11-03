// Package song 实现歌曲列表/上传/点播/停止与 song_playing 事件。
//
// 蓝本事实：歌曲即 `/api/tts/files` 里的音频；点播走 `/api/say/file`（engine.say_file）：
// 未开播拒绝、先解码、歌词优先 lrc.json > 同名字幕 txt > 传入脚本、播放中回写 song_playing。
// 解码走 ffmpeg 端口（FakeDecoder），喂流走 Vidu Player 端口（FakePlayer）。
package song

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/teamwork/digital-human/internal/biz/media"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

var (
	ErrNotLive   = errors.New("未开播")
	ErrNotFound  = errors.New("歌曲不存在")
	ErrBusy      = errors.New("正在播放其他歌曲")
	ErrEmptyName = errors.New("file_name 为空")
)

type State struct {
	File         string   `json:"file"`
	Script       string   `json:"script"`
	Playing      bool     `json:"song_playing"`
	Epoch        int64    `json:"epoch"`
	StartedAtMs  int64    `json:"started_at_ms,omitempty"`
	CaptionLines []string `json:"caption_lines"`
	LyricsSource string   `json:"lyrics_source"`
	Decoder      string   `json:"decoder"`
}

type Service struct {
	lib    *media.Library
	dec    media.Decoder
	player media.Player
	bus    *eventbus.Bus
	isLive func() bool

	mu     sync.Mutex
	state  State
	cancel context.CancelFunc
	epoch  int64
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
	out.CaptionLines = append([]string(nil), s.state.CaptionLines...)
	return out
}

// Play 点播：先停掉当前曲目，再解码、读取字幕、异步喂流；立即返回 playing=true。
func (s *Service) Play(ctx context.Context, name, script string) (State, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return State{}, ErrEmptyName
	}
	s.mu.Lock()
	live := s.isLive
	s.mu.Unlock()
	if live == nil || !live() {
		return State{}, ErrNotLive
	}
	path, _, err := s.lib.Path(name)
	if err != nil {
		return State{}, ErrNotFound
	}
	pcm, err := s.dec.Decode(ctx, path)
	if err != nil {
		return State{}, err
	}
	lines, source := readLyrics(path, script)

	s.mu.Lock()
	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
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
	_ = pcm
	s.publish(ctx, "song.playing", map[string]any{
		"file": name, "script": script, "epoch": epoch, "song_playing": true,
		"caption_lines": lines, "lyrics_source": source,
	})
	go s.run(pctx, epoch, path, script)
	return out, nil
}

func (s *Service) run(ctx context.Context, epoch int64, path, script string) {
	err := s.player.Play(ctx, path, script)
	s.mu.Lock()
	if s.epoch != epoch {
		s.mu.Unlock()
		return
	}
	s.state.Playing = false
	s.cancel = nil
	s.mu.Unlock()
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
