package settings

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// F 批礼物/贵宾切片新增 gift 组（F27 合并播报参数）：组数 12 → 13。
func TestSaveRequiresAllThirteenGroups(t *testing.T) {
	svc := NewService(NewMemoryStore())
	groups, _, err := svc.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(groups) != 13 {
		t.Fatalf("default groups = %d, want 13", len(groups))
	}
	if _, _, err := svc.Save(context.Background(), groups[:12], 1); err == nil {
		t.Fatal("partial save must be rejected")
	}
}

func TestGiftTierDefaults(t *testing.T) {
	for _, group := range DefaultGroups() {
		if group.ID != "gift" {
			continue
		}
		want := map[string]any{
			"tier_medium_diamond": 50, "tier_large_diamond": 200,
			"tier_small_template": "{thanks}", "tier_medium_template": "{thanks}", "tier_large_template": "{thanks}",
			"tier_small_action": "", "tier_medium_action": "", "tier_large_action": "",
		}
		for key, value := range want {
			if group.Values[key] != value {
				t.Errorf("gift.%s=%v want %v", key, group.Values[key], value)
			}
		}
		return
	}
	t.Fatal("gift group missing")
}

func TestFrontendGiftTierFields(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "src", "components", "settings", "schema.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{
		"tier_medium_diamond", "tier_large_diamond",
		"tier_small_template", "tier_medium_template", "tier_large_template",
		"tier_small_action", "tier_medium_action", "tier_large_action",
	} {
		if !strings.Contains(string(raw), "key: '"+key+"'") {
			t.Errorf("前端礼物设置缺少字段 %s", key)
		}
	}
}

func TestDanmakuModeValidated(t *testing.T) {
	svc := NewService(NewMemoryStore())
	groups, rev, err := svc.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i := range groups {
		if groups[i].ID == "danmaku" {
			groups[i].Values["mode"] = "bogus"
		}
	}
	if _, _, err := svc.Save(context.Background(), groups, rev); err == nil {
		t.Fatal("invalid danmaku mode must be rejected")
	}
}

func TestEffectiveMatrix(t *testing.T) {
	svc := NewService(NewMemoryStore())
	groups, rev, _ := svc.Get(context.Background())
	_, reload, err := svc.Save(context.Background(), groups, rev)
	if err != nil {
		t.Fatal(err)
	}
	immediate := map[string]bool{"voice": true, "captions": true, "danmaku": true, "gift": true, "member": true}
	for _, id := range reload {
		if immediate[id] {
			t.Fatalf("group %s marked next_session but should be immediate", id)
		}
	}
	if len(reload) != 8 {
		t.Fatalf("reload groups = %d, want 8（13 组 - 5 个 immediate）", len(reload))
	}
}

// TestFrontendEffectiveMatrixMatchesDefaultGroups 以后端 DefaultGroups 为唯一权威，
// 直接对拍抽屉实际消费的 GROUP_SPECS，防止前后端各自维护的生效时机发生漂移。
func TestFrontendEffectiveMatrixMatchesDefaultGroups(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "web", "src", "components", "settings", "schema.ts"))
	if err != nil {
		t.Fatal(err)
	}
	matches := regexp.MustCompile(`(?m)id:\s*'([^']+)',\s*title:\s*'([^']+)',\s*effective:\s*'([^']+)'`).FindAllStringSubmatch(string(raw), -1)
	groups := DefaultGroups()
	if len(groups) != 13 {
		t.Fatalf("backend groups = %d, want 13", len(groups))
	}
	if len(matches) != len(groups) {
		t.Fatalf("frontend groups = %d, backend groups = %d", len(matches), len(groups))
	}
	frontCounts := map[string]int{}
	for i, group := range groups {
		front := matches[i]
		if front[1] != group.ID || front[2] != group.Title || front[3] != group.Effective {
			t.Errorf("group %d mismatch: frontend={id:%s title:%s effective:%s}, backend={id:%s title:%s effective:%s}",
				i, front[1], front[2], front[3], group.ID, group.Title, group.Effective)
		}
		frontCounts[front[3]]++
	}
	if frontCounts["immediate"] != 5 || frontCounts["next_session"] != 8 {
		t.Errorf("frontend effective counts = immediate:%d next_session:%d, want 5/8", frontCounts["immediate"], frontCounts["next_session"])
	}
}

// F15：llm 组默认值带 base_url/api_key/fallback_models（对齐蓝本 llm.py 默认 DeepSeek）。
func TestLLMGroupDefaults(t *testing.T) {
	svc := NewService(NewMemoryStore())
	groups, _, err := svc.Get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if g.ID != "llm" {
			continue
		}
		if g.Values["base_url"] != "https://api.deepseek.com" {
			t.Fatalf("base_url = %v", g.Values["base_url"])
		}
		if g.Values["api_key"] != "" {
			t.Fatalf("api_key 默认应为空: %v", g.Values["api_key"])
		}
		if _, ok := g.Values["fallback_models"]; !ok {
			t.Fatal("fallback_models 缺默认值")
		}
		return
	}
	t.Fatal("缺 llm 组")
}

// F15：api_key 明文不回显（vidu_key active_tail8 同款脱敏），内部可取明文；
// 脱显结果原样回存不冲掉明文（空视为未修改）。
func TestLLMAPIKeyMaskedAndPreserved(t *testing.T) {
	ctx := context.Background()
	svc := NewService(NewMemoryStore())
	if err := svc.Patch(ctx, "llm", map[string]any{"api_key": "sk-1234567890abcdef"}); err != nil {
		t.Fatal(err)
	}
	groups, rev, err := svc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if g.ID != "llm" {
			continue
		}
		if g.Values["api_key"] != "" {
			t.Fatalf("api_key 不应明文回显: %v", g.Values["api_key"])
		}
		if g.Values["api_key_tail8"] != "90abcdef" {
			t.Fatalf("api_key_tail8 = %v", g.Values["api_key_tail8"])
		}
	}
	// 内部读取仍是明文。
	if got := svc.Value(ctx, "llm", "api_key"); got != "sk-1234567890abcdef" {
		t.Fatalf("内部明文 = %v", got)
	}
	// 前端拿脱显结果整组回存 → 明文保留。
	if _, _, err := svc.Save(ctx, groups, rev); err != nil {
		t.Fatal(err)
	}
	if got := svc.Value(ctx, "llm", "api_key"); got != "sk-1234567890abcdef" {
		t.Fatalf("回存后明文被冲掉: %v", got)
	}
	// Patch 空 key 同样视为未修改。
	if err := svc.Patch(ctx, "llm", map[string]any{"api_key": ""}); err != nil {
		t.Fatal(err)
	}
	if got := svc.Value(ctx, "llm", "api_key"); got != "sk-1234567890abcdef" {
		t.Fatalf("Patch 空 key 不应覆盖: %v", got)
	}
}

// TestSaveKeepsSecondaryPromptAndStripsDeadPersonaContent：llm.system 是 G05 第二来源，
// 必须持久化；persona.content 仍是死键，必须剥离。
func TestSaveKeepsSecondaryPromptAndStripsDeadPersonaContent(t *testing.T) {
	svc := NewService(NewMemoryStore())
	ctx := context.Background()
	groups, rev, err := svc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for i := range groups {
		switch groups[i].ID {
		case "llm":
			groups[i].Values["system"] = "你是猫娘"
		case "persona":
			groups[i].Values["content"] = "死数据人设"
		}
	}
	if _, _, err := svc.Save(ctx, groups, rev); err != nil {
		t.Fatal(err)
	}
	after, _, err := svc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range after {
		switch g.ID {
		case "llm":
			if g.Values["system"] != "你是猫娘" {
				t.Fatalf("llm.system 第二来源必须保留: %+v", g.Values)
			}
			if g.Values["base_url"] == nil {
				t.Fatal("llm 其余键不得受影响")
			}
		case "persona":
			if _, has := g.Values["content"]; has {
				t.Fatalf("persona.content 必须被剥离: %+v", g.Values)
			}
		}
	}
}

// TestPatchIgnoresDeadPromptKeys G3-3：Patch 窄写入同样忽略死键。
func TestPatchIgnoresDeadPromptKeys(t *testing.T) {
	svc := NewService(NewMemoryStore())
	ctx := context.Background()
	if err := svc.Patch(ctx, "persona", map[string]any{"content": "x", "active_id": "p1"}); err != nil {
		t.Fatal(err)
	}
	groups, _, err := svc.Get(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, g := range groups {
		if g.ID != "persona" {
			continue
		}
		if _, has := g.Values["content"]; has {
			t.Fatalf("Patch 必须忽略 persona.content: %+v", g.Values)
		}
		if g.Values["active_id"] != "p1" {
			t.Fatalf("合法键必须生效: %+v", g.Values)
		}
	}
}
