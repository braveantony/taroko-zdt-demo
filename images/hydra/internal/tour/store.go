package tour

import (
	"context"
	"sync"
)

// StateStore 訪客進度的存放抽象;實作:memory(本檔)、valkey(store_valkey.go)。
// 存放位置的差異正是三幕 demo 的主旨(specs/006 spec.md)。
type StateStore interface {
	Get(ctx context.Context, id string) (Session, bool, error)
	Set(ctx context.Context, s Session) error
	Ping(ctx context.Context) error // 啟動自檢:後端不可用須快速失敗(FR-012)
}

// MemoryStore 存於 pod 記憶體 — pod 死進度亡(act1/act2 的對照組行為)。
type MemoryStore struct {
	mu sync.RWMutex
	m  map[string]Session
}

func NewMemoryStore() *MemoryStore {
	return &MemoryStore{m: make(map[string]Session)}
}

func (s *MemoryStore) Get(_ context.Context, id string) (Session, bool, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	sess, ok := s.m[id]
	return sess, ok, nil
}

func (s *MemoryStore) Set(_ context.Context, sess Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[sess.ID] = sess
	return nil
}

func (s *MemoryStore) Ping(context.Context) error { return nil }
