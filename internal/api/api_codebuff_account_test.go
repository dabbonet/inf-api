package api

import (
	"testing"

	"orchids-api/internal/store"
)

func TestTruncateAccountDisplayToken(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		acc  *store.Account
		want string
	}{
		{
			name: "nil account returns empty",
			acc:  nil,
			want: "",
		},
		{
			name: "codebuff credential via client_cookie short token",
			acc: &store.Account{
				AccountType:  "codebuff",
				ClientCookie: "abc123",
			},
			want: "abc123",
		},
		{
			name: "codebuff credential via client_cookie long token",
			acc: &store.Account{
				AccountType:  "codebuff",
				ClientCookie: "sk-1234567890abcdefghij-very-long-bearer-token-value",
			},
			want: "sk-1234567890abcdefghij-very-l...",
		},
		{
			name: "puter uses client_cookie fallback",
			acc: &store.Account{
				AccountType:  "puter",
				ClientCookie: "puter-auth-token-1234567890",
			},
			want: "puter-auth-token-1234567890",
		},
		{
			name: "warp uses refresh_token when set",
			acc: &store.Account{
				AccountType:  "warp",
				RefreshToken: "warp-refresh-token-abcdef0123456789",
				ClientCookie: "warp-cookie-should-be-ignored",
			},
			want: "warp-refresh-token-abcdef01234...",
		},
		{
			name: "empty fields returns empty",
			acc: &store.Account{
				AccountType: "codebuff",
			},
			want: "",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := truncateAccountDisplayToken(tc.acc)
			if got != tc.want {
				t.Fatalf("truncateAccountDisplayToken()=%q want %q", got, tc.want)
			}
		})
	}
}

func TestIsActiveModelChannel(t *testing.T) {
	t.Parallel()

	enabled := []string{"puter", "codebuff", "PUTER", " Codebuff "}
	for _, raw := range enabled {
		if !isActiveModelChannel(raw) {
			t.Fatalf("expected %q to be active", raw)
		}
	}
	disabled := []string{"warp", "grok", "aihubmix", "zenmux", "", "bedrock"}
	for _, raw := range disabled {
		if isActiveModelChannel(raw) {
			t.Fatalf("expected %q to NOT be active", raw)
		}
	}
}

func TestEnsureDefaultSubscription(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name           string
		acc            *store.Account
		wantSubscribed string
		wantApplied    bool
	}{
		{
			name:           "nil account",
			acc:            nil,
			wantSubscribed: "",
			wantApplied:    false,
		},
		{
			name:           "codebuff empty gets basic",
			acc:            &store.Account{AccountType: "codebuff"},
			wantSubscribed: "basic",
			wantApplied:    true,
		},
		{
			name:           "puter empty gets basic",
			acc:            &store.Account{AccountType: "puter"},
			wantSubscribed: "basic",
			wantApplied:    true,
		},
		{
			name:           "warp empty is untouched",
			acc:            &store.Account{AccountType: "warp"},
			wantSubscribed: "",
			wantApplied:    false,
		},
		{
			name:           "existing subscription untouched",
			acc:            &store.Account{AccountType: "codebuff", Subscription: "pro"},
			wantSubscribed: "pro",
			wantApplied:    false,
		},
		{
			name:           "whitespace subscription treated as empty",
			acc:            &store.Account{AccountType: "codebuff", Subscription: "   "},
			wantSubscribed: "basic",
			wantApplied:    true,
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			applied := ensureDefaultSubscription(tc.acc)
			if applied != tc.wantApplied {
				t.Fatalf("ensureDefaultSubscription returned %v, want %v", applied, tc.wantApplied)
			}
			got := ""
			if tc.acc != nil {
				got = tc.acc.Subscription
			}
			if got != tc.wantSubscribed {
				t.Fatalf("subscription = %q, want %q", got, tc.wantSubscribed)
			}
		})
	}
}

func TestNormalizeAccountOutputAppliesDefaultSubscription(t *testing.T) {
	t.Parallel()

	codebuffAcc := &store.Account{AccountType: "codebuff"}
	normalized := normalizeAccountOutput(codebuffAcc)
	if normalized.Subscription != "basic" {
		t.Fatalf("codebuff normalize got %q, want %q", normalized.Subscription, "basic")
	}

	warpAcc := &store.Account{AccountType: "warp"}
	if got := normalizeAccountOutput(warpAcc).Subscription; got != "" {
		t.Fatalf("warp normalize should not default, got %q", got)
	}

	premiumAcc := &store.Account{AccountType: "codebuff", Subscription: "pro"}
	if got := normalizeAccountOutput(premiumAcc).Subscription; got != "pro" {
		t.Fatalf("existing sub should pass through, got %q", got)
	}
}

func TestResolveCodebuffAuthToken(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		acc  *store.Account
		want string
	}{
		{
			name: "nil account returns empty",
			acc:  nil,
			want: "",
		},
		{
			name: "empty fields returns empty",
			acc:  &store.Account{AccountType: "codebuff"},
			want: "",
		},
		{
			name: "client_cookie wins over truncated token preview",
			acc: &store.Account{
				AccountType:  "codebuff",
				ClientCookie: "REAL_FULL_BEARER_TOKEN_12345",
				Token:        "REAL_FULL_BEARER_TOKEN_1...",
			},
			want: "REAL_FULL_BEARER_TOKEN_12345",
		},
		{
			name: "session_cookie used when client_cookie empty",
			acc: &store.Account{
				AccountType:   "codebuff",
				SessionCookie: "session_token_xyz",
				Token:         "truncated_one...",
			},
			want: "session_token_xyz",
		},
		{
			name: "refresh_token used when client_cookie and session empty",
			acc: &store.Account{
				AccountType:  "codebuff",
				RefreshToken: "refresh_token_abc",
				Token:        "truncated_two...",
			},
			want: "refresh_token_abc",
		},
		{
			name: "truncated token preview alone returns empty",
			acc: &store.Account{
				AccountType: "codebuff",
				Token:       "abcdefghij1234567890abcdefghij...",
			},
			want: "",
		},
		{
			name: "non-truncated token used as final fallback",
			acc: &store.Account{
				AccountType: "codebuff",
				Token:       "short_real_bearer",
			},
			want: "short_real_bearer",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := resolveCodebuffAuthToken(tc.acc)
			if got != tc.want {
				t.Fatalf("resolveCodebuffAuthToken = %q, want %q", got, tc.want)
			}
		})
	}
}
