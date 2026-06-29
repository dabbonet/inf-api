package kimchi

import (
	"context"
	"fmt"

	"orchids-api/internal/config"
	"orchids-api/internal/store"
)

// VerifyAccount confirms the bearer token resolves to a real Kimchi account
// and returns the canonical user id + email so callers can populate
// store.Account.Email and stand in as a stable per-user identifier.
//
// Called from the admin POST /api/accounts handler when account_type=kimchi.
// The cfg parameter is required so the same HTTP client stack (with shared
// proxy pool) is used as the runtime Provider.
func VerifyAccount(ctx context.Context, acc *store.Account, cfg *config.Config) (*Me, error) {
	if acc == nil {
		return nil, fmt.Errorf("kimchi: nil account")
	}
	token := ResolveAuthToken(acc)
	if token == "" {
		return nil, fmt.Errorf("kimchi: no bearer token on account")
	}
	c := NewClient(token, cfg)
	return c.VerifyAccount(ctx)
}
