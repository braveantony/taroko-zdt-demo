package server

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"gitlab.com/kubeantony/kubernetes-summit-2026/internal/config"
)

// syncBuffer 讓 slog 併發寫入不觸發 -race。
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func testConfig(shutdownTimeout time.Duration) config.Config {
	return config.Config{
		Port:            0, // 測試用隨機 port
		ShutdownTimeout: shutdownTimeout,
		LogLevel:        slog.LevelDebug,
	}
}

// startServer 啟動 Run 於 goroutine,回傳 server、signal chan、結果 chan 與 log buffer。
func startServer(t *testing.T, shutdownTimeout time.Duration) (*Server, chan os.Signal, chan error, *syncBuffer) {
	t.Helper()
	buf := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s := New(testConfig(shutdownTimeout), logger)

	signals := make(chan os.Signal, 2)
	done := make(chan error, 1)
	go func() { done <- s.Run(t.Context(), signals) }()

	select {
	case <-s.Started():
	case <-time.After(3 * time.Second):
		t.Fatal("server 未在 3 秒內啟動")
	}
	return s, signals, done, buf
}

func waitRun(t *testing.T, done chan error, within time.Duration) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(within):
		t.Fatalf("Run 未在 %v 內返回", within)
		return nil
	}
}

func TestShutdownFlipsReadinessImmediately(t *testing.T) {
	s, signals, done, _ := startServer(t, time.Second)
	if !s.Health().IsReady() {
		t.Fatal("啟動後應為 ready")
	}

	signals <- syscall.SIGTERM

	deadline := time.Now().Add(200 * time.Millisecond)
	for s.Health().IsReady() {
		if time.Now().After(deadline) {
			t.Fatal("SIGTERM 後 200ms 內就緒狀態未翻轉")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if err := waitRun(t, done, 3*time.Second); err != nil {
		t.Fatalf("乾淨關機應回傳 nil,得到:%v", err)
	}
}

func TestInFlightRequestCompletes(t *testing.T) {
	s, signals, done, _ := startServer(t, time.Second)
	s.HandleFunc("GET /test-inflight", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		fmt.Fprint(w, "finished")
	})

	type result struct {
		status int
		body   string
		err    error
	}
	resCh := make(chan result, 1)
	go func() {
		resp, err := http.Get("http://" + s.Addr() + "/test-inflight")
		if err != nil {
			resCh <- result{err: err}
			return
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		resCh <- result{status: resp.StatusCode, body: string(body)}
	}()

	time.Sleep(50 * time.Millisecond) // 請求已在途
	signals <- syscall.SIGTERM

	r := <-resCh
	if r.err != nil {
		t.Fatalf("在途請求不得被截斷:%v", r.err)
	}
	if r.status != 200 || r.body != "finished" {
		t.Fatalf("在途請求回應異常:%d %q", r.status, r.body)
	}
	if err := waitRun(t, done, 3*time.Second); err != nil {
		t.Fatalf("乾淨關機應回傳 nil,得到:%v", err)
	}
}

// 2026-08-21 修訂:drain 移至 K8s preStop,關機序列為四步、不得再有 drain 等待。
func TestShutdownLogSequence(t *testing.T) {
	_, signals, done, buf := startServer(t, time.Second)
	signals <- syscall.SIGTERM
	if err := waitRun(t, done, 3*time.Second); err != nil {
		t.Fatalf("乾淨關機應回傳 nil,得到:%v", err)
	}

	want := []string{
		"shutdown signal received",
		"readiness flipped to draining",
		"shutting down http server",
		"shutdown complete",
	}
	var msgs []string
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log 非合法 JSON:%s", line)
		}
		if m, ok := entry["msg"].(string); ok {
			msgs = append(msgs, m)
		}
	}
	idx := 0
	for _, m := range msgs {
		if strings.Contains(m, "drain delay") {
			t.Fatalf("drain 已移至 preStop,不應再出現 drain 等待 log:%v", msgs)
		}
		if idx < len(want) && m == want[idx] {
			idx++
		}
	}
	if idx != len(want) {
		t.Fatalf("關機序列 log 不完整或順序錯誤,期望依序出現 %v,實際 msgs=%v", want, msgs)
	}
}

func TestSecondSignalForcesExit(t *testing.T) {
	s, signals, done, buf := startServer(t, 2*time.Second)
	// 用一個在途慢請求把 Shutdown 撐住,製造二次訊號的時間窗
	s.HandleFunc("GET /test-inflight", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(800 * time.Millisecond)
		fmt.Fprint(w, "done")
	})
	go func() {
		resp, err := http.Get("http://" + s.Addr() + "/test-inflight")
		if err == nil {
			resp.Body.Close()
		}
	}()
	time.Sleep(50 * time.Millisecond) // 請求已在途

	signals <- syscall.SIGTERM
	time.Sleep(30 * time.Millisecond) // 已進入 Shutdown 等待
	signals <- syscall.SIGTERM

	err := waitRun(t, done, 500*time.Millisecond) // 遠小於慢請求的 800ms,證明是強制路徑
	if err == nil {
		t.Fatal("二次訊號應回傳錯誤(exit 非 0)")
	}
	if !strings.Contains(buf.String(), "second signal received, forcing exit") {
		t.Error("缺少 forcing exit 的 log")
	}
}

func TestShutdownTimeoutExceeded(t *testing.T) {
	s, signals, done, buf := startServer(t, 50*time.Millisecond)
	s.HandleFunc("GET /hang", func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(600 * time.Millisecond)
	})

	go func() {
		resp, err := http.Get("http://" + s.Addr() + "/hang")
		if err == nil {
			resp.Body.Close()
		}
	}()
	time.Sleep(50 * time.Millisecond) // 掛住的請求已在途
	signals <- syscall.SIGTERM

	err := waitRun(t, done, 2*time.Second)
	if err == nil {
		t.Fatal("收尾逾時應回傳錯誤(exit 非 0)")
	}
	if !strings.Contains(buf.String(), "shutdown timeout exceeded") {
		t.Error("缺少 shutdown timeout exceeded 的 log")
	}
}
