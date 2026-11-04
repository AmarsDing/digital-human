package interaction

import (
	"context"
	"errors"
	"strings"

	"github.com/teamwork/digital-human/internal/biz/action"
	"github.com/teamwork/digital-human/internal/biz/chat"
)

// ChatChatter 是现有 chat.Manager 的对话端口。
type ChatChatter interface {
	Chat(ctx context.Context, key, userText string, sink chat.SentenceSink) (string, error)
}

// ChatSpeaker 是现有口播出口；每句 LLM 回复都经它发送。
type ChatSpeaker interface {
	Say(ctx context.Context, text string) error
}

// ActionCommander 由现有 action.Scheduler 实现，禁止 effect 旁路 Sender。
type ActionCommander interface {
	Command(ctx context.Context, text, source, user string) (action.Result, error)
}

// RegisterChatReplyHandler 将普通聊天 effect 接到现有 Chat → Speaker 链。
func RegisterChatReplyHandler(d *Dispatcher, chatter ChatChatter, speaker ChatSpeaker) {
	d.RegisterHandler(EffectTypeChatReply, func(ctx context.Context, eff Effect) (string, error) {
		if chatter == nil || speaker == nil {
			return "", errors.New("interaction: chat reply is not fully wired")
		}
		text, _ := eff.PayloadSafe["text"].(string)
		if strings.TrimSpace(text) == "" {
			return "", errors.New("interaction: chat_reply payload missing text")
		}
		if _, err := chatter.Chat(ctx, eff.SessionID, text, speaker.Say); err != nil {
			return "", err
		}
		return "chat:" + eff.DispatchID, nil
	})
}

// RegisterActionHandler 将白名单动作 effect 接到现有 Scheduler 的 auto 入口。
func RegisterActionHandler(d *Dispatcher, scheduler ActionCommander) {
	d.RegisterHandler(EffectTypeAction, func(ctx context.Context, eff Effect) (string, error) {
		if scheduler == nil {
			return "", errors.New("interaction: action scheduler is not wired")
		}
		instruction, _ := eff.PayloadSafe["instruction"].(string)
		if strings.TrimSpace(instruction) == "" {
			return "", errors.New("interaction: action payload missing instruction")
		}
		user, _ := eff.PayloadSafe["user_key"].(string)
		result, err := scheduler.Command(ctx, instruction, "auto", user)
		if err != nil {
			return "", err
		}
		if !result.OK {
			return "", errors.New("interaction: action scheduler rejected command")
		}
		return "action:" + eff.DispatchID, nil
	})
}
