package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	khttp "github.com/go-kratos/kratos/v2/transport/http"

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
}

func (a *App) Run() error {
	// 启动服务前处理崩溃遗留场次：只标记 crash_recovered 并发事件，不自动恢复会话。
	if a.Manager != nil {
		if err := a.Manager.Recover(context.Background()); err != nil {
			a.Logger.Warn("recover stale sessions failed", "err", err)
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
