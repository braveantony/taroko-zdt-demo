// Package config 解析並驗證 hydra 的環境變數設定(fail-fast)。
package config

import (
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

// Config 為服務啟動設定;欄位與驗證規則見 specs/001 data-model.md。
// 註:排水等待(drain)由 K8s 層 preStop sleep 承擔,不在程式設定範圍(2026-08-21 修訂)。
type Config struct {
	Port            int
	ShutdownTimeout time.Duration
	LogLevel        slog.Level

	// 006 導覽功能(specs/006 contracts/config.md)
	StateBackend string        // memory | valkey
	ValkeyAddr   string        // 僅 valkey 後端使用
	TourInterval time.Duration // 導覽推進間隔
	SSEDrain     bool          // 關機時是否廣播 bye 並收線

	// Graceful=off 時 main 不註冊 SIGTERM handler → 收到 SIGTERM 由 Go runtime
	// 預設行為立即終止(exit 143),不做優雅關機。act1「完全不處理」用之(specs/006)。
	Graceful bool
}

// Load 讀取環境變數,套用預設值並驗證;任何無效值回傳含變數名與原值的錯誤。
func Load() (Config, error) {
	cfg := Config{
		Port:            8080,
		ShutdownTimeout: 15 * time.Second,
		LogLevel:        slog.LevelInfo,
		StateBackend:    "memory",
		ValkeyAddr:      "valkey:6379",
		TourInterval:    10 * time.Second,
		SSEDrain:        false,
		Graceful:        true,
	}

	if v, ok := os.LookupEnv("HYDRA_PORT"); ok {
		p, err := strconv.Atoi(v)
		if err != nil || p < 1 || p > 65535 {
			return Config{}, fmt.Errorf("HYDRA_PORT=%s 無效:必須是 1–65535 的整數", v)
		}
		cfg.Port = p
	}

	if v, ok := os.LookupEnv("HYDRA_SHUTDOWN_TIMEOUT_SECONDS"); ok {
		s, err := strconv.Atoi(v)
		if err != nil || s <= 0 {
			return Config{}, fmt.Errorf("HYDRA_SHUTDOWN_TIMEOUT_SECONDS=%s 無效:必須是 >0 的整數秒數", v)
		}
		cfg.ShutdownTimeout = time.Duration(s) * time.Second
	}

	if v, ok := os.LookupEnv("HYDRA_LOG_LEVEL"); ok {
		var lvl slog.Level
		if err := lvl.UnmarshalText([]byte(v)); err != nil {
			return Config{}, fmt.Errorf("HYDRA_LOG_LEVEL=%s 無效:必須是 debug/info/warn/error", v)
		}
		cfg.LogLevel = lvl
	}

	if v, ok := os.LookupEnv("HYDRA_STATE_BACKEND"); ok {
		if v != "memory" && v != "valkey" {
			return Config{}, fmt.Errorf("HYDRA_STATE_BACKEND=%s 無效:必須是 memory 或 valkey", v)
		}
		cfg.StateBackend = v
	}

	if v, ok := os.LookupEnv("HYDRA_VALKEY_ADDR"); ok {
		cfg.ValkeyAddr = v
	}

	if v, ok := os.LookupEnv("HYDRA_TOUR_INTERVAL_SECONDS"); ok {
		s, err := strconv.Atoi(v)
		if err != nil || s < 1 || s > 60 {
			return Config{}, fmt.Errorf("HYDRA_TOUR_INTERVAL_SECONDS=%s 無效:必須是 1–60 的整數秒數", v)
		}
		cfg.TourInterval = time.Duration(s) * time.Second
	}

	if v, ok := os.LookupEnv("HYDRA_SSE_DRAIN"); ok {
		switch v {
		case "on":
			cfg.SSEDrain = true
		case "off":
			cfg.SSEDrain = false
		default:
			return Config{}, fmt.Errorf("HYDRA_SSE_DRAIN=%s 無效:必須是 on 或 off", v)
		}
	}

	if v, ok := os.LookupEnv("HYDRA_GRACEFUL"); ok {
		switch v {
		case "on":
			cfg.Graceful = true
		case "off":
			cfg.Graceful = false
		default:
			return Config{}, fmt.Errorf("HYDRA_GRACEFUL=%s 無效:必須是 on 或 off", v)
		}
	}

	return cfg, nil
}
