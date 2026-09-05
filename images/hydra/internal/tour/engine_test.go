package tour

import "testing"

func TestNewSession(t *testing.T) {
	s := NewSession("abc")
	if s.ID != "abc" || s.Seq != 1 || s.Phase != PhaseTouring {
		t.Errorf("NewSession 應從第 1 站 touring 開始,得到 %+v", s)
	}
}

func TestAdvanceThroughStations(t *testing.T) {
	s := NewSession("abc")
	for want := 2; want <= len(Stations()); want++ {
		s = Advance(s)
		if s.Seq != want || s.Phase != PhaseTouring {
			t.Fatalf("推進後應為第 %d 站 touring,得到 %+v", want, s)
		}
	}
}

// 第 7 站之後轉入公告模式(單向,data-model 狀態轉移)。
func TestAdvanceToAnnounce(t *testing.T) {
	s := Session{ID: "abc", Seq: len(Stations()), Phase: PhaseTouring}
	s = Advance(s)
	if s.Phase != PhaseAnnounce {
		t.Fatalf("第 %d 站推進後應轉 announce,得到 %+v", len(Stations()), s)
	}
	if s.Seq != len(Stations()) {
		t.Errorf("轉 announce 時站序應停留在 %d,得到 %d", len(Stations()), s.Seq)
	}
	// announce 為終態:再推進不變
	again := Advance(s)
	if again != s {
		t.Errorf("announce 後 Advance 應不變,得到 %+v", again)
	}
}

func TestStationsContract(t *testing.T) {
	st := Stations()
	if len(st) != 7 {
		t.Fatalf("站點應為 7 站,得到 %d", len(st))
	}
	for i, s := range st {
		if s.Seq != i+1 {
			t.Errorf("站序應連續:第 %d 筆 Seq=%d", i, s.Seq)
		}
		if s.Name == "" || s.NameEN == "" || s.Desc == "" || s.Photo == "" ||
			s.Credit == "" || s.License == "" || s.SourceURL == "" {
			t.Errorf("站點 %d 欄位不得為空(data-model Station):%+v", s.Seq, s)
		}
	}
	if st[0].Name != "遊客中心" || st[6].Name != "清水斷崖" {
		t.Errorf("站序應為遊客中心→…→清水斷崖,得到 %s→…→%s", st[0].Name, st[6].Name)
	}
}

// 公告文案輪播:索引遞增循環,不越界。
func TestNoticeCycling(t *testing.T) {
	first := NoticeText(0)
	if first == "" {
		t.Fatal("公告文案不得為空")
	}
	n := noticeCount()
	if NoticeText(n) != first {
		t.Errorf("公告應循環:第 %d 則應等於第 0 則", n)
	}
}
