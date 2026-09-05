package tour

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// ValkeyStore 進度外部化(act3):狀態不住在 pod 裡,任何 pod 接手都查得到
// (specs/006 FR-010a;key 結構見 data-model.md)。
type ValkeyStore struct {
	c *redis.Client
}

// NewValkeyStore 建立客戶端(懶連線;可用性由 Ping 於啟動時把關,FR-012)。
func NewValkeyStore(addr string) *ValkeyStore {
	return &ValkeyStore{c: redis.NewClient(&redis.Options{Addr: addr})}
}

func sessionKey(id string) string { return "tour:session:" + id }

func (s *ValkeyStore) Get(ctx context.Context, id string) (Session, bool, error) {
	b, err := s.c.Get(ctx, sessionKey(id)).Bytes()
	if errors.Is(err, redis.Nil) {
		return Session{}, false, nil
	}
	if err != nil {
		return Session{}, false, fmt.Errorf("valkey 讀取失敗: %w", err)
	}
	var sess Session
	if err := json.Unmarshal(b, &sess); err != nil {
		return Session{}, false, fmt.Errorf("session 反序列化失敗: %w", err)
	}
	return sess, true, nil
}

func (s *ValkeyStore) Set(ctx context.Context, sess Session) error {
	b, err := json.Marshal(sess)
	if err != nil {
		return fmt.Errorf("session 序列化失敗: %w", err)
	}
	// TTL 24h:過期自動清理,無需另寫回收邏輯(data-model)
	return s.c.Set(ctx, sessionKey(sess.ID), b, 24*time.Hour).Err()
}

func (s *ValkeyStore) Ping(ctx context.Context) error {
	return s.c.Ping(ctx).Err()
}
