package codebuff

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// Session holds a cached codebuff session.
type Session struct {
	InstanceID  string
	Model       string
	ExpiresAt   string
	RemainingMs int
}

// IsFresh returns true if the session has more than 30s of remaining time.
func (s *Session) IsFresh() bool {
	return s == nil || s.RemainingMs == 0 || s.RemainingMs > 30000
}

// SessionCache stores codebuff sessions in Redis with per-token locking.
type SessionCache struct {
	redis       *redis.Client
	prefix      string
	lockTTL     time.Duration
	pollTimeout time.Duration
}

// NewSessionCache creates a new Redis-backed session cache.
func NewSessionCache(client *redis.Client, prefix string) *SessionCache {
	if prefix == "" {
		prefix = "codebuff"
	}
	return &SessionCache{
		redis:       client,
		prefix:      prefix,
		lockTTL:     60 * time.Second,
		pollTimeout: 5 * time.Second,
	}
}

// EnsureSession returns a valid session for the given token and model,
// creating one if necessary.  This mirrors the Python SessionManager logic.
// The returned map is non-nil only when a new session was created upstream.
func (sc *SessionCache) EnsureSession(ctx context.Context, client *Client, token, model string) (*Session, map[string]any, error) {
	tokenHash := hashToken(token)
	lockKey := fmt.Sprintf("%s:session_lock:%s", sc.prefix, tokenHash)
	sessionKey := fmt.Sprintf("%s:session:%s:%s", sc.prefix, tokenHash, model)

	// 1. Try cached session.
	cached, err := sc.loadSession(ctx, sessionKey)
	if err == nil && cached != nil && cached.IsFresh() {
		data, err := client.GetSession(ctx, cached.InstanceID)
		if err == nil {
			if status, _ := data["status"].(string); status == "active" {
				upstreamModel, _ := data["model"].(string)
				if upstreamModel == "" || upstreamModel == model {
					// Update remaining_ms from upstream.
					if v, ok := data["remainingMs"].(float64); ok {
						cached.RemainingMs = int(v)
					}
					_ = sc.saveSession(ctx, sessionKey, cached)
					return cached, nil, nil
				}
				// Model mismatch — evict.
				_ = sc.redis.Del(ctx, sessionKey).Err()
			} else {
				// Not active — evict.
				_ = sc.redis.Del(ctx, sessionKey).Err()
			}
		} else {
			// Verify failed — evict.
			_ = sc.redis.Del(ctx, sessionKey).Err()
		}
	}

	// 2. Try to acquire lock for session creation.
	locked, err := sc.redis.SetNX(ctx, lockKey, "1", sc.lockTTL).Result()
	if err != nil {
		return nil, nil, fmt.Errorf("redis lock error: %w", err)
	}
	if !locked {
		// Another process is creating the session; poll Redis briefly.
		deadline := time.Now().Add(sc.pollTimeout)
		for time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(250 * time.Millisecond):
			}
			cached, err = sc.loadSession(ctx, sessionKey)
			if err == nil && cached != nil && cached.IsFresh() {
				return cached, nil, nil
			}
		}
		return nil, nil, fmt.Errorf("timeout waiting for session lock")
	}
	// Guarantee lock release.
	defer sc.redis.Del(ctx, lockKey)

	// 3. Double-check cache after acquiring lock.
	cached, err = sc.loadSession(ctx, sessionKey)
	if err == nil && cached != nil && cached.IsFresh() {
		return cached, nil, nil
	}

	// 4. Check if upstream already has an active session for this model.
	data, err := client.GetSession(ctx, "")
	if err == nil {
		if status, _ := data["status"].(string); status == "active" {
			upstreamModel, _ := data["model"].(string)
			instanceID, _ := data["instanceId"].(string)
			if upstreamModel == model && instanceID != "" {
				sess := &Session{
					InstanceID:  instanceID,
					Model:       upstreamModel,
					ExpiresAt:   stringOrEmpty(data["expiresAt"]),
					RemainingMs: intOrZero(data["remainingMs"]),
				}
				_ = sc.saveSession(ctx, sessionKey, sess)
				return sess, nil, nil
			}
		}
	}

	// 5. Create a new session.
	sess, sessData, err := client.CreateSession(ctx, model)
	if err != nil {
		if IsModelLocked(err) {
			// Delete locked session and retry once.
			_ = client.DeleteSession(ctx)
			_ = sc.redis.Del(ctx, sessionKey).Err()
			sess, sessData, err = client.CreateSession(ctx, model)
		}
	}
	if err != nil {
		return nil, nil, err
	}
	_ = sc.saveSession(ctx, sessionKey, sess)
	return sess, sessData, nil
}

func (sc *SessionCache) loadSession(ctx context.Context, key string) (*Session, error) {
	data, err := sc.redis.Get(ctx, key).Result()
	if err != nil {
		return nil, err
	}
	var sess Session
	if err := json.Unmarshal([]byte(data), &sess); err != nil {
		return nil, err
	}
	return &sess, nil
}

func (sc *SessionCache) saveSession(ctx context.Context, key string, sess *Session) error {
	if sess == nil {
		return sc.redis.Del(ctx, key).Err()
	}
	raw, err := json.Marshal(sess)
	if err != nil {
		return err
	}
	// TTL based on remaining_ms if available, otherwise 24h.
	ttl := 24 * time.Hour
	if sess.RemainingMs > 0 {
		ttl = time.Duration(sess.RemainingMs) * time.Millisecond
	}
	return sc.redis.Set(ctx, key, raw, ttl).Err()
}

func hashToken(token string) string {
	h := sha256.New()
	h.Write([]byte(token))
	return hex.EncodeToString(h.Sum(nil))[:16]
}

func stringOrEmpty(v any) string {
	s, _ := v.(string)
	return s
}

func intOrZero(v any) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}
