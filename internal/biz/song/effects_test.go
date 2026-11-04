package song

import (
	"context"
	"testing"
	"time"

	"github.com/teamwork/digital-human/internal/biz/interaction"
)

func TestSongRequestEffectEnqueuesAndUsesScheduler(t *testing.T) {
	player := newGatePlayer()
	songs := newQueueService(t, player, "a.wav")
	requests := NewMemoryRequestStore(QueuePolicy{MaxQueue: 10}, time.Now)
	scheduler := NewScheduler(requests, songs, time.Now)

	st := interaction.NewMemoryStore()
	disp, err := interaction.NewDispatcher(st, time.Second, time.Second, nil)
	if err != nil {
		t.Fatal(err)
	}
	RegisterInteractionHandler(disp, requests, scheduler)
	decision, effects, err := interaction.NewService(st).Decide(context.Background(), interaction.DecisionInput{
		SessionID: "s1", SourceEventRowID: "src-song-effect", Intent: interaction.IntentSongRequest,
		Outcome: interaction.DecideQueued, ReasonCode: "song_rule_matched",
		Effects: []interaction.EffectInput{{
			EffectType: interaction.EffectTypeSongRequest, TargetID: "song", Seq: 1,
			PayloadSafe: map[string]any{
				"file": "a.wav", "source_event_row_id": "src-song-effect", "user_key": "u1",
			},
		}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if ok, err := disp.DispatchOnce(context.Background()); err != nil || !ok {
		t.Fatalf("dispatch song_request: ok=%v err=%v", ok, err)
	}
	if got := waitStarted(t, player); got != "a.wav" {
		t.Fatalf("点歌 effect 未进入唯一 Scheduler: %s", got)
	}
	list, err := requests.List(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("同源 effect 只能创建一个请求: %+v", list)
	}
	req := list[0]
	if req.SourceEventRowID != "src-song-effect" || req.UserKey != "u1" || req.DecisionID != decision.ID || req.EffectIdempotencyKey != effects[0].EffectIdempotencyKey {
		t.Fatalf("点歌请求幂等元数据: %+v", req)
	}
	got, _ := st.GetEffect(effects[0].ID)
	if got.Status != interaction.EffectSucceeded || got.DownstreamReceiptID == "" {
		t.Fatalf("song_request effect 回执: %+v", got)
	}
	player.release <- struct{}{}
	waitRequestStatus(t, requests, req.ID, RequestDone)
}
