package loadbalancer

import (
	"context"
	"testing"

	"orchids-api/internal/store"
)

// TestEndToEnd_TierWithRegistryConflict is the cross-feature integration
// covering Phase C (registry + conflict marker) and Phase D (tiered
// selection).
//
//  1. A 'pro' account and a 'free' account are both available.
//  2. The free account gets a model-conflict marker for claude-3-5;
//     the LB must skip it via a predicate and serve the pro fallback
//     even though 'pro' is the lower-numbered tier.
//  3. Once the conflict is forgotten, 'pro' is preferred again.
func TestEndToEnd_TierWithRegistryConflict(t *testing.T) {
	lb := NewWithCacheTTL(nil, 0)
	reg := NewSessionRegistry(SessionRegistryConfig{})

	pro := &store.Account{ID: 1, Subscription: "pro", Weight: 1}
	free := &store.Account{ID: 2, Subscription: "free", Weight: 1}
	accounts := []*store.Account{pro, free}

	reg.MarkModelConflict(context.Background(), free.ID, "claude-3-5")

	got := lb.selectAccountByTier(accounts, nil, func(a *store.Account) bool {
		return !reg.IsReportedModelConflict(context.Background(), a.ID, "claude-3-5")
	})
	if got == nil || got.ID != pro.ID {
		t.Fatalf("expected pro to be selected, got %+v", got)
	}

	// Forget conflict → pro wins by tier order.
	reg.Forget(context.Background(), free.ID, "claude-3-5")

	got = lb.selectAccountByTier(accounts, nil, func(a *store.Account) bool {
		return !reg.IsReportedModelConflict(context.Background(), a.ID, "claude-3-5")
	})
	if got == nil {
		t.Fatal("expected selection after forgetting")
	}
	// Pro is tier 2 (Subscription='pro'), Free is tier 3; pro wins.
	if got.ID != pro.ID {
		t.Fatalf("expected pro (id=1) after forget, got %d", got.ID)
	}
}
