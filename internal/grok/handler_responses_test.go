package grok

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/goccy/go-json"
)

func TestChatRequestFromResponses_ConvertsInputToolsAndReasoning(t *testing.T) {
	effort := map[string]interface{}{"effort": "high"}
	parallel := false
	req := ResponsesCreateRequest{
		Model:        "grok-4.3",
		Instructions: "用中文回答",
		Input: []interface{}{
			map[string]interface{}{"type": "message", "role": "user", "content": []interface{}{
				map[string]interface{}{"type": "input_text", "text": "上海天气"},
			}},
			map[string]interface{}{"type": "function_call", "call_id": "call_1", "name": "get_weather", "arguments": `{"city":"Shanghai"}`},
			map[string]interface{}{"type": "function_call_output", "call_id": "call_1", "output": `{"temp":25}`},
		},
		Reasoning: effort,
		Tools: []map[string]interface{}{{
			"type":        "function",
			"name":        "get_weather",
			"description": "Get weather",
			"parameters":  map[string]interface{}{"type": "object"},
		}},
		ToolChoice:        map[string]interface{}{"type": "function", "name": "get_weather"},
		ParallelToolCalls: &parallel,
	}

	chatReq, err := chatRequestFromResponses(req)
	if err != nil {
		t.Fatalf("chatRequestFromResponses() error: %v", err)
	}
	if len(chatReq.Messages) != 4 {
		t.Fatalf("messages len=%d want 4: %#v", len(chatReq.Messages), chatReq.Messages)
	}
	if chatReq.Messages[0].Role != "system" || chatReq.Messages[0].Content != "用中文回答" {
		t.Fatalf("unexpected system message: %#v", chatReq.Messages[0])
	}
	if got := chatReq.Messages[1].Content.([]interface{})[0].(map[string]interface{})["type"]; got != "text" {
		t.Fatalf("content type=%#v want text", got)
	}
	if len(chatReq.Messages[2].ToolCalls) != 1 {
		t.Fatalf("tool calls missing: %#v", chatReq.Messages[2])
	}
	if chatReq.Messages[3].Role != "tool" || chatReq.Messages[3].ToolCallID != "call_1" {
		t.Fatalf("tool output message mismatch: %#v", chatReq.Messages[3])
	}
	if chatReq.ReasoningEffort == nil || *chatReq.ReasoningEffort != "high" {
		t.Fatalf("reasoning_effort=%v want high", chatReq.ReasoningEffort)
	}
	if len(chatReq.Tools) != 1 || chatReq.Tools[0].Function["name"] != "get_weather" {
		t.Fatalf("tools mismatch: %#v", chatReq.Tools)
	}
	choice := chatReq.ToolChoice.(map[string]interface{})
	fn := choice["function"].(map[string]interface{})
	if fn["name"] != "get_weather" {
		t.Fatalf("tool_choice mismatch: %#v", choice)
	}
}

func TestResponsesObjectFromChat_ConvertsMessageAndToolCalls(t *testing.T) {
	chat := map[string]interface{}{
		"model": "grok-4.3",
		"choices": []interface{}{map[string]interface{}{
			"message": map[string]interface{}{
				"role":    "assistant",
				"content": "answer",
				"annotations": []interface{}{map[string]interface{}{
					"type": "url_citation",
					"url_citation": map[string]interface{}{
						"url": "https://example.com",
					},
				}},
				"tool_calls": []interface{}{map[string]interface{}{
					"id":   "call_1",
					"type": "function",
					"function": map[string]interface{}{
						"name":      "get_weather",
						"arguments": `{"city":"Shanghai"}`,
					},
				}},
			},
		}},
		"usage": map[string]interface{}{"prompt_tokens": float64(3), "completion_tokens": float64(4), "total_tokens": float64(7)},
	}

	resp := responsesObjectFromChat("grok-4.3", chat)
	if resp["object"] != "response" || resp["status"] != "completed" {
		t.Fatalf("unexpected response metadata: %#v", resp)
	}
	output := resp["output"].([]interface{})
	if len(output) != 2 {
		t.Fatalf("output len=%d want function_call + message: %#v", len(output), output)
	}
	fc := output[0].(map[string]interface{})
	if fc["type"] != "function_call" || fc["call_id"] != "call_1" || fc["name"] != "get_weather" {
		t.Fatalf("function_call mismatch: %#v", fc)
	}
	msg := output[1].(map[string]interface{})
	content := msg["content"].([]interface{})[0].(map[string]interface{})
	if content["text"] != "answer" {
		t.Fatalf("message text mismatch: %#v", content)
	}
	usage := resp["usage"].(map[string]interface{})
	if usage["input_tokens"] != 3 || usage["output_tokens"] != 4 || usage["total_tokens"] != 7 {
		t.Fatalf("usage mismatch: %#v", usage)
	}
}

func TestWriteResponsesStreamFromChat_ConvertsToolCallChunk(t *testing.T) {
	var b strings.Builder
	chunk := map[string]interface{}{
		"id":     "chatcmpl_1",
		"object": "chat.completion.chunk",
		"choices": []interface{}{map[string]interface{}{
			"index": 0,
			"delta": map[string]interface{}{
				"tool_calls": []interface{}{map[string]interface{}{
					"index": 0,
					"id":    "call_1",
					"type":  "function",
					"function": map[string]interface{}{
						"name":      "get_weather",
						"arguments": `{"city":"Shanghai"}`,
					},
				}},
			},
			"finish_reason": "tool_calls",
		}},
		"usage": map[string]interface{}{"prompt_tokens": 3, "completion_tokens": 2, "total_tokens": 5},
	}
	raw, _ := json.Marshal(chunk)
	b.WriteString("data: ")
	b.Write(raw)
	b.WriteString("\n\n")
	b.WriteString("data: [DONE]\n\n")

	rec := httptest.NewRecorder()
	writeResponsesStreamFromChat(rec, "grok-4.3", b.String())

	out := rec.Body.String()
	if !strings.Contains(out, "response.output_item.added") || !strings.Contains(out, "response.function_call_arguments.done") {
		t.Fatalf("expected function call response events, out=%q", out)
	}
	if !strings.Contains(out, `"call_id":"call_1"`) || !strings.Contains(out, `"name":"get_weather"`) {
		t.Fatalf("expected function call id/name, out=%q", out)
	}
	if !strings.Contains(out, `data: [DONE]`) {
		t.Fatalf("expected DONE, out=%q", out)
	}
}
