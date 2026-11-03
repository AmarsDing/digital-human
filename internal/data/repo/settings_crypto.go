package repo

import (
	"context"
	"encoding/base64"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/teamwork/digital-human/internal/data/ent"
	"github.com/teamwork/digital-human/pkg/secrets"
)

// settingSecretPrefix 是 settings value_json 中密文的自识别格式标记：
// enc:v1:<base64(cipher)>。ViduKey 用独立 bytes 列存密文无需前缀；settings 整组
// value_json 落库，必须靠前缀区分明文与密文（迁移幂等与 Load 解密都依赖它）。
// 加密机制与密钥源和 ViduKey 完全同一套：pkg/secrets（Windows DPAPI /
// 非 Windows AES-256-GCM + DH_SECRET_KEY，密钥缺失即报错 fail-closed）。
const settingSecretPrefix = "enc:v1:"

func isEncryptedSettingSecret(s string) bool { return strings.HasPrefix(s, settingSecretPrefix) }

func encryptSettingSecret(plain string) (string, error) {
	cipher, err := secrets.Encrypt([]byte(plain))
	if err != nil {
		return "", err
	}
	return settingSecretPrefix + base64.StdEncoding.EncodeToString(cipher), nil
}

func decryptSettingSecret(s string) (string, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(s, settingSecretPrefix))
	if err != nil {
		return "", fmt.Errorf("settings secret: bad base64: %w", err)
	}
	plain, err := secrets.Decrypt(raw)
	if err != nil {
		return "", fmt.Errorf("settings secret: decrypt: %w", err)
	}
	return string(plain), nil
}

// MigrateLLMAPIKey 把 settings 表 llm 组 value_json 里的存量明文 api_key 原地加密。
// 幂等：enc:v1: 前缀即密文格式自识别，已加密/空值/非字符串一律跳过；llm 行不存在
// 视为无事可迁。加密失败（非 Windows 缺 DH_SECRET_KEY）返回错误由启动方记录、
// 下次启动重试，绝不把明文继续留在库里还假装迁移成功。
// 日志只记事实，不含任何密钥材料（明文/密文/尾号都不出日志）。
func MigrateLLMAPIKey(ctx context.Context, client *ent.Client, logger *slog.Logger) error {
	row, err := client.Setting.Get(ctx, "llm")
	if err != nil {
		if ent.IsNotFound(err) {
			return nil
		}
		return err
	}
	raw, ok := row.ValueJSON["api_key"].(string)
	if !ok || raw == "" || isEncryptedSettingSecret(raw) {
		return nil
	}
	enc, err := encryptSettingSecret(raw)
	if err != nil {
		return fmt.Errorf("llm api_key migration: encrypt: %w", err)
	}
	vals := cloneValues(row.ValueJSON)
	vals["api_key"] = enc
	if _, err := client.Setting.UpdateOneID("llm").
		SetValueJSON(vals).
		SetUpdatedAtMs(time.Now().UnixMilli()).
		SetUpdatedBy("migration").
		Save(ctx); err != nil {
		return fmt.Errorf("llm api_key migration: update: %w", err)
	}
	logger.Info("settings migration: llm.api_key 已原地加密（enc:v1）")
	return nil
}
