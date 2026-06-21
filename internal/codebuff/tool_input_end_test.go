package codebuff

import (
	"testing"
)

func TestDecodeChunk_ToolCallEndSequence(t *testing.T) {
	sp := NewStreamParser(nil)

	chunks := []map[string]any{
		{
			"choices": []any{
				map[string]any{
					"delta": map[string]any{
						"role": "assistant",
						"tool_calls": []any{
							map[string]any{
								"id":       "tool_1",
								"type":     "function",
								"function": map[string]any{"name": "bash", "arguments": ""},
							},
						},
					},
				},
			},
		},
		{
			"choices": []any{
				map[string]any{
					"delta": map[string]any{
						"tool_calls": []any{
							map[string]any{
								"index":    0,
								"function": map[string]any{"arguments": "{\"cmd\":"},
							},
						},
					},
				},
			},
		},
		{
			"choices": []any{
				map[string]any{
					"delta": map[string]any{
						"tool_calls": []any{
							map[string]any{
								"index":    0,
								"function": map[string]any{"arguments": "\"ls\"}"},
							},
						},
					},
				},
			},
		},
		{
			"choices": []any{
				map[string]any{
					"delta": map[string]any{},
					"finish_reason": "tool_calls",
				},
			},
		},
	}

	var events []string
	for _, chunk := range chunks {
		for _, m := range decodeChunk(chunk, sp) {
			events = append(events, m.Type)
		}
	}

	startCount, deltaCount, endCount := 0, 0, 0
	for _, e := range events {
		switch e {
		case "model.tool-input-start":
			startCount++
		case "model.tool-input-delta":
			deltaCount++
		case "model.tool-input-end":
			endCount++
		}
	}

	if startCount != 1 {
		t.Fatalf("expected exactly 1 start event, got %d. events=%v", startCount, events)
	}
	if deltaCount != 2 {
		t.Fatalf("expected exactly 2 delta events, got %d. events=%v", deltaCount, events)
	}
	if endCount != 1 {
		t.Fatalf("expected exactly 1 end event on finish_reason, got %d. events=%v", endCount, events)
	}
}

func TestDecodeChunk_ToolCallNoDuplicateStart(t *testing.T) {
	sp := NewStreamParser(nil)

	chunk := map[string]any{
		"choices": []any{
			map[string]any{
				"delta": map[string]any{
					"role": "assistant",
					"tool_calls": []any{
						map[string]any{
							"id":       "tool_42",
							"type":     "function",
							"function": map[string]any{"name": "bash", "arguments": ""},
						},
					},
				},
			},
		},
	}

	first := decodeChunk(chunk, sp)
	second := decodeChunk(chunk, sp)

	startCountFirst := 0
	for _, m := range first {
		if m.Type == "model.tool-input-start" {
			startCountFirst++
		}
	}
	if startCountFirst != 1 {
		t.Fatalf("first decode expected 1 start, got %d", startCountFirst)
	}
	for _, m := range second {
		if m.Type == "model.tool-input-start" {
			t.Fatalf("second decode emitted duplicate start for same id; events=%v", second)
		}
	}
}
