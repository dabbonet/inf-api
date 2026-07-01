package loadbalancer

import (
	"context"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"strings"
	"sync"
	"time"

	"orchids-api/internal/auth"
	"orchids-api/internal/store"

	"golang.org/x/sync/singleflight"
)

const defaultCacheTTL = 5 * time.Second

type LoadBalancer struct {
	Store          *store.Store
	mu             sync.RWMutex
	cachedAccounts []*store.Account
	cacheExpires   time.Time
	cacheTTL       time.Duration
	connTracker    ConnTracker
	sfGroup        singleflight.Group
}

func NewWithCacheTTL(s *store.Store, cacheTTL time.Duration) *LoadBalancer {
	if cacheTTL <= 0 {
		cacheTTL = defaultCacheTTL
	}
	return &LoadBalancer{
		Store:       s,
		cacheTTL:    cacheTTL,
		connTracker: NewMemoryConnTracker(),
	}
}

// SetConnTracker replaces the default in-memory connection tracker.
func (lb *LoadBalancer) SetConnTracker(ct ConnTracker) {
	lb.connTracker = ct
}

func (lb *LoadBalancer) GetModelChannel(ctx context.Context, modelID string) string {
	if lb.Store == nil {
		return ""
	}
	m, err := lb.Store.GetModelByModelID(ctx, modelID)
	if err != nil || m == nil {
		return ""
	}
	return m.Channel
}

func (lb *LoadBalancer) GetNextAccountExcludingByChannel(ctx context.Context, excludeIDs []int64, channel string) (*store.Account, error) {
	return lb.GetNextAccountExcludingByChannelWithTracker(ctx, excludeIDs, channel, nil)
}

func (lb *LoadBalancer) GetNextAccountExcludingByChannelWithTracker(ctx context.Context, excludeIDs []int64, channel string, tracker ConnTracker) (*store.Account, error) {
	return lb.GetNextAccountExcludingByChannelWithTrackerFilter(ctx, excludeIDs, channel, tracker, nil)
}

func (lb *LoadBalancer) GetNextAccountExcludingByChannelWithTrackerFilter(ctx context.Context, excludeIDs []int64, channel string, tracker ConnTracker, filter func(*store.Account) bool) (*store.Account, error) {
	tStart := time.Now()
	accounts, err := lb.getEnabledAccounts(ctx)
	tGetAccs := time.Since(tStart).Milliseconds()
	if err != nil {
		return nil, err
	}

	tFilterStart := time.Now()
	var filtered []*store.Account
	excludeSet := make(map[int64]bool)
	channelMatched := 0
	rateLimitedUnavailable := 0
	for _, id := range excludeIDs {
		excludeSet[id] = true
	}

	for _, acc := range accounts {
		if excludeSet[acc.ID] {
			continue
		}
		if channel != "" {
			accType := acc.AccountType
			if !strings.EqualFold(accType, channel) && !strings.EqualFold(acc.AgentMode, channel) {
				continue
			}
		}
		if filter != nil && !filter(acc) {
			continue
		}
		channelMatched++

		// For codebuff channel, skip the LB-level 429 cooldown check.
		// Codebuff 429s are per-model and handled by the quota filter
		// (RecordBlock + IsBlocked). The LB's StatusCode="429" is
		// account-wide and would incorrectly block ALL models.
		if !lb.isAccountAvailable(ctx, acc) {
			if strings.TrimSpace(acc.StatusCode) == "429" && strings.EqualFold(channel, "codebuff") {
				slog.Debug("LB: ignoring codebuff StatusCode=429 (per-model quota filter handles it)", "account_id", acc.ID)
			} else {
				if strings.TrimSpace(acc.StatusCode) == "429" {
					rateLimitedUnavailable++
				}
				continue
			}
		}
		filtered = append(filtered, acc)
	}
	tFilter := time.Since(tFilterStart).Milliseconds()
	accounts = filtered

	if len(accounts) == 0 {
		if channel != "" && channelMatched > 0 && rateLimitedUnavailable == channelMatched {
			return nil, fmt.Errorf("no enabled accounts available for channel: %s (all matching accounts are rate-limited or cooling down)", channel)
		}
		return nil, fmt.Errorf("no enabled accounts available for channel: %s", channel)
	}

	account := lb.selectAccountWithTracker(accounts, tracker)
	slog.Debug("DEBUG_LATENCY LB.GetNextAccountWithFilter",
		"channel", channel,
		"ms_total", time.Since(tStart).Milliseconds(),
		"ms_getEnabledAccounts", tGetAccs,
		"ms_filterLoop", tFilter,
		"matched_count", len(accounts),
		"selected_id", account.ID)

	slog.Debug("Selected account", "id", account.ID, "name", account.Name, "type", account.AccountType, "session", auth.MaskSensitive(account.SessionID))

	return account, nil
}

func (lb *LoadBalancer) getEnabledAccounts(ctx context.Context) ([]*store.Account, error) {
	now := time.Now()

	lb.mu.RLock()
	if len(lb.cachedAccounts) > 0 && now.Before(lb.cacheExpires) {
		accounts := lb.cachedAccounts
		lb.mu.RUnlock()
		return accounts, nil
	}
	lb.mu.RUnlock()

	// Use singleflight to prevent cache stampede
	val, err, _ := lb.sfGroup.Do("getEnabledAccounts", func() (interface{}, error) {
		// Double check after acquiring singleflight lock
		lb.mu.RLock()
		if len(lb.cachedAccounts) > 0 && now.Before(lb.cacheExpires) {
			accounts := lb.cachedAccounts
			lb.mu.RUnlock()
			return accounts, nil
		}
		lb.mu.RUnlock()

		accounts, err := lb.Store.GetEnabledAccounts(ctx)
		if err != nil {
			return nil, err
		}

		lb.mu.Lock()
		lb.cachedAccounts = accounts
		lb.cacheExpires = time.Now().Add(lb.cacheTTL)
		lb.mu.Unlock()

		return accounts, nil
	})

	if err != nil {
		return nil, err
	}
	return val.([]*store.Account), nil
}

func (lb *LoadBalancer) selectAccount(accounts []*store.Account) *store.Account {
	return lb.selectAccountWithTracker(accounts, nil)
}

func (lb *LoadBalancer) selectAccountWithTracker(accounts []*store.Account, tracker ConnTracker) *store.Account {
	if len(accounts) == 0 {
		return nil
	}
	if len(accounts) == 1 {
		return accounts[0]
	}
	if tracker == nil {
		tracker = lb.connTracker
	}
	if tracker == nil {
		tracker = NewMemoryConnTracker()
	}

	// Batch-fetch connection counts
	ids := make([]int64, len(accounts))
	for i, acc := range accounts {
		ids[i] = acc.ID
	}
	connCounts := tracker.GetCounts(ids)

	var bestAccounts []*store.Account
	minScore := float64(-1)

	for _, acc := range accounts {
		weight := acc.Weight
		if weight <= 0 {
			weight = 1
		}

		conns := connCounts[acc.ID]
		score := float64(conns) / float64(weight)

		if bestAccounts == nil || score < minScore {
			bestAccounts = []*store.Account{acc}
			minScore = score
		} else if score == minScore {
			bestAccounts = append(bestAccounts, acc)
		}
	}

	if len(bestAccounts) > 0 {
		// Randomly select one from the best accounts to ensure load balancing
		return bestAccounts[rand.IntN(len(bestAccounts))]
	}
	return accounts[0]
}

func (lb *LoadBalancer) AcquireConnection(accountID int64) {
	lb.connTracker.Acquire(accountID)
}

func (lb *LoadBalancer) ReleaseConnection(accountID int64) {
	lb.connTracker.Release(accountID)
}

const (
	// 401 Cooling time: token may have been refreshed, try again after a shorter interval
	retry401Default = 5 * time.Minute
	// 402 to Puter usually means insufficient balance/credits. Puter currently has no stable quota/reset time API.
	// The default is to cool down on a daily basis to prevent accounts without quota from repeatedly hitting the upstream.
	retry402Default = 24 * time.Hour
	// 429 Cooling time: Current limiting is usually temporary, priority is given to waiting for a shorter window before resuming attempts.
	retry429Default = 1 * time.Minute
	// 403/404 Cooling time: The account may be banned or configured incorrectly. Please try again after a longer interval.
	retry403Default = 24 * time.Hour
)

func (lb *LoadBalancer) isAccountAvailable(ctx context.Context, acc *store.Account) bool {
	status := strings.TrimSpace(acc.StatusCode)
	if status == "" {
		return true
	}

	now := time.Now()
	switch status {
	case "401":
		// 401 means that the token has expired or the session has expired. It will automatically resume after a short cooling period.
		if acc.LastAttempt.IsZero() {
			return false
		}
		if now.Sub(acc.LastAttempt) >= retry401Default {
			lb.clearAccountStatus(ctx, acc, "401 Cooling completed, automatic recovery attempt")
			return true
		}
		return false
	case "429":
		if acc.LastAttempt.IsZero() {
			return false
		}
		cooldown := retry429Default
		if !acc.QuotaResetAt.IsZero() {
			if !now.Before(acc.QuotaResetAt) {
				lb.clearAccountStatus(ctx, acc, "429 Cooling completed, automatic recovery attempt")
				return true
			}
			return false
		}
		if now.Sub(acc.LastAttempt) >= cooldown {
			lb.clearAccountStatus(ctx, acc, "429 Cooling completed, automatic recovery attempt")
			return true
		}
		return false
	case "402":
		// 402 usually means insufficient balance/credits. If the reset time is given by the upstream, it will be respected first.
		// Otherwise use a longer cooldown to prevent the scheduler from continually hitting the same unquoted account.
		// For aihubmix/zenmux (Bearer-token auth) the user often just needs to
		// top up — keep the cooldown short so a recharge takes effect quickly.
		if !acc.QuotaResetAt.IsZero() {
			if !now.Before(acc.QuotaResetAt) {
				lb.clearAccountStatus(ctx, acc, "402 Cooling completed, automatic recovery attempt")
				return true
			}
			return false
		}
		if acc.LastAttempt.IsZero() {
			return false
		}
		cooldown := retry402Default
		if now.Sub(acc.LastAttempt) >= cooldown {
			lb.clearAccountStatus(ctx, acc, "402 Cooling completed, automatic recovery attempt")
			return true
		}
		return false
	case "403", "404":
		// 403/404 may be a temporary ban or configuration issue.
		if acc.LastAttempt.IsZero() {
			return false
		}
		cooldown := retry403Default
		if now.Sub(acc.LastAttempt) >= cooldown {
			lb.clearAccountStatus(ctx, acc, status+"Cooling completed, automatic recovery attempt")
			return true
		}
		return false
	default:
		// Unknown status codes are treated as transient errors with a short cooldown
		// to prevent permanent account exclusion.
		if acc.LastAttempt.IsZero() {
			return false
		}
		if now.Sub(acc.LastAttempt) >= retry401Default {
			lb.clearAccountStatus(ctx, acc, status+"Unknown status cooling completed, automatic recovery attempt")
			return true
		}
		return false
	}
}

func (lb *LoadBalancer) clearAccountStatus(ctx context.Context, acc *store.Account, reason string) {
	// Find and update the account in the cached slice so the change reflects immediately
	lb.mu.Lock()
	acc.StatusCode = ""
	acc.LastAttempt = time.Time{}
	acc.QuotaResetAt = time.Time{}
	for _, cached := range lb.cachedAccounts {
		if cached.ID == acc.ID {
			cached.StatusCode = ""
			cached.LastAttempt = time.Time{}
			cached.QuotaResetAt = time.Time{}
			break
		}
	}
	lb.mu.Unlock()
	lb.persistAccountStatus(ctx, acc, reason)
}

// MarkAccountStatus marks the account status (for use by external calls such as background refresh).
// When status=="429" and quotaResetAt is non-zero, the LB will use it as the cooldown end
// instead of the default retry429Default. When quotaResetAt is zero, the default 1m is used.
func (lb *LoadBalancer) MarkAccountStatus(ctx context.Context, acc *store.Account, status string, quotaResetAt ...time.Time) {
	if acc == nil || lb.Store == nil || status == "" {
		return
	}
	var resetAt time.Time
	if len(quotaResetAt) > 0 {
		resetAt = quotaResetAt[0]
	}
	lb.mu.Lock()
	now := time.Now()
	acc.StatusCode = status
	acc.LastAttempt = now
	if status == "429" && !resetAt.IsZero() {
		acc.QuotaResetAt = resetAt
	} else {
		acc.QuotaResetAt = time.Time{}
	}

	// Ensure the cache is updated as well
	for _, cached := range lb.cachedAccounts {
		if cached.ID == acc.ID {
			cached.StatusCode = status
			cached.LastAttempt = now
			if status == "429" && !resetAt.IsZero() {
				cached.QuotaResetAt = resetAt
			} else {
				cached.QuotaResetAt = time.Time{}
			}
			break
		}
	}
	lb.mu.Unlock()
	lb.persistAccountStatus(ctx, acc, "Background refresh failed:"+status)
}

func (lb *LoadBalancer) persistAccountStatus(ctx context.Context, acc *store.Account, reason string) {
	if lb.Store == nil {
		return
	}
	if err := lb.Store.UpdateAccount(ctx, acc); err != nil {
		slog.Warn("Account status update failed", "account_id", acc.ID, "reason", reason, "error", err)
		return
	}
	slog.Debug("Account status has been updated", "account_id", acc.ID, "status", acc.StatusCode, "reason", reason)
}
