package config

import (
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 預設值不應失敗:%v", err)
	}
	if cfg.Port != 8080 {
		t.Errorf("Port 預設應為 8080,得到 %d", cfg.Port)
	}
	if cfg.ShutdownTimeout != 15*time.Second {
		t.Errorf("ShutdownTimeout 預設應為 15s,得到 %v", cfg.ShutdownTimeout)
	}
	if cfg.LogLevel != slog.LevelInfo {
		t.Errorf("LogLevel 預設應為 info,得到 %v", cfg.LogLevel)
	}
	if cfg.StateBackend != "memory" {
		t.Errorf("StateBackend 預設應為 memory,得到 %q", cfg.StateBackend)
	}
	if cfg.ValkeyAddr != "valkey:6379" {
		t.Errorf("ValkeyAddr 預設應為 valkey:6379,得到 %q", cfg.ValkeyAddr)
	}
	if cfg.TourInterval != 10*time.Second {
		t.Errorf("TourInterval 預設應為 10s,得到 %v", cfg.TourInterval)
	}
	if cfg.SSEDrain {
		t.Error("SSEDrain 預設應為 off(false)")
	}
	if !cfg.Graceful {
		t.Error("Graceful 預設應為 on(true) — 一般情況必須優雅關機")
	}
}

// 006:導覽功能的四個新環境變數(契約 specs/006 contracts/config.md)。
func TestLoadTourOverrides(t *testing.T) {
	t.Setenv("HYDRA_STATE_BACKEND", "valkey")
	t.Setenv("HYDRA_VALKEY_ADDR", "127.0.0.1:16379")
	t.Setenv("HYDRA_TOUR_INTERVAL_SECONDS", "5")
	t.Setenv("HYDRA_SSE_DRAIN", "on")
	t.Setenv("HYDRA_GRACEFUL", "off")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 合法覆寫不應失敗:%v", err)
	}
	if cfg.Graceful {
		t.Error("Graceful 應為 off(false) — act1 裸奔:不註冊 SIGTERM handler")
	}
	if cfg.StateBackend != "valkey" {
		t.Errorf("StateBackend 應為 valkey,得到 %q", cfg.StateBackend)
	}
	if cfg.ValkeyAddr != "127.0.0.1:16379" {
		t.Errorf("ValkeyAddr 應為 127.0.0.1:16379,得到 %q", cfg.ValkeyAddr)
	}
	if cfg.TourInterval != 5*time.Second {
		t.Errorf("TourInterval 應為 5s,得到 %v", cfg.TourInterval)
	}
	if !cfg.SSEDrain {
		t.Error("SSEDrain 應為 on(true)")
	}
}

func TestLoadOverrides(t *testing.T) {
	t.Setenv("HYDRA_PORT", "9090")
	t.Setenv("HYDRA_SHUTDOWN_TIMEOUT_SECONDS", "30")
	t.Setenv("HYDRA_LOG_LEVEL", "debug")

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() 合法覆寫不應失敗:%v", err)
	}
	if cfg.Port != 9090 {
		t.Errorf("Port 應為 9090,得到 %d", cfg.Port)
	}
	if cfg.ShutdownTimeout != 30*time.Second {
		t.Errorf("ShutdownTimeout 應為 30s,得到 %v", cfg.ShutdownTimeout)
	}
	if cfg.LogLevel != slog.LevelDebug {
		t.Errorf("LogLevel 應為 debug,得到 %v", cfg.LogLevel)
	}
}

// drain 已移至 K8s preStop hook(2026-08-21 修訂):
// 舊環境變數即使設定也必須被忽略,不得造成錯誤。
func TestLoadIgnoresRemovedDrainVariable(t *testing.T) {
	t.Setenv("HYDRA_DRAIN_SECONDS", "5")
	if _, err := Load(); err != nil {
		t.Fatalf("已移除的 HYDRA_DRAIN_SECONDS 應被忽略,得到錯誤:%v", err)
	}
}

func TestLoadInvalid(t *testing.T) {
	cases := []struct {
		name   string
		envVar string
		value  string
	}{
		{"port 非數字", "HYDRA_PORT", "abc"},
		{"port 為零", "HYDRA_PORT", "0"},
		{"port 越界", "HYDRA_PORT", "70000"},
		{"timeout 為零", "HYDRA_SHUTDOWN_TIMEOUT_SECONDS", "0"},
		{"timeout 負數", "HYDRA_SHUTDOWN_TIMEOUT_SECONDS", "-3"},
		{"timeout 非數字", "HYDRA_SHUTDOWN_TIMEOUT_SECONDS", "soon"},
		{"level 非法", "HYDRA_LOG_LEVEL", "loud"},
		{"backend 非法", "HYDRA_STATE_BACKEND", "redis"},
		{"interval 為零", "HYDRA_TOUR_INTERVAL_SECONDS", "0"},
		{"interval 越界", "HYDRA_TOUR_INTERVAL_SECONDS", "61"},
		{"interval 非數字", "HYDRA_TOUR_INTERVAL_SECONDS", "fast"},
		{"drain 非法", "HYDRA_SSE_DRAIN", "yes"},
		{"graceful 非法", "HYDRA_GRACEFUL", "maybe"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(tc.envVar, tc.value)
			_, err := Load()
			if err == nil {
				t.Fatalf("%s=%s 應該回傳錯誤", tc.envVar, tc.value)
			}
			// 錯誤訊息必須含變數名與收到的值(FR-006)
			if !strings.Contains(err.Error(), tc.envVar) || !strings.Contains(err.Error(), tc.value) {
				t.Errorf("錯誤訊息應含 %q 與 %q,得到:%v", tc.envVar, tc.value, err)
			}
		})
	}
}
