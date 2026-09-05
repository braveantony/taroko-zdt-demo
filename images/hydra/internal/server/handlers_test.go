package server

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"gitlab.com/kubeantony/kubernetes-summit-2026/internal/config"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server) {
	t.Helper()
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	s := New(config.Config{Port: 0, ShutdownTimeout: 15 * time.Second, LogLevel: slog.LevelInfo}, logger)
	ts := httptest.NewServer(s.Handler())
	t.Cleanup(ts.Close)
	return s, ts
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatalf("GET %s 失敗:%v", url, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestHealthzAlwaysOK(t *testing.T) {
	s, ts := newTestServer(t)

	// starting 狀態:活著即 200
	if code, body := get(t, ts.URL+"/healthz"); code != 200 || body != "ok" {
		t.Errorf("starting 時 /healthz 應為 200 ok,得到 %d %q", code, body)
	}

	s.Health().SetReady()
	s.Health().StartDraining()
	// draining 狀態:仍然 200(liveness 與 readiness 分離)
	if code, _ := get(t, ts.URL+"/healthz"); code != 200 {
		t.Errorf("draining 時 /healthz 應為 200,得到 %d", code)
	}
}

func TestReadyzFollowsState(t *testing.T) {
	s, ts := newTestServer(t)

	// starting:503,body 反映實際狀態(A4 修正)
	if code, body := get(t, ts.URL+"/readyz"); code != 503 || body != "starting" {
		t.Errorf("starting 時 /readyz 應為 503 starting,得到 %d %q", code, body)
	}

	s.Health().SetReady()
	if code, body := get(t, ts.URL+"/readyz"); code != 200 || body != "ok" {
		t.Errorf("ready 時 /readyz 應為 200 ok,得到 %d %q", code, body)
	}

	s.Health().StartDraining()
	if code, body := get(t, ts.URL+"/readyz"); code != 503 || body != "draining" {
		t.Errorf("draining 時 /readyz 應為 503 draining,得到 %d %q", code, body)
	}
}

func TestRootReturnsVersionHostnameTimestamp(t *testing.T) {
	_, ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET / 應為 200,得到 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type 應為 application/json,得到 %q", ct)
	}
	var payload struct {
		Version   string `json:"version"`
		Hostname  string `json:"hostname"`
		Timestamp string `json:"timestamp"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("回應非合法 JSON:%v", err)
	}
	if payload.Version != "dev" {
		t.Errorf("version 應為 dev(未注入),得到 %q", payload.Version)
	}
	host, _ := os.Hostname()
	if payload.Hostname != host {
		t.Errorf("hostname 應為 %q,得到 %q", host, payload.Hostname)
	}
	ts2, err := time.Parse(time.RFC3339, payload.Timestamp)
	if err != nil {
		t.Fatalf("timestamp 非 RFC3339:%q", payload.Timestamp)
	}
	if d := time.Since(ts2); d < -time.Minute || d > time.Minute {
		t.Errorf("timestamp 偏差過大:%v", d)
	}
}

func TestVersionEndpoint(t *testing.T) {
	_, ts := newTestServer(t)
	if code, body := get(t, ts.URL+"/version"); code != 200 || body != "dev" {
		t.Errorf("GET /version 應為 200 dev,得到 %d %q", code, body)
	}
}

func TestUnknownPathIs404(t *testing.T) {
	_, ts := newTestServer(t)
	if code, _ := get(t, ts.URL+"/no-such-path"); code != 404 {
		t.Errorf("未知路徑應為 404,得到 %d", code)
	}
}

func TestParseSlowSeconds(t *testing.T) {
	cases := []struct {
		in      string
		want    time.Duration
		wantErr bool
	}{
		{"", 3 * time.Second, false}, // 預設 3 秒
		{"0", 0, false},
		{"60", 60 * time.Second, false},
		{"61", 0, true},
		{"-1", 0, true},
		{"abc", 0, true},
	}
	for _, tc := range cases {
		got, err := parseSlowSeconds(tc.in)
		if tc.wantErr && err == nil {
			t.Errorf("parseSlowSeconds(%q) 應回錯誤", tc.in)
		}
		if !tc.wantErr && (err != nil || got != tc.want) {
			t.Errorf("parseSlowSeconds(%q) = %v, %v;want %v", tc.in, got, err, tc.want)
		}
	}
}

func TestSlowEndpoint(t *testing.T) {
	_, ts := newTestServer(t)
	resp, err := http.Get(ts.URL + "/slow?seconds=0")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("GET /slow?seconds=0 應為 200,得到 %d", resp.StatusCode)
	}
	var payload struct {
		SleptSeconds int    `json:"slept_seconds"`
		Version      string `json:"version"`
		Hostname     string `json:"hostname"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		t.Fatalf("回應非合法 JSON:%v", err)
	}
	host, _ := os.Hostname()
	if payload.SleptSeconds != 0 || payload.Version != "dev" || payload.Hostname != host {
		t.Errorf("回應欄位錯誤:%+v", payload)
	}
}

func TestSlowEndpointRejectsInvalid(t *testing.T) {
	_, ts := newTestServer(t)
	for _, q := range []string{"seconds=61", "seconds=-1", "seconds=abc"} {
		if code, _ := get(t, ts.URL+"/slow?"+q); code != 400 {
			t.Errorf("GET /slow?%s 應為 400,得到 %d", q, code)
		}
	}
}

func TestSlowEndpointClientDisconnect(t *testing.T) {
	s, _ := newTestServer(t)
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequestWithContext(ctx, "GET", "/slow?seconds=5", nil)
	rec := httptest.NewRecorder()

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel() // 模擬 client 斷線
	}()
	start := time.Now()
	s.Handler().ServeHTTP(rec, req)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("client 斷線後 handler 應提早結束,實際等了 %v", elapsed)
	}
}

func TestHealthEndpointsRejectNonGET(t *testing.T) {
	_, ts := newTestServer(t)
	resp, err := http.Post(ts.URL+"/healthz", "text/plain", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Errorf("POST /healthz 應為 405,得到 %d", resp.StatusCode)
	}
}
