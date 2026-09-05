package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promauto"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"gitlab.com/kubeantony/kubernetes-summit-2026/internal/config"
	"gitlab.com/kubeantony/kubernetes-summit-2026/internal/tour"
	"gitlab.com/kubeantony/kubernetes-summit-2026/internal/version"
)

// ErrForcedExit 表示關機期間收到第二次訊號而被強制結束(main 據此 exit 非 0)。
var ErrForcedExit = errors.New("second signal received")

// Server 組裝 http.Server、路由與就緒狀態,並負責優雅關機序列(ADR-0004)。
type Server struct {
	cfg     config.Config
	log     *slog.Logger
	health  *Health
	mux     *http.ServeMux
	started chan struct{}
	addr    string

	// metrics:每個 Server 一個 registry(測試可建多個實例不衝突)
	reqTotal       *prometheus.CounterVec
	reqDuration    *prometheus.HistogramVec
	metricsHandler http.Handler

	// 006 導覽功能(specs/006):SSE 端點與連線 Hub(關機排水用)
	tour  *tour.Tour
	store tour.StateStore
}

// New 建立尚未啟動的 Server。
func New(cfg config.Config, logger *slog.Logger) *Server {
	// 泛用指標名 + 標準 collectors(B 組裁定):app 身分由 scrape 端的 job label 區分
	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	s := &Server{
		cfg:     cfg,
		log:     logger,
		health:  NewHealth(),
		mux:     http.NewServeMux(),
		started: make(chan struct{}),
		reqTotal: promauto.With(reg).NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "HTTP 請求總數(依狀態碼/方法/路由)。",
		}, []string{"code", "method", "path"}),
		reqDuration: promauto.With(reg).NewHistogramVec(prometheus.HistogramOpts{
			Name: "http_request_duration_seconds",
			Help: "HTTP 請求延遲分布。",
		}, []string{"method", "path"}),
		metricsHandler: promhttp.HandlerFor(reg, promhttp.HandlerOpts{}),
	}
	s.store = newStateStore(cfg)
	s.tour = tour.New(tour.Options{
		Store: s.store,
		Gauge: promauto.With(reg).NewGauge(prometheus.GaugeOpts{
			Name: "sse_active_connections",
			Help: "目前活躍的 SSE 導覽連線數(specs/006 FR-014)。",
		}),
		Interval: cfg.TourInterval,
		Log:      logger,
	})
	s.registerRoutes()
	return s
}

// newStateStore 依設定選擇進度存放後端(specs/006 contracts/config.md)。
func newStateStore(cfg config.Config) tour.StateStore {
	if cfg.StateBackend == "valkey" {
		return tour.NewValkeyStore(cfg.ValkeyAddr)
	}
	return tour.NewMemoryStore()
}

// Health 曝露就緒狀態機(handlers 與測試使用)。
func (s *Server) Health() *Health { return s.health }

// Started 在 HTTP listener 就緒後關閉。
func (s *Server) Started() <-chan struct{} { return s.started }

// Addr 回傳實際監聽位址(Started 之後有效;Port=0 時為隨機 port)。
func (s *Server) Addr() string { return s.addr }

// HandleFunc 註冊路由(Go 1.22+ method pattern)。
func (s *Server) HandleFunc(pattern string, h func(http.ResponseWriter, *http.Request)) {
	s.mux.HandleFunc(pattern, h)
}

// Handler 回傳對外服務的 http.Handler(Run 與 httptest 共用同一入口)。
func (s *Server) Handler() http.Handler { return s.logRequests(s.mux) }

// Run 啟動服務並阻塞至關機完成。signals 由呼叫端注入(可測試性,research.md R4);
// 回傳 nil = 乾淨關機(exit 0),非 nil = 強制結束或逾時(exit 非 0)。
func (s *Server) Run(ctx context.Context, signals <-chan os.Signal) error {
	// 狀態庫自檢(FR-012):valkey 不可用即 fail-fast,禁止無聲降級為記憶體模式
	if s.cfg.StateBackend == "valkey" {
		pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.store.Ping(pingCtx)
		cancel()
		if err != nil {
			return fmt.Errorf("valkey 狀態庫不可用(%s): %w", s.cfg.ValkeyAddr, err)
		}
		s.log.Info("valkey state store connected", "addr", s.cfg.ValkeyAddr)
	}

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", s.cfg.Port))
	if err != nil {
		return fmt.Errorf("無法監聽 port %d: %w", s.cfg.Port, err)
	}
	s.addr = ln.Addr().String()

	httpSrv := &http.Server{Handler: s.Handler()}
	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.Serve(ln) }()

	s.health.SetReady()
	s.log.Info("server started", "version", version.Version, "addr", s.addr)
	close(s.started)

	select {
	case sig := <-signals:
		s.log.Info("shutdown signal received", "signal", sig.String())
	case err := <-serveErr:
		return fmt.Errorf("http server 意外終止: %w", err)
	case <-ctx.Done():
		s.log.Info("shutdown signal received", "signal", "context canceled")
	}

	// 1. 翻轉就緒(defense-in-depth:排水由 K8s 層的 preStop sleep 承擔,
	//    EndpointSlice terminating 語意會先把本 Pod 移出 LB;見 ADR-0004 修訂)
	s.health.StartDraining()
	s.log.Info("readiness flipped to draining")

	// 1.5 SSE 連線善終(act3,specs/006 FR-010b):廣播 bye 並收線,
	//     讓下方 Shutdown 不被永不結束的長連線卡到逾時;off 時行為與 001 完全相同
	if s.cfg.SSEDrain {
		s.log.Info("draining sse connections", "count", s.tour.Hub().Count())
		s.tour.Hub().Drain()
	}

	// 關機全程監看第二次訊號 → 強制結束
	forced := make(chan os.Signal, 1)
	watchDone := make(chan struct{})
	defer close(watchDone)
	go func() {
		select {
		case sig := <-signals:
			forced <- sig
		case <-watchDone:
		}
	}()

	// 2. 停收新連線並完成在途請求
	s.log.Info("shutting down http server")
	shutdownCtx, cancel := context.WithTimeout(context.Background(), s.cfg.ShutdownTimeout)
	defer cancel()

	shutdownErr := make(chan error, 1)
	go func() { shutdownErr <- httpSrv.Shutdown(shutdownCtx) }()

	select {
	case err := <-shutdownErr:
		if err != nil {
			s.log.Error("shutdown timeout exceeded", "error", err.Error())
			httpSrv.Close()
			return fmt.Errorf("shutdown timeout exceeded: %w", err)
		}
	case sig := <-forced:
		s.log.Error("second signal received, forcing exit", "signal", sig.String())
		httpSrv.Close()
		return ErrForcedExit
	}

	s.log.Info("shutdown complete")
	return nil
}
