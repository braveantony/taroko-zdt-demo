package server

import (
	"bufio"
	"context"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"

	"gitlab.com/kubeantony/kubernetes-summit-2026/internal/config"
)

// startServerCfg:同 startServer,但接受完整 config(006 drain 測試用)。
func startServerCfg(t *testing.T, cfg config.Config) (*Server, chan os.Signal, chan error, *syncBuffer) {
	t.Helper()
	buf := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s := New(cfg, logger)

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

func tourCfg(drain bool, shutdownTimeout time.Duration) config.Config {
	return config.Config{
		Port:            0,
		ShutdownTimeout: shutdownTimeout,
		LogLevel:        slog.LevelDebug,
		StateBackend:    "memory",
		TourInterval:    time.Hour, // 測試期間不推站,只看關機行為
		SSEDrain:        drain,
	}
}

// openSSE 連上 /tour/events,讀到 hello 後回傳 reader(連線保持開啟)。
func openSSE(t *testing.T, addr string) *bufio.Reader {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/tour/events", nil)
	req.AddCookie(&http.Cookie{Name: "hydra_session", Value: strings.Repeat("a", 32)})
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("連線 SSE 失敗:%v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	br := bufio.NewReader(resp.Body)
	deadline := time.After(3 * time.Second)
	lines := make(chan string, 16)
	go func() {
		for {
			l, err := br.ReadString('\n')
			if err != nil {
				close(lines)
				return
			}
			lines <- l
		}
	}()
	for {
		select {
		case l, ok := <-lines:
			if !ok {
				t.Fatal("SSE 在 hello 前中斷")
			}
			if strings.HasPrefix(l, "event: hello") {
				// 後續行仍由上方 goroutine 搬運,呼叫端經 chanReader 續讀
				return bufio.NewReader(&chanReader{lines: lines})
			}
		case <-deadline:
			t.Fatal("等待 hello 逾時")
		}
	}
}

// chanReader 把行 channel 轉回 io.Reader(單純串接測試用)。
type chanReader struct {
	lines chan string
	rest  string
}

func (c *chanReader) Read(p []byte) (int, error) {
	if c.rest == "" {
		l, ok := <-c.lines
		if !ok {
			return 0, contextDoneErr{}
		}
		c.rest = l
	}
	n := copy(p, c.rest)
	c.rest = c.rest[n:]
	return n, nil
}

type contextDoneErr struct{}

func (contextDoneErr) Error() string { return "stream closed" }

// act3 核心(FR-010b):drain=on 時,SIGTERM → 每條 SSE 收到 bye 並被收線 →
// Shutdown 不被長連線卡住,於逾時前乾淨返回 nil(= exit 0)。
func TestRunDrainOnClosesSSEAndExitsClean(t *testing.T) {
	s, signals, done, _ := startServerCfg(t, tourCfg(true, 5*time.Second))

	br1 := openSSE(t, s.Addr())
	br2 := openSSE(t, s.Addr())

	signals <- syscall.SIGTERM

	for i, br := range []*bufio.Reader{br1, br2} {
		sawBye := false
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) {
			l, err := br.ReadString('\n')
			if err != nil {
				break // 收線
			}
			if strings.HasPrefix(l, "event: bye") {
				sawBye = true
			}
		}
		if !sawBye {
			t.Errorf("第 %d 條連線應在收線前收到 bye 事件", i+1)
		}
	}

	if err := waitRun(t, done, 4*time.Second); err != nil {
		t.Errorf("drain=on 時 Run 應乾淨返回 nil,得到:%v", err)
	}
}

// 回歸鎖:drain=off 時行為與 001 現況完全一致 —
// SSE 卡住 Shutdown → 逾時 → 強制剪線、Run 回錯誤(act1/act2 的預期死法)。
func TestRunDrainOffKeepsLegacyTimeout(t *testing.T) {
	s, signals, done, buf := startServerCfg(t, tourCfg(false, time.Second))

	_ = openSSE(t, s.Addr())
	signals <- syscall.SIGTERM

	err := waitRun(t, done, 4*time.Second)
	if err == nil {
		t.Fatal("drain=off + 活躍 SSE 時,Run 應以 shutdown timeout 錯誤返回")
	}
	if !strings.Contains(err.Error(), "shutdown timeout") {
		t.Errorf("錯誤應為 shutdown timeout,得到:%v", err)
	}
	if !strings.Contains(buf.String(), "shutdown timeout exceeded") {
		t.Error("log 應含 shutdown timeout exceeded(act1 的旁白素材)")
	}
}

// FR-012:backend=valkey 且狀態庫不可用 → Run 啟動即失敗(不得無聲降級)。
func TestRunFailsFastWhenValkeyUnavailable(t *testing.T) {
	cfg := config.Config{
		Port:            0,
		ShutdownTimeout: time.Second,
		LogLevel:        slog.LevelDebug,
		StateBackend:    "valkey",
		ValkeyAddr:      "127.0.0.1:1", // 不可達
		TourInterval:    time.Hour,
		SSEDrain:        true,
	}
	buf := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s := New(cfg, logger)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	err := s.Run(ctx, make(chan os.Signal, 2))
	if err == nil {
		t.Fatal("valkey 不可用時 Run 應回錯誤(fail-fast,禁止無聲降級)")
	}
	if !strings.Contains(err.Error(), "valkey") {
		t.Errorf("錯誤訊息應指明 valkey,得到:%v", err)
	}
}
