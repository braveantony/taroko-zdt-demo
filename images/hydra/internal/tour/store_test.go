package tour

import (
	"context"
	"testing"
)

func TestMemoryStoreGetUnknown(t *testing.T) {
	st := NewMemoryStore()
	_, found, err := st.Get(context.Background(), "nobody")
	if err != nil {
		t.Fatalf("Get 不應失敗:%v", err)
	}
	if found {
		t.Error("未知 session 應回 found=false")
	}
}

func TestMemoryStoreRoundTrip(t *testing.T) {
	st := NewMemoryStore()
	want := Session{ID: "v1", Seq: 3, Phase: PhaseTouring}
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
}

func TestMemoryStorePing(t *testing.T) {
	if err := NewMemoryStore().Ping(context.Background()); err != nil {
		t.Errorf("memory store Ping 應恆成功:%v", err)
	}
}
