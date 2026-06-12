package openai

import (
	"encoding/json"
	"strings"

	"orchids-api/internal/prompt"
)

func systemText(system []prompt.SystemItem) string {
	if len(system) == 0 {
		return ""
	}
	parts := make([]string, 0, len(system))
	for _, s := range system {
		if text := strings.TrimSpace(s.Text); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, "\n\n")
}

func jsonRawString(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage(`""`)
	}
	b, _ := json.Marshal(s)
	return b
}

func hasOnlyToolResults(blocks []prompt.ContentBlock) bool {
	if len(blocks) == 0 {
		return false
	}
	for _, b := range blocks {
		if b.Type != "tool_result" {
			return false
		}
	}
	return true
}

func extractToolResultText(b prompt.ContentBlock) string {
	if b.Content == nil {
		return ""
	}
	if str, ok := b.Content.(string); ok {
		return str
	}
	raw, err := json.Marshal(b.Content)
	if err != nil {
		return ""
	}
	return string(raw)
}

func collectAssistantToolCalls(blocks []prompt.ContentBlock) []ToolCall {
	out := make([]ToolCall, 0, len(blocks))
	for _, b := range blocks {
		if b.Type != "tool_use" {
			continue
		}
		tc := ToolCall{
			ID:   strings.TrimSpace(b.ToolUseID),
			Type: "function",
		}
		tc.Function.Name = strings.TrimSpace(b.Name)
		if b.Input != nil {
			raw, err := json.Marshal(b.Input)
			if err == nil {
				tc.Function.Arguments = string(raw)
			}
		}
		out = append(out, tc)
	}
	return out
}

func concatText(blocks []prompt.ContentBlock) string {
	parts := make([]string, 0, len(blocks))
	for _, b := range blocks {
		if b.Type != "text" {
			continue
		}
		if t := strings.TrimSpace(b.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, "\n\n")
}

func contentBlocksToParts(blocks []prompt.ContentBlock) []interface{} {
	parts := make([]interface{}, 0, len(blocks))
	for _, b := range blocks {
		switch b.Type {
		case "text":
			if strings.TrimSpace(b.Text) == "" {
				continue
			}
			parts = append(parts, TextPart{Type: "text", Text: b.Text})
		case "image":
			url := strings.TrimSpace(b.URL)
			if url == "" {
				continue
			}
			p := ImageURLPart{Type: "image_url"}
			p.ImageURL.URL = url
			p.ImageURL.Detail = "auto"
			parts = append(parts, p)
		case "image_url":
			url := strings.TrimSpace(b.URL)
			if url == "" {
				continue
			}
			p := ImageURLPart{Type: "image_url"}
			p.ImageURL.URL = url
			p.ImageURL.Detail = "auto"
			parts = append(parts, p)
		}
	}
	return parts
}
