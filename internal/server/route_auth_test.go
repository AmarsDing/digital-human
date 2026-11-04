package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"
	configv1 "github.com/teamwork/digital-human/api/gen/go/config/v1"
	livev1 "github.com/teamwork/digital-human/api/gen/go/live/v1"

	"github.com/teamwork/digital-human/internal/biz/action"
	"github.com/teamwork/digital-human/internal/biz/archive"
	"github.com/teamwork/digital-human/internal/biz/avatar"
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
	"github.com/teamwork/digital-human/internal/biz/voice"
	"github.com/teamwork/digital-human/internal/conf"
	"github.com/teamwork/digital-human/internal/service"
	"github.com/teamwork/digital-human/pkg/eventbus"
)

// TestRealRouteTableRequiresAuth 用真实路由表（panels 85 条 + proto 服务路由）
// 套真实鉴权链（serverFilters: timeout/log/audit/origin/auth）：
// 所有非 GET 路由无 Bearer 必须 401；带合法 Bearer 不得再 401。
// 防止新增接口漏挂鉴权（P1-15）。
func TestRealRouteTableRequiresAuth(t *testing.T) {
	root := t.TempDir()
	audioDir := filepath.Join(root, "audio")
	songsDir := filepath.Join(root, "songs")
	kouboDir := filepath.Join(root, "koubo")
	for _, d := range []string{audioDir, songsDir, kouboDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	bus := eventbus.New(64)
	mgr := live.NewManagerWithStores(bus, live.NewMemorySink(64), live.NewFakeController(slog.Default()), nil,
		live.NewMemoryIdempotencyStore(time.Hour), nil)
	dm := danmaku.NewService(danmaku.ModeConfirm)
	st := settings.NewService(settings.NewMemoryStore())
	player := media.NewFakePlayer()
	player.SetDelay(0)
	synth := tts.NewFakeSynth()
	remote := action.NewMemoryRemote()
	sched := action.NewScheduler(action.NewFakeSender(), nil, action.DefaultConfig())
	chor := choreo.NewService(choreo.NewMemoryStore(choreo.DefaultConfig()), sched)
	sched.SetPauseChoreo(func(d time.Duration) { chor.Pause(d) })
	sessions := live.NewMemorySessionStore()
	avatarStore := avatar.NewMemoryStore()
	imagesDir := filepath.Join(root, "images")
	deps := PanelDeps{
		TTS:     tts.NewService(media.NewLibrary(audioDir), synth, st),
		Actions: action.NewService(remote, sched),
		Choreo:  chor,
		Koubo:   koubo.NewService(koubo.NewMemoryStore(koubo.State{MaxChars: 120}), synth, player, koubo.FakeActionGen{}, kouboDir, func() bool { return true }),
		Refs:    refs.NewService(refs.NewFakeSignaler(), nil),
		Songs:   song.NewService(media.NewLibrary(songsDir), media.FakeDecoder{}, player, nil, func() bool { return true }),
		Archive: archive.NewService(sessions, nil, billing.NewService(billing.NewMemoryStore())),
		Billing: billing.NewService(billing.NewMemoryStore()),
		Danmaku: dm,
		// G 批 M2：档案服务 + 图片管线（真实注册，A22 GET 401 断言才有意义）。
		Avatars:      avatar.NewService(avatarStore),
		AvatarImages: avatar.NewImageService(avatarStore, imagesDir),
	}

	token := "route-auth-token"
	sum := sha256.Sum256([]byte(token))
	cfg := &conf.Config{
		Server:   conf.Server{ReadTimeout: 5 * time.Second},
		Security: conf.Security{Mode: "lan", AdminTokenSHA256: hex.EncodeToString(sum[:]), TicketTTL: time.Minute},
	}
	tickets := NewMemoryTicketStore(time.Minute)
	t.Cleanup(tickets.Close)

	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := khttp.NewServer(
		khttp.Listener(lis),
		khttp.Timeout(0),
		khttp.Filter(serverFilters(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)), tickets, nil)...),
	)
	registerPanelRoutes(srv, deps)
	registerVoiceRoutes(srv, cfg, nil, &voice.StatusService{}, NewMemoryVoiceTicketStore())
	livev1.RegisterLiveServiceHTTPServer(srv, service.NewLiveService(mgr, nil, dm, nil))
	configv1.RegisterSettingsServiceHTTPServer(srv, service.NewSettingsService(st))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Stop(context.Background())
		<-done
	})
	base := "http://" + lis.Addr().String()

	// 面板 85 条 + proto 服务的关键写路由。
	raw, err := os.ReadFile("../../web/src/services/api/panel-routes.json")
	if err != nil {
		t.Fatalf("读取面板路由清单失败: %v", err)
	}
	var routes []panelRoute
	if err := json.Unmarshal(raw, &routes); err != nil {
		t.Fatal(err)
	}
	routes = append(routes,
		panelRoute{Method: "POST", Path: "/api/v1/live/start"},
		panelRoute{Method: "POST", Path: "/api/v1/live/stop"},
		panelRoute{Method: "POST", Path: "/api/v1/live/say"},
		panelRoute{Method: "POST", Path: "/api/v1/settings"},
		panelRoute{Method: "POST", Path: "/api/v1/voice/ticket"},
	)

	client := &http.Client{Timeout: 10 * time.Second}
	fail := 0
	for _, r := range routes {
		if r.Method == http.MethodGet {
			continue
		}
		// 无鉴权 → 401
		req, _ := http.NewRequest(r.Method, base+r.Path, strings.NewReader("{}"))
		req.Header.Set("Content-Type", "application/json")
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", r.Method, r.Path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("无鉴权 %s %s = %d, want 401", r.Method, r.Path, resp.StatusCode)
			fail++
			continue
		}
		// 合法 Bearer → 不允许再是 401（业务码 200/400/409 均可）。
		req2, _ := http.NewRequest(r.Method, base+r.Path, strings.NewReader("{}"))
		req2.Header.Set("Content-Type", "application/json")
		req2.Header.Set("Authorization", "Bearer "+token)
		resp2, err := client.Do(req2)
		if err != nil {
			t.Fatalf("%s %s: %v", r.Method, r.Path, err)
		}
		_ = resp2.Body.Close()
		if resp2.StatusCode == http.StatusUnauthorized {
			t.Errorf("合法 Bearer %s %s 仍 401", r.Method, r.Path)
			fail++
		}
	}

	// A22（v3 §5.1）：List/GetAvatar/图片三条 GET 同样强制 Bearer——filter 对 GET 不豁免，
	// handler 内不做冗余鉴权（§5 论证）；逐个断言无 Bearer → 401、合法 Bearer → 非 401。
	for _, p := range []string{
		"/api/v1/avatars",
		"/api/v1/avatars/some-id",
		"/api/v1/avatars/some-id/image",
		"/api/v1/avatars/some-id/assets/some-asset/image",
		"/api/v1/voice/status",
	} {
		req, _ := http.NewRequest(http.MethodGet, base+p, nil)
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("无鉴权 GET %s = %d, want 401", p, resp.StatusCode)
			fail++
			continue
		}
		req2, _ := http.NewRequest(http.MethodGet, base+p, nil)
		req2.Header.Set("Authorization", "Bearer "+token)
		resp2, err := client.Do(req2)
		if err != nil {
			t.Fatalf("GET %s: %v", p, err)
		}
		_ = resp2.Body.Close()
		if resp2.StatusCode == http.StatusUnauthorized {
			t.Errorf("合法 Bearer GET %s 仍 401", p)
			fail++
		}
	}
	if fail > 0 {
		t.Fatalf("共 %d 条路由鉴权不合规", fail)
	}
}
