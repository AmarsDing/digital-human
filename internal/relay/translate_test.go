package relay

import (
	"testing"
)

func TestTranslateChat(t *testing.T) {
	out := Translate(map[string]any{
		"method":  "WebcastChatMessage",
		"user":    map[string]any{"nickname": " 小明 ", "id": "u1"},
		"content": " 主播好 ",
		"msgId":   "m1",
	})
	if out == nil || out.Kind != "chat" || out.UserName != "小明" || out.Text != "主播好" {
		t.Fatalf("chat: %+v", out)
	}
	if out.Source != "douyin" || out.IdempotencyKey == "" {
		t.Fatalf("chat meta: %+v", out)
	}
}

func TestTranslateGiftComboAntiDuplicate(t *testing.T) {
	base := map[string]any{
		"method":  "WebcastGiftMessage",
		"user":    map[string]any{"nickname": "老板", "id": "u9"},
		"gift":    map[string]any{"name": "小心心", "diamondCount": float64(1)},
		"groupId": "g1", "repeatCount": float64(1),
	}
	// 连击中帧：不转发
	if out := Translate(base); out != nil {
		t.Fatalf("连击中帧应过滤: %+v", out)
	}
	// repeatEnd 帧：转发，数量取最大
	end := map[string]any{
		"method":  "WebcastGiftMessage",
		"user":    map[string]any{"nickname": "老板", "id": "u9"},
		"gift":    map[string]any{"name": "小心心", "diamondCount": float64(1)},
		"groupId": "g1", "repeatEnd": true, "repeatCount": float64(3), "comboCount": float64(5),
	}
	out := Translate(end)
	if out == nil || out.Kind != "gift" || out.Text != "小心心 x5" || out.Diamond != 1 {
		t.Fatalf("repeatEnd: %+v", out)
	}
	// 非连击礼物（无 groupId）：直接转发
	single := map[string]any{
		"method": "WebcastGiftMessage",
		"user":   map[string]any{"nickname": "老板"},
		"gift":   map[string]any{"name": "玫瑰", "diamondCount": float64(2)},
		"count":  float64(2),
	}
	out = Translate(single)
	if out == nil || out.Text != "玫瑰 x2" {
		t.Fatalf("非连击: %+v", out)
	}
}

func TestTranslateBindingGift(t *testing.T) {
	out := Translate(map[string]any{
		"method": "WebcastBindingGiftMessage",
		"msg": map[string]any{
			"user":  map[string]any{"nickname": "绑礼用户"},
			"gift":  map[string]any{"name": "灯牌", "diamondCount": float64(3)},
			"count": float64(1), "msgId": "b1",
		},
	})
	if out == nil || out.Kind != "gift" || out.UserName != "绑礼用户" || out.Diamond != 3 {
		t.Fatalf("binding: %+v", out)
	}
}

func TestTranslateJoinAndSystem(t *testing.T) {
	out := Translate(map[string]any{
		"method": "WebcastMemberMessage",
		"user":   map[string]any{"nickname": "路人", "id": "u2"},
	})
	if out == nil || out.Kind != "join" {
		t.Fatalf("join: %+v", out)
	}
	if Translate(map[string]any{"type": "system"}) != nil {
		t.Fatal("system 应过滤")
	}
	if Translate(nil) != nil {
		t.Fatal("nil 应过滤")
	}
	// 缺昵称/内容 → nil
	if Translate(map[string]any{"method": "WebcastChatMessage", "content": "x"}) != nil {
		t.Fatal("缺昵称应过滤")
	}
}

func TestMsgKeyStable(t *testing.T) {
	m := map[string]any{"method": "WebcastChatMessage", "user": map[string]any{"id": "u"}, "msgId": "m1"}
	if msgKey(m) != msgKey(m) {
		t.Fatal("幂等键不稳定")
	}
	m2 := map[string]any{"method": "WebcastChatMessage", "user": map[string]any{"id": "u"}, "msgId": "m2"}
	if msgKey(m) == msgKey(m2) {
		t.Fatal("不同消息幂等键相同")
	}
}
