package tour

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// ---- 測試工具 ----

func newTestServer(t *testing.T, store StateStore) *httptest.Server {
	t.Helper()
	tr := New(Options{
		Store:          store,
		Interval:       30 * time.Millisecond, // 測試用毫秒級節奏
		NoticeInterval: 40 * time.Millisecond,
		Hostname:       "test-pod",
		Log:            slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	mux := http.NewServeMux()
	mux.Handle("GET /tour", tr.Page())
	mux.Handle("GET /tour/events", tr.Events())
	mux.Handle("GET /tour/static/", tr.Static())
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

type frame struct {
	id, event, data, retry string
}

// readFrame 讀一個 SSE frame(至空行);2 秒無 frame 視為失敗。
func readFrame(t *testing.T, br *bufio.Reader) frame {
	t.Helper()
	ch := make(chan frame, 1)
	errCh := make(chan error, 1)
	go func() {
		var f frame
		for {
			line, err := br.ReadString('\n')
			if err != nil {
				errCh <- err
				return
			}
			line = strings.TrimRight(line, "\n")
			switch {
			case line == "":
				ch <- f
				return
			case strings.HasPrefix(line, "id: "):
				f.id = strings.TrimPrefix(line, "id: ")
			case strings.HasPrefix(line, "event: "):
				f.event = strings.TrimPrefix(line, "event: ")
			case strings.HasPrefix(line, "data: "):
				f.data = strings.TrimPrefix(line, "data: ")
			case strings.HasPrefix(line, "retry: "):
				f.retry = strings.TrimPrefix(line, "retry: ")
			}
		}
	}()
	select {
	case f := <-ch:
		return f
	case err := <-errCh:
		t.Fatalf("讀取 SSE frame 失敗:%v", err)
	case <-time.After(2 * time.Second):
		t.Fatal("等待 SSE frame 逾時")
	}
	return frame{}
}

// openEvents 以指定 session cookie 連上 /tour/events,回傳讀取器。
func openEvents(t *testing.T, srv *httptest.Server, sessionID string) *bufio.Reader {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/tour/events", nil)
	req.AddCookie(&http.Cookie{Name: "hydra_session", Value: sessionID})
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("連線 /tour/events 失敗:%v", err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("/tour/events 應為 200,得到 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("Content-Type 應為 text/event-stream,得到 %s", ct)
	}
	return bufio.NewReader(resp.Body)
}

type helloData struct {
	Pod   string `json:"pod"`
	Seq   int    `json:"seq"`
	Phase string `json:"phase"`
}

type stationData struct {
	Seq     int    `json:"seq"`
	Name    string `json:"name"`
	NameEN  string `json:"name_en"`
	Desc    string `json:"desc"`
	Photo   string `json:"photo"`
	Credit  string `json:"credit"`
	License string `json:"license"`
	Pod     string `json:"pod"`
}

// ---- 測試 ----

func TestPageSetsCookieAndAnchors(t *testing.T) {
	srv := newTestServer(t, NewMemoryStore())
	resp, err := srv.Client().Get(srv.URL + "/tour")
	if err != nil {
		t.Fatalf("GET /tour 失敗:%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("應為 200,得到 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("Content-Type 應為 text/html,得到 %s", ct)
	}
	var sessionCookie *http.Cookie
	for _, c := range resp.Cookies() {
		if c.Name == "hydra_session" {
			sessionCookie = c
		}
	}
	if sessionCookie == nil {
		t.Fatal("首次造訪應發放 hydra_session cookie")
	}
	if len(sessionCookie.Value) != 32 {
		t.Errorf("session ID 應為 32 hex 字元,得到 %q", sessionCookie.Value)
	}
	if !sessionCookie.HttpOnly {
		t.Error("cookie 應為 HttpOnly")
	}
	body, _ := io.ReadAll(resp.Body)
	// 契約 DOM 錨點(contracts/tour-http.md)
	for _, anchor := range []string{
		`id="photo"`, `id="station-name"`, `id="station-desc"`, `id="progress"`,
		`id="pod-name"`, `id="conn-light"`, `id="credit"`, `id="start"`,
	} {
		if !strings.Contains(string(body), anchor) {
			t.Errorf("頁面缺少契約錨點 %s", anchor)
		}
	}
}

func TestPageKeepsExistingCookie(t *testing.T) {
	srv := newTestServer(t, NewMemoryStore())
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/tour", nil)
	req.AddCookie(&http.Cookie{Name: "hydra_session", Value: strings.Repeat("a", 32)})
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("GET /tour 失敗:%v", err)
	}
	defer resp.Body.Close()
	for _, c := range resp.Cookies() {
		if c.Name == "hydra_session" {
			t.Error("已有 session cookie 時不應重發")
		}
	}
}

func TestEventsRequiresCookie(t *testing.T) {
	srv := newTestServer(t, NewMemoryStore())
	resp, err := srv.Client().Get(srv.URL + "/tour/events")
	if err != nil {
		t.Fatalf("GET /tour/events 失敗:%v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("無 cookie 應為 400,得到 %d", resp.StatusCode)
	}
}

// 新訪客:retry → hello(seq=1) → station(1) → 間隔後 station(2)。
func TestEventsFreshVisitorFlow(t *testing.T) {
	srv := newTestServer(t, NewMemoryStore())
	br := openEvents(t, srv, strings.Repeat("b", 32))

	f := readFrame(t, br)
	if f.retry != "1000" {
		t.Fatalf("首 frame 應為 retry: 1000,得到 %+v", f)
	}
	f = readFrame(t, br)
	if f.event != "hello" {
		t.Fatalf("第二 frame 應為 hello,得到 %+v", f)
	}
	var h helloData
	if err := json.Unmarshal([]byte(f.data), &h); err != nil {
		t.Fatalf("hello data 不是合法 JSON:%v", err)
	}
	if h.Pod != "test-pod" || h.Seq != 1 || h.Phase != PhaseTouring {
		t.Errorf("hello 應為 test-pod/1/touring,得到 %+v", h)
	}

	f = readFrame(t, br)
	if f.event != "station" || f.id != "1" {
		t.Fatalf("應立即重推目前站(id:1),得到 %+v", f)
	}
	var st stationData
	if err := json.Unmarshal([]byte(f.data), &st); err != nil {
		t.Fatalf("station data 不是合法 JSON:%v", err)
	}
	if st.Seq != 1 || st.Name != "遊客中心" || st.Pod != "test-pod" {
		t.Errorf("第 1 站內容不符,得到 %+v", st)
	}
	if !strings.HasPrefix(st.Photo, "/tour/static/") {
		t.Errorf("photo 應為 /tour/static/ 路徑,得到 %s", st.Photo)
	}
	if st.Credit == "" || st.License == "" {
		t.Errorf("station 事件應含出處欄位,得到 %+v", st)
	}

	f = readFrame(t, br)
	if f.event != "station" || f.id != "2" {
		t.Errorf("間隔後應推進至第 2 站,得到 %+v", f)
	}
}

// 帶進度的 session 重連:hello.seq 不回退、續播自當前站(SC-001 的機器可讀依據)。
func TestEventsResumeFromStore(t *testing.T) {
	store := NewMemoryStore()
	id := strings.Repeat("c", 32)
	if err := store.Set(context.Background(), Session{ID: id, Seq: 3, Phase: PhaseTouring}); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store)
	br := openEvents(t, srv, id)

	_ = readFrame(t, br) // retry
	f := readFrame(t, br)
	var h helloData
	if err := json.Unmarshal([]byte(f.data), &h); err != nil {
		t.Fatalf("hello data 不是合法 JSON:%v", err)
	}
	if h.Seq != 3 {
		t.Fatalf("resume 後 hello.seq 應為 3(不回退),得到 %d", h.Seq)
	}
	f = readFrame(t, br)
	if f.event != "station" || f.id != "3" {
		t.Errorf("應自第 3 站續播,得到 %+v", f)
	}
}

// announce 階段重連:停在公告模式,收到 notice(spec edge case)。
func TestEventsAnnouncePhase(t *testing.T) {
	store := NewMemoryStore()
	id := strings.Repeat("d", 32)
	last := len(Stations())
	if err := store.Set(context.Background(), Session{ID: id, Seq: last, Phase: PhaseAnnounce}); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store)
	br := openEvents(t, srv, id)

	_ = readFrame(t, br) // retry
	f := readFrame(t, br)
	var h helloData
	_ = json.Unmarshal([]byte(f.data), &h)
	if h.Phase != PhaseAnnounce {
		t.Fatalf("hello.phase 應為 announce,得到 %+v", h)
	}
	f = readFrame(t, br)
	if f.event != "notice" {
		t.Fatalf("announce 階段首事件應為 notice,得到 %+v", f)
	}
	if !strings.Contains(f.data, "text") {
		t.Errorf("notice data 應含 text 欄位,得到 %s", f.data)
	}
}

// 走到最後一站後轉入公告模式(handler 層的完整轉移)。
func TestEventsTransitionToAnnounce(t *testing.T) {
	store := NewMemoryStore()
	id := strings.Repeat("e", 32)
	last := len(Stations())
	if err := store.Set(context.Background(), Session{ID: id, Seq: last, Phase: PhaseTouring}); err != nil {
		t.Fatal(err)
	}
	srv := newTestServer(t, store)
	br := openEvents(t, srv, id)

	_ = readFrame(t, br) // retry
	_ = readFrame(t, br) // hello
	f := readFrame(t, br)
	if f.event != "station" || f.id != "7" {
		t.Fatalf("應重推第 7 站,得到 %+v", f)
	}
	f = readFrame(t, br)
	if f.event != "notice" {
		t.Fatalf("第 7 站之後應轉公告模式,得到 %+v", f)
	}
	// 進度應已落 store(平時外部化,FR-010)
	s, found, _ := store.Get(context.Background(), id)
	if !found || s.Phase != PhaseAnnounce {
		t.Errorf("store 應記錄 announce 轉移,得到 %+v found=%v", s, found)
	}
}

func TestStaticUnknown404(t *testing.T) {
	srv := newTestServer(t, NewMemoryStore())
	resp, err := srv.Client().Get(srv.URL + "/tour/static/nope.jpg")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("未知檔案應 404,得到 %d", resp.StatusCode)
	}
}

func TestStaticServesStationPhoto(t *testing.T) {
	srv := newTestServer(t, NewMemoryStore())
	resp, err := srv.Client().Get(srv.URL + "/tour/static/" + Stations()[0].Photo)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("站點照片應可取得,得到 %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Errorf("Content-Type 應為 image/jpeg,得到 %s", ct)
	}
}
