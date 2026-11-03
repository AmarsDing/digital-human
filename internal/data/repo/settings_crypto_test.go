package repo

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"entgo.io/ent/dialect"
	"github.com/google/uuid"
	_ "github.com/mattn/go-sqlite3"
	"github.com/teamwork/digital-human/internal/biz/settings"
	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/internal/data/ent/enttest"
	"github.com/teamwork/digital-human/internal/llm"
)

// setSecretEnv 为非 Windows 平台提供 DH_SECRET_KEY（Windows 走 DPAPI 忽略该 env），
// 与 ViduKey 同一密钥源，保证 CI 上加解密可用。
func setSecretEnv(t *testing.T) {
	t.Helper()
	t.Setenv("DH_SECRET_KEY", strings.Repeat("0123456789abcdef", 4))
}

func openSettingsSQLite(t *testing.T) *ent.Client {
	t.Helper()
	client := enttest.Open(t, dialect.SQLite, "file:"+uuid.NewString()+"?mode=memory&cache=shared&_fk=1")
	t.Cleanup(func() { client.Close() })
	return client
}

const testLLMKey = "sk-live-1234567890abcdef"

// ① 写入后直读 DB：value_json 中无明文子串，api_key 为 enc:v1: 密文；
// 再次 Patch 空 api_key（保留语义）不得双重加密，且解密后仍是同一明文。
func TestEntSettingStoreEncryptsLLMAPIKeyAtRest(t *testing.T) {
	setSecretEnv(t)
	ctx := context.Background()
	client := openSettingsSQLite(t)
	svc := settings.NewService(NewEntSettingStore(client))

	if err := svc.Patch(ctx, "llm", map[string]any{"api_key": testLLMKey}); err != nil {
		t.Fatalf("patch llm: %v", err)
	}

	assertCiphertextAtRest := func() string {
		row, err := client.Setting.Get(ctx, "llm")
		if err != nil {
			t.Fatalf("read llm row: %v", err)
		}
		raw, _ := row.ValueJSON["api_key"].(string)
		if !isEncryptedSettingSecret(raw) {
			t.Fatalf("api_key 未加密落库: %q", raw)
		}
		blob, _ := json.Marshal(row.ValueJSON)
		if strings.Contains(string(blob), testLLMKey) {
			t.Fatalf("value_json 含明文子串: %s", blob)
		}
		return raw
	}
	assertCiphertextAtRest()

	// 保留语义重存（前端不回传密钥的场景）：不得出现 enc:v1:enc:v1: 双重加密。
	if err := svc.Patch(ctx, "llm", map[string]any{"api_key": ""}); err != nil {
		t.Fatalf("re-patch llm: %v", err)
	}
	raw := assertCiphertextAtRest()
	if strings.Contains(strings.TrimPrefix(raw, settingSecretPrefix), settingSecretPrefix) {
		t.Fatalf("api_key 被双重加密: %q", raw)
	}
	if got := svc.Value(ctx, "llm", "api_key"); got != testLLMKey {
		t.Fatalf("重存后解密明文不匹配: got %q", got)
	}
}

// ② GET 脱敏：响应只含尾 8 位，明文与密文都不出接口。
func TestSettingsGetMasksEncryptedLLMAPIKey(t *testing.T) {
	setSecretEnv(t)
	ctx := context.Background()
	client := openSettingsSQLite(t)
	svc := settings.NewService(NewEntSettingStore(client))

	if err := svc.Patch(ctx, "llm", map[string]any{"api_key": testLLMKey}); err != nil {
		t.Fatalf("patch llm: %v", err)
	}
	row, err := client.Setting.Get(ctx, "llm")
	if err != nil {
		t.Fatalf("read llm row: %v", err)
	}
	cipher, _ := row.ValueJSON["api_key"].(string)

	groups, _, err := svc.Get(ctx)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	var llmGroup *settings.Group
	for i := range groups {
		if groups[i].ID == "llm" {
			llmGroup = &groups[i]
		}
	}
	if llmGroup == nil {
		t.Fatal("llm group missing")
	}
	if got := llmGroup.Values["api_key"]; got != "" {
		t.Fatalf("GET 不应回传 api_key 值: %v", got)
	}
	if got := llmGroup.Values["api_key_tail8"]; got != "90abcdef" {
		t.Fatalf("api_key_tail8 = %v, want 90abcdef", got)
	}
	blob, _ := json.Marshal(groups)
	if strings.Contains(string(blob), testLLMKey) {
		t.Fatal("GET 响应含明文")
	}
	if strings.Contains(string(blob), cipher) || strings.Contains(string(blob), settingSecretPrefix) {
		t.Fatal("GET 响应含密文")
	}
}

// ③ 消费方解密后可用：模拟 wire.go confGet 从 st.Value 组装 llm.Config，
// fake OpenAI 服务端断言 Authorization 头携带的正是解密后的明文 key。
func TestLLMClientUsesDecryptedKey(t *testing.T) {
	setSecretEnv(t)
	ctx := context.Background()
	client := openSettingsSQLite(t)
	svc := settings.NewService(NewEntSettingStore(client))

	var gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth = r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
	}))
	defer srv.Close()

	if err := svc.Patch(ctx, "llm", map[string]any{
		"api_key":  testLLMKey,
		"base_url": srv.URL,
	}); err != nil {
		t.Fatalf("patch llm: %v", err)
	}

	// 与 cmd/dh/wire.go provideLLMClient 的 confGet 一致。
	llmClient := llm.NewClient(func() llm.Config {
		cfg := llm.Config{}
		if s, ok := svc.Value(ctx, "llm", "base_url").(string); ok {
			cfg.BaseURL = s
		}
		if s, ok := svc.Value(ctx, "llm", "api_key").(string); ok {
			cfg.APIKey = s
		}
		if s, ok := svc.Value(ctx, "llm", "model").(string); ok {
			cfg.Model = s
		}
		return cfg
	})
	sentences, err := llmClient.Stream(ctx, []llm.Message{{Role: "user", Content: "hi"}})
	if err != nil {
		t.Fatalf("stream: %v", err)
	}
	for s := range sentences {
		if s.Err != nil {
			t.Fatalf("stream sentence err: %v", s.Err)
		}
	}
	if gotAuth != "Bearer "+testLLMKey {
		t.Fatalf("Authorization = %q, want Bearer %s", gotAuth, testLLMKey)
	}
}

// ④ 迁移幂等：明文→密文→再跑不变；无 llm 行/无 api_key 字段均安全跳过。
func TestMigrateLLMAPIKeyIdempotent(t *testing.T) {
	setSecretEnv(t)
	ctx := context.Background()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("明文加密后可解密且再跑不变", func(t *testing.T) {
		client := openSettingsSQLite(t)
		const legacy = "sk-legacy-abcdef123456"
		// 模拟 F 批迁移前的存量明文行。
		if _, err := client.Setting.Create().
			SetID("llm").
			SetValueJSON(map[string]any{
				"api_key":  legacy,
				"base_url": "https://api.openai.com/v1",
				"model":    "gpt-4o-mini",
			}).
			SetUpdatedAtMs(1).
			SetUpdatedBy("test").
			Save(ctx); err != nil {
			t.Fatalf("seed llm row: %v", err)
		}

		if err := MigrateLLMAPIKey(ctx, client, logger); err != nil {
			t.Fatalf("migrate run1: %v", err)
		}
		row, err := client.Setting.Get(ctx, "llm")
		if err != nil {
			t.Fatalf("read llm row: %v", err)
		}
		raw, _ := row.ValueJSON["api_key"].(string)
		if !isEncryptedSettingSecret(raw) {
			t.Fatalf("迁移后仍为明文: %q", raw)
		}
		blob, _ := json.Marshal(row.ValueJSON)
		if strings.Contains(string(blob), legacy) {
			t.Fatalf("迁移后 value_json 仍含明文: %s", blob)
		}
		// 迁移后 Load 解密可用。
		svc := settings.NewService(NewEntSettingStore(client))
		if got := svc.Value(ctx, "llm", "api_key"); got != legacy {
			t.Fatalf("迁移后解密明文不匹配: got %q", got)
		}

		// 幂等：第二次运行值逐字节不变。
		if err := MigrateLLMAPIKey(ctx, client, logger); err != nil {
			t.Fatalf("migrate run2: %v", err)
		}
		row2, err := client.Setting.Get(ctx, "llm")
		if err != nil {
			t.Fatalf("read llm row run2: %v", err)
		}
		if got, _ := row2.ValueJSON["api_key"].(string); got != raw {
			t.Fatalf("迁移不幂等: run1 %q != run2 %q", raw, got)
		}
	})

	t.Run("无 llm 行或无 api_key 字段安全跳过", func(t *testing.T) {
		client := openSettingsSQLite(t)
		if err := MigrateLLMAPIKey(ctx, client, logger); err != nil {
			t.Fatalf("migrate without llm row: %v", err)
		}
		if _, err := client.Setting.Create().
			SetID("llm").
			SetValueJSON(map[string]any{"model": "gpt-4o-mini"}).
			SetUpdatedAtMs(1).
			SetUpdatedBy("test").
			Save(ctx); err != nil {
			t.Fatalf("seed llm row: %v", err)
		}
		if err := MigrateLLMAPIKey(ctx, client, logger); err != nil {
			t.Fatalf("migrate without api_key: %v", err)
		}
		row, err := client.Setting.Get(ctx, "llm")
		if err != nil {
			t.Fatalf("read llm row: %v", err)
		}
		if _, exists := row.ValueJSON["api_key"]; exists {
			t.Fatalf("无 api_key 的行不应被改写: %v", row.ValueJSON)
		}
	})
}
