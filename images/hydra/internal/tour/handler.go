package tour

import (
	"crypto/rand"
	"embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

//go:embed web/index.html web/photos
var webFS embed.FS

// Options 組裝參數;零值欄位套用預設(見 New)。
type Options struct {
	Store          StateStore
	Gauge          prometheus.Gauge // sse_active_connections;可為 nil
	Interval       time.Duration    // 導覽推進間隔(預設 10s)
	NoticeInterval time.Duration    // 公告間隔(預設 15s,兼保活)
	Hostname       string           // 預設 os.Hostname
	Log            *slog.Logger
}

// Tour 導覽功能的 HTTP 介面(契約 specs/006 contracts/tour-http.md)。
type Tour struct {
	store          StateStore
	hub            *Hub
	interval       time.Duration
	noticeInterval time.Duration
	pod            string
	log            *slog.Logger
	page           []byte
	photos         fs.FS
}

// New 建立 Tour;Store 與 Log 必填。
func New(o Options) *Tour {
	if o.Interval <= 0 {
		o.Interval = 10 * time.Second
	}
	if o.NoticeInterval <= 0 {
		o.NoticeInterval = 15 * time.Second
	}
	if o.Hostname == "" {
		o.Hostname, _ = os.Hostname()
	}
	page, err := webFS.ReadFile("web/index.html")
	if err != nil {
		panic(fmt.Sprintf("embed 缺 web/index.html: %v", err)) // 編譯期資產,缺=建置錯誤
	}
	photos, err := fs.Sub(webFS, "web/photos")
	if err != nil {
		panic(fmt.Sprintf("embed 缺 web/photos: %v", err))
	}
	return &Tour{
		store:          o.Store,
		hub:            NewHub(o.Gauge),
		interval:       o.Interval,
		noticeInterval: o.NoticeInterval,
		pod:            o.Hostname,
		log:            o.Log,
		page:           page,
		photos:         photos,
	}
}

// Hub 曝露連線登記處(關機序列呼叫 Drain)。
func (t *Tour) Hub() *Hub { return t.hub }

// Page GET /tour:導覽頁;首次造訪發放 session cookie。
func (t *Tour) Page() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie("hydra_session"); err != nil || c.Value == "" {
			buf := make([]byte, 16)
			if _, err := rand.Read(buf); err != nil {
				http.Error(w, "無法產生 session id", http.StatusInternalServerError)
				return
			}
			http.SetCookie(w, &http.Cookie{
				Name:     "hydra_session",
				Value:    hex.EncodeToString(buf),
				Path:     "/tour",
				MaxAge:   86400,
				HttpOnly: true,
				SameSite: http.SameSiteLaxMode,
			})
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write(t.page)
	})
}

// Static GET /tour/static/{file}:embed 照片。
func (t *Tour) Static() http.Handler {
	fileServer := http.StripPrefix("/tour/static/", http.FileServerFS(t.photos))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=86400")
		fileServer.ServeHTTP(w, r)
	})
}

// Events GET /tour/events:SSE 事件流(hello → station/notice …;drain 時 bye)。
func (t *Tour) Events() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie("hydra_session")
		if err != nil || c.Value == "" {
			http.Error(w, "missing hydra_session cookie(先造訪 /tour)", http.StatusBadRequest)
			return
		}
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("X-Accel-Buffering", "no")
		fmt.Fprint(w, "retry: 1000\n\n") // EventSource 重連間隔提示(契約)
		fl.Flush()

		ctx := r.Context()
		s, found, err := t.store.Get(ctx, c.Value)
		if err != nil {
			t.log.ErrorContext(ctx, "讀取進度失敗", "error", err.Error())
			return
		}
		if !found {
			s = NewSession(c.Value)
			s.UpdatedAt = time.Now()
			if err := t.store.Set(ctx, s); err != nil {
				t.log.ErrorContext(ctx, "寫入進度失敗", "error", err.Error())
				return
			}
		}

		bye, remove := t.hub.Add()
		defer remove()

		t.writeEvent(w, fl, "", "hello", map[string]any{
			"pod": t.pod, "seq": s.Seq, "phase": s.Phase,
		})
		t.log.InfoContext(ctx, "tour connected",
			"session", s.ID[:8], "seq", s.Seq, "phase", s.Phase,
			"last_event_id", r.Header.Get("Last-Event-ID")) // 佐證用;權威是 store

		// 立即重推當前內容,恢復畫面不等下一個 tick
		noticeIdx := 0
		if s.Phase == PhaseTouring {
			t.sendStation(w, fl, s.Seq)
		} else {
			t.sendNotice(w, fl, noticeIdx)
			noticeIdx++
		}

		timer := time.NewTimer(t.tickAfter(s.Phase))
		defer timer.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-bye:
				t.writeEvent(w, fl, "", "bye", map[string]string{"reason": "pod shutting down"})
				return
			case <-timer.C:
				if s.Phase == PhaseTouring {
					s = Advance(s)
					s.UpdatedAt = time.Now()
					if err := t.store.Set(ctx, s); err != nil {
						t.log.ErrorContext(ctx, "寫入進度失敗", "error", err.Error())
						return
					}
					if s.Phase == PhaseTouring {
						t.sendStation(w, fl, s.Seq)
					} else {
						t.sendNotice(w, fl, noticeIdx)
						noticeIdx++
					}
				} else {
					t.sendNotice(w, fl, noticeIdx)
					noticeIdx++
				}
				timer.Reset(t.tickAfter(s.Phase))
			}
		}
	})
}

func (t *Tour) tickAfter(phase string) time.Duration {
	if phase == PhaseAnnounce {
		return t.noticeInterval
	}
	return t.interval
}

func (t *Tour) sendStation(w http.ResponseWriter, fl http.Flusher, seq int) {
	st := stations[seq-1]
	t.writeEvent(w, fl, fmt.Sprint(st.Seq), "station", map[string]any{
		"seq": st.Seq, "name": st.Name, "name_en": st.NameEN, "desc": st.Desc,
		"photo": "/tour/static/" + st.Photo,
		"credit": st.Credit, "license": st.License, "pod": t.pod,
	})
}

func (t *Tour) sendNotice(w http.ResponseWriter, fl http.Flusher, idx int) {
	t.writeEvent(w, fl, "", "notice", map[string]string{
		"text": NoticeText(idx), "pod": t.pod,
	})
}

// writeEvent 送出一個 SSE frame;寫入錯誤代表 client 已離線,由 ctx 收尾。
func (t *Tour) writeEvent(w http.ResponseWriter, fl http.Flusher, id, event string, data any) {
	payload, err := json.Marshal(data)
	if err != nil {
		t.log.Error("事件序列化失敗", "event", event, "error", err.Error())
		return
	}
	if id != "" {
		fmt.Fprintf(w, "id: %s\n", id)
	}
	fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, payload)
	fl.Flush()
}
