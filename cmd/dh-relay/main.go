// dh-relay：抖音弹幕采集转发器（独立进程）。
// 订阅 douyinLive ws → 翻译 → HMAC 签名 POST /api/danmaku。
// 密钥只从环境变量读：DH_COLLECTOR_SECRET（必填）。
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/teamwork/digital-human/internal/relay"
)

func main() {
	var (
		wsBase  = flag.String("ws", envOr("DH_DOUYIN_WS", "ws://127.0.0.1:1089"), "douyinLive ws 基址")
		bridge  = flag.String("bridge", envOr("DH_BRIDGE", "http://127.0.0.1:8795"), "digital-human 基址")
		room    = flag.String("room", os.Getenv("DH_ROOM_ID"), "抖音房间号")
		verbose = flag.Bool("verbose", false, "打印调试信息")
	)
	flag.Parse()

	secret := os.Getenv("DH_COLLECTOR_SECRET")
	if secret == "" {
		log.Fatal("dh-relay: DH_COLLECTOR_SECRET 未设置（密钥只从环境变量读）")
	}
	if *room == "" {
		log.Fatal("dh-relay: 房间号为空（--room 或 DH_ROOM_ID）")
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	r := relay.NewRelay(relay.Config{
		WSBase:  *wsBase,
		Bridge:  *bridge,
		Room:    *room,
		Secret:  secret,
		Verbose: *verbose,
		Logf:    log.Printf,
	})
	if err := r.Run(ctx); err != nil && ctx.Err() == nil {
		log.Fatalf("dh-relay: %v", err)
	}
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}
