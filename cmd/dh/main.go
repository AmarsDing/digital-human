package main

import (
	"flag"
	"log"
	"log/slog"

	_ "github.com/teamwork/digital-human/internal/data/ent/runtime"
)

var version = "0.1.0-m0"

func main() {
	cfgPath := flag.String("conf", "configs/config.yaml", "配置文件路径")
	flag.Parse()

	app, cleanup, err := InitApp(*cfgPath, VersionInfo(version))
	if err != nil {
		log.Fatalf("init failed: %v", err)
	}
	defer cleanup()
	slog.SetDefault(app.Logger)

	if err := app.Run(); err != nil {
		log.Fatalf("run failed: %v", err)
	}
}
