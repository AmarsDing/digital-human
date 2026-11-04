package koubo

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/teamwork/digital-human/internal/biz/choreo"
	"github.com/teamwork/digital-human/internal/llm"
)

// ActionStreamer 是动作计划所需的 LLM 流式端口。
type ActionStreamer interface {
	Stream(ctx context.Context, messages []llm.Message) (<-chan llm.Sentence, error)
}

type actionGroup struct {
	name     string
	commands []string
}

func loadActionGroups(store choreo.Store) ([]actionGroup, error) {
	if store == nil {
		return nil, errors.New("koubo_actiongen_unconfigured: choreo 内容包未配置")
	}
	cfg, err := store.Load()
	if err != nil {
		return nil, err
	}
	groups := make([]actionGroup, 0, len(cfg.Groups))
	for _, group := range cfg.Groups {
		plan := actionGroup{name: strings.TrimSpace(group.Name)}
		for _, step := range group.Steps {
			if cmd := strings.TrimSpace(step.Cmd); cmd != "" {
				plan.commands = append(plan.commands, cmd)
			}
		}
		if len(plan.commands) > 0 {
			groups = append(groups, plan)
		}
	}
	if len(groups) == 0 {
		return nil, errors.New("koubo_actiongen_unconfigured: choreo 内容包没有可用动作")
	}
	return groups, nil
}

func joinActionPlan(commands []string) string { return strings.Join(commands, " -> ") }

// RuleActionGen 无 LLM 时按口播关键词和 choreo 顺序生成确定的动作计划。
type RuleActionGen struct{ store choreo.Store }

func NewRuleActionGen(store choreo.Store) *RuleActionGen { return &RuleActionGen{store: store} }

func (g *RuleActionGen) Generate(_ context.Context, text string) (string, error) {
	groups, err := loadActionGroups(g.store)
	if err != nil {
		return "", err
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", errors.New("口播文本为空")
	}
	keywords := []string{"感谢", "谢谢", "礼物", "欢迎", "观众", "点赞", "关注", "微笑", "点头", "鞠躬", "比心", "挥手", "再见"}
	selected := groups[0]
	for _, group := range groups {
		candidate := group.name + " " + strings.Join(group.commands, " ")
		matched := false
		for _, keyword := range keywords {
			if strings.Contains(text, keyword) && strings.Contains(candidate, keyword) {
				selected = group
				matched = true
				break
			}
		}
		if matched {
			break
		}
	}
	return joinActionPlan(selected.commands), nil
}

// LLMActionGen 配置 LLM 时使用 choreo 动作作为封闭候选集；失败或越界输出均直接报错。
type LLMActionGen struct {
	store    choreo.Store
	streamer ActionStreamer
}

func NewLLMActionGen(store choreo.Store, streamer ActionStreamer) *LLMActionGen {
	return &LLMActionGen{store: store, streamer: streamer}
}

func (g *LLMActionGen) Generate(ctx context.Context, text string) (string, error) {
	groups, err := loadActionGroups(g.store)
	if err != nil {
		return "", err
	}
	if g.streamer == nil {
		return "", errors.New("koubo_actiongen_unconfigured: LLM 客户端未配置")
	}
	allowed := map[string]bool{}
	commands := make([]string, 0)
	for _, group := range groups {
		for _, cmd := range group.commands {
			if !allowed[cmd] {
				allowed[cmd] = true
				commands = append(commands, cmd)
			}
		}
	}
	stream, err := g.streamer.Stream(ctx, []llm.Message{
		{Role: "system", Content: "根据口播文本生成动作计划。只能使用这些动作：" + strings.Join(commands, "、") + "。按执行顺序用 -> 分隔，不要输出解释。"},
		{Role: "user", Content: strings.TrimSpace(text)},
	})
	if err != nil {
		return "", err
	}
	var out strings.Builder
	for sentence := range stream {
		if sentence.Err != nil {
			return "", sentence.Err
		}
		out.WriteString(sentence.Text)
	}
	parts := strings.Split(strings.TrimSpace(out.String()), "->")
	plan := make([]string, 0, len(parts))
	for _, part := range parts {
		cmd := strings.TrimSpace(part)
		if !allowed[cmd] {
			return "", fmt.Errorf("LLM 返回 choreo 外动作: %q", cmd)
		}
		plan = append(plan, cmd)
	}
	if len(plan) == 0 {
		return "", errors.New("LLM 未返回动作计划")
	}
	return joinActionPlan(plan), nil
}
