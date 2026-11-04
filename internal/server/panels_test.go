package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"

	"github.com/teamwork/digital-human/internal/biz/action"
	"github.com/teamwork/digital-human/internal/biz/archive"
	"github.com/teamwork/digital-human/internal/biz/billing"
	"github.com/teamwork/digital-human/internal/biz/choreo"
	"github.com/teamwork/digital-human/internal/biz/danmaku"
	"github.com/teamwork/digital-human/internal/biz/koubo"
	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/biz/media"
	"github.com/teamwork/digital-human/internal/biz/refs"
	"github.com/teamwork/digital-human/internal/biz/settings"
	"github.com/teamwork/digital-human/internal/biz/song"
	"github.com/teamwork/digital-human/internal/biz/tts"
)

// startPanelServer 用进程内 fake 依赖起真实 Kratos HTTP 服务，验证路由/JSON/文件下载。
func startPanelServer(t *testing.T) (base string, deps PanelDeps, stop func()) {
	t.Helper()
	root := t.TempDir()
	audioDir := filepath.Join(root, "audio")
	songsDir := filepath.Join(root, "songs")
	kouboDir := filepath.Join(root, "koubo")
	for _, d := range []string{audioDir, songsDir, kouboDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(songsDir, "song.wav"), []byte("song"), 0o644); err != nil {
		t.Fatal(err)
	}

	player := media.NewFakePlayer()
	player.SetDelay(0)
	synth := tts.NewFakeSynth()
	st := settings.NewService(settings.NewMemoryStore())

	remote := action.NewMemoryRemote()
	sched := action.NewScheduler(action.NewFakeSender(), nil, action.DefaultConfig())
	actions := action.NewService(remote, sched)
	chor := choreo.NewService(choreo.NewMemoryStore(choreo.DefaultConfig()), sched)
	sched.SetPauseChoreo(func(d time.Duration) { chor.Pause(d) })
	songs := song.NewService(media.NewLibrary(songsDir), media.FakeDecoder{}, player, nil, func() bool { return true })
	kou := koubo.NewService(koubo.NewMemoryStore(koubo.State{MaxChars: 120}), synth, player, koubo.FakeActionGen{}, kouboDir, func() bool { return true })
	bill := billing.NewService(billing.NewMemoryStore())
	sessions := live.NewMemorySessionStore()
	_ = sessions.Save(context.Background(), live.SessionRecord{ID: "s1", Phase: live.PhaseEnded, StartedAtMs: 1, UpdatedAtMs: 1})
	arch := archive.NewService(sessions, nil, bill)
	dm := danmaku.NewService(danmaku.ModeAuto)

	deps = PanelDeps{
		TTS:     tts.NewService(media.NewLibrary(audioDir), synth, st),
		Actions: actions,
		Choreo:  chor,
		Koubo:   kou,
		Refs:    refs.NewService(refs.NewFakeSignaler(), nil),
		Songs:   songs,
		Archive: arch,
		Billing: bill,
		Danmaku: dm,
	}

	// 预先创建 listener 再交给 Kratos，避免 Endpoint()/Start() 并发创建两个 listener 导致端口错位。
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := khttp.NewServer(khttp.Listener(lis))
	registerPanelRoutes(srv, deps)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	stop = func() {
		cancel()
		_ = srv.Stop(context.Background())
		<-done
	}
	return "http://" + lis.Addr().String(), deps, stop
}

func callJSON(t *testing.T, base, method, path string, body any) (*http.Response, []byte) {
	t.Helper()
	var rdr io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatal(err)
		}
		rdr = bytes.NewReader(raw)
	}
	req, err := http.NewRequest(method, base+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, raw
}

func mustStatus(t *testing.T, resp *http.Response, want int, path string) {
	t.Helper()
	if resp.StatusCode != want {
		t.Fatalf("%s status=%d want %d", path, resp.StatusCode, want)
	}
}

func uploadFile(t *testing.T, base, path, filename string, data []byte) (*http.Response, []byte) {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	fw, err := w.CreateFormFile("file", filename)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fw.Write(data); err != nil {
		t.Fatal(err)
	}
	_ = w.Close()
	req, err := http.NewRequest(http.MethodPost, base+path, &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	return resp, raw
}

func TestPanelCapabilitySourcesUseSharedEnum(t *testing.T) {
	base, _, stop := startPanelServer(t)
	defer stop()

	for _, tc := range []struct {
		path  string
		state string
	}{
		{path: "/api/v1/songs/status", state: "state"},
		{path: "/api/v1/actions/queue", state: "status"},
		{path: "/api/v1/tts/voices", state: ""},
	} {
		resp, raw := callJSON(t, base, http.MethodGet, tc.path, nil)
		mustStatus(t, resp, http.StatusOK, tc.path)
		var body map[string]any
		if err := json.Unmarshal(raw, &body); err != nil {
			t.Fatalf("%s decode: %v", tc.path, err)
		}
		value := body
		if tc.state != "" {
			nested, ok := body[tc.state].(map[string]any)
			if !ok {
				t.Fatalf("%s 缺少 %s 对象: %s", tc.path, tc.state, raw)
			}
			value = nested
		}
		if value["source"] != "fake" {
			t.Fatalf("%s fake 实现必须显式返回 source=fake: %s", tc.path, raw)
		}
	}
}

func TestPanelRoutesHTTPSmoke(t *testing.T) {
	base, _, stop := startPanelServer(t)
	defer stop()

	// ---- TTS ----
	resp, _ := callJSON(t, base, http.MethodGet, "/api/v1/tts/voices", nil)
	mustStatus(t, resp, 200, "/tts/voices")
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/tts/voice", map[string]any{"voice": "dh-bright-f"})
	mustStatus(t, resp, 200, "/tts/voice")
	resp, raw := callJSON(t, base, http.MethodPost, "/api/v1/tts/preview", map[string]any{"text": "你好"})
	mustStatus(t, resp, 200, "/tts/preview")
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "audio/wav") || !strings.HasPrefix(string(raw), "RIFF") {
		t.Fatalf("preview ctype=%q raw=%q", ct, string(raw[:min(4, len(raw))]))
	}
	resp, _ = callJSON(t, base, http.MethodGet, "/api/v1/tts/files", nil)
	mustStatus(t, resp, 200, "/tts/files")
	resp, _ = uploadFile(t, base, "/api/v1/tts/upload", "up.wav", []byte("abc"))
	mustStatus(t, resp, 200, "/tts/upload")
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/tts/delete", map[string]any{"file_name": "up.wav"})
	mustStatus(t, resp, 200, "/tts/delete")
	resp, _ = callJSON(t, base, http.MethodGet, "/api/v1/tts/queue", nil)
	mustStatus(t, resp, 200, "/tts/queue")

	// ---- Actions ----
	_, raw = callJSON(t, base, http.MethodPost, "/api/v1/actions/libraries", map[string]any{"name": "L", "description": "d"})
	var libResp struct {
		Library struct {
			ID string `json:"id"`
		} `json:"library"`
	}
	if err := json.Unmarshal(raw, &libResp); err != nil || libResp.Library.ID == "" {
		t.Fatalf("library resp=%s err=%v", raw, err)
	}
	libID := libResp.Library.ID
	resp, _ = callJSON(t, base, http.MethodGet, "/api/v1/actions/libraries", nil)
	mustStatus(t, resp, 200, "/actions/libraries")
	_, raw = callJSON(t, base, http.MethodPost, "/api/v1/actions", map[string]any{"library_id": libID, "name": "wave", "instruction": "挥手"})
	var actResp struct {
		Action struct {
			ID string `json:"id"`
		} `json:"action"`
	}
	if err := json.Unmarshal(raw, &actResp); err != nil || actResp.Action.ID == "" {
		t.Fatalf("action resp=%s err=%v", raw, err)
	}
	resp, _ = callJSON(t, base, http.MethodGet, "/api/v1/actions?library_id="+libID, nil)
	mustStatus(t, resp, 200, "/actions")
	resp, _ = callJSON(t, base, http.MethodDelete, "/api/v1/actions/"+actResp.Action.ID, nil)
	mustStatus(t, resp, 200, "/actions/{id} delete")
	resp, _ = callJSON(t, base, http.MethodDelete, "/api/v1/actions/libraries/"+libID, nil)
	mustStatus(t, resp, 200, "/actions/libraries/{id} delete")
	resp, _ = callJSON(t, base, http.MethodGet, "/api/v1/actions/queue", nil)
	mustStatus(t, resp, 200, "/actions/queue")
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/actions/send", map[string]any{"text": "wave", "source": "manual"})
	mustStatus(t, resp, 200, "/actions/send")

	// ---- Choreo ----
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/choreo", map[string]any{
		"enabled": false, "step_gap_s": 0.1, "group_gap_s": 0.1,
		"groups": []map[string]any{{"id": "g1", "name": "g", "steps": []map[string]any{{"cmd": "挥手"}}}},
	})
	mustStatus(t, resp, 200, "/choreo save")
	resp, _ = callJSON(t, base, http.MethodGet, "/api/v1/choreo", nil)
	mustStatus(t, resp, 200, "/choreo get")
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/choreo/groups", map[string]any{"id": "g2", "name": "g2", "steps": []map[string]any{{"cmd": "点头"}}})
	mustStatus(t, resp, 200, "/choreo/groups")
	resp, _ = callJSON(t, base, http.MethodDelete, "/api/v1/choreo/groups/g2", nil)
	mustStatus(t, resp, 200, "/choreo/groups delete")
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/choreo/run-once", nil)
	mustStatus(t, resp, 200, "/choreo/run-once")
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/choreo/start", nil)
	mustStatus(t, resp, 200, "/choreo/start")
	resp, _ = callJSON(t, base, http.MethodGet, "/api/v1/choreo/status", nil)
	mustStatus(t, resp, 200, "/choreo/status")
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/choreo/stop", nil)
	mustStatus(t, resp, 200, "/choreo/stop")

	// ---- Koubo (14 routes) ----
	_, raw = callJSON(t, base, http.MethodPost, "/api/v1/koubo/split", map[string]any{"text": "第一句。第二句。第三句。", "max_chars": 20})
	var stateResp struct {
		State koubo.State `json:"state"`
	}
	if err := json.Unmarshal(raw, &stateResp); err != nil || len(stateResp.State.Segments) != 2 {
		t.Fatalf("split resp=%s err=%v", raw, err)
	}
	id1 := stateResp.State.Segments[0].ID
	id2 := stateResp.State.Segments[1].ID
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/koubo/seg", map[string]any{"id": id1, "text": "第一句。"})
	mustStatus(t, resp, 200, "/koubo/seg")
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/koubo/merge", map[string]any{"ids": []string{id1, id2}})
	mustStatus(t, resp, 200, "/koubo/merge")
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/koubo/seg/split", map[string]any{"id": id1, "pos": 3})
	mustStatus(t, resp, 200, "/koubo/seg/split")
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/koubo/tts", map[string]any{"id": id1})
	mustStatus(t, resp, 200, "/koubo/tts")
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/koubo/select", map[string]any{"id": id1, "take": 0})
	mustStatus(t, resp, 200, "/koubo/select")
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/koubo/action", map[string]any{"id": id1})
	mustStatus(t, resp, 200, "/koubo/action")
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/koubo/actions_all", nil)
	mustStatus(t, resp, 200, "/koubo/actions_all")
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/koubo/tts_all", map[string]any{"only_missing": true})
	mustStatus(t, resp, 200, "/koubo/tts_all")
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/koubo/play", map[string]any{"id": id1})
	mustStatus(t, resp, 200, "/koubo/play")
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/koubo/play_all", nil)
	mustStatus(t, resp, 200, "/koubo/play_all")
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/koubo/stop", nil)
	mustStatus(t, resp, 200, "/koubo/stop")
	resp, _ = callJSON(t, base, http.MethodGet, "/api/v1/koubo/state", nil)
	mustStatus(t, resp, 200, "/koubo/state")
	// 取回 TTS 生成的 wav 下载。
	_, raw = callJSON(t, base, http.MethodGet, "/api/v1/koubo/state", nil)
	if err := json.Unmarshal(raw, &stateResp); err == nil {
		for _, seg := range stateResp.State.Segments {
			if len(seg.Takes) > 0 {
				resp, raw = callJSON(t, base, http.MethodGet, "/api/v1/koubo/audio/"+seg.Takes[0].File, nil)
				mustStatus(t, resp, 200, "/koubo/audio/{name}")
				if !strings.HasPrefix(string(raw), "RIFF") {
					t.Fatalf("audio not wav: %q", string(raw[:min(4, len(raw))]))
				}
				break
			}
		}
	}

	// ---- Refs ----
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/prompt", map[string]any{"op_type": "switch", "images": []map[string]any{{"image_id": "a", "url": "u"}}})
	mustStatus(t, resp, 200, "/prompt switch")
	resp, _ = callJSON(t, base, http.MethodGet, "/api/v1/prompt", nil)
	mustStatus(t, resp, 200, "/prompt get")
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/prompt/type14", map[string]any{"error": "rejected"})
	mustStatus(t, resp, 200, "/prompt/type14")
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/prompt", map[string]any{"op_type": "remove"})
	mustStatus(t, resp, 200, "/prompt remove")

	// ---- Songs ----
	resp, _ = uploadFile(t, base, "/api/v1/songs/upload", "up-song.wav", []byte("abc"))
	mustStatus(t, resp, 200, "/songs/upload")
	resp, _ = callJSON(t, base, http.MethodGet, "/api/v1/songs", nil)
	mustStatus(t, resp, 200, "/songs")
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/songs/play", map[string]any{"file_name": "song.wav", "script": "歌词"})
	mustStatus(t, resp, 200, "/songs/play")
	resp, _ = callJSON(t, base, http.MethodGet, "/api/v1/songs/status", nil)
	mustStatus(t, resp, 200, "/songs/status")
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/songs/stop", nil)
	mustStatus(t, resp, 200, "/songs/stop")
	resp, _ = callJSON(t, base, http.MethodDelete, "/api/v1/songs/song.wav", nil)
	mustStatus(t, resp, 200, "/songs/{name} delete")

	// ---- Sessions / Cost / Danmaku ----
	resp, _ = callJSON(t, base, http.MethodGet, "/api/v1/sessions?limit=10", nil)
	mustStatus(t, resp, 200, "/sessions")
	resp, _ = callJSON(t, base, http.MethodGet, "/api/v1/sessions/s1", nil)
	mustStatus(t, resp, 200, "/sessions/{id}")
	resp, raw = callJSON(t, base, http.MethodGet, "/api/v1/sessions/s1/export?format=csv", nil)
	mustStatus(t, resp, 200, "/sessions/{id}/export csv")
	if !strings.HasPrefix(string(raw), "seq,ts_ms,event_id") {
		t.Fatalf("csv=%q", string(raw))
	}
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/cost/ledger", map[string]any{"session_id": "s1", "credits_start": 100, "credits_end": 90})
	mustStatus(t, resp, 200, "/cost/ledger post")
	resp, _ = callJSON(t, base, http.MethodGet, "/api/v1/cost/ledger/s1", nil)
	mustStatus(t, resp, 200, "/cost/ledger get")
	resp, _ = callJSON(t, base, http.MethodGet, "/api/v1/cost/summary?group_by=account", nil)
	mustStatus(t, resp, 200, "/cost/summary")
	resp, _ = callJSON(t, base, http.MethodGet, "/api/v1/danmaku/state", nil)
	mustStatus(t, resp, 200, "/danmaku/state")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
