package handler

import "testing"

func TestMapModel(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		// Opus 4.6 series
		{"claude-opus-4-6", "claude-opus-4-6"},
		{"claude-opus-4.6", "claude-opus-4-6"},
		{"claude-opus-4-6-thinking", "claude-opus-4-6"},
		{"claude-opus-4.6-thinking", "claude-opus-4-6"},

		// Opus 4.5 Series
		{"claude-opus-4-5", "claude-opus-4-6"},
		{"claude-opus-4.5", "claude-opus-4-6"},
		{"claude-opus-4-5-thinking", "claude-opus-4-5-thinking"},
		{"claude-opus-4.5-thinking", "claude-opus-4-5-thinking"},

		// Sonnet 3.7 exact version
		{"claude-3-7-sonnet-20250219", "claude-3-7-sonnet-20250219"},

		// Sonnet 4.5 series
		{"claude-sonnet-4-5", "claude-sonnet-4-6"},
		{"claude-sonnet-4.5", "claude-sonnet-4-6"},
		{"claude-sonnet-4-5-thinking", "claude-sonnet-4-5-thinking"},
		{"claude-sonnet-4.5-thinking", "claude-sonnet-4-5-thinking"},

		// Sonnet 4.6 Series
		{"claude-sonnet-4-6", "claude-sonnet-4-6"},
		{"claude-sonnet-4.6", "claude-sonnet-4-6"},
		{"claude-sonnet-4-6-thinking", "claude-sonnet-4-6"},
		{"claude-sonnet-4.6-thinking", "claude-sonnet-4-6"},

		// Sonnet 4 exact version number
		{"claude-sonnet-4-20250514", "claude-sonnet-4-20250514"},

		// Haiku 4.5 series
		{"claude-haiku-4-5", "claude-haiku-4-5"},
		{"claude-haiku-4.5", "claude-haiku-4-5"},

		// Gemini
		{"gemini-3-flash", "gemini-3-flash"},
		{"gemini-3-pro", "gemini-3-pro"},

		// GPT
		{"gpt-5.3-codex", "gpt-5.3-codex"},
		{"gpt-5.2-codex", "gpt-5.2-codex"},
		{"gpt-5.2", "gpt-5.2"},

		// Other models
		{"grok-4.1-fast", "grok-4.1-fast"},
		{"glm-5", "glm-5"},
		{"kimi-k2.5", "kimi-k2.5"},

		// default
		{"", "claude-sonnet-4-6"},
		{"unknown-model", "claude-sonnet-4-6"},

		// Mixed case
		{"Claude-Opus-4-5", "claude-opus-4-6"},
		{"CLAUDE-SONNET-4-5-THINKING", "claude-sonnet-4-5-thinking"},
		{"Claude-Haiku-4.5", "claude-haiku-4-5"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := mapModel(tt.input)
			if got != tt.want {
				t.Errorf("mapModel(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}
