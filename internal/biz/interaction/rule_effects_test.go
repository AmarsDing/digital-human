package interaction

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/action"
	"github.com/teamwork/digital-human/internal/biz/chat"
	"github.com/teamwork/digital-human/internal/llm"
)

type replyStreamer struct {
	messages []llm.Message
}

func (s *replyStreamer) Stream(_ context.Context, messages []llm.Message) (<-chan llm.Sentence, error) {
	s.messages = append([]llm.Message(nil), messages...)
	out := make(chan llm.Sentence, 1)
	out <- llm.Sentence{Text: "收到，你好"}
	close(out)
	return out, nil
}

type replySpeaker struct{ texts []string }

func (s *replySpeaker) Say(_ context.Context, text string) error {
	s.texts = append(s.texts, text)
	return nil
}

type actionCommandProbe struct {
	scheduler *action.Scheduler
	source    string
	user      string
}

func (p *actionCommandProbe) Command(ctx context.Context, text, source, user string) (action.Result, error) {
	p.source, p.user = source, user
	return p.scheduler.Command(ctx, text, source, user)
}

func decideHandlerEffect(t *testing.T, st *MemoryStore, source, effectType string, payload map[string]any) Effect {
	t.Helper()
	_, effects, err := NewService(st).Decide(context.Background(), DecisionInput{
		SessionID: "s1", SourceEventRowID: source, Intent: effectType,
		Outcome: DecideExecute, ReasonCode: "rule_test",
		Effects: []EffectInput{{
			EffectType: effectType, TargetID: effectType, Seq: 1, PayloadSafe: payload,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(effects) != 1 {
		t.Fatalf("effect count=%d", len(effects))
	}
	return effects[0]
}

func TestChatReplyEffectUsesChatManagerAndSpeaker(t *testing.T) {
	st := NewMemoryStore()
	disp, err := NewDispatcher(st, time.Second, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	streamer := &replyStreamer{}
	speaker := &replySpeaker{}
	RegisterChatReplyHandler(disp, chat.NewManager(streamer, nil), speaker)
	created := decideHandlerEffect(t, st, "src-chat-effect", EffectTypeChatReply, map[string]any{"text": "你好"})

	if ok, err := disp.DispatchOnce(context.Background()); err != nil || !ok {
		t.Fatalf("dispatch chat: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(speaker.texts, []string{"收到，你好"}) {
		t.Fatalf("LLM 回复必须经现有 Speaker 出口: %+v", speaker.texts)
	}
	if len(streamer.messages) == 0 || streamer.messages[len(streamer.messages)-1].Content != "你好" {
		t.Fatalf("chat manager 输入: %+v", streamer.messages)
	}
	got, _ := st.GetEffect(created.ID)
	if got.Status != EffectSucceeded || got.DownstreamReceiptID == "" {
		t.Fatalf("chat effect 回执: %+v", got)
	}
}

func TestActionEffectUsesExistingScheduler(t *testing.T) {
	st := NewMemoryStore()
	disp, err := NewDispatcher(st, time.Second, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	sender := action.NewFakeSender()
	probe := &actionCommandProbe{scheduler: action.NewScheduler(sender, nil, action.DefaultConfig())}
	RegisterActionHandler(disp, probe)
	created := decideHandlerEffect(t, st, "src-action-effect", EffectTypeAction, map[string]any{
		"instruction": "dance-01", "user_key": "u1",
	})

	if ok, err := disp.DispatchOnce(context.Background()); err != nil || !ok {
		t.Fatalf("dispatch action: ok=%v err=%v", ok, err)
	}
	if probe.source != "auto" || probe.user != "u1" {
		t.Fatalf("弹幕动作必须经 auto Scheduler: source=%q user=%q", probe.source, probe.user)
	}
	if got := sender.Sent(); !reflect.DeepEqual(got, []string{"dance-01"}) {
		t.Fatalf("动作真实出口调用: %+v", got)
	}
	got, _ := st.GetEffect(created.ID)
	if got.Status != EffectSucceeded || got.DownstreamReceiptID == "" {
		t.Fatalf("action effect 回执: %+v", got)
	}
}
