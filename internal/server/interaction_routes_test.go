package server

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	"github.com/teamwork/digital-human/internal/biz/contentpkg"
	"github.com/teamwork/digital-human/internal/biz/interaction"
	"github.com/teamwork/digital-human/internal/biz/song"
)

func startInteractionRouteServer(t *testing.T, deps PanelDeps) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := khttp.NewServer(khttp.Listener(lis))
	registerPanelRoutes(srv, deps)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Stop(context.Background())
		<-done
	})
	return "http://" + lis.Addr().String()
}

func TestContentDraftApplyRoute(t *testing.T) {
	ctx := context.Background()
	svc := contentpkg.NewServiceWithFileStores(contentpkg.NewMemoryStore(), map[string]contentpkg.FileStore{
		"koubo": contentpkg.NewMemoryFileStore(), "choreo": contentpkg.NewMemoryFileStore(), "refs": contentpkg.NewMemoryFileStore(),
	})
	draft, err := svc.CreateDraft(ctx, "tpl-1", 1, 1, map[string]any{"bgm": "a"})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := svc.OpenEdit(ctx, draft.ID, "owner-1", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	base := startInteractionRouteServer(t, PanelDeps{Contentpkg: svc})
	resp, raw := callJSON(t, base, http.MethodPost, "/api/v1/live-drafts/"+draft.ID+"/apply", map[string]any{
		"owner_id": "owner-1", "fencing_token": lease.FencingToken,
		"expected_revision": lease.Revision, "preflight_id": "pf-1",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("apply status=%d body=%s", resp.StatusCode, raw)
	}
	var body struct {
		OK      bool               `json:"ok"`
		Journal contentpkg.Journal `json:"journal"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatal(err)
	}
	if !body.OK || body.Journal.Status != contentpkg.JournalCommitted || body.Journal.DraftID != draft.ID {
		t.Fatalf("apply response=%s", raw)
	}
}

func TestSongRequestManagementRoutes(t *testing.T) {
	ctx := context.Background()
	store := song.NewMemoryRequestStore(song.QueuePolicy{MaxQueue: 10}, time.Now)
	newRequest := func(source string) song.Request {
		t.Helper()
		req, _, err := store.Enqueue(ctx, song.Request{
			SessionID: "s1", SourceEventRowID: source, UserKey: "user-" + source,
			File: source + ".wav", Status: song.RequestPendingConfirm,
		})
		if err != nil {
			t.Fatal(err)
		}
		return req
	}
	approve := newRequest("approve")
	reject := newRequest("reject")
	cancel := newRequest("cancel")
	base := startInteractionRouteServer(t, PanelDeps{SongRequests: store})

	resp, raw := callJSON(t, base, http.MethodGet, "/api/v1/song-requests", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d body=%s", resp.StatusCode, raw)
	}
	var listed struct {
		Requests []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"requests"`
		Count int `json:"count"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil || listed.Count != 3 || len(listed.Requests) != 3 || listed.Requests[0].ID == "" {
		t.Fatalf("list response=%s err=%v", raw, err)
	}
	for _, tc := range []struct {
		id     string
		action string
		want   string
	}{{approve.ID, "approve", song.RequestQueued}, {reject.ID, "reject", song.RequestRejected}, {cancel.ID, "cancel", song.RequestCancelled}} {
		resp, raw = callJSON(t, base, http.MethodPost, "/api/v1/song-requests/"+tc.id+"/"+tc.action, map[string]any{})
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", tc.action, resp.StatusCode, raw)
		}
		got, ok, err := store.Get(ctx, tc.id)
		if err != nil || !ok || got.Status != tc.want {
			t.Fatalf("%s result=%+v ok=%v err=%v", tc.action, got, ok, err)
		}
	}
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/song-requests/"+approve.ID+"/approve", map[string]any{})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("重复 approve 必须 409，实际=%d", resp.StatusCode)
	}
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/song-requests/missing/cancel", map[string]any{})
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在请求必须 404，实际=%d", resp.StatusCode)
	}
}

func TestUnknownEffectListAndReconcileRoutes(t *testing.T) {
	ctx := context.Background()
	store := interaction.NewMemoryStore()
	_, effects, err := interaction.NewService(store).Decide(ctx, interaction.DecisionInput{
		SessionID: "s1", SourceEventRowID: "src-route-unknown", Intent: interaction.IntentChatReply,
		Effects: []interaction.EffectInput{{EffectType: interaction.EffectTypeChatReply, TargetID: "speech", Seq: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	effectID := effects[0].ID
	if ok, err := store.MarkDispatching(ctx, effectID, "owner", "dispatch-1", time.Now().Add(time.Minute).UnixMilli(), 1); err != nil || !ok {
		t.Fatalf("mark dispatching ok=%v err=%v", ok, err)
	}
	if err := store.MarkUnknown(ctx, effectID, "receipt_timeout", 2); err != nil {
		t.Fatal(err)
	}
	dispatcher, err := interaction.NewDispatcher(store, time.Minute, time.Minute, nil)
	if err != nil {
		t.Fatal(err)
	}
	base := startInteractionRouteServer(t, PanelDeps{Effects: store, InteractionDispatcher: dispatcher})
	resp, raw := callJSON(t, base, http.MethodGet, "/api/v1/effects?status=unknown", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("list status=%d body=%s", resp.StatusCode, raw)
	}
	var listed struct {
		Effects []interaction.Effect `json:"effects"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		t.Fatal(err)
	}
	if len(listed.Effects) != 1 || listed.Effects[0].ID != effectID {
		t.Fatalf("unknown effects=%s", raw)
	}
	resp, raw = callJSON(t, base, http.MethodPost, "/api/v1/effects/"+effectID+"/reconcile", map[string]any{
		"action": "succeeded", "receipt_id": "receipt-real", "reason": "confirmed_downstream",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("reconcile status=%d body=%s", resp.StatusCode, raw)
	}
	got, ok := store.GetEffect(effectID)
	if !ok || got.Status != interaction.EffectReconcileSucceeded || got.DownstreamReceiptID != "receipt-real" {
		t.Fatalf("reconciled effect=%+v ok=%v", got, ok)
	}
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/effects/"+effectID+"/reconcile", map[string]any{"action": "failed"})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("second reconcile must conflict, status=%d", resp.StatusCode)
	}
}
