package openai

import (
	"encoding/json"
	"strings"

	"orchids-api/internal/prompt"
)

// ChatMessage is the OpenAI Chat Completions message format.
// Content can be a plain string or an array of multimodal parts.
type ChatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content,omitempty"`
	Name       string          `json:"name,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
	ToolCalls  []ToolCall      `json:"tool_calls,omitempty"`
}

// TextPart is a plain text part of a multimodal message.
type TextPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// ImageURLPart is an image_url part of a multimodal message.
type ImageURLPart struct {
	Type     string `json:"type"`
	ImageURL struct {
		URL    string `json:"url"`
		Detail string `json:"detail,omitempty"`
	} `json:"image_url"`
}

// Tool defines a function/tool the model may call.
type Tool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
		Strict      bool            `json:"strict,omitempty"`
	} `json:"function"`
}

// ToolCall is a model-emitted tool invocation.
type ToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

// ChatRequest is the OpenAI Chat Completions request body.
type ChatRequest struct {
	Model            string          `json:"model"`
	Messages         []ChatMessage   `json:"messages"`
	Temperature      *float64        `json:"temperature,omitempty"`
	TopP             *float64        `json:"top_p,omitempty"`
	MaxTokens        *int            `json:"max_tokens,omitempty"`
	MaxCompletionTok *int            `json:"max_completion_tokens,omitempty"`
	Stop             json.RawMessage `json:"stop,omitempty"`
	PresencePenalty  *float64        `json:"presence_penalty,omitempty"`
	FrequencyPenalty *float64        `json:"frequency_penalty,omitempty"`
	N                *int            `json:"n,omitempty"`
	ResponseFormat   json.RawMessage `json:"response_format,omitempty"`
	Seed             *int64          `json:"seed,omitempty"`
	User             string          `json:"user,omitempty"`
	Tools            []Tool          `json:"tools,omitempty"`
	ToolChoice       json.RawMessage `json:"tool_choice,omitempty"`
	Stream           bool            `json:"stream"`
	StreamOptions    *StreamOptions  `json:"stream_options,omitempty"`
}

// StreamOptions controls streaming behaviour (e.g. include_usage).
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

// ChatResponse is the non-streaming OpenAI Chat Completions response.
type ChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

// Choice is one completion choice.
type Choice struct {
	Index        int          `json:"index"`
	Message      ChatMessage  `json:"message"`
	FinishReason *string      `json:"finish_reason"`
	Delta        *ChatMessage `json:"delta,omitempty"`
}

// Usage is the OpenAI token usage block.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ErrorEnvelope is the OpenAI error response format.
type ErrorEnvelope struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
		Code    string `json:"code,omitempty"`
		Param   string `json:"param,omitempty"`
	} `json:"error"`
}

// ImageRequest is the OpenAI Image Generation request body.
type ImageRequest struct {
	Prompt         string `json:"prompt"`
	Model          string `json:"model,omitempty"`
	N              *int   `json:"n,omitempty"`
	Size           string `json:"size,omitempty"`
	ResponseFormat string `json:"response_format,omitempty"`
	User           string `json:"user,omitempty"`
	Quality        string `json:"quality,omitempty"`
	Style          string `json:"style,omitempty"`
}

// ImageData is a single image in the response.
type ImageData struct {
	URL           string `json:"url,omitempty"`
	B64JSON       string `json:"b64_json,omitempty"`
	RevisedPrompt string `json:"revised_prompt,omitempty"`
}

// ImageResponse is the OpenAI Image Generation response body.
type ImageResponse struct {
	Created int64       `json:"created"`
	Data    []ImageData `json:"data"`
}

// ModelInfo is a single model entry in the /v1/models response.
type ModelInfo struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ModelsResponse is the /v1/models response body.
type ModelsResponse struct {
	Object string      `json:"object"`
	Data   []ModelInfo `json:"data"`
}

// promptToOpenAIMessages converts internal prompt messages into OpenAI ChatMessage.
// It merges the system items into a leading "system" message, flattens tool_result
// blocks into role="tool" messages, and keeps multimodal (image_url) parts intact.
func promptToOpenAIMessages(system []prompt.SystemItem, messages []prompt.Message) []ChatMessage {
	out := make([]ChatMessage, 0, len(messages)+1)

	if sys := systemText(system); sys != "" {
		out = append(out, ChatMessage{
			Role:    "system",
			Content: jsonRawString(sys),
		})
	}

	for _, m := range messages {
		role := strings.ToLower(strings.TrimSpace(m.Role))
		if role == "" {
			continue
		}

		// Tool result blocks become a "tool" message.
		if role == "user" && !m.Content.IsString() {
			blocks := m.Content.GetBlocks()
			if hasOnlyToolResults(blocks) {
				for _, b := range blocks {
					if b.Type != "tool_result" {
						continue
					}
					out = append(out, ChatMessage{
						Role:       "tool",
						Content:    jsonRawString(extractToolResultText(b)),
						ToolCallID: strings.TrimSpace(b.ToolUseID),
					})
				}
				continue
			}
		}

		// Assistant with tool_use blocks: emit assistant message with tool_calls (no text).
		if role == "assistant" && !m.Content.IsString() {
			blocks := m.Content.GetBlocks()
			if tcBlocks := collectAssistantToolCalls(blocks); len(tcBlocks) > 0 {
				msg := ChatMessage{Role: "assistant"}
				msg.ToolCalls = tcBlocks
				// Keep any accompanying text in a content part.
				if text := concatText(blocks); text != "" {
					msg.Content = jsonRawString(text)
				}
				out = append(out, msg)
				continue
			}
		}

		// Standard string content.
		if m.Content.IsString() {
			out = append(out, ChatMessage{
				Role:    role,
				Content: jsonRawString(m.Content.GetText()),
			})
			continue
		}

		// Multimodal content: text + image_url parts.
		parts := contentBlocksToParts(m.Content.GetBlocks())
		if len(parts) == 0 {
			out = append(out, ChatMessage{Role: role, Content: jsonRawString("")})
			continue
		}
		raw, _ := json.Marshal(parts)
		out = append(out, ChatMessage{
			Role:    role,
			Content: raw,
		})
	}
	return out
}
