package tour

import (
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

func TestHubCounting(t *testing.T) {
	g := prometheus.NewGauge(prometheus.GaugeOpts{Name: "test_sse_active"})
	h := NewHub(g)

	_, remove1 := h.Add()
	_, remove2 := h.Add()
	if h.Count() != 2 {
		t.Fatalf("兩條連線後 Count 應為 2,得到 %d", h.Count())
	}
	if v := testutil.ToFloat64(g); v != 2 {
		t.Errorf("gauge 應為 2,得到 %v", v)
	}

	remove1()
	if h.Count() != 1 || testutil.ToFloat64(g) != 1 {
		t.Errorf("移除一條後應為 1,得到 Count=%d gauge=%v", h.Count(), testutil.ToFloat64(g))
	}
	// 重複移除不得重複遞減
	remove1()
	if h.Count() != 1 || testutil.ToFloat64(g) != 1 {
		t.Errorf("重複移除不應改變計數,得到 Count=%d gauge=%v", h.Count(), testutil.ToFloat64(g))
	}
	remove2()
	if h.Count() != 0 || testutil.ToFloat64(g) != 0 {
		t.Errorf("全移除後應為 0,得到 Count=%d gauge=%v", h.Count(), testutil.ToFloat64(g))
	}
}

// Drain:所有連線的 bye channel 被關閉;之後的新連線立即收線(act3 關機排水)。
func TestHubDrain(t *testing.T) {
	h := NewHub(nil)
	bye1, _ := h.Add()
	bye2, _ := h.Add()

	h.Drain()
	for i, ch := range []<-chan struct{}{bye1, bye2} {
		select {
		case <-ch:
		default:
			t.Fatalf("Drain 後第 %d 條連線的 bye channel 應已關閉", i+1)
		}
	}
	// 排水中的新連線:立即收線
	bye3, remove3 := h.Add()
	select {
	case <-bye3:
	default:
		t.Fatal("Drain 後的新連線應立即收到 bye")
	}
	remove3()
	// 冪等
	h.Drain()
}

func TestHubNilGauge(t *testing.T) {
	h := NewHub(nil) // 測試場景允許無 gauge
	_, remove := h.Add()
	if h.Count() != 1 {
		t.Fatalf("nil gauge 不應影響計數,得到 %d", h.Count())
	}
	remove()
}
