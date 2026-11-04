// Package joke 提供本地授权段子库与弹幕关键词触发；不抓取外部内容，也不主动循环播放。
package joke

import (
	"os"
	"strings"
	"sync"

	"gopkg.in/yaml.v3"
)

type Category string

const (
	CategoryFunny       Category = "搞笑"
	CategorySelfMockery Category = "自嘲"
	CategoryInteractive Category = "互动"
)

// Entry 是可审计的段子条目；Source 与 License 缺一时强制禁用。
type Entry struct {
	ID       string   `yaml:"id" json:"id"`
	Text     string   `yaml:"text" json:"text"`
	Keywords []string `yaml:"keywords" json:"keywords"`
	Category Category `yaml:"category" json:"category"`
	Source   string   `yaml:"source" json:"source"`
	License  string   `yaml:"license" json:"license"`
	Enabled  bool     `yaml:"enabled" json:"enabled"`
}

type Options struct {
	Enabled     bool
	LibraryPath string
	SongPlaying func() bool
	ChatActive  func(sessionID string) bool
}

type libraryFile struct {
	Jokes []Entry `yaml:"jokes"`
}

// Service 只响应显式弹幕触发；used 按场次隔离，防止同场重复播同一条。
type Service struct {
	mu      sync.Mutex
	enabled bool
	entries []Entry
	used    map[string]struct{}

	songPlaying func() bool
	chatActive  func(sessionID string) bool
}

func NewService(opts Options) *Service {
	s := &Service{
		enabled: opts.Enabled, used: map[string]struct{}{},
		songPlaying: opts.SongPlaying, chatActive: opts.ChatActive,
	}
	if !opts.Enabled {
		return s
	}
	raw, err := os.ReadFile(opts.LibraryPath)
	if err != nil {
		return s
	}
	var file libraryFile
	if err := yaml.Unmarshal(raw, &file); err != nil {
		return s
	}
	s.entries = append([]Entry(nil), file.Jokes...)
	seen := map[string]struct{}{}
	for i := range s.entries {
		entry := &s.entries[i]
		entry.ID = strings.TrimSpace(entry.ID)
		entry.Text = strings.TrimSpace(entry.Text)
		entry.Source = strings.TrimSpace(entry.Source)
		entry.License = strings.TrimSpace(entry.License)
		_, duplicate := seen[entry.ID]
		seen[entry.ID] = struct{}{}
		if duplicate || !validEntry(*entry) {
			entry.Enabled = false
		}
	}
	return s
}

func validEntry(entry Entry) bool {
	if entry.ID == "" || entry.Text == "" || entry.Source == "" || entry.License == "" {
		return false
	}
	switch entry.Category {
	case CategoryFunny, CategorySelfMockery, CategoryInteractive:
	default:
		return false
	}
	for _, keyword := range entry.Keywords {
		if strings.TrimSpace(keyword) != "" {
			return true
		}
	}
	return false
}

// Entries 返回清理后的库快照；不完整或重复 ID 的条目保持可见但 Enabled=false。
func (s *Service) Entries() []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Entry, len(s.entries))
	for i, entry := range s.entries {
		entry.Keywords = append([]string(nil), entry.Keywords...)
		out[i] = entry
	}
	return out
}

// Reserve 匹配并预留一条段子。matched=true 时调用方不得再转入普通聊天；
// reason 为 joke_repeated/song_playing/chat_active 时不返回可播文本。
func (s *Service) Reserve(sessionID, input string) (id, text, reason string, matched bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.enabled {
		return "", "", "", false
	}
	input = strings.ToLower(strings.TrimSpace(input))
	for i := range s.entries {
		entry := &s.entries[i]
		if !entry.Enabled || !matches(*entry, input) {
			continue
		}
		matched = true
		if id == "" {
			id = entry.ID
		}
		key := sessionID + "\x00" + entry.ID
		if _, exists := s.used[key]; exists {
			reason = "joke_repeated"
			continue
		}
		if s.songPlaying != nil && s.songPlaying() {
			return entry.ID, "", "song_playing", true
		}
		if s.chatActive != nil && s.chatActive(sessionID) {
			return entry.ID, "", "chat_active", true
		}
		s.used[key] = struct{}{}
		return entry.ID, entry.Text, "", true
	}
	return id, "", reason, matched
}

func matches(entry Entry, input string) bool {
	if input == "" {
		return false
	}
	for _, keyword := range entry.Keywords {
		keyword = strings.ToLower(strings.TrimSpace(keyword))
		if keyword != "" && strings.Contains(input, keyword) {
			return true
		}
	}
	return false
}
