// Package server 實作 hydra 的 HTTP 服務與優雅關機序列。
package server

import "sync/atomic"

// Health 狀態值:starting → ready → draining(單向)。
const (
	stateStarting int32 = iota
	stateReady
	stateDraining
)

// Health 是並發安全的就緒狀態機。
type Health struct {
	state atomic.Int32
}

// NewHealth 建立初始為 starting(非 ready)的狀態機。
func NewHealth() *Health { return &Health{} }

// SetReady 將狀態由 starting 轉為 ready;draining 之後呼叫無效(不可逆)。
func (h *Health) SetReady() {
	h.state.CompareAndSwap(stateStarting, stateReady)
}

// StartDraining 將狀態轉為 draining(冪等、不可逆)。
func (h *Health) StartDraining() {
	h.state.Store(stateDraining)
}

// IsReady 回報是否可接收流量。
func (h *Health) IsReady() bool {
	return h.state.Load() == stateReady
}

// State 回傳人類可讀的狀態名(/readyz body 用;A4 修正)。
func (h *Health) State() string {
	switch h.state.Load() {
	case stateReady:
		return "ready"
	case stateDraining:
		return "draining"
	default:
		return "starting"
	}
}
