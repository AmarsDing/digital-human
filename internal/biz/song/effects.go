package song

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/teamwork/digital-human/internal/biz/interaction"
)

// RegisterInteractionHandler 将 song_request effect 持久入队，并只经 Scheduler 触发播放。
func RegisterInteractionHandler(d *interaction.Dispatcher, store RequestEnqueuer, scheduler *Scheduler) {
	d.RegisterHandler(interaction.EffectTypeSongRequest, func(ctx context.Context, eff interaction.Effect) (string, error) {
		if store == nil || scheduler == nil {
			return "", errors.New("song: request queue is not fully wired")
		}
		file, _ := eff.PayloadSafe["file"].(string)
		source, _ := eff.PayloadSafe["source_event_row_id"].(string)
		user, _ := eff.PayloadSafe["user_key"].(string)
		if strings.TrimSpace(file) == "" || strings.TrimSpace(source) == "" || strings.TrimSpace(user) == "" {
			return "", errors.New("song: request payload missing file, source event or user")
		}
		req, _, err := store.Enqueue(ctx, Request{
			SessionID: eff.SessionID, SourceEventRowID: source,
			DecisionID: eff.DecisionID, EffectIdempotencyKey: eff.EffectIdempotencyKey,
			UserKey: user, File: file,
		})
		if err != nil {
			return "", err
		}
		if req.Status != RequestQueued && req.Status != RequestPlaying {
			return "", fmt.Errorf("song: request %s is %s", req.ID, req.Status)
		}
		if _, err := scheduler.DispatchNext(ctx); err != nil {
			return "", err
		}
		return "song_request:" + req.ID, nil
	})
}
