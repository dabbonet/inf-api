package upstream

import (
	stdjson "encoding/json"

	"github.com/goccy/go-json"

	"orchids-api/internal/prompt"
)

type DirectSSEEmitter interface {
	WriteDirectSSE(event string, payload []byte, final bool)
	ObserveTextDelta(text string)
	ObserveThinkingDelta(text string)
	ObserveToolCall(name, input string)
	ObserveUsage(inputTokens, outputTokens int)
	ObserveStopReason(stopReason string)
	FinishDirectSSE(stopReason string)
}

// UpstreamRequest unified upstream request structure (Warp/Orchids reuse)
type UpstreamRequest struct {
	Prompt               string
	ChatHistory          []interface{}
	Model                string
	Stream               bool
	Messages             []prompt.Message
	System               []prompt.SystemItem
	Tools                []interface{}
	ToolChoice           interface{}
	NoTools              bool
	NoThinking           bool
	TraceID              string
	Attempt              int
	ChatSessionID        string
	Workdir              string // Dynamic local workdir override
	ProjectID            string
	IsFirstPrompt        bool
	WarpCliAgentModel    string
	WarpComputerUseModel string
	DirectSSE            DirectSSEEmitter

	// RawOpenAI preserves the original OpenAI-format messages/system JSON.
	// When set, codebuff BuildPayload uses these directly instead of
	// reconstructing from the converted prompt.Message ContentBlocks.
	RawOpenAIMessages stdjson.RawMessage `json:"-"`
	RawOpenAISystem   stdjson.RawMessage `json:"-"`
}

// SSEMessage unifies the upstream SSE message structure (Warp/Orchids reuse)
type SSEMessage struct {
	Type    string                 `json:"type"`
	Event   map[string]interface{} `json:"event,omitempty"`
	Raw     map[string]interface{} `json:"-"`
	RawJSON json.RawMessage        `json:"-"`
}
