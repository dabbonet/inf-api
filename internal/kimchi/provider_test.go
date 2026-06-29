package kimchi

import (
	"context"
	"strings"
	"testing"

	"orchids-api/internal/store"
	"orchids-api/internal/upstream"
)

func TestResolveAuthToken(t *testing.T) {
	cases := []struct {
		name string
		acc  *store.Account
		want string
	}{
		{"nil account returns empty", nil, ""},
		{"empty account returns empty", &store.Account{AccountType: "kimchi"}, ""},
		{
			"refresh_token wins over all",
			&store.Account{
				AccountType:   "kimchi",
				RefreshToken:  "castai_v1_R",
				SessionCookie: "castai_v1_S",
				ClientCookie:  "castai_v1_C",
				Token:         "castai_v1_T",
			},
			"castai_v1_R",
		},
		{
			"session_cookie used when refresh_token empty",
			&store.Account{AccountType: "kimchi", SessionCookie: "castai_v1_S", ClientCookie: "castai_v1_C", Token: "castai_v1_T"},
			"castai_v1_S",
		},
		{
			"client_cookie used when refresh+session empty",
			&store.Account{AccountType: "kimchi", ClientCookie: "castai_v1_C", Token: "castai_v1_T"},
			"castai_v1_C",
		},
		{
			"token used as last fallback",
			&store.Account{AccountType: "kimchi", Token: "castai_v1_T"},
			"castai_v1_T",
		},
		{
			"truncated token preview rejected",
			&store.Account{AccountType: "kimchi", Token: "castai_v1_fb183518ce28d750504e1bf9..."},
			"",
		},
		{
			"whitespace trimmed",
			&store.Account{AccountType: "kimchi", RefreshToken: "  castai_v1_X  "},
			"castai_v1_X",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := ResolveAuthToken(tc.acc)
			if got != tc.want {
				t.Fatalf("ResolveAuthToken: want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestNewFromAccount_NilOnMissingCredential(t *testing.T) {
	if got := NewFromAccount(&store.Account{AccountType: "kimchi"}, nil); got != nil {
		t.Fatalf("expected nil client for empty account, got %+v", got)
	}
}

func TestGuessKind(t *testing.T) {
	cases := []struct {
		name string
		body string
		want endpointKind
	}{
		{"openai chat completions", `{"model":"k","messages":[{"role":"user","content":"hi"}]}`, endpointOpenAI},
		{"openai with string system", `{"model":"k","system":"be brief","messages":[]}`, endpointOpenAI},
		{"anthropic messages with system array", `{"model":"k","system":[{"type":"text","text":"be brief"}],"messages":[]}`, endpointAnthropic},
		{"anthropic messages with version header", `{"model":"k","anthropic_version":"2023-06-01","messages":[]}`, endpointAnthropic},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := guessKind([]byte(tc.body))
			if got != tc.want {
				t.Fatalf("guessKind(%q): want %d, got %d", tc.body, tc.want, got)
			}
		})
	}
}

func TestHTTPErrorFormatting(t *testing.T) {
	e := &HTTPError{Status: 401, Body: "  bad token\n"}
	if !strings.Contains(e.Error(), "401") {
		t.Fatalf("error must mention status: %s", e.Error())
	}
	if !strings.Contains(e.Error(), "bad token") {
		t.Fatalf("error must include body: %s", e.Error())
	}
	// Large body should be truncated.
	long := strings.Repeat("x", 1000)
	e2 := &HTTPError{Status: 500, Body: long}
	if !strings.HasSuffix(e2.Error(), "...") {
		t.Fatalf("expected truncation suffix on long body: %s", e2.Error())
	}
}

// TestProviderImplementsUpstreamClient is a compile-time check that the
// provider wires into the upstream interface. It catches accidental
// signature drift when UpstreamClient evolves.
func TestProviderImplementsUpstreamClient(t *testing.T) {
	var _ upstream.UpstreamClient = (*Provider)(nil)
	var _ upstream.UpstreamClient = NewFromAccount(&store.Account{
		AccountType:  "kimchi",
		RefreshToken: "castai_v1_test",
	}, nil)
}

// TestProviderRespectsContextCancelled verifies SendRequestWithPayload
// returns promptly when the request context is already cancelled.
func TestProviderRespectsContextCancelled(t *testing.T) {
	p := NewFromAccount(&store.Account{
		AccountType:  "kimchi",
		RefreshToken: "castai_v1_test",
	}, nil)
	if p == nil {
		t.Fatalf("expected non-nil provider")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := upstream.UpstreamRequest{
		RawBody: []byte(`{"model":"kimi-k2.7","messages":[{"role":"user","content":"hi"}]}`),
		Stream:  false,
	}
	err := p.SendRequestWithPayload(ctx, req, nil, nil)
	if err == nil {
		t.Fatalf("expected error when context cancelled")
	}
}
