package kimchi

import (
	"strings"

	"orchids-api/internal/store"
)

// ResolveAuthToken extracts the bearer token Kimchi accepts upstream. Kimchi
// has only ONE credential type — the bearer itself — so the 4 Account
// credential fields (RefreshToken, SessionCookie, ClientCookie, Token) are
// interchangeable aliases for the same token.
//
// Order of preference matches the established inf-api 5-token playbook
// (docs/PROVIDER_PLAYBOOK.md §6.5):
//  1. RefreshToken — recommended storage location for kimchi accounts
//  2. SessionCookie
//  3. ClientCookie
//  4. Token — only accepted if it is NOT truncated (no "..." suffix)
//
// Returns "" when no usable token is found, so callers can 401 cleanly.
func ResolveAuthToken(acc *store.Account) string {
	if acc == nil {
		return ""
	}
	for _, v := range []string{acc.RefreshToken, acc.SessionCookie, acc.ClientCookie} {
		if t := strings.TrimSpace(v); t != "" {
			return t
		}
	}
	if t := strings.TrimSpace(acc.Token); t != "" {
		if strings.HasSuffix(t, "...") {
			// "..." marks a display placeholder, not a real bearer.
			return ""
		}
		return t
	}
	return ""
}
