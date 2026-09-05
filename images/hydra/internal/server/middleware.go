package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// quietPaths 的 request log 記 debug 級:kubelet probe 與 Prometheus 抓取
// 每幾秒就來一次,info 級會淹沒關機序列旁白(spec clarify 決議)。
var quietPaths = map[string]bool{
	"/healthz": true,
	"/readyz":  true,
	"/metrics": true,
}

// instrumented 以 client_golang 官方 instrument 函式包裝單一路由(B 組裁定):
// path label 於註冊時 curry 固定 — 未註冊路徑不產生 metrics,基數炸彈由結構杜絕。
func (s *Server) instrumented(path string, next http.Handler) http.Handler {
	cnt := s.reqTotal.MustCurryWith(prometheus.Labels{"path": path})
	dur := s.reqDuration.MustCurryWith(prometheus.Labels{"path": path})
	return promhttp.InstrumentHandlerDuration(dur,
		promhttp.InstrumentHandlerCounter(cnt, next))
}

// statusRecorder 攔截回應狀態碼供 request log 使用。
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flush 轉發給底層(SSE 需要;不轉發會讓 /tour/events 的 Flusher 斷言失敗)。
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// logRequests:每請求一行結構化 log(metrics 已改由 per-route instrument 承擔)。
func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		dur := time.Since(start)

		level := slog.LevelInfo
		if quietPaths[r.URL.Path] {
			level = slog.LevelDebug
		}
		s.log.Log(r.Context(), level, "request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration_ms", float64(dur.Microseconds())/1000.0,
		)
	})
}
