package loadbalancer

import (
	"context"
	"strings"
	"testing"
	"time"

	"orchids-api/internal/store"
)

type fixedConnTracker struct {
	counts map[int64]int64
}

func (t *fixedConnTracker) Acquire(accountID int64) {}

func (t *fixedConnTracker) Release(accountID int64) {}

func (t *fixedConnTracker) GetCount(accountID int64) int64 {
	if t == nil {
		return 0
	}
	return t.counts[accountID]
}

func (t *fixedConnTracker) GetCounts(accountIDs []int64) map[int64]int64 {
	out := make(map[int64]int64, len(accountIDs))
	for _, id := range accountIDs {
		out[id] = t.GetCount(id)
	}
	return out
}

func TestSelectAccount_Distribution(t *testing.T) {
	lb := &LoadBalancer{connTracker: NewMemoryConnTracker()}
	accounts := []*store.Account{
		{ID: 1, Name: "Acc1", Weight: 1},
		{ID: 2, Name: "Acc2", Weight: 1},
		{ID: 3, Name: "Acc3", Weight: 1},
	}

	counts := make(map[int64]int)
	iterations := 1000

	for i := 0; i < iterations; i++ {
		acc := lb.selectAccount(accounts)
		if acc == nil {
			t.Fatal("selectAccount returned nil")
		}
		counts[acc.ID]++
	}

	if len(counts) < 2 {
		t.Errorf("Expected distribution across multiple accounts, but only got %d accounts", len(counts))
	}

	t.Logf("Counts after %d iterations: %+v", iterations, counts)

	// Ensure each account got a reasonable number of hits (rough check)
	for id, count := range counts {
		if count < 200 {
			t.Errorf("Account %d got suspiciously low hits: %d", id, count)
		}
	}
}

func TestSelectAccount_WeightedDistribution(t *testing.T) {
	lb := &LoadBalancer{connTracker: NewMemoryConnTracker()}
	// Acc1 has weight 10, Acc2 has weight 1
	// With 0 active conns, the score for both is 0/10 = 0 and 0/1 = 0.
	// So they should still be tied and picked randomly.
	accounts := []*store.Account{
		{ID: 1, Name: "Acc1", Weight: 10},
		{ID: 2, Name: "Acc2", Weight: 1},
	}

	counts := make(map[int64]int)
	iterations := 1000

	for i := 0; i < iterations; i++ {
		acc := lb.selectAccount(accounts)
		counts[acc.ID]++
	}

	if counts[1] == 0 || counts[2] == 0 {
		t.Errorf("Expected both accounts to be picked when tied at score 0, got counts: %+v", counts)
	}
}

func TestSelectAccount_ActiveConnections(t *testing.T) {
	lb := &LoadBalancer{connTracker: NewMemoryConnTracker()}
	acc1 := &store.Account{ID: 1, Name: "Acc1", Weight: 1}
	acc2 := &store.Account{ID: 2, Name: "Acc2", Weight: 1}
	accounts := []*store.Account{acc1, acc2}

	// Mock active connections
	lb.AcquireConnection(acc1.ID) // acc1 has 1 conn, score 1/1 = 1
	// acc2 has 0 conns, score 0/1 = 0

	// Should always pick acc2
	for i := 0; i < 100; i++ {
		selected := lb.selectAccount(accounts)
		if selected.ID != acc2.ID {
			t.Errorf("Expected Acc2 to be selected, got %s", selected.Name)
		}
	}
}

func TestSelectAccountWithTracker_UsesProvidedTracker(t *testing.T) {
	lb := &LoadBalancer{connTracker: NewMemoryConnTracker()}
	acc1 := &store.Account{ID: 1, Name: "Acc1", Weight: 1}
	acc2 := &store.Account{ID: 2, Name: "Acc2", Weight: 1}
	accounts := []*store.Account{acc1, acc2}

	custom := &fixedConnTracker{
		counts: map[int64]int64{
			acc1.ID: 5,
			acc2.ID: 0,
		},
	}

	for i := 0; i < 100; i++ {
		selected := lb.selectAccountWithTracker(accounts, custom)
		if selected == nil || selected.ID != acc2.ID {
			t.Fatalf("expected Acc2 to be selected via custom tracker, got %#v", selected)
		}
	}
}

func TestGetNextAccountExcludingByChannelWithTracker_AllRateLimitedReturnsHelpfulError(t *testing.T) {
	now := time.Now()
	lb := &LoadBalancer{
		connTracker: NewMemoryConnTracker(),
		cachedAccounts: []*store.Account{
			{ID: 1, Name: "Puter1", AccountType: "puter", Enabled: true, StatusCode: "429", LastAttempt: now},
			{ID: 2, Name: "Puter2", AccountType: "puter", Enabled: true, StatusCode: "429", LastAttempt: now},
		},
		cacheExpires: now.Add(time.Minute),
	}

	_, err := lb.GetNextAccountExcludingByChannelWithTracker(context.Background(), nil, "puter", nil)
	if err == nil {
		t.Fatal("expected rate-limited selector error, got nil")
	}
	if !strings.Contains(err.Error(), "all matching accounts are rate-limited or cooling down") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIsAccountAvailable_429UsesQuotaResetAt(t *testing.T) {
	lb := &LoadBalancer{connTracker: NewMemoryConnTracker()}
	acc := &store.Account{
		ID:           1,
		AccountType:  "warp",
		StatusCode:   "429",
		LastAttempt:  time.Now(),
		QuotaResetAt: time.Now().Add(-time.Second),
	}

	if !lb.isAccountAvailable(context.Background(), acc) {
		t.Fatal("expected expired quota reset to re-enable account")
	}
	if acc.StatusCode != "" {
		t.Fatalf("expected status to be cleared after cooldown, got %q", acc.StatusCode)
	}
	if !acc.QuotaResetAt.IsZero() {
		t.Fatalf("expected quota reset timestamp to be cleared, got %v", acc.QuotaResetAt)
	}
}

func TestIsAccountAvailable_402UsesLongCooldown(t *testing.T) {
	lb := &LoadBalancer{connTracker: NewMemoryConnTracker()}
	acc := &store.Account{
		ID:          1,
		AccountType: "puter",
		StatusCode:  "402",
		LastAttempt: time.Now().Add(-2 * time.Hour),
	}

	if lb.isAccountAvailable(context.Background(), acc) {
		t.Fatal("expected 402 account to remain unavailable before long cooldown expires")
	}

	acc.LastAttempt = time.Now().Add(-(retry402Default + time.Minute))
	if !lb.isAccountAvailable(context.Background(), acc) {
		t.Fatal("expected expired 402 cooldown to re-enable account")
	}
	if acc.StatusCode != "" {
		t.Fatalf("expected status to be cleared after 402 cooldown, got %q", acc.StatusCode)
	}
}

func TestMarkAccountStatus_Repeated429RefreshesCooldownStart(t *testing.T) {
	lb := &LoadBalancer{
		Store:       &store.Store{},
		connTracker: NewMemoryConnTracker(),
		cachedAccounts: []*store.Account{
			{ID: 1, Name: "Puter1", AccountType: "puter", Enabled: true},
		},
	}
	acc := &store.Account{
		ID:          1,
		AccountType: "puter",
		StatusCode:  "429",
		LastAttempt: time.Now().Add(-30 * time.Second),
	}

	before := acc.LastAttempt
	lb.MarkAccountStatus(context.Background(), acc, "429")

	if !acc.LastAttempt.After(before) {
		t.Fatalf("expected repeated 429 to refresh cooldown start, before=%v after=%v", before, acc.LastAttempt)
	}
	if got := lb.cachedAccounts[0].LastAttempt; !got.After(before) {
		t.Fatalf("expected cached repeated 429 to refresh cooldown start, before=%v after=%v", before, got)
	}
}

func TestIsModelAvailable_NoBlock(t *testing.T) {
	lb := &LoadBalancer{connTracker: NewMemoryConnTracker()}
	acc := &store.Account{
		ID:          1,
		AccountType: "puter",
		Enabled:     true,
	}
	if !lb.IsModelAvailable(acc, "gpt-4") {
		t.Fatal("expected model available when no block exists")
	}
}

func TestIsModelAvailable_NilAccount(t *testing.T) {
	lb := &LoadBalancer{connTracker: NewMemoryConnTracker()}
	if !lb.IsModelAvailable(nil, "gpt-4") {
		t.Fatal("expected nil account to return available")
	}
}

func TestIsModelAvailable_EmptyModelID(t *testing.T) {
	lb := &LoadBalancer{connTracker: NewMemoryConnTracker()}
	acc := &store.Account{
		ID:          1,
		AccountType: "puter",
		ModelStatuses: map[string]string{
			"gpt-4": "402",
		},
		ModelStatusAt: map[string]time.Time{
			"gpt-4": time.Now(),
		},
	}
	if !lb.IsModelAvailable(acc, "") {
		t.Fatal("expected empty modelID to return available regardless of blocks")
	}
}

func TestIsModelAvailable_NilMaps(t *testing.T) {
	lb := &LoadBalancer{connTracker: NewMemoryConnTracker()}
	acc := &store.Account{
		ID:          1,
		AccountType: "puter",
	}
	if !lb.IsModelAvailable(acc, "gpt-4") {
		t.Fatal("expected nil maps to return available")
	}
}

func TestIsModelAvailable_402BlockWithinCooldown(t *testing.T) {
	lb := &LoadBalancer{connTracker: NewMemoryConnTracker()}
	acc := &store.Account{
		ID:          1,
		AccountType: "puter",
		ModelStatuses: map[string]string{
			"gpt-4": "402",
		},
		ModelStatusAt: map[string]time.Time{
			"gpt-4": time.Now(),
		},
	}
	if lb.IsModelAvailable(acc, "gpt-4") {
		t.Fatal("expected model unavailable within 402 cooldown")
	}
}

func TestIsModelAvailable_402BlockExpired(t *testing.T) {
	lb := &LoadBalancer{connTracker: NewMemoryConnTracker()}
	acc := &store.Account{
		ID:          1,
		AccountType: "puter",
		ModelStatuses: map[string]string{
			"gpt-4": "402",
		},
		ModelStatusAt: map[string]time.Time{
			"gpt-4": time.Now().Add(-(retry402Default + time.Hour)),
		},
	}
	if !lb.IsModelAvailable(acc, "gpt-4") {
		t.Fatal("expected model available after 402 cooldown expired")
	}
}

func TestIsModelAvailable_402BlockExpired_Aihubmix(t *testing.T) {
	lb := &LoadBalancer{connTracker: NewMemoryConnTracker()}
	acc := &store.Account{
		ID:          1,
		AccountType: "aihubmix",
		ModelStatuses: map[string]string{
			"gpt-4": "402",
		},
		ModelStatusAt: map[string]time.Time{
			"gpt-4": time.Now().Add(-(5*time.Minute + time.Second)),
		},
	}
	if !lb.IsModelAvailable(acc, "gpt-4") {
		t.Fatal("expected aihubmix 402 model available after 5min cooldown")
	}
	acc2 := &store.Account{
		ID:          1,
		AccountType: "aihubmix",
		ModelStatuses: map[string]string{
			"gpt-4": "402",
		},
		ModelStatusAt: map[string]time.Time{
			"gpt-4": time.Now().Add(-3 * time.Minute),
		},
	}
	if lb.IsModelAvailable(acc2, "gpt-4") {
		t.Fatal("expected aihubmix 402 model unavailable within 5min cooldown")
	}
}

func TestIsModelAvailable_429BlockWithinCooldown(t *testing.T) {
	lb := &LoadBalancer{connTracker: NewMemoryConnTracker()}
	acc := &store.Account{
		ID:          1,
		AccountType: "warp",
		ModelStatuses: map[string]string{
			"claude-sonnet": "429",
		},
		ModelStatusAt: map[string]time.Time{
			"claude-sonnet": time.Now().Add(-30 * time.Second),
		},
	}
	if lb.IsModelAvailable(acc, "claude-sonnet") {
		t.Fatal("expected model unavailable within 429 cooldown")
	}
}

func TestIsModelAvailable_429BlockExpired(t *testing.T) {
	lb := &LoadBalancer{connTracker: NewMemoryConnTracker()}
	acc := &store.Account{
		ID:          1,
		AccountType: "warp",
		ModelStatuses: map[string]string{
			"claude-sonnet": "429",
		},
		ModelStatusAt: map[string]time.Time{
			"claude-sonnet": time.Now().Add(-(retry429Default + time.Minute)),
		},
	}
	if !lb.IsModelAvailable(acc, "claude-sonnet") {
		t.Fatal("expected model available after 429 cooldown expired")
	}
}

func TestIsModelAvailable_DifferentModelUnaffected(t *testing.T) {
	lb := &LoadBalancer{connTracker: NewMemoryConnTracker()}
	acc := &store.Account{
		ID:          1,
		AccountType: "puter",
		ModelStatuses: map[string]string{
			"gpt-4": "402",
		},
		ModelStatusAt: map[string]time.Time{
			"gpt-4": time.Now(),
		},
	}
	if !lb.IsModelAvailable(acc, "gpt-3.5") {
		t.Fatal("expected other model to remain available when only one is blocked")
	}
}

func TestMarkModelStatus_SetsModelBlocked(t *testing.T) {
	lb := &LoadBalancer{
		Store:       &store.Store{},
		connTracker: NewMemoryConnTracker(),
	}
	acc := &store.Account{
		ID:          1,
		AccountType: "puter",
	}
	lb.MarkModelStatus(context.Background(), acc, "gpt-4", "402")
	if acc.ModelStatuses["gpt-4"] != "402" {
		t.Fatalf("expected model status 402, got %q", acc.ModelStatuses["gpt-4"])
	}
	if acc.ModelStatusAt["gpt-4"].IsZero() {
		t.Fatal("expected model blocked timestamp to be set")
	}
}

func TestMarkModelStatus_UpdatesCachedAccount(t *testing.T) {
	lb := &LoadBalancer{
		Store:       &store.Store{},
		connTracker: NewMemoryConnTracker(),
		cachedAccounts: []*store.Account{
			{ID: 1, AccountType: "puter", Enabled: true},
		},
	}
	acc := &store.Account{
		ID:          1,
		AccountType: "puter",
	}
	lb.MarkModelStatus(context.Background(), acc, "gpt-4", "402")
	if lb.cachedAccounts[0].ModelStatuses["gpt-4"] != "402" {
		t.Fatal("expected cached account model status to be updated")
	}
	if lb.cachedAccounts[0].ModelStatusAt["gpt-4"].IsZero() {
		t.Fatal("expected cached account model blocked timestamp to be set")
	}
}

func TestClearAccountStatus_ClearsModelBlocks(t *testing.T) {
	lb := &LoadBalancer{
		Store:       &store.Store{},
		connTracker: NewMemoryConnTracker(),
		cachedAccounts: []*store.Account{
			{
				ID:          1,
				AccountType: "puter",
				Enabled:     true,
				ModelStatuses: map[string]string{
					"gpt-4":    "402",
					"gpt-3.5":  "429",
				},
				ModelStatusAt: map[string]time.Time{
					"gpt-4":   time.Now(),
					"gpt-3.5": time.Now(),
				},
			},
		},
	}
	acc := &store.Account{
		ID:          1,
		AccountType: "puter",
		ModelStatuses: map[string]string{
			"gpt-4":    "402",
			"gpt-3.5":  "429",
		},
		ModelStatusAt: map[string]time.Time{
			"gpt-4":   time.Now(),
			"gpt-3.5": time.Now(),
		},
	}
	lb.clearAccountStatus(context.Background(), acc, "cooldown-expired")
	if acc.ModelStatuses != nil {
		t.Fatal("expected model statuses to be cleared on account recovery")
	}
	if acc.ModelStatusAt != nil {
		t.Fatal("expected model blocked timestamps to be cleared on account recovery")
	}
	if lb.cachedAccounts[0].ModelStatuses != nil {
		t.Fatal("expected cached model statuses to be cleared on account recovery")
	}
	if lb.cachedAccounts[0].ModelStatusAt != nil {
		t.Fatal("expected cached model blocked timestamps to be cleared on account recovery")
	}
}
