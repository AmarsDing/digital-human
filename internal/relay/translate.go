// Package relay 实现 dh-relay：订阅本地抖音弹幕采集服务（douyinLive ws），
// 翻译为 digital-human 弹幕契约，HMAC 签名后 POST /api/danmaku。
// 采集内核外置（合规边界）；本包只做翻译+转发+重连+心跳。
package relay

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// Out 是发往 /api/danmaku 的请求体（对齐 IngestRequest 契约）。
type Out struct {
	Kind           string `json:"kind"` // chat | gift | join
	UserID         string `json:"user_id,omitempty"`
	UserName       string `json:"user_name"`
	Text           string `json:"text"`
	Diamond        int64  `json:"diamond,omitempty"`
	Source         string `json:"source"` // 固定 douyin
	IdempotencyKey string `json:"idempotency_key"`
	OccurredAtMs   int64  `json:"occurred_at_ms,omitempty"`
	ReceivedAtMs   int64  `json:"received_at_ms,omitempty"`
	ExpiresAtMs    int64  `json:"expires_at_ms,omitempty"`
}

// Translate 把 douyinLive 的一条 JSON 消息翻译为 Out；不关心的类型返回 nil。
// 行为事实（来自蓝本转发器契约）：连击礼物只在 repeatEnd 或非连击时转发，防数量算重。
func Translate(msg map[string]any) *Out {
	if msg == nil {
		return nil
	}
	if t, _ := msg["type"].(string); t == "system" {
		return nil
	}
	method, _ := msg["method"].(string)
	user, _ := msg["user"].(map[string]any)
	nick := strings.TrimSpace(str(user["nickname"]))

	switch method {
	case "WebcastChatMessage":
		content := strings.TrimSpace(str(msg["content"]))
		if nick == "" || content == "" {
			return nil
		}
		return &Out{Kind: "chat", UserName: nick, UserID: str(user["id"]),
			Text: content, Source: "douyin", IdempotencyKey: msgKey(msg),
			OccurredAtMs: eventTimeMs(msg), ExpiresAtMs: toI64(msg["expiresAtMs"])}

	case "WebcastGiftMessage", "WebcastLightGiftMessage", "WebcastBindingGiftMessage":
		inner := msg
		if method == "WebcastBindingGiftMessage" {
			m, _ := msg["msg"].(map[string]any)
			if m == nil {
				return nil
			}
			inner = m
			if iu, _ := inner["user"].(map[string]any); iu != nil {
				if n := strings.TrimSpace(str(iu["nickname"])); n != "" {
					nick = n
				}
			}
		}
		gift, _ := inner["gift"].(map[string]any)
		item := strings.TrimSpace(str(gift["name"]))
		if nick == "" || item == "" {
			return nil
		}
		// 连击礼物会连续推很多帧：只在连击结束（repeatEnd）或非连击（无 groupId）时转发
		if str(inner["groupId"]) != "" && !truthy(inner["repeatEnd"]) {
			return nil
		}
		num := maxI64(toI64(inner["repeatCount"]), toI64(inner["comboCount"]),
			toI64(inner["totalCount"]), toI64(inner["count"]), toI64(inner["groupCount"]), 1)
		return &Out{Kind: "gift", UserName: nick, UserID: str(user["id"]),
			Text: fmt.Sprintf("%s x%d", item, num), Diamond: toI64(gift["diamondCount"]),
			Source: "douyin", IdempotencyKey: msgKey(inner)}

	case "WebcastMemberMessage": // 进场（契约支持 join；蓝本未转发，属增强）
		if nick == "" {
			return nil
		}
		return &Out{Kind: "join", UserName: nick, UserID: str(user["id"]),
			Text: nick + " 进入直播间", Source: "douyin", IdempotencyKey: msgKey(msg)}
	}
	return nil
}

// msgKey 生成幂等键：sha256(method + userId + groupId/msgId + ts) 前 16 位。
func msgKey(m map[string]any) string {
	var b strings.Builder
	b.WriteString(str(m["method"]))
	b.WriteString("|")
	if u, _ := m["user"].(map[string]any); u != nil {
		b.WriteString(str(u["id"]))
	}
	b.WriteString("|")
	for _, k := range []string{"msgId", "msg_id", "groupId", "logId", "createTime", "timestamp"} {
		if v := str(m[k]); v != "" {
			b.WriteString(k)
			b.WriteString("=")
			b.WriteString(v)
			b.WriteString(";")
		}
	}
	sum := sha256.Sum256([]byte(b.String()))
	return "dy-" + hex.EncodeToString(sum[:])[:16]
}

func eventTimeMs(m map[string]any) int64 {
	if ts := toI64(m["createTime"]); ts > 0 {
		return ts
	}
	return toI64(m["timestamp"])
}

func str(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case float64:
		return fmt.Sprintf("%v", t)
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", t)
	}
}

func toI64(v any) int64 {
	switch t := v.(type) {
	case float64:
		return int64(t)
	case string:
		var n int64
		_, _ = fmt.Sscanf(t, "%d", &n)
		return n
	case bool:
		return 0
	case nil:
		return 0
	default:
		return 0
	}
}

func truthy(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case float64:
		return t != 0
	case string:
		return t == "1" || t == "true"
	default:
		return false
	}
}

func maxI64(vs ...int64) int64 {
	m := vs[0]
	for _, v := range vs[1:] {
		if v > m {
			m = v
		}
	}
	return m
}
