package joke

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeLibrary(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jokes.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestServiceKeywordTriggerAndSessionDeduplication(t *testing.T) {
	svc := NewService(Options{Enabled: true, LibraryPath: writeLibrary(t, `jokes:
  - id: original-1
    text: 我的记性很好，尤其是记住自己忘了什么。
    keywords: [讲个笑话]
    category: 搞笑
    source: 项目组原创
    license: 项目内部使用授权
    enabled: true
`)})
	id, text, reason, matched := svc.Reserve("s1", "主播，讲个笑话吧")
	if !matched || id != "original-1" || text == "" || reason != "" {
		t.Fatalf("关键词必须命中已授权段子: id=%q text=%q reason=%q matched=%v", id, text, reason, matched)
	}
	id, text, reason, matched = svc.Reserve("s1", "再讲个笑话")
	if !matched || id != "original-1" || text != "" || reason != "joke_repeated" {
		t.Fatalf("同场同段子不得重复: id=%q text=%q reason=%q matched=%v", id, text, reason, matched)
	}
	_, text, reason, matched = svc.Reserve("s2", "讲个笑话")
	if !matched || text == "" || reason != "" {
		t.Fatalf("不同场次应有独立去重状态: text=%q reason=%q matched=%v", text, reason, matched)
	}
}

func TestServiceRejectsEntriesWithoutSourceOrLicense(t *testing.T) {
	svc := NewService(Options{Enabled: true, LibraryPath: writeLibrary(t, `jokes:
  - id: missing-source
    text: 不得启用
    keywords: [缺来源]
    category: 搞笑
    license: 有授权
    enabled: true
  - id: missing-license
    text: 不得启用
    keywords: [缺授权]
    category: 自嘲
    source: 项目组原创
    enabled: true
  - id: licensed
    text: 可以启用
    keywords: [有授权]
    category: 互动
    source: 项目组原创
    license: 项目内部使用授权
    enabled: true
`)})
	for _, keyword := range []string{"缺来源", "缺授权"} {
		if id, text, reason, matched := svc.Reserve("s1", keyword); matched || id != "" || text != "" || reason != "" {
			t.Fatalf("来源与授权缺失时必须视为未启用: keyword=%q id=%q text=%q reason=%q matched=%v", keyword, id, text, reason, matched)
		}
	}
	for _, entry := range svc.Entries() {
		if (entry.ID == "missing-source" || entry.ID == "missing-license") && entry.Enabled {
			t.Fatalf("不完整条目必须被强制禁用: %+v", entry)
		}
	}
	if _, text, _, matched := svc.Reserve("s1", "有授权"); !matched || text != "可以启用" {
		t.Fatalf("完整授权条目应可触发: text=%q matched=%v", text, matched)
	}
}

func TestDefaultLibraryContainsOnlyCompleteOriginalEntries(t *testing.T) {
	svc := NewService(Options{Enabled: true, LibraryPath: filepath.Join("..", "..", "..", "configs", "jokes.yaml")})
	entries := svc.Entries()
	if len(entries) != 3 {
		t.Fatalf("默认库应为三类共三条原创示例，实际 %d", len(entries))
	}
	categories := map[Category]bool{}
	for _, entry := range entries {
		if !entry.Enabled || entry.Source == "" || entry.License == "" {
			t.Fatalf("默认条目必须完整授权且启用: %+v", entry)
		}
		if !strings.Contains(entry.Source, "原创") {
			t.Fatalf("本批默认库不得包含搬运或第三方条目: %+v", entry)
		}
		categories[entry.Category] = true
	}
	for _, category := range []Category{CategoryFunny, CategorySelfMockery, CategoryInteractive} {
		if !categories[category] {
			t.Fatalf("默认库缺少分类 %q", category)
		}
	}
}

func TestServiceYieldsToSongAndTextChat(t *testing.T) {
	for _, tt := range []struct {
		name       string
		song       bool
		chat       bool
		wantReason string
	}{
		{name: "song", song: true, wantReason: "song_playing"},
		{name: "text chat", chat: true, wantReason: "chat_active"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			svc := NewService(Options{
				Enabled: true, LibraryPath: writeLibrary(t, `jokes:
  - id: priority
    text: 低优先级段子
    keywords: [笑话]
    category: 搞笑
    source: 项目组原创
    license: 项目内部使用授权
    enabled: true
`),
				SongPlaying: func() bool { return tt.song },
				ChatActive:  func(string) bool { return tt.chat },
			})
			id, text, reason, matched := svc.Reserve("s1", "笑话")
			if !matched || id != "priority" || text != "" || reason != tt.wantReason {
				t.Fatalf("高优先级活动必须抑制段子: id=%q text=%q reason=%q matched=%v", id, text, reason, matched)
			}
		})
	}
}
