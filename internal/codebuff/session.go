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

// SessionCacheConfig holds tunable parameters for SessionCache.
type SessionCacheConfig struct {
	// LockTTL controls how long a Redis SETNX lock survives without renewal.
	// Default: 60s.
	LockTTL time.Duration
	// PollTimeout bounds how long a waiter spins while another process creates
	// the session before it gives up. Default: 5s.
	PollTimeout time.Duration
	// PollDelay is the interval between Redis cache checks while waiting on
	// a lock. Default: 250ms.
	PollDelay time.Duration
	// FreshThresholdMs marks a cached session "fresh enough" once its
	// upstream-reported remaining time exceeds this in milliseconds.
	// Default: 30000 (30s).
	FreshThresholdMs int
}

func (c *SessionCacheConfig) withDefaults() {
	if c.LockTTL <= 0 {
		c.LockTTL = 60 * time.Second
	}
	if c.PollTimeout <= 0 {
		c.PollTimeout = 5 * time.Second
	}
	if c.PollDelay <= 0 {
		c.PollDelay = 250 * time.Millisecond
	}
	if c.FreshThresholdMs <= 0 {
		c.FreshThresholdMs = 5000
	}
}

// Session holds a cached codebuff session.
type Session struct {
	InstanceID  string
	Model       string
	ExpiresAt   string
	RemainingMs int
}

// IsFresh returns true if the session has more than the configured
// threshold of remaining time. A nil session is treated as "not fresh":
// callers must hit cache to confirm and fall through to the create path.
func (s *Session) IsFresh(thresholdMs int) bool {
	if s == nil {
		return false
	}
	// 0 means upstream didn't report remainingMs; assume fresh on first
	// use, eviction handles the next refresh.
	if s.RemainingMs == 0 {
		return true
	}
	return s.RemainingMs > thresholdMs
}

// SessionCache stores codebuff sessions in Redis with per-(token,model)
// locking so different models on the same token don't contend.
type SessionCache struct {
	redis       *redis.Client
	prefix      string
	lockTTL     time.Duration
	pollTimeout time.Duration
	pollDelay   time.Duration
	freshMs     int
}

// NewSessionCache creates a new Redis-backed session cache with defaults.
func NewSessionCache(client *redis.Client, prefix string) *SessionCache {
	return NewSessionCacheWith(client, prefix, SessionCacheConfig{})
}

// NewSessionCacheWith creates a new Redis-backed session cache with the
// provided tuning. Zero-valued config fields fall back to documented
// defaults.
func NewSessionCacheWith(client *redis.Client, prefix string, cfg SessionCacheConfig) *SessionCache {
	cfg.withDefaults()
	if prefix == "" {
		prefix = "codebuff"
	}
	return &SessionCache{
		redis:       client,
		prefix:      prefix,
		lockTTL:     cfg.LockTTL,
		pollTimeout: cfg.PollTimeout,
		pollDelay:   cfg.PollDelay,
		freshMs:     cfg.FreshThresholdMs,
	}
}

// SessionOutcome describes how EnsureSession reached the session it returned.
// Telemetry providers use it to record create-vs-reuse counts without having
// to thread an account_id into the cache layer.
type SessionOutcome string

const (
	// SessionOutcomeReuse covers all paths where the cached session was
	// still valid (or recovered from upstream's existing active session).
	SessionOutcomeReuse SessionOutcome = "reuse"
	// SessionOutcomeCreate is recorded when EnsureSession successfully
	// called POST /api/v1/freebuff/session after no usable cache.
	SessionOutcomeCreate SessionOutcome = "create"
)

// EnsureSession returns a valid session for the given token and model,
// creating one if necessary.  This mirrors the Python SessionManager logic.
// The returned map is non-nil only when a new session was created upstream.
//
// The SessionOutcome return value identifies whether the returned session
// was a cache hit, an upstream-recovered session, or a freshly created
// session. Callers may ignore it; the telemetry layer records create-vs-
// reuse counts from this signal.
func (sc *SessionCache) EnsureSession(ctx context.Context, client *Client, token, model string) (*Session, map[string]any, SessionOutcome, error) {
	tokenHash := hashToken(token)
	// Per-(token,model) lock so concurrent requests for *different* models
	// on the same token don't fight over a single key.
	lockKey := fmt.Sprintf("%s:session_lock:%s:%s", sc.prefix, tokenHash, model)
	sessionKey := fmt.Sprintf("%s:session:%s:%s", sc.prefix, tokenHash, model)

	// 1. Try cached session.
	cached, err := sc.loadSession(ctx, sessionKey)
	if err == nil && cached != nil && cached.IsFresh(sc.freshMs) {
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
					return cached, nil, SessionOutcomeReuse, nil
				}
				// Model mismatch — evict.
				_ = sc.redis.Del(ctx, sessionKey).Err()
			} else {
				// Not active — evict.
				_ = sc.redis.Del(ctx, sessionKey).Err()
			}
		}
		// Validation call failed (network, timeout, etc.) — do NOT evict the cached session.
		// Return the stale-but-probably-fine cache entry and let the actual chat request
		// fail if the session is truly dead. One network blip should not cost a bill.
	}

	// 2. Try to acquire lock for session creation.
	locked, err := sc.redis.SetNX(ctx, lockKey, "1", sc.lockTTL).Result()
	if err != nil {
		return nil, nil, "", fmt.Errorf("redis lock error: %w", err)
	}
	if !locked {
		// Another process is creating the session; poll Redis briefly.
		deadline := time.Now().Add(sc.pollTimeout)
		for time.Now().Before(deadline) {
			select {
			case <-ctx.Done():
				return nil, nil, "", ctx.Err()
			case <-time.After(sc.pollDelay):
			}
			cached, err = sc.loadSession(ctx, sessionKey)
			if err == nil && cached != nil && cached.IsFresh(sc.freshMs) {
				return cached, nil, SessionOutcomeReuse, nil
			}
		}
		return nil, nil, "", fmt.Errorf("timeout waiting for session lock")
	}
	// Guarantee lock release.
	defer sc.redis.Del(ctx, lockKey)

	// 3. Double-check cache after acquiring lock.
	cached, err = sc.loadSession(ctx, sessionKey)
	if err == nil && cached != nil && cached.IsFresh(sc.freshMs) {
		return cached, nil, SessionOutcomeReuse, nil
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
				return sess, nil, SessionOutcomeReuse, nil
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
		return nil, nil, "", err
	}
	_ = sc.saveSession(ctx, sessionKey, sess)
	return sess, sessData, SessionOutcomeCreate, nil
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

// EvictSession removes the cached session for the given token and model,
// forcing the next EnsureSession call to create a fresh session upstream.
func (sc *SessionCache) EvictSession(ctx context.Context, token, model string) error {
	key := fmt.Sprintf("%s:session:%s:%s", sc.prefix, hashToken(token), model)
	return sc.redis.Del(ctx, key).Err()
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
	// Add a 120s buffer so the Redis key outlives the upstream expiry
	// and stays findable in the recovery path (Path 4).
	ttl := 24 * time.Hour
	if sess.RemainingMs > 0 {
		ttl = time.Duration(sess.RemainingMs+120000) * time.Millisecond
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
