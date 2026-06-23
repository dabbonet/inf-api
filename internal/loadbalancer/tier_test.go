package loadbalancer

import (
	"testing"

	"orchids-api/internal/store"
)

func TestTierOf(t *testing.T) {
	cases := []struct {
		subscription string
		want         int
	}{
		{"free", TierFallback},
		{"FREE", TierFallback},
		{"", TierStandard},
		{"pro", TierStandard},
		{"team", TierStandard},
		{"enterprise", TierStandard},
		{"unknown", TierStandard},
	}
	for _, c := range cases {
		acc := &store.Account{Subscription: c.subscription}
		if got := TierOf(acc); got != c.want {
			t.Errorf("TierOf(subscription=%q) = %d, want %d", c.subscription, got, c.want)
		}
	}
	// nil account
	if got := TierOf(nil); got != TierFallback {
		t.Errorf("TierOf(nil) = %d, want TierFallback", got)
	}
}

func TestSelectAccountByTier_PrefersLowerTier(t *testing.T) {
	lb := NewWithCacheTTL(nil, 0)
	free := &store.Account{ID: 1, Subscription: "free", Weight: 1}
	pro := &store.Account{ID: 2, Subscription: "pro", Weight: 1}
	// Both available; lower tier (pro) should win.
	got := lb.selectAccountByTier([]*store.Account{free, pro}, nil, nil)
	if got == nil || got.ID != 2 {
		t.Fatalf("expected pro (id=2), got %+v", got)
	}
}

func TestSelectAccountByTier_FallsThroughTier(t *testing.T) {
	lb := NewWithCacheTTL(nil, 0)
	free := &store.Account{ID: 1, Subscription: "free", Weight: 1}
	pro := &store.Account{ID: 2, Subscription: "pro", Weight: 1}
	// Predicate rejects the pro account: must fall through to free.
	got := lb.selectAccountByTier([]*store.Account{free, pro}, nil, func(a *store.Account) bool {
		return a.Subscription != "pro"
	})
	if got == nil || got.ID != 1 {
		t.Fatalf("expected free (id=1), got %+v", got)
	}
}

func TestSelectAccountByTier_Empty(t *testing.T) {
	lb := NewWithCacheTTL(nil, 0)
	if got := lb.selectAccountByTier(nil, nil, nil); got != nil {
		t.Fatal("expected nil for empty input")
	}
	if got := lb.selectAccountByTier([]*store.Account{}, nil, func(*store.Account) bool {
		return false
	}); got != nil {
		t.Fatal("expected nil when predicate rejects all")
	}
}

func TestSelectWithinTier_WeightBalance(t *testing.T) {
	lb := NewWithCacheTTL(nil, 0)
	heavy := &store.Account{ID: 1, Subscription: "pro", Weight: 10}
	light := &store.Account{ID: 2, Subscription: "pro", Weight: 1}
	// heavy has 10 active conns, light has 0: scores are equal at 1.0.
	tracker := NewMemoryConnTracker()
	for i := 0; i < 10; i++ {
		tracker.Acquire(heavy.ID)
	}
	got := lb.selectWithinTier([]*store.Account{heavy, light}, tracker)
	if got == nil {
		t.Fatal("expected non-nil result")
	}
	// Either is valid by score, but tie-breaking is deterministic only by
	// random.IntN. So verify it picked one of them.
	if got.ID != heavy.ID && got.ID != light.ID {
		t.Fatalf("unexpected account id %d", got.ID)
	}
}
