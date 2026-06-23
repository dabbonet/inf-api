package loadbalancer

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// SessionRegistry is a read-through cache of (accountID, model) -> metadata
// pairs used by the load balancer to remember which account successfully
// served which model. The key shape keeps every (accountID, model) pair
// independent, so a model switch on the same account gets a fresh entry.
//
// In-memory only; survives a single process. Persistence lives in Redis
// (the codebuff SessionCache covers the longer-term store). This layer
// removes redundant Redis hits and lets us detect model-mismatch
// conflicts before round-tripping upstream.
type SessionRegistry struct {
	mu      sync.RWMutex
	entries map[string]sessionEntry

	ttl     time.Duration
	maxSize int
	now     func() time.Time
}

type sessionEntry struct {
	accountID int64
	model     string
	createdAt time.Time
}

// SessionRegistryConfig tunes the registry.
type SessionRegistryConfig struct {
	// MaxEntries caps the cache size. When full, the oldest entry is
	// evicted. Zero means unbounded (not recommended for long-lived
	// processes).
	MaxEntries int
	// EntryTTL bounds how long an entry stays fresh. Zero means 5m.
	EntryTTL time.Duration
}

func (c *SessionRegistryConfig) withDefaults() {
	if c.EntryTTL <= 0 {
		c.EntryTTL = 5 * time.Minute
	}
}

// NewSessionRegistry constructs a registry with the given config.
func NewSessionRegistry(cfg SessionRegistryConfig) *SessionRegistry {
	cfg.withDefaults()
	return &SessionRegistry{
		entries: make(map[string]sessionEntry, 256),
		ttl:     cfg.EntryTTL,
		maxSize: cfg.MaxEntries,
		now:     time.Now,
	}
}

func (r *SessionRegistry) key(accountID int64, model string) string {
	return fmt.Sprintf("%d:%s", accountID, model)
}

// Lookup returns the recorded model for (accountID, model) and whether the
// entry is still fresh. A model stored as <conflict> means a previous
// request observed a model-mismatch via ModelConflictError — callers can
// dedupe the model-switch retry.
func (r *SessionRegistry) Lookup(ctx context.Context, accountID int64, model string) (string, bool) {
	if r == nil {
		return "", false
	}
	r.mu.RLock()
	e, ok := r.entries[r.key(accountID, model)]
	r.mu.RUnlock()
	if !ok {
		return "", false
	}
	if r.now().Sub(e.createdAt) > r.ttl {
		r.mu.Lock()
		delete(r.entries, r.key(accountID, model))
		r.mu.Unlock()
		return "", false
	}
	return e.model, true
}

// Record stores (accountID, observed-model) for read-through.
func (r *SessionRegistry) Record(ctx context.Context, accountID int64, model string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.maxSize > 0 && len(r.entries) >= r.maxSize {
		// Evict the oldest entry — sufficient because we hold the write
		// lock so a single sweep is consistent.
		var oldestKey string
		var oldestAt time.Time
		for k, e := range r.entries {
			if oldestKey == "" || e.createdAt.Before(oldestAt) {
				oldestKey = k
				oldestAt = e.createdAt
			}
		}
		if oldestKey != "" {
			delete(r.entries, oldestKey)
		}
	}
	r.entries[r.key(accountID, model)] = sessionEntry{
		accountID: accountID,
		model:     model,
		createdAt: r.now(),
	}
}

// Forget clears the (accountID, model) entry — used after a 401/403/404
// forces account rotation.
func (r *SessionRegistry) Forget(ctx context.Context, accountID int64, model string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	delete(r.entries, r.key(accountID, model))
	r.mu.Unlock()
}

// IsReportedModelConflict reports whether the recorded model for
// (accountID, model) is marked as an upstream conflict — a reservation
// that the model slot is currently unavailable.
func (r *SessionRegistry) IsReportedModelConflict(ctx context.Context, accountID int64, model string) bool {
	m, ok := r.Lookup(ctx, accountID, model)
	return ok && m == conflictMarker
}

// MarkModelConflict records (accountID, model) as a known model conflict.
// Subsequent lookups from the same account for the same model can short
// circuit retries via IsReportedModelConflict.
func (r *SessionRegistry) MarkModelConflict(ctx context.Context, accountID int64, model string) {
	r.record(ctx, accountID, model, conflictMarker)
}

func (r *SessionRegistry) record(ctx context.Context, accountID int64, model, observedModel string) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.maxSize > 0 && len(r.entries) >= r.maxSize {
		var oldestKey string
		var oldestAt time.Time
		for k, e := range r.entries {
			if oldestKey == "" || e.createdAt.Before(oldestAt) {
				oldestKey = k
				oldestAt = e.createdAt
			}
		}
		if oldestKey != "" {
			delete(r.entries, oldestKey)
		}
	}
	r.entries[r.key(accountID, model)] = sessionEntry{
		accountID: accountID,
		model:     observedModel,
		createdAt: r.now(),
	}
}

// conflictMarker is the sentinel value stored when IsReportedModelConflict
// is true. It can never collide with a real model name because it is a
// non-printable rune sequence.
const conflictMarker = "\x00conflict\x00"

// ModelConflictError describes an upstream-reported model mismatch.
// It's returned by codebuff-style session lookups when the upstream
// session pinned a different model than the request asked for. Callers
// can use errors.As to detect and route.
type ModelConflictError struct {
	AccountID    int64
	AccountName  string
	RequestedModel    string
	UpstreamModel     string
	UpstreamInstanceID string
}

func (e *ModelConflictError) Error() string {
	return fmt.Sprintf(
		"model conflict on account %s (%d): requested %q, upstream session pinned %q",
		e.AccountName, e.AccountID, e.RequestedModel, e.UpstreamModel,
	)
}

// AsModelConflict is a convenience wrapper around errors.As for the
// typed error.
func AsModelConflict(err error) (*ModelConflictError, bool) {
	var target *ModelConflictError
	if errors.As(err, &target) {
		return target, true
	}
	return nil, false
}
