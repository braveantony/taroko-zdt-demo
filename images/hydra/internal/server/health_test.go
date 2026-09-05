package server

import (
	"sync"
	"testing"
)

func TestHealthInitialStateNotReady(t *testing.T) {
	h := NewHealth()
	if h.IsReady() {
		t.Error("starting 狀態不應為 ready")
	}
}

func TestHealthSetReady(t *testing.T) {
	h := NewHealth()
	h.SetReady()
	if !h.IsReady() {
		t.Error("SetReady 後應為 ready")
	}
}

func TestHealthDrainingIrreversible(t *testing.T) {
	h := NewHealth()
	h.SetReady()
	h.StartDraining()
	if h.IsReady() {
		t.Error("StartDraining 後不應為 ready")
	}
	// draining 不可逆:再 SetReady 也不能回到 ready(spec: data-model 狀態機)
	h.SetReady()
	if h.IsReady() {
		t.Error("draining 後 SetReady 不得使狀態回到 ready(不可逆)")
	}
}

func TestHealthDrainingIdempotent(t *testing.T) {
	h := NewHealth()
	h.SetReady()
	h.StartDraining()
	h.StartDraining() // 第二次呼叫不 panic、狀態不變
	if h.IsReady() {
		t.Error("重複 StartDraining 後仍不應為 ready")
	}
}

func TestHealthStateNames(t *testing.T) {
	h := NewHealth()
	if h.State() != "starting" {
		t.Errorf("初始 State 應為 starting,得到 %q", h.State())
	}
	h.SetReady()
	if h.State() != "ready" {
		t.Errorf("SetReady 後 State 應為 ready,得到 %q", h.State())
	}
	h.StartDraining()
	if h.State() != "draining" {
		t.Errorf("StartDraining 後 State 應為 draining,得到 %q", h.State())
	}
}

func TestHealthConcurrentAccess(t *testing.T) {
	h := NewHealth()
	h.SetReady()

	var wg sync.WaitGroup
	for range 50 {
		wg.Add(3)
		go func() { defer wg.Done(); h.SetReady() }()
		go func() { defer wg.Done(); h.StartDraining() }()
		go func() { defer wg.Done(); _ = h.IsReady() }()
	}
	wg.Wait()

	// 至少發生過一次 StartDraining → 終態必為非 ready
	if h.IsReady() {
		t.Error("並發翻轉後(含 StartDraining)終態不應為 ready")
	}
}
