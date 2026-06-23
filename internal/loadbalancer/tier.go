package loadbalancer

import (
	"math/rand/v2"
	"sort"
	"strings"

	"orchids-api/internal/store"
)

// Tier identifies the priority bucket an account falls into. Lower
// numeric tiers are tried first; an unavailable tier falls through to
// higher ones.
//
// The numbers are a stable ordering — higher tiers get the same shape
// but are deprioritised. We do not skip tiers by default.
const (
	// TierPreferred — premium/internal accounts (paid subscription).
	TierPreferred = 1
	// TierStandard — pro subscription or other paid-with-quota plans.
	TierStandard = 2
	// TierFallback — free accounts, used when preferred tiers exhausted.
	TierFallback = 3
)

// TierOf derives the routing tier of an account from its declared
// subscription. Unknown values fall to TierStandard so a brand-new
// account is never starved.
//
// This is intentionally a pure function of (read-only) account state:
// nothing else the LB does should change a tier.
func TierOf(acc *store.Account) int {
	if acc == nil {
		return TierFallback
	}
	switch strings.ToLower(strings.TrimSpace(acc.Subscription)) {
	case "free":
		return TierFallback
	case "pro", "team", "enterprise":
		return TierStandard
	default:
		return TierStandard
	}
}

// selectAccountByTier picks an account by tier-first-then-weight.
//
// Algorithm:
//  1. Bucket accounts by tier (TierOf).
//  2. Sort tiers ascending and visit each in order.
//  3. Within a tier, use the existing connection-count / weight scoring:
//     pick the minimum-score account, breaking ties randomly.
//
// Returns nil if every account is filtered out by the predicate.
//
// The predicate can be nil to select from all accounts.
func (lb *LoadBalancer) selectAccountByTier(
	accounts []*store.Account,
	tracker ConnTracker,
	predicate func(*store.Account) bool,
) *store.Account {
	if len(accounts) == 0 {
		return nil
	}
	if predicate == nil {
		predicate = func(*store.Account) bool { return true }
	}

	type bucket struct {
		tier     int
		accounts []*store.Account
	}
	buckets := map[int][]*store.Account{}
	for _, acc := range accounts {
		if !predicate(acc) {
			continue
		}
		buckets[TierOf(acc)] = append(buckets[TierOf(acc)], acc)
	}
	if len(buckets) == 0 {
		return nil
	}
	keys := make([]int, 0, len(buckets))
	for k := range buckets {
		keys = append(keys, k)
	}
	sort.Ints(keys)

	if tracker == nil {
		tracker = lb.connTracker
	}
	if tracker == nil {
		tracker = NewMemoryConnTracker()
	}

	for _, tier := range keys {
		pick := lb.selectWithinTier(buckets[tier], tracker)
		if pick != nil {
			return pick
		}
	}
	return nil
}

// selectWithinTier is the existing conns/weight scoring, scoped to a
// single tier. It is exported only inside this package.
func (lb *LoadBalancer) selectWithinTier(accounts []*store.Account, tracker ConnTracker) *store.Account {
	if len(accounts) == 0 {
		return nil
	}
	if len(accounts) == 1 {
		return accounts[0]
	}

	ids := make([]int64, len(accounts))
	for i, acc := range accounts {
		ids[i] = acc.ID
	}
	connCounts := tracker.GetCounts(ids)

	var bestAccounts []*store.Account
	minScore := -1.0

	for _, acc := range accounts {
		weight := acc.Weight
		if weight <= 0 {
			weight = 1
		}
		score := float64(connCounts[acc.ID]) / float64(weight)

		if bestAccounts == nil || score < minScore {
			bestAccounts = []*store.Account{acc}
			minScore = score
		} else if score == minScore {
			bestAccounts = append(bestAccounts, acc)
		}
	}
	if len(bestAccounts) == 0 {
		return accounts[0]
	}
	return bestAccounts[rand.IntN(len(bestAccounts))]
}
