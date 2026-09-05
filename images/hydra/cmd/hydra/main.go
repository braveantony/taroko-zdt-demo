// hydra:Kubernetes Summit 2026 零停機部署 demo 的 HTTP 服務。
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"gitlab.com/kubeantony/kubernetes-summit-2026/internal/config"
	"gitlab.com/kubeantony/kubernetes-summit-2026/internal/server"
	"gitlab.com/kubeantony/kubernetes-summit-2026/internal/version"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		// 此時尚無正式 logger:用預設 JSON handler 報錯後 fail-fast(FR-006)
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).
			Error("設定無效,啟動中止", "error", err.Error())
		os.Exit(1)
	}

	levelVar := new(slog.LevelVar)
	levelVar.Set(cfg.LogLevel)
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: levelVar}))
	slog.SetDefault(logger)
	logger.Info("hydra starting", "version", version.Version,
		"port", cfg.Port,
		"shutdown_timeout", cfg.ShutdownTimeout.String(), "log_level", cfg.LogLevel.String())

	// 容量 2:第一次訊號啟動關機序列,第二次強制結束(research.md R4)
	// F004 場景 2 驗收樣本:此註解變更產生新 commit → 新 image → 觀察自動部署
	signals := make(chan os.Signal, 2)
	if cfg.Graceful {
		signal.Notify(signals, syscall.SIGTERM, os.Interrupt)
	} else {
		// act1 完全不處理(specs/006):不註冊 SIGTERM handler → 收到 SIGTERM 由 Go
		// runtime 預設行為立即終止程序(exit 143),不 drain、不 Shutdown、不等在途連線
		logger.Warn("graceful shutdown DISABLED — SIGTERM 將直接終止程序(act1 裸奔)")
	}

	if err := server.New(cfg, logger).Run(context.Background(), signals); err != nil {
		logger.Error("exiting with failure", "error", err.Error())
		os.Exit(1)
	}
}
