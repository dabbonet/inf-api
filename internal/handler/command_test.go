package handler

import (
	"testing"

	"orchids-api/internal/prompt"
)

func TestIsCurrentWorkdirRequest(t *testing.T) {
	tests := []struct {
		name string
		text string
		want bool
	}{
		{name: "zh current directory", text: "current running directory", want: true},
		{name: "en workspace path", text: "workspace path", want: true},
		{name: "pwd", text: "pwd", want: true},
		{name: "directory tree", text: "Current directory structure", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := ClaudeRequest{
				Messages: []prompt.Message{{
					Role:    "user",
					Content: prompt.MessageContent{Text: tt.text},
				}},
			}
			if got := isCurrentWorkdirRequest(req); got != tt.want {
				t.Fatalf("isCurrentWorkdirRequest(%q) = %v, want %v", tt.text, got, tt.want)
			}
		})
	}
}
