package server

import (
	"bufio"
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.com/kubeantony/kubernetes-summit-2026/internal/config"
)

// SSE 必須穿過完整 middleware 鏈(instrumented + logRequests)仍可串流:
// statusRecorder 若不轉發 Flush,/tour/events 會 500(specs/006 T014 整合鎖)。
func TestTourEventsThroughMiddleware(t *testing.T) {
	cfg := config.Config{
		ShutdownTimeout: time.Second,
		TourInterval:    50 * time.Millisecond,
		StateBackend:    "memory",
	}
	s := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/tour/events", nil)
	req.AddCookie(&http.Cookie{Name: "hydra_session", Value: strings.Repeat("f", 32)})
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("連線 /tour/events 失敗:%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("穿過 middleware 的 SSE 應為 200,得到 %d", resp.StatusCode)
	}

	br := bufio.NewReader(resp.Body)
	var got []string
	for len(got) < 3 { // retry → hello → station
		line, err := br.ReadString('\n')
		if err != nil {
			t.Fatalf("串流中斷(Flusher 未轉發?):%v;已讀 %v", err, got)
		}
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "retry:") || strings.HasPrefix(line, "event:") {
			got = append(got, line)
		}
	}
	if got[0] != "retry: 1000" || got[1] != "event: hello" || got[2] != "event: station" {
		t.Errorf("事件順序應為 retry→hello→station,得到 %v", got)
	}
}

// /tour 頁面經完整鏈仍正常(cookie 發放 + HTML)。
func TestTourPageThroughMiddleware(t *testing.T) {
	cfg := config.Config{ShutdownTimeout: time.Second, TourInterval: time.Second, StateBackend: "memory"}
	s := New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()

	resp, err := srv.Client().Get(srv.URL + "/tour")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /tour 應為 200,得到 %d", resp.StatusCode)
	}
	found := false
	for _, c := range resp.Cookies() {
		if c.Name == "hydra_session" {
			found = true
		}
	}
	if !found {
		t.Error("應發放 hydra_session cookie")
	}
}
