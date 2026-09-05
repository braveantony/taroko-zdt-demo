package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"time"

	"gitlab.com/kubeantony/kubernetes-summit-2026/internal/version"
)

// registerRoutes 掛載全部端點(契約見 specs/001 contracts/http-api.md);
// 每條路由以官方 instrument 包裝,path label 於此 curry 固定(B 組裁定)。
func (s *Server) registerRoutes() {
	s.mux.Handle("GET /{$}", s.instrumented("/", http.HandlerFunc(s.handleRoot))) // /{$} = 僅精確匹配根路徑
	s.mux.Handle("GET /version", s.instrumented("/version", http.HandlerFunc(s.handleVersion)))
	s.mux.Handle("GET /healthz", s.instrumented("/healthz", http.HandlerFunc(s.handleHealthz)))
	s.mux.Handle("GET /readyz", s.instrumented("/readyz", http.HandlerFunc(s.handleReadyz)))
	s.mux.Handle("GET /metrics", s.instrumented("/metrics", s.metricsHandler))
	s.mux.Handle("GET /slow", s.instrumented("/slow", http.HandlerFunc(s.handleSlow)))

	// 006 導覽功能(specs/006 contracts/tour-http.md)
	s.mux.Handle("GET /tour", s.instrumented("/tour", s.tour.Page()))
	s.mux.Handle("GET /tour/events", s.instrumented("/tour/events", s.tour.Events()))
	s.mux.Handle("GET /tour/static/", s.instrumented("/tour/static", s.tour.Static()))
}

// parseSlowSeconds 解析 /slow 的 seconds 參數(預設 3、範圍 0–60)。
func parseSlowSeconds(v string) (time.Duration, error) {
	if v == "" {
		return 3 * time.Second, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 || n > 60 {
		return 0, fmt.Errorf("seconds=%s 無效:必須是 0–60 的整數", v)
	}
	return time.Duration(n) * time.Second, nil
}

// handleSlow:模擬慢請求 — 演示在途請求跨越關機不被截斷(FR-011)。
func (s *Server) handleSlow(w http.ResponseWriter, r *http.Request) {
	d, err := parseSlowSeconds(r.URL.Query().Get("seconds"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-r.Context().Done():
		// client 斷線:提早結束,不留殭屍等待
		s.log.DebugContext(r.Context(), "slow request canceled", "requested_seconds", d.Seconds())
		return
	}

	host, _ := os.Hostname()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]any{
		"slept_seconds": int(d.Seconds()),
		"version":       version.Version,
		"hostname":      host,
	})
}

// handleRoot:demo 主視窗 — 觀察滾動更新時的版本交替。
func (s *Server) handleRoot(w http.ResponseWriter, r *http.Request) {
	host, _ := os.Hostname()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{
		"version":   version.Version,
		"hostname":  host,
		"timestamp": time.Now().Format(time.RFC3339),
	})
}

func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, version.Version)
}

// handleHealthz:liveness — 程序活著即 200,與就緒狀態無關。
func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	fmt.Fprint(w, "ok")
}

// handleReadyz:readiness — ready 時 200;starting/draining 時 503,body 反映實際狀態。
func (s *Server) handleReadyz(w http.ResponseWriter, r *http.Request) {
	if !s.health.IsReady() {
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, s.health.State())
		return
	}
	fmt.Fprint(w, "ok")
}
