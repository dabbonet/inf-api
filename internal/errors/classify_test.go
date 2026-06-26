package errors

import (
	"errors"
	"testing"
)

func TestIsAccountAuthFailure(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "signed out", err: errors.New("signed out: no active sessions found"), want: true},
		{name: "forbidden", err: errors.New("HTTP 403 forbidden"), want: true},
		{name: "rate limit", err: errors.New("too many requests"), want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsAccountAuthFailure(tt.err); got != tt.want {
				t.Fatalf("IsAccountAuthFailure(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestClassifyUpstreamError(t *testing.T) {
	tests := []struct {
		name         string
		errStr       string
		wantCategory string
		wantRetry    bool
		wantSwitch   bool
	}{
		{
			name:         "model not found is client error",
			errStr:       "puter API error: message=Model not found, please try another model",
			wantCategory: "client",
			wantRetry:    false,
			wantSwitch:   false,
		},
		{
			name:         "no implementation available is client error",
			errStr:       "puter API error: code=no_implementation_available, status=502, message=No implementation available for interface `puter-chat-completion`.",
			wantCategory: "client",
			wantRetry:    false,
			wantSwitch:   false,
		},
		{
			name:         "insufficient funds is rate limit",
			errStr:       "puter API error: code=insufficient_funds, status=402, message=Available funding is insufficient for this request.",
			wantCategory: "rate_limit",
			wantRetry:    true,
			wantSwitch:   true,
		},
		{
			name:         "warp quota limit switches account",
			errStr:       "warp stream finished with quota_limit: no remaining quota",
			wantCategory: "rate_limit",
			wantRetry:    true,
			wantSwitch:   true,
		},
		{
			name:         "warp context window is client error",
			errStr:       "warp stream finished with context_window_exceeded: input is too long",
			wantCategory: "client",
			wantRetry:    false,
			wantSwitch:   false,
		},
		{
			name:         "warp invalid api key switches account",
			errStr:       "warp stream finished with invalid_api_key: provider=openai model=gpt-test",
			wantCategory: "auth",
			wantRetry:    true,
			wantSwitch:   true,
		},
		{
			name:         "warp llm unavailable is server error",
			errStr:       "warp stream finished with llm_unavailable: model unavailable",
			wantCategory: "model_unavailable",
			wantRetry:    true,
			wantSwitch:   true,
		},
		{
			name:         "warp 400 model not allowed switches account",
			errStr:       `warp stream request failed: HTTP 400: {"error":"Invalid request: the requested base model (claude-4-5-opus) is not allowed for your account"}`,
			wantCategory: "model_unavailable",
			wantRetry:    true,
			wantSwitch:   true,
		},
		{
			name:         "warp 400 no model available switches account",
			errStr:       `warp stream request failed: HTTP 400: {"error":"Invalid request: the requested base model (gemini-3-1-pro) has no model available"}`,
			wantCategory: "model_unavailable",
			wantRetry:    true,
			wantSwitch:   true,
		},
		{
			name:         "warp max token limit is client error",
			errStr:       "warp stream finished with max_token_limit: maximum output tokens reached",
			wantCategory: "client",
			wantRetry:    false,
			wantSwitch:   false,
		},
		{
			name:         "codebuff 409 model_locked",
			errStr:       "Codebuff chat failed: status=409 body={\"error\":\"model_locked\"}",
			wantCategory: "session_conflict",
			wantRetry:    true,
			wantSwitch:   true,
		},
		{
			name:         "codebuff 409 session_model_mismatch",
			errStr:       "Codebuff request failed: status=409 body={\"error\":\"session_model_mismatch\"}",
			wantCategory: "session_conflict",
			wantRetry:    true,
			wantSwitch:   true,
		},
		{
			name:         "codebuff model_locked in error string",
			errStr:       "codebuff session acquisition failed: Codebuff request failed: status=409 body={\"error\":\"model_locked\",\"retryAfterMs\":2000}",
			wantCategory: "session_conflict",
			wantRetry:    true,
			wantSwitch:   true,
		},
		{
			name:         "scanner unexpected eof",
			errStr:       "codebuff stream error: unexpected EOF",
			wantCategory: "network",
			wantRetry:    true,
			wantSwitch:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ClassifyUpstreamError(tt.errStr)
			if got.Category != tt.wantCategory || got.Retryable != tt.wantRetry || got.SwitchAccount != tt.wantSwitch {
				t.Fatalf("ClassifyUpstreamError(%q) = %#v, want category=%q retry=%v switch=%v", tt.errStr, got, tt.wantCategory, tt.wantRetry, tt.wantSwitch)
			}
		})
	}
}
