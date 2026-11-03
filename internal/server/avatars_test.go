package server

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"

	"github.com/teamwork/digital-human/internal/biz/avatar"
	"github.com/teamwork/digital-human/internal/engine/vidu"
)

// startAvatarServer 只挂档案路由（内存存储 + 临时图片目录），验证 M2 11 RPC + 图片路由。
func startAvatarServer(t *testing.T, store *avatar.MemoryAvatarStore, images *avatar.ImageService) (string, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := khttp.NewServer(khttp.Listener(lis))
	registerPanelRoutes(srv, PanelDeps{
		Avatars:      avatar.NewService(store),
		AvatarImages: images,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	stop := func() {
		cancel()
		_ = srv.Stop(context.Background())
		<-done
	}
	return "http://" + lis.Addr().String(), stop
}

func testPNG(t *testing.T, w, h int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func uploadMap(t *testing.T, base, path, filename string, content []byte) (*http.Response, map[string]any) {
	t.Helper()
	resp, raw := uploadFile(t, base, path, filename, content)
	return resp, decodeMap(t, raw)
}

// TestAvatarRoutesEndToEnd M2 验收（A1/A3/A10/A11/A16 主线）：
// CRUD → CAS → duplicate → activate → soft delete/slug 复用 → 上传/dedup → refs ≤3 → 图片读取。
func TestAvatarRoutesEndToEnd(t *testing.T) {
	store := avatar.NewMemoryStore()
	images := avatar.NewImageService(store, t.TempDir())
	base, stop := startAvatarServer(t, store, images)
	defer stop()

	// 空库列表：storage=memory。
	resp, raw := callJSON(t, base, http.MethodGet, "/api/v1/avatars", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list = %d %s", resp.StatusCode, raw)
	}
	m := decodeMap(t, raw)
	if m["storage"] != "memory" || m["total"].(float64) != 0 {
		t.Fatalf("空库 list = %v", m)
	}

	// 校验失败（§9.1）：空 name / 非法 slug → 400 validation_failed。
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/avatars", map[string]any{"name": ""})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("空 name = %d", resp.StatusCode)
	}
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/avatars", map[string]any{"name": "小雅", "slug": "BAD_SLUG"})
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("非法 slug = %d", resp.StatusCode)
	}

	// 创建 → 201，revision=1，slug 由 name 派生。
	resp, raw = callJSON(t, base, http.MethodPost, "/api/v1/avatars", map[string]any{
		"name": "小雅", "system_prompt": "你是小雅", "greeting": "你好", "voice_id": "voice-a",
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d %s", resp.StatusCode, raw)
	}
	created := decodeMap(t, raw)["avatar"].(map[string]any)
	id := created["persona_id"].(string)
	if created["revision"].(float64) != 1 || created["slug"] == "" || created["is_active"].(bool) {
		t.Fatalf("create 响应 = %v", created)
	}

	// slug 冲突 → 409 slug_conflict。
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/avatars", map[string]any{
		"name": "小雅二号", "slug": created["slug"],
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("slug 冲突 = %d", resp.StatusCode)
	}

	// Get 详情：refs 空数组。
	resp, raw = callJSON(t, base, http.MethodGet, "/api/v1/avatars/"+id, nil)
	if resp.StatusCode != 200 {
		t.Fatalf("get = %d %s", resp.StatusCode, raw)
	}
	detail := decodeMap(t, raw)["avatar"].(map[string]any)
	if detail["refs"] == nil || len(detail["refs"].([]any)) != 0 {
		t.Fatalf("detail refs = %v", detail["refs"])
	}

	// CAS：错 revision → 409 revision_conflict；对 revision → 200 revision=2。
	resp, _ = callJSON(t, base, http.MethodPut, "/api/v1/avatars/"+id, map[string]any{
		"name": "小雅改", "revision": 99,
	})
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("CAS 撞 = %d", resp.StatusCode)
	}
	resp, raw = callJSON(t, base, http.MethodPut, "/api/v1/avatars/"+id, map[string]any{
		"name": "小雅改", "greeting": "改了", "revision": 1,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("update = %d %s", resp.StatusCode, raw)
	}
	if got := decodeMap(t, raw)["avatar"].(map[string]any); got["revision"].(float64) != 2 || got["greeting"] != "改了" {
		t.Fatalf("update 响应 = %v", got)
	}

	// duplicate：slug 递增 -copy/-copy2，vidu_* 清空、is_active=false。
	resp, raw = callJSON(t, base, http.MethodPost, "/api/v1/avatars/"+id+"/duplicate", nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("duplicate = %d %s", resp.StatusCode, raw)
	}
	dup := decodeMap(t, raw)["avatar"].(map[string]any)
	if dup["slug"] != created["slug"].(string)+"-copy" || dup["is_active"].(bool) || dup["revision"].(float64) != 1 {
		t.Fatalf("duplicate 响应 = %v", dup)
	}
	resp, raw = callJSON(t, base, http.MethodPost, "/api/v1/avatars/"+id+"/duplicate", nil)
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("duplicate2 = %d", resp.StatusCode)
	}
	if got := decodeMap(t, raw)["avatar"].(map[string]any); got["slug"] != created["slug"].(string)+"-copy2" {
		t.Fatalf("duplicate2 slug = %v", got["slug"])
	}

	// activate：排他（A3）。
	resp, raw = callJSON(t, base, http.MethodPost, "/api/v1/avatars/"+id+"/activate", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("activate = %d %s", resp.StatusCode, raw)
	}
	resp, raw = callJSON(t, base, http.MethodPost, "/api/v1/avatars/"+dup["persona_id"].(string)+"/activate", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("activate dup = %d", resp.StatusCode)
	}
	resp, raw = callJSON(t, base, http.MethodGet, "/api/v1/avatars", nil)
	items := decodeMap(t, raw)["avatars"].([]any)
	actives := 0
	for _, it := range items {
		if it.(map[string]any)["is_active"].(bool) {
			actives++
		}
	}
	if actives != 1 {
		t.Fatalf("排他 active 数 = %d（list=%s）", actives, raw)
	}

	// 上传主形象图（A10 主线）：png → 200 dedup=false；同内容 → dedup=true 同 asset。
	png1 := testPNG(t, 8, 8)
	resp, up := uploadMap(t, base, "/api/v1/avatars/upload", "a.png", png1)
	if resp.StatusCode != 200 || up["dedup"].(bool) {
		t.Fatalf("upload = %d %v", resp.StatusCode, up)
	}
	assetID := up["asset_id"].(string)
	resp, up2 := uploadMap(t, base, "/api/v1/avatars/upload", "b.png", png1)
	if resp.StatusCode != 200 || !up2["dedup"].(bool) || up2["asset_id"] != assetID {
		t.Fatalf("dedup = %d %v", resp.StatusCode, up2)
	}
	// webp/svg/文本 → 400 unsupported_image_type。
	resp, _ = uploadMap(t, base, "/api/v1/avatars/upload", "x.txt", []byte("not an image"))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("文本上传 = %d", resp.StatusCode)
	}
	// 像素超 4096 → 400。
	resp, _ = uploadMap(t, base, "/api/v1/avatars/upload", "big.png", testPNG(t, 5000, 10))
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("超像素 = %d", resp.StatusCode)
	}

	// 绑定主形象图后 GET 图片：读取时嗅探 image/png（§5.2）。
	resp, raw = callJSON(t, base, http.MethodPut, "/api/v1/avatars/"+id, map[string]any{
		"name": "小雅改", "image_asset_id": assetID, "revision": 2,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("绑定形象 = %d %s", resp.StatusCode, raw)
	}
	// 列表项必须带 avatar_asset_id（前端「设为当前」预检依赖，与 detail 对齐）。
	resp, raw = callJSON(t, base, http.MethodGet, "/api/v1/avatars", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("list = %d %s", resp.StatusCode, raw)
	}
	foundItem := false
	for _, it := range decodeMap(t, raw)["avatars"].([]any) {
		item := it.(map[string]any)
		if item["persona_id"] != id {
			continue
		}
		foundItem = true
		if item["avatar_asset_id"] != assetID {
			t.Fatalf("列表项 avatar_asset_id = %v, want %s", item["avatar_asset_id"], assetID)
		}
	}
	if !foundItem {
		t.Fatalf("列表缺 %s 项: %s", id, raw)
	}
	imgResp, err := http.Get(base + "/api/v1/avatars/" + id + "/image")
	if err != nil {
		t.Fatal(err)
	}
	_ = imgResp.Body.Close()
	if imgResp.StatusCode != 200 || imgResp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("图片读取 = %d ct=%s", imgResp.StatusCode, imgResp.Header.Get("Content-Type"))
	}
	if imgResp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("缺 nosniff 头: %v", imgResp.Header)
	}

	// refs（A11）：上传 3 张成功，第 4 张 → 400 too_many_refs。
	refIDs := make([]string, 0, 3)
	for i := 0; i < 3; i++ {
		resp, up = uploadMap(t, base, "/api/v1/avatars/"+id+"/refs", "r.png", testPNG(t, 4+i, 4))
		if resp.StatusCode != 200 {
			t.Fatalf("ref %d = %d", i, resp.StatusCode)
		}
		refIDs = append(refIDs, up["asset_id"].(string))
	}
	resp, up = uploadMap(t, base, "/api/v1/avatars/"+id+"/refs", "r4.png", testPNG(t, 9, 9))
	if resp.StatusCode != http.StatusBadRequest || up["error"] != "too_many_refs" {
		t.Fatalf("第 4 张 ref = %d %v", resp.StatusCode, up)
	}
	// 详情 refs 有序 0..2，url 可读。
	resp, raw = callJSON(t, base, http.MethodGet, "/api/v1/avatars/"+id, nil)
	detail = decodeMap(t, raw)["avatar"].(map[string]any)
	refs := detail["refs"].([]any)
	if len(refs) != 3 {
		t.Fatalf("refs 数 = %d", len(refs))
	}
	for i, rf := range refs {
		rm := rf.(map[string]any)
		if rm["ord"].(float64) != float64(i) {
			t.Fatalf("refs ord = %v", refs)
		}
	}
	refURL := refs[0].(map[string]any)["url"].(string)
	imgResp, err = http.Get(base + refURL)
	if err != nil {
		t.Fatal(err)
	}
	_ = imgResp.Body.Close()
	if imgResp.StatusCode != 200 || imgResp.Header.Get("Content-Type") != "image/png" {
		t.Fatalf("ref 图片读取 = %d", imgResp.StatusCode)
	}
	// 解绑中间一张 → ord 重排 0..1。
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/avatars/"+id+"/refs/remove", map[string]any{"asset_id": refIDs[1]})
	if resp.StatusCode != 200 {
		t.Fatalf("remove ref = %d", resp.StatusCode)
	}
	resp, raw = callJSON(t, base, http.MethodGet, "/api/v1/avatars/"+id, nil)
	refs = decodeMap(t, raw)["avatar"].(map[string]any)["refs"].([]any)
	if len(refs) != 2 || refs[0].(map[string]any)["ord"].(float64) != 0 || refs[1].(map[string]any)["ord"].(float64) != 1 {
		t.Fatalf("删后 ord 重排 = %v", refs)
	}

	// sync-vidu：未装配 SyncService（无 Vidu Key）→ 503 vidu_key_unconfigured（fail-closed，M3）。
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/avatars/"+id+"/sync-vidu", nil)
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("sync-vidu unconfigured = %d", resp.StatusCode)
	}

	// 删 active → 409 avatar_in_use（dup 当前 active）；软删后 slug 可复用（A16）。
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/avatars/"+dup["persona_id"].(string)+"/delete", nil)
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("删 active = %d", resp.StatusCode)
	}
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/avatars/"+id+"/delete", nil)
	if resp.StatusCode != 200 {
		t.Fatalf("delete = %d", resp.StatusCode)
	}
	resp, _ = callJSON(t, base, http.MethodGet, "/api/v1/avatars/"+id, nil)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("删后 get = %d", resp.StatusCode)
	}
	resp, _ = callJSON(t, base, http.MethodPost, "/api/v1/avatars", map[string]any{
		"name": "小雅重生", "slug": created["slug"],
	})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("软删 slug 复用 = %d", resp.StatusCode)
	}
}

// fakeViduHTTP 三步上传 + avatars 组 fake；create 可注入延迟（A19 超全局超时验证）。
type fakeViduHTTP struct {
	srv     *httptest.Server
	delayMs atomic.Int64
	uploads atomic.Int32
	creates atomic.Int32
}

func newFakeViduHTTP(t *testing.T) *fakeViduHTTP {
	t.Helper()
	f := &fakeViduHTTP{}
	mux := http.NewServeMux()
	mux.HandleFunc("/tools/v2/files/uploads", func(w http.ResponseWriter, r *http.Request) {
		f.uploads.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"up-1","put_url":"` + f.srv.URL + `/put"}`))
	})
	mux.HandleFunc("/tools/v2/files/uploads/up-1/finish", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"uri":"ssupload:fake-1"}`))
	})
	mux.HandleFunc("/put", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("etag", `"etag-1"`)
		w.WriteHeader(http.StatusOK)
	})
	mux.HandleFunc("/live/v1/avatars", func(w http.ResponseWriter, r *http.Request) {
		f.creates.Add(1)
		if d := f.delayMs.Load(); d > 0 {
			time.Sleep(time.Duration(d) * time.Millisecond)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"avatar_id":"va-http-1"}`))
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

// startSyncServer 挂档案路由 + sync 管线 + 全局 timeout 过滤器（生产 15s，测试压到 100ms）。
func startSyncServer(t *testing.T, store *avatar.MemoryAvatarStore, images *avatar.ImageService, sync *avatar.SyncService, timeout time.Duration) (string, func()) {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := khttp.NewServer(khttp.Listener(lis), khttp.Filter(timeoutFilter(timeout)))
	registerPanelRoutes(srv, PanelDeps{
		Avatars:      avatar.NewService(store),
		AvatarImages: images,
		AvatarSync:   sync,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	stop := func() {
		cancel()
		_ = srv.Stop(context.Background())
		<-done
	}
	return "http://" + lis.Addr().String(), stop
}

// syncAvatarViaHTTP 建档案 + 上传主形象 + 绑定，返回 persona id。
func syncAvatarViaHTTP(t *testing.T, base, name string, png []byte) string {
	t.Helper()
	resp, raw := callJSON(t, base, http.MethodPost, "/api/v1/avatars", map[string]any{"name": name})
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("create = %d %s", resp.StatusCode, raw)
	}
	id := decodeMap(t, raw)["avatar"].(map[string]any)["persona_id"].(string)
	resp, up := uploadMap(t, base, "/api/v1/avatars/upload", "a.png", png)
	if resp.StatusCode != 200 {
		t.Fatalf("upload = %d %v", resp.StatusCode, up)
	}
	resp, raw = callJSON(t, base, http.MethodPut, "/api/v1/avatars/"+id, map[string]any{
		"name": name, "image_asset_id": up["asset_id"], "revision": 1,
	})
	if resp.StatusCode != 200 {
		t.Fatalf("bind image = %d %s", resp.StatusCode, raw)
	}
	return id
}

// pollSyncState 经 GET /api/v1/avatars/{id} 轮询 vidu_sync_state（不新增轮询路由的闭环验证）。
func pollSyncState(t *testing.T, base, id string, wantState string) map[string]any {
	t.Helper()
	deadline := time.Now().Add(15 * time.Second)
	for time.Now().Before(deadline) {
		resp, raw := callJSON(t, base, http.MethodGet, "/api/v1/avatars/"+id, nil)
		if resp.StatusCode != 200 {
			t.Fatalf("poll get = %d", resp.StatusCode)
		}
		st, _ := decodeMap(t, raw)["avatar"].(map[string]any)["vidu_sync_state"].(map[string]any)
		if st != nil && st["state"] == wantState {
			return st
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("轮询收敛超时（want state=%s）", wantState)
	return nil
}

// TestSyncViduRouteA19 M3 验收（v3 §2.2 / A19）：
// 1) fake vidu 注入 >全局超时 的延迟（300ms vs 100ms），sync 仍能完成且状态可查；
// 2) 10 并发同档案 sync → 仅 1 个真实 job（恰好 1 个 202，其余 200 复放同 job_id）；
// 3) sha256 未变 → 200 复放 dedup:true，不重复上传/创建。
func TestSyncViduRouteA19(t *testing.T) {
	fake := newFakeViduHTTP(t)
	fake.delayMs.Store(300) // create 延迟 > 全局超时 100ms
	store := avatar.NewMemoryStore()
	images := avatar.NewImageService(store, t.TempDir())
	vc := vidu.NewClient(vidu.Options{BaseURL: fake.srv.URL, APIKey: "test", RetryDelay: time.Millisecond})
	syncSvc := avatar.NewSyncService(store, images, vc, nil)
	base, stop := startSyncServer(t, store, images, syncSvc, 100*time.Millisecond)
	defer stop()

	// A19-1：202 即返；延迟 >全局超时 仍完成；状态经既有 GetAvatar 轮询闭环。
	id := syncAvatarViaHTTP(t, base, "小雅A19", testPNG(t, 8, 8))
	start := time.Now()
	resp, raw := callJSON(t, base, http.MethodPost, "/api/v1/avatars/"+id+"/sync-vidu", nil)
	if resp.StatusCode != http.StatusAccepted {
		t.Fatalf("sync-vidu = %d %s", resp.StatusCode, raw)
	}
	body := decodeMap(t, raw)
	jobID := body["job_id"].(string)
	if body["state"] != avatar.SyncStateQueued || jobID == "" {
		t.Fatalf("202 响应 = %v", body)
	}
	if el := time.Since(start); el > 100*time.Millisecond {
		t.Fatalf("入队耗时 %v > 全局超时（应即返）", el)
	}
	st := pollSyncState(t, base, id, avatar.SyncStateDone)
	if st["job_id"] != jobID {
		t.Fatalf("轮询 job_id = %v want %s", st["job_id"], jobID)
	}
	resp, raw = callJSON(t, base, http.MethodGet, "/api/v1/avatars/"+id, nil)
	detail := decodeMap(t, raw)["avatar"].(map[string]any)
	if detail["vidu_avatar_id"] != "va-http-1" {
		t.Fatalf("vidu_avatar_id = %v", detail["vidu_avatar_id"])
	}
	if fake.creates.Load() != 1 || fake.uploads.Load() != 1 {
		t.Fatalf("fake creates=%d uploads=%d want 1/1", fake.creates.Load(), fake.uploads.Load())
	}

	// A19-3：sha256 未变 → 200 复放 dedup:true。
	resp, raw = callJSON(t, base, http.MethodPost, "/api/v1/avatars/"+id+"/sync-vidu", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("dedup 复放 = %d %s", resp.StatusCode, raw)
	}
	body = decodeMap(t, raw)
	if body["dedup"] != true || body["job_id"] != jobID {
		t.Fatalf("dedup 响应 = %v", body)
	}
	if fake.creates.Load() != 1 {
		t.Fatalf("dedup 后 creates = %d want 1", fake.creates.Load())
	}

	// A19-2：10 并发同档案 sync → 仅 1 个真实 job。
	fake.delayMs.Store(20)
	id2 := syncAvatarViaHTTP(t, base, "小雅并发", testPNG(t, 6, 6))
	type result struct {
		code int
		job  string
	}
	results := make([]result, 10)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			resp, raw := callJSON(t, base, http.MethodPost, "/api/v1/avatars/"+id2+"/sync-vidu", nil)
			results[i] = result{code: resp.StatusCode}
			if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusAccepted {
				results[i].job = decodeMap(t, raw)["job_id"].(string)
			}
		}(i)
	}
	wg.Wait()
	n202 := 0
	for _, r := range results {
		if r.code == http.StatusAccepted {
			n202++
		} else if r.code != http.StatusOK {
			t.Fatalf("并发 sync 状态码异常: %v", results)
		}
		if r.job != results[0].job {
			t.Fatalf("并发返回不同 job: %v", results)
		}
	}
	if n202 != 1 {
		t.Fatalf("202 数 = %d want 1（%v）", n202, results)
	}
	pollSyncState(t, base, id2, avatar.SyncStateDone)
	if fake.creates.Load() != 2 { // id 一次 + id2 一次
		t.Fatalf("并发后 creates = %d want 2（10 并发仅 1 个真实 job）", fake.creates.Load())
	}
}
