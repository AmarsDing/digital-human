package server

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	livev1 "github.com/teamwork/digital-human/api/gen/go/live/v1"

	"github.com/teamwork/digital-human/internal/biz/danmaku"
	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/biz/rtc"
	"github.com/teamwork/digital-human/internal/service"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

type liveHTTPFixture struct {
	base string
	bus  *eventbus.Bus
	mgr  *live.Manager
	dm   *danmaku.Service
	stop func()
}

// startLiveHTTP 起真实 Kratos HTTP 服务，走完整 handler/编解码/transport context。
func startLiveHTTP(t *testing.T, ctrl live.Controller) *liveHTTPFixture {
	return startLiveHTTPWithRTC(t, ctrl, nil)
}

func startLiveHTTPWithRTC(t *testing.T, ctrl live.Controller, issuer *rtc.Issuer) *liveHTTPFixture {
	t.Helper()
	bus := eventbus.New(64)
	mgr := live.NewManagerWithStores(bus, live.NewMemorySink(64), ctrl, nil,
		live.NewMemoryIdempotencyStore(time.Hour), nil)
	mgr.SetModePolicy(live.ModePolicy{AllowLocalPreview: true, EngineMode: "dev"})
	dm := danmaku.NewService(danmaku.ModeConfirm)
	svc := service.NewLiveService(mgr, nil, dm, issuer)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := khttp.NewServer(khttp.Listener(lis), khttp.Timeout(0))
	livev1.RegisterLiveServiceHTTPServer(srv, svc)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	stop := func() {
		cancel()
		_ = srv.Stop(context.Background())
		<-done
	}
	return &liveHTTPFixture{base: "http://" + lis.Addr().String(), bus: bus, mgr: mgr, dm: dm, stop: stop}
}

func postLive(t *testing.T, base, path, headerKey string, body map[string]any) (*http.Response, map[string]any) {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, base+path, bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if headerKey != "" {
		req.Header.Set("Idempotency-Key", headerKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeBody(t, resp)
	return resp, payload
}

func getLive(t *testing.T, base, path string) (*http.Response, map[string]any) {
	t.Helper()
	resp, err := http.Get(base + path)
	if err != nil {
		t.Fatal(err)
	}
	payload := decodeBody(t, resp)
	return resp, payload
}

func decodeBody(t *testing.T, resp *http.Response) map[string]any {
	t.Helper()
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	var out map[string]any
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatalf("decode %s: %v (body=%s)", resp.Request.URL.Path, err, raw)
		}
	}
	out["_raw"] = string(raw)
	return out
}

func nested(m map[string]any, key string) map[string]any {
	if v, ok := m[key].(map[string]any); ok {
		return v
	}
	return map[string]any{}
}

// 无真实凭据且未开 dev：live/start 必须 503 unavailable，绝不返回 fake-live-*。
func TestLiveStartFailClosedWithoutEngine(t *testing.T) {
	f := startLiveHTTP(t, live.NewUnavailableController("missing_credentials: DH_VIDU_KEY"))
	defer f.stop()

	resp, body := postLive(t, f.base, "/api/v1/live/start", "", map[string]any{"persona_id": "p", "avatar_id": "a"})
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("start without engine: status=%d body=%v", resp.StatusCode, body)
	}
	if body["reason"] != "engine_unavailable" {
		t.Fatalf("reason=%v want engine_unavailable", body["reason"])
	}
	if strings.Contains(body["_raw"].(string), "fake-live-") {
		t.Fatalf("不得返回 fake 场次: %s", body["_raw"])
	}

	_, st := getLive(t, f.base, "/api/v1/live/status")
	// Kratos JSON 走 protojson，字段为 camelCase。
	if st["phase"] != "error" || st["errorCode"] != "engine_not_configured" {
		t.Fatalf("status after failed start: %v", st)
	}
	if raw := st["_raw"].(string); strings.Contains(raw, "fake-live-") {
		t.Fatalf("status 不得出现 fake live_id: %s", raw)
	}
}

// 没有真实引擎时，状态必须显式 unknown/unconfigured/disconnected，不能伪造 owner=true。
func TestLiveStatusDoesNotFakeRuntimeState(t *testing.T) {
	f := startLiveHTTP(t, live.NewUnavailableController("no_engine"))
	defer f.stop()

	resp, st := getLive(t, f.base, "/api/v1/live/status")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if st["phase"] != "idle" {
		t.Fatalf("phase=%v want idle", st["phase"])
	}
	feed := nested(st, "feed")
	if feed["ownerKind"] != "unknown" {
		t.Fatalf("feed.ownerKind=%v want unknown (不得伪造)", feed["ownerKind"])
	}
	if owner, ok := feed["owner"].(bool); ok && owner {
		t.Fatalf("feed.owner must not be true without real engine: %v", feed)
	}
	ws := nested(st, "ws")
	if ws["state"] != "disconnected" {
		t.Fatalf("ws.state=%v want disconnected (真实无连接)", ws["state"])
	}
	rtc := nested(st, "rtc")
	if rtc["mode"] != "unconfigured" {
		t.Fatalf("rtc.mode=%v want unconfigured", rtc["mode"])
	}
}

// Idempotency-Key 头与 body.idempotency_key 必须一致，否则 400。
func TestIdempotencyHeaderBodyMismatchRejected(t *testing.T) {
	f := startLiveHTTP(t, live.NewFakeController(slog.Default()))
	defer f.stop()

	resp, body := postLive(t, f.base, "/api/v1/live/start", "k-header", map[string]any{
		"persona_id": "p", "avatar_id": "a", "idempotency_key": "k-body",
	})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("mismatch: status=%d body=%v", resp.StatusCode, body)
	}
	if body["reason"] != "idempotency_key_mismatch" {
		t.Fatalf("reason=%v want idempotency_key_mismatch", body["reason"])
	}

	// 一致时正常开播，同一头重放返回同一场次。
	resp, body = postLive(t, f.base, "/api/v1/live/start", "k-same", map[string]any{
		"persona_id": "p", "avatar_id": "a", "idempotency_key": "k-same",
	})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("consistent: status=%d body=%v", resp.StatusCode, body)
	}
	sid, _ := body["sessionId"].(string)
	if sid == "" {
		t.Fatalf("missing sessionId: %v", body)
	}
	_, replay := postLive(t, f.base, "/api/v1/live/start", "k-same", map[string]any{
		"persona_id": "p", "avatar_id": "a", "idempotency_key": "k-same",
	})
	if replay["sessionId"] != sid {
		t.Fatalf("replay session=%v want %s", replay["sessionId"], sid)
	}
}

// 只带 Idempotency-Key 头（body 不带）也必须生效：同头重放同一场次。
func TestIdempotencyHeaderAloneDedupes(t *testing.T) {
	f := startLiveHTTP(t, live.NewFakeController(slog.Default()))
	defer f.stop()

	resp, first := postLive(t, f.base, "/api/v1/live/start", "hdr-1", map[string]any{"persona_id": "p", "avatar_id": "a"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("first: status=%d body=%v", resp.StatusCode, first)
	}
	_, second := postLive(t, f.base, "/api/v1/live/start", "hdr-1", map[string]any{"persona_id": "p", "avatar_id": "a"})
	if first["sessionId"] != second["sessionId"] {
		t.Fatalf("same header must replay session: %v vs %v", first["sessionId"], second["sessionId"])
	}
}

// 跨 session 读取状态必须 404，不能把别的场次数据返回。
func TestGetLiveStatusRejectsForeignSession(t *testing.T) {
	f := startLiveHTTP(t, live.NewFakeController(slog.Default()))
	defer f.stop()

	_, body := postLive(t, f.base, "/api/v1/live/start", "", map[string]any{"persona_id": "p", "avatar_id": "a"})
	sid, _ := body["sessionId"].(string)

	resp, st := getLive(t, f.base, "/api/v1/live/status?session_id="+sid)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("own session status=%d body=%v", resp.StatusCode, st)
	}

	resp, st = getLive(t, f.base, "/api/v1/live/status?session_id=foreign")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign session status=%d body=%v", resp.StatusCode, st)
	}
	if st["reason"] != "session_not_found" {
		t.Fatalf("reason=%v want session_not_found", st["reason"])
	}
}

// 弹幕计数按 session 过滤：换场次后旧场次查询返回空统计（不跨场次串数据）。
func TestDanmakuStatsScopedToSession(t *testing.T) {
	dm := danmaku.NewService(danmaku.ModeConfirm)
	dm.BeginSession("s-1")
	dm.Ingest(context.Background(), "chat", "u1", "用户1", "hello", 0, "", false)
	if pending, dropped, t2, ok := dm.StatsFor("s-1"); !ok || pending != 1 || dropped != 0 || t2 != 0 {
		t.Fatalf("s-1 stats: pending=%d dropped=%d t2=%d ok=%v", pending, dropped, t2, ok)
	}
	dm.BeginSession("s-2")
	if _, _, _, ok := dm.StatsFor("s-1"); ok {
		t.Fatal("旧场次必须返回 ok=false")
	}
	if pending, _, _, ok := dm.StatsFor("s-2"); !ok || pending != 0 {
		t.Fatalf("新场次必须从零开始: pending=%d ok=%v", pending, ok)
	}
}

// 未配置 RTC：HTTP 200 但 ok=false/mode=unconfigured，不伪造 token。
func TestRtcTokenUnconfigured(t *testing.T) {
	f := startLiveHTTPWithRTC(t, live.NewFakeController(slog.Default()),
		rtc.NewIssuer(rtc.Config{Mode: "disabled", Provider: "artc"}))
	defer f.stop()

	_, body := postLive(t, f.base, "/api/v1/live/start", "", map[string]any{"persona_id": "p", "avatar_id": "a"})
	sid := body["sessionId"].(string)

	resp, tok := getLive(t, f.base, "/api/v1/live/rtc-token?session_id="+sid)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status=%d body=%v", resp.StatusCode, tok)
	}
	if tok["ok"] != false || tok["mode"] != "unconfigured" || tok["provider"] != "none" {
		t.Fatalf("unconfigured reply=%v", tok)
	}
	if tok["token"] != nil && tok["token"] != "" {
		t.Fatalf("不得返回 token: %v", tok["token"])
	}
	_, st := getLive(t, f.base, "/api/v1/live/status?session_id="+sid)
	if nested(st, "rtc")["mode"] != "unconfigured" {
		t.Fatalf("status.rtc=%v", nested(st, "rtc"))
	}
}

// 有凭据：签发 token/过期时间/room/user id，重复调用可刷新。
func TestRtcTokenIssuedAndRefreshes(t *testing.T) {
	issuer := rtc.NewIssuer(rtc.Config{
		Mode: "dev", Provider: "artc", RoomPrefix: "dh-",
		APIKey: "ak", APISecret: "sk", TokenTTL: 10 * time.Minute,
	})
	f := startLiveHTTPWithRTC(t, live.NewFakeController(slog.Default()), issuer)
	defer f.stop()

	_, body := postLive(t, f.base, "/api/v1/live/start", "", map[string]any{"persona_id": "p", "avatar_id": "a"})
	sid := body["sessionId"].(string)

	_, first := getLive(t, f.base, "/api/v1/live/rtc-token?session_id="+sid)
	if first["ok"] != true || first["mode"] != "managed" || first["provider"] != "artc" {
		t.Fatalf("issued reply=%v", first)
	}
	token, _ := first["token"].(string)
	if !strings.HasPrefix(token, "rtc-dev.") {
		t.Fatalf("dev token prefix=%q", token)
	}
	if first["roomId"] != "dh-"+sid || first["userId"] != "viewer-"+sid {
		t.Fatalf("room/user=%v/%v", first["roomId"], first["userId"])
	}
	expStr, _ := first["expiresAt"].(string)
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || exp <= time.Now().UnixMilli() {
		t.Fatalf("expiresAt=%v err=%v", first["expiresAt"], err)
	}

	// 过期前刷新：再次签发得到新 token，房间/用户不变。
	_, second := getLive(t, f.base, "/api/v1/live/rtc-token?session_id="+sid)
	if second["token"] == token {
		t.Fatalf("refresh should rotate token: %v", second["token"])
	}
	if second["roomId"] != first["roomId"] || second["userId"] != first["userId"] {
		t.Fatalf("refresh must keep room/user: %v", second)
	}

	_, st := getLive(t, f.base, "/api/v1/live/status?session_id="+sid)
	if nested(st, "rtc")["mode"] != "managed" {
		t.Fatalf("status.rtc=%v", nested(st, "rtc"))
	}
}

// managed 模式但没有真实签发器：fail-closed，不得用 dev 凭据冒充。
func TestRtcManagedWithoutIssuerFailsClosed(t *testing.T) {
	f := startLiveHTTPWithRTC(t, live.NewFakeController(slog.Default()),
		rtc.NewIssuer(rtc.Config{Mode: "managed", Provider: "artc"}))
	defer f.stop()

	_, body := postLive(t, f.base, "/api/v1/live/start", "", map[string]any{"persona_id": "p", "avatar_id": "a"})
	sid := body["sessionId"].(string)
	_, tok := getLive(t, f.base, "/api/v1/live/rtc-token?session_id="+sid)
	if tok["ok"] != false || tok["mode"] != "unconfigured" {
		t.Fatalf("managed without issuer must be unavailable: %v", tok)
	}
}

// 跨 session 取 RTC 凭据必须 404。
func TestRtcTokenRejectsForeignSession(t *testing.T) {
	issuer := rtc.NewIssuer(rtc.Config{Mode: "dev", Provider: "artc", APIKey: "ak", APISecret: "sk"})
	f := startLiveHTTPWithRTC(t, live.NewFakeController(slog.Default()), issuer)
	defer f.stop()

	resp, _ := getLive(t, f.base, "/api/v1/live/rtc-token?session_id=foreign")
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("foreign token status=%d", resp.StatusCode)
	}
}

// say → transcript.partial → transcript.final → 列表落库可见。
func TestSayPublishesTranscriptPartialThenFinal(t *testing.T) {
	f := startLiveHTTP(t, live.NewFakeController(slog.Default()))
	defer f.stop()

	_, started := postLive(t, f.base, "/api/v1/live/start", "", map[string]any{"persona_id": "p", "avatar_id": "a"})
	sid := started["sessionId"].(string)
	ch, cancel := f.bus.SubscribeFor(sid, 0)
	defer cancel()

	resp, said := postLive(t, f.base, "/api/v1/live/say", "", map[string]any{"text": "你好，世界", "mode": "sentence"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("say status=%d body=%v", resp.StatusCode, said)
	}

	var partial, final eventbus.Event
	deadline := time.After(3 * time.Second)
	for partial.Type == "" || final.Type == "" {
		select {
		case e := <-ch:
			switch e.Type {
			case "transcript.partial":
				partial = e
			case "transcript.final":
				final = e
			}
		case <-deadline:
			t.Fatalf("missing transcript events: partial=%v final=%v", partial.Type, final.Type)
		}
	}
	if partial.Seq >= final.Seq {
		t.Fatalf("partial seq=%d must precede final seq=%d", partial.Seq, final.Seq)
	}
	if partial.Payload["text"] != "你好，世界" || final.Payload["text"] != "你好，世界" {
		t.Fatalf("payload text: %v / %v", partial.Payload, final.Payload)
	}
	if id, _ := final.Payload["utterance_id"].(string); id == "" {
		t.Fatalf("final must carry utterance_id: %v", final.Payload)
	}

	// 列表落库可见（同一 session 过滤）。
	_, list := getLive(t, f.base, "/api/v1/live/events?session_id="+sid+"&after_seq=0&limit=200")
	seen := map[string]map[string]any{}
	rows, _ := list["events"].([]any)
	for _, row := range rows {
		m, _ := row.(map[string]any)
		typ, _ := m["type"].(string)
		if typ != "transcript.partial" && typ != "transcript.final" {
			continue
		}
		raw, _ := m["payloadJson"].(string)
		b, _ := base64.StdEncoding.DecodeString(raw)
		var p map[string]any
		_ = json.Unmarshal(b, &p)
		seen[typ] = p
	}
	if seen["transcript.partial"] == nil || seen["transcript.final"] == nil {
		t.Fatalf("transcript events not persisted: %v", seen)
	}

	_, st := getLive(t, f.base, "/api/v1/live/status?session_id="+sid)
	tr, _ := st["transcripts"].([]any)
	found := false
	for _, v := range tr {
		if v == "你好，世界" {
			found = true
		}
	}
	if !found {
		t.Fatalf("status transcripts=%v", st["transcripts"])
	}
}

// REST 列表与 WS 广播同源：song/refs/action 的 PublishScoped 事件也必须进同一 sink，
// 不能只出现在 WS backlog。
func TestRESTListAndWSBroadcastShareSource(t *testing.T) {
	f := startLiveHTTP(t, live.NewFakeController(slog.Default()))
	defer f.stop()

	_, started := postLive(t, f.base, "/api/v1/live/start", "", map[string]any{"persona_id": "p", "avatar_id": "a"})
	sid := started["sessionId"].(string)
	ch, cancel := f.bus.SubscribeFor(sid, 0)
	defer cancel()

	ctx := context.Background()
	published := map[string]int64{}
	f.bus.PublishScoped(ctx, "song.playing", map[string]any{"file": "a.mp3"})
	f.bus.PublishScoped(ctx, "refs.mounted", map[string]any{"type": "ref"})
	f.bus.PublishScoped(ctx, "action.sent", map[string]any{"text": "wave"})

	deadline := time.After(3 * time.Second)
	for len(published) < 3 {
		select {
		case e := <-ch:
			if e.Type == "song.playing" || e.Type == "refs.mounted" || e.Type == "action.sent" {
				published[e.Type] = e.Seq
			}
		case <-deadline:
			t.Fatalf("WS 未广播全部业务事件: %v", published)
		}
	}

	// REST 列表必须给出同一 seq（同源验证）。
	_, list := getLive(t, f.base, "/api/v1/live/events?session_id="+sid+"&after_seq=0&limit=200")
	rows, _ := list["events"].([]any)
	rest := map[string]int64{}
	for _, row := range rows {
		m, _ := row.(map[string]any)
		typ, _ := m["type"].(string)
		if _, want := published[typ]; !want {
			continue
		}
		raw, _ := m["seq"].(string)
		seq, _ := strconv.ParseInt(raw, 10, 64)
		rest[typ] = seq
	}
	for typ, seq := range published {
		if rest[typ] != seq {
			t.Fatalf("%s REST seq=%d WS seq=%d（不同源）", typ, rest[typ], seq)
		}
	}
}

// live./song./refs./action. 全部带同一场次 epoch；换场次后 epoch 变化且 seq 从 1 重排。
func TestAllEventTypesShareSessionEpoch(t *testing.T) {
	f := startLiveHTTP(t, live.NewFakeController(slog.Default()))
	defer f.stop()

	_, started := postLive(t, f.base, "/api/v1/live/start", "", map[string]any{"persona_id": "p", "avatar_id": "a"})
	sid1 := started["sessionId"].(string)
	ctx := context.Background()
	f.bus.PublishScoped(ctx, "song.playing", map[string]any{"song_id": "s1"})
	f.bus.PublishScoped(ctx, "refs.switched", map[string]any{"ref_id": "r1"})
	f.bus.PublishScoped(ctx, "action.triggered", map[string]any{"action": "wave"})

	_, list1 := getLive(t, f.base, "/api/v1/live/events?session_id="+sid1+"&after_seq=0&limit=200")
	rows1, _ := list1["events"].([]any)
	if len(rows1) == 0 {
		t.Fatal("no events for s1")
	}
	var wantEpoch string
	seenTypes := map[string]bool{}
	for _, row := range rows1 {
		m, _ := row.(map[string]any)
		typ, _ := m["type"].(string)
		seenTypes[typ] = true
		if m["sessionId"] != sid1 {
			t.Fatalf("串场次: %v", m)
		}
		if wantEpoch == "" {
			wantEpoch, _ = m["epoch"].(string)
		} else if m["epoch"] != wantEpoch {
			t.Fatalf("epoch mismatch: %v vs %s", m["epoch"], wantEpoch)
		}
	}
	// 三类业务事件必须都进了 REST 列表（与 WS 同源），而不是只活在 bus 内存。
	for _, typ := range []string{"song.playing", "refs.switched", "action.triggered"} {
		if !seenTypes[typ] {
			t.Fatalf("REST 列表缺少 %s: %v", typ, seenTypes)
		}
	}

	// 不同 key 重开 → 新场次新 epoch，seq 从 1 开始。
	_, restarted := postLive(t, f.base, "/api/v1/live/start", "k2", map[string]any{"persona_id": "p", "avatar_id": "a"})
	sid2 := restarted["sessionId"].(string)
	if sid2 == sid1 {
		t.Fatalf("restart must create new session")
	}
	f.bus.PublishScoped(ctx, "action.triggered", map[string]any{"action": "nod"})
	_, list2 := getLive(t, f.base, "/api/v1/live/events?session_id="+sid2+"&after_seq=0&limit=200")
	rows2, _ := list2["events"].([]any)
	if len(rows2) == 0 {
		t.Fatal("no events for s2")
	}
	first2, _ := rows2[0].(map[string]any)
	if first2["epoch"] == wantEpoch {
		t.Fatalf("new session must have new epoch, both=%v", first2["epoch"])
	}
	if first2["seq"] != "1" {
		t.Fatalf("new session seq must restart at 1, got %v", first2["seq"])
	}
	for _, row := range rows2 {
		m, _ := row.(map[string]any)
		if m["epoch"] != first2["epoch"] {
			t.Fatalf("s2 epoch mismatch: %v", m)
		}
	}
}
