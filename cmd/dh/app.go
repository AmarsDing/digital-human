package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	khttp "github.com/go-kratos/kratos/v2/transport/http"

	"github.com/teamwork/digital-human/internal/biz/contentpkg"
	"github.com/teamwork/digital-human/internal/biz/danmaku"
	"github.com/teamwork/digital-human/internal/biz/live"
	"github.com/teamwork/digital-human/internal/data"
)

// VersionInfo 用独立类型避免 Wire 把 string 版本号与配置文件路径混淆。
type VersionInfo string

// App 由 Wire 组装；cmd/dh 是唯一依赖注入入口。
type App struct {
	Server  *khttp.Server
	Data    *data.Data
	Logger  *slog.Logger
	Manager *live.Manager
	// DanmakuWorker F24 弹幕批读消费循环；持有引用保证 wire 装配（副作用：注册进 Manager 生命周期）。
	DanmakuWorker *danmaku.Worker
	// Interaction B3 互动一致性闭环（§5.4/§5.5）：持有引用启动崩溃扫描 goroutine。
	Interaction *interactionRuntime
	// Contentpkg B4 内容包原子性骨架（§7.1/§7.4）：持有引用保证 wire 装配（含 PG 热切）。
	Contentpkg *contentpkg.Service
}

func (a *App) Run() error {
	// 启动服务前处理崩溃遗留场次：只标记 crash_recovered 并发事件，不自动恢复会话。
	if a.Manager != nil {
		if err := a.Manager.Recover(context.Background()); err != nil {
			a.Logger.Warn("recover stale sessions failed", "err", err)
		}
	}
	// B3 §5.5：崩溃扫描 goroutine——启动即扫一次，此后 30s 周期
	//（补建缺失 Decision、lease 过期回收、超回执期转 unknown+reconcile_pending）。
	if a.Interaction != nil && a.Interaction.disp != nil {
		a.Interaction.disp.Run(context.Background(), 30*time.Second)
	}
	// 内容包启动重放：committed 账本 roll-forward，其余回滚或显式 manual_required。
	if a.Contentpkg != nil {
		if err := a.Contentpkg.ReplayPending(context.Background()); err != nil {
			a.Logger.Warn("replay content package journals failed", "err", err)
		}
	}

	errCh := make(chan error, 1)
	go func() { errCh <- a.Server.Start(context.Background()) }()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGINT, syscall.SIGTERM)
	select {
	case err := <-errCh:
		return err
	case <-stop:
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		return a.Server.Stop(ctx)
	}
}
