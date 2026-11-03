package server

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"

	collectorgen "github.com/teamwork/digital-human/api/gen/go/collector/v1"
	streamgen "github.com/teamwork/digital-human/api/gen/go/stream/v1"
	"github.com/teamwork/digital-human/internal/conf"
	"github.com/teamwork/digital-human/internal/publish"
	"github.com/teamwork/digital-human/internal/relay"
)

// DZ 批接线回归：6 条推流/采集路由必须已在 mux 注册且统一走 lan 模式 Bearer
// 鉴权链。streamSvc 恒注册；collectorSvc 用真实 relay 服务（tracker + 内存房间
// 配置存储）装配，与 wire 接线一致。无 token 一律 401（而非 404——404 意味着
// 接线遗漏）；带合法 Bearer 一律 200。
func TestDZRoutesRegisteredAndAuthed(t *testing.T) {
	pub := publish.NewPublisher(publish.Config{
		Source: publish.SourceSynthetic,
		Logf:   func(string, ...any) {},
	}, &publish.MemTargetStore{})
	streamSvc := publish.NewStreamService(pub)
	collectorSvc := relay.NewCollectorService(relay.NewTracker(), relay.NewRoomConfigStore())

	token := "dz-route-secret"
	sum := sha256.Sum256([]byte(token))
	cfg := &conf.Config{
		Server: conf.Server{ReadTimeout: 5 * time.Second},
		Security: conf.Security{
			Mode:             "lan",
			AdminTokenSHA256: hex.EncodeToString(sum[:]),
			AllowedOrigins:   []string{"http://localhost"},
		},
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
	streamgen.RegisterStreamServiceHTTPServer(srv, streamServiceAdapter{streamSvc})
	collectorgen.RegisterCollectorServiceHTTPServer(srv, collectorSvc)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Start(ctx) }()
	t.Cleanup(func() {
		cancel()
		_ = srv.Stop(context.Background())
		<-done
	})
	base := "http://" + lis.Addr().String()
	client := &http.Client{Timeout: 10 * time.Second}

	// POST 均传 {}：target/config 因缺必填字段返回 {ok:false} 200；
	// start 因未配置推流目标返回 {ok:false} 200（在拉起 ffmpeg 之前失败）；stop 幂等。
	routes := []struct {
		method string
		path   string
		body   string
	}{
		{"GET", "/api/v1/stream/status", ""},
		{"POST", "/api/v1/stream/target", `{}`},
		{"POST", "/api/v1/stream/start", `{}`},
		{"POST", "/api/v1/stream/stop", `{}`},
		{"GET", "/api/v1/collector/status", ""},
		{"POST", "/api/v1/collector/config", `{}`},
	}
	for _, r := range routes {
		req, _ := http.NewRequest(r.method, base+r.path, strings.NewReader(r.body))
		if r.body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := client.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", r.method, r.path, err)
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Errorf("无鉴权 %s %s = %d, want 401", r.method, r.path, resp.StatusCode)
		}

		req2, _ := http.NewRequest(r.method, base+r.path, strings.NewReader(r.body))
		if r.body != "" {
			req2.Header.Set("Content-Type", "application/json")
		}
		req2.Header.Set("Authorization", "Bearer "+token)
		resp2, err := client.Do(req2)
		if err != nil {
			t.Fatalf("%s %s: %v", r.method, r.path, err)
		}
		_ = resp2.Body.Close()
		if resp2.StatusCode != http.StatusOK {
			t.Errorf("合法 Bearer %s %s = %d, want 200（路由须已注册）", r.method, r.path, resp2.StatusCode)
		}
	}

	// 对照：带合法 Bearer 访问未注册路径必须 404（防止鉴权通过后的假阳性）。
	req, _ := http.NewRequest("GET", base+"/api/v1/stream/nonexistent", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("未注册路径 = %d, want 404", resp.StatusCode)
	}
}
