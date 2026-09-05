package server

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"gitlab.com/kubeantony/kubernetes-summit-2026/internal/config"
)

func newTestServerWithLog(t *testing.T) (*Server, *httptest.Server, *syncBuffer) {
	t.Helper()
	buf := &syncBuffer{}
	logger := slog.New(slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	s := New(config.Config{Port: 0, ShutdownTimeout: time.Second, LogLevel: slog.LevelDebug}, logger)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts, buf
}

// requestLogs 取出 msg=="request" 的結構化記錄。
func requestLogs(t *testing.T, buf *syncBuffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("log 非合法 JSON:%s", line)
		}
		if entry["msg"] == "request" {
			out = append(out, entry)
		}
	}
	return out
}

func TestRequestLogFieldsAndLevels(t *testing.T) {
	s, ts, buf := newTestServerWithLog(t)
	s.Health().SetReady()

	mustGet(t, ts.URL+"/")        // 業務端點 → info
	mustGet(t, ts.URL+"/healthz") // probe → debug
	mustGet(t, ts.URL+"/readyz")  // probe → debug
	mustGet(t, ts.URL+"/metrics") // metrics → debug

	logs := requestLogs(t, buf)
	if len(logs) != 4 {
		t.Fatalf("應有 4 筆 request log,得到 %d:%v", len(logs), logs)
	}

	byPath := map[string]map[string]any{}
	for _, l := range logs {
		byPath[l["path"].(string)] = l
	}

	root := byPath["/"]
	if root == nil {
		t.Fatal("缺少 / 的 request log")
	}
	if root["level"] != "INFO" {
		t.Errorf("/ 的 log 應為 INFO,得到 %v", root["level"])
	}
	for _, field := range []string{"method", "path", "status", "duration_ms"} {
		if _, ok := root[field]; !ok {
			t.Errorf("request log 缺少欄位 %s:%v", field, root)
		}
	}
	if root["method"] != "GET" || root["status"].(float64) != 200 {
		t.Errorf("/ 的 log 欄位錯誤:%v", root)
	}

	for _, p := range []string{"/healthz", "/readyz", "/metrics"} {
		entry := byPath[p]
		if entry == nil {
			t.Fatalf("缺少 %s 的 request log", p)
		}
		if entry["level"] != "DEBUG" {
			t.Errorf("%s 的 log 應為 DEBUG(probe 靜音),得到 %v", p, entry["level"])
		}
	}
}

// B 組標準化(2026-08-24 裁定):泛用指標名(app 身分靠 scrape 的 job label)、
// client_golang 官方 instrument 函式(method label 為小寫)、標準 go_*/process_* collectors、
// 未註冊路徑不進 metrics(per-route currying,基數炸彈由結構杜絕,不再需要 other 桶)。
func TestMetricsCounterAndHistogram(t *testing.T) {
	s, ts, _ := newTestServerWithLog(t)
	s.Health().SetReady()

	mustGet(t, ts.URL+"/")
	mustGet(t, ts.URL+"/")
	mustGet(t, ts.URL+"/slow?seconds=0")
	http.Get(ts.URL + "/no-such-path") // 未註冊路徑:不產生 metrics

	_, body := get(t, ts.URL+"/metrics")

	if !strings.Contains(body, `http_requests_total{code="200",method="get",path="/"} 2`) {
		t.Errorf("metrics 應含 / 的 200 計數 2,body 片段:%s", grepLines(body, "http_requests_total"))
	}
	if !strings.Contains(body, `http_requests_total{code="200",method="get",path="/slow"} 1`) {
		t.Errorf("/slow 應有獨立 path label,body 片段:%s", grepLines(body, "http_requests_total"))
	}
	if strings.Contains(body, "hydra_http_") {
		t.Error("不應再有 hydra_ prefix(泛用名 + job label)")
	}
	if strings.Contains(body, `path="other"`) || strings.Contains(body, "no-such-path") {
		t.Errorf("未註冊路徑不應出現在 metrics:%s", grepLines(body, "other"))
	}
	if !strings.Contains(body, "http_request_duration_seconds_bucket") {
		t.Error("metrics 應含延遲 histogram")
	}
	// 標準 collectors(B-1)
	if !strings.Contains(body, "go_goroutines") {
		t.Error("metrics 應含標準 Go runtime collector(go_goroutines)")
	}
	if !strings.Contains(body, "process_cpu_seconds_total") {
		t.Error("metrics 應含標準 process collector(process_cpu_seconds_total)")
	}
}

func mustGet(t *testing.T, url string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s 失敗:%v", url, err)
	}
	resp.Body.Close()
}

func grepLines(s, substr string) string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, substr) {
			out = append(out, line)
		}
	}
	return strings.Join(out, "\n")
}
