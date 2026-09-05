package tour

import "time"

// Phase 值(data-model VisitorSession 狀態機:touring → announce,單向)。
const (
	PhaseTouring  = "touring"
	PhaseAnnounce = "announce"
)

// Session 訪客會話 — 唯一需要跨請求存活的狀態(specs/006 data-model.md)。
type Session struct {
	ID        string    `json:"id"`
	Seq       int       `json:"seq"`
	Phase     string    `json:"phase"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewSession 從第 1 站開始的新會話。
func NewSession(id string) Session {
	return Session{ID: id, Seq: 1, Phase: PhaseTouring}
}

// Advance 推進一步:站序遞增至最後一站,再推進即轉入公告模式(終態)。
func Advance(s Session) Session {
	if s.Phase != PhaseTouring {
		return s
	}
	if s.Seq >= len(stations) {
		s.Phase = PhaseAnnounce
		return s
	}
	s.Seq++
	return s
}
