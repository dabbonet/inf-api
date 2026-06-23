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
