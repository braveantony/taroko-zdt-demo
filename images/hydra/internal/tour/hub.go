package tour

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
)

// Hub 登記活躍 SSE 連線:計數供 sse_active_connections gauge,
// 關機排水時廣播收線(act3,HYDRA_SSE_DRAIN=on;見 contracts/tour-http.md 關機序列)。
type Hub struct {
	mu       sync.Mutex
	conns    map[int]chan struct{}
	next     int
	draining bool
	gauge    prometheus.Gauge // 可為 nil(測試)
}

func NewHub(g prometheus.Gauge) *Hub {
	return &Hub{conns: make(map[int]chan struct{}), gauge: g}
}

// Add 登記一條連線。回傳 bye channel(Drain 時關閉,handler 據此送 bye 並收線)
// 與移除函式(冪等;handler 返回時呼叫)。
func (h *Hub) Add() (<-chan struct{}, func()) {
	h.mu.Lock()
	defer h.mu.Unlock()

	ch := make(chan struct{})
	if h.draining {
		close(ch) // 排水中的新連線立即收線
		return ch, func() {}
	}
	id := h.next
	h.next++
	h.conns[id] = ch
	if h.gauge != nil {
		h.gauge.Inc()
	}
	removed := false
	return ch, func() {
		h.mu.Lock()
		defer h.mu.Unlock()
		if removed {
			return
		}
		removed = true
		if _, ok := h.conns[id]; ok {
			delete(h.conns, id)
			if h.gauge != nil {
				h.gauge.Dec()
			}
		}
	}
}

// Count 目前活躍連線數。
func (h *Hub) Count() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.conns)
}

// Drain 廣播收線:關閉所有 bye channel;之後的新連線一律立即收線。
// 冪等;由關機序列呼叫(internal/server Run)。
func (h *Hub) Drain() {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.draining {
		return
	}
	h.draining = true
	for _, ch := range h.conns {
		close(ch)
	}
}
