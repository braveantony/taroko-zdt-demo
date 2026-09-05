package tour

import (
	"context"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
)

func TestValkeyStoreRoundTrip(t *testing.T) {
	mr := miniredis.RunT(t)
	st := NewValkeyStore(mr.Addr())

	want := Session{ID: "v1", Seq: 3, Phase: PhaseTouring, UpdatedAt: time.Now()}
	if err := st.Set(context.Background(), want); err != nil {
		t.Fatalf("Set 不應失敗:%v", err)
	}
	got, found, err := st.Get(context.Background(), "v1")
	if err != nil || !found {
		t.Fatalf("Set 後 Get 應命中:found=%v err=%v", found, err)
	}
	if got.Seq != 3 || got.Phase != PhaseTouring {
		t.Errorf("Get 應回寫入值,得到 %+v", got)
	}
	// TTL 必須存在(過期自動清理,data-model:24h)
	if ttl := mr.TTL("tour:session:v1"); ttl <= 0 || ttl > 24*time.Hour {
		t.Errorf("key 應帶 24h 內的 TTL,得到 %v", ttl)
	}
}

func TestValkeyStoreGetUnknown(t *testing.T) {
	mr := miniredis.RunT(t)
	st := NewValkeyStore(mr.Addr())
	_, found, err := st.Get(context.Background(), "nobody")
	if err != nil {
		t.Fatalf("未知 session 不應回錯誤:%v", err)
	}
	if found {
		t.Error("未知 session 應回 found=false")
	}
}

// FR-012:狀態庫不可用必須回報錯誤(啟動 fail-fast 的依據)。
func TestValkeyStorePingDown(t *testing.T) {
	mr := miniredis.RunT(t)
	addr := mr.Addr()
	mr.Close()
	st := NewValkeyStore(addr)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := st.Ping(ctx); err == nil {
		t.Error("狀態庫關閉時 Ping 應回錯誤")
	}
}
