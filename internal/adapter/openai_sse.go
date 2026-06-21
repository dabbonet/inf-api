package adapter

import (
	"bytes"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/goccy/go-json"
)

type openAIChunk struct {
	ID      string         `json:"id"`
	Object  string         `json:"object"`
	Created int64          `json:"created"`
	Model   string         `json:"model"`
	Choices []openAIChoice `json:"choices"`
}

type openAIChoice struct {
	Index        int         `json:"index"`
	Delta        openAIDelta `json:"delta"`
	FinishReason *string     `json:"finish_reason,omitempty"`
}

type openAIDelta struct {
	Role             *string          `json:"role,omitempty"`
	Content          *string          `json:"content,omitempty"`
	ReasoningContent *string          `json:"reasoning_content,omitempty"`
	ToolCalls        []openAIToolCall `json:"tool_calls,omitempty"`
}

type openAIToolCall struct {
	Index    int            `json:"index"`
	ID       string         `json:"id,omitempty"`
	Type     string         `json:"type,omitempty"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name      *string `json:"name,omitempty"`
	Arguments string  `json:"arguments"`
}

type openAIMessageStartPayload struct {
	Message *struct {
		Model string `json:"model"`
	} `json:"message"`
}

type openAIContentBlockStartPayload struct {
	ContentBlock *struct {
		Type string `json:"type"`
		Text string `json:"text,omitempty"`
		ID   string `json:"id,omitempty"`
		Name string `json:"name,omitempty"`
	} `json:"content_block"`
}

type openAIContentBlockDeltaPayload struct {
	Delta *struct {
		Type        string  `json:"type"`
		Text        *string `json:"text,omitempty"`
		PartialJSON *string `json:"partial_json,omitempty"`
		Thinking    *string `json:"thinking,omitempty"`
	} `json:"delta"`
}

type openAIMessageDeltaPayload struct {
	Delta *struct {
		StopReason *string `json:"stop_reason,omitempty"`
	} `json:"delta"`
}

const (
	openAIChunkIDPrefix        = "{\"id\":"
	openAIChunkObjectCreated   = ",\"object\":\"chat.completion.chunk\",\"created\":"
	openAIChunkModelPrefix     = ",\"model\":"
	openAIChunkChoicesPrefix   = ",\"choices\":[{\"index\":0,"
	openAIMessageStartSuffix   = "\"delta\":{\"role\":\"assistant\"}}]}"
	openAITextDeltaPrefix      = "\"delta\":{\"content\":"
	openAIContentDeltaSuffix   = "}}]}"
	openAIThinkingDeltaPrefix  = "\"delta\":{\"reasoning_content\":"
	openAIToolArgsDeltaPrefix  = "\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":"
	openAIToolArgsPrefixHead   = "\"delta\":{\"tool_calls\":[{\"index\":"
	openAIToolArgsPrefixTail   = ",\"function\":{\"arguments\":"
	openAIToolArgsDeltaSuffix  = "}}]}}]}"
	openAIToolStartDeltaPrefix = "\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":"
	openAIToolStartPrefixHead  = "\"delta\":{\"tool_calls\":[{\"index\":"
	openAIToolStartPrefixMid   = ",\"id\":"
	openAIToolStartNamePrefix  = ",\"type\":\"function\",\"function\":{\"name\":"
	openAIToolStartDeltaSuffix = ",\"arguments\":\"\"}}]}}]}"
	openAIMessageDeltaPrefix   = "\"delta\":{},\"finish_reason\":"
	openAIMessageDeltaSuffix   = "}]}"
)

func newOpenAIChunk(msgID string, created int64) openAIChunk {
	return openAIChunk{
		ID:      msgID,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   "",
		Choices: []openAIChoice{{Index: 0, Delta: openAIDelta{}}},
	}
}

func (d openAIDelta) empty() bool {
	return d.Role == nil && d.Content == nil && d.ReasoningContent == nil && len(d.ToolCalls) == 0
}

func stringPtr(v string) *string {
	return &v
}

func appendOpenAIJSONString(dst []byte, value string) ([]byte, bool) {
	for i := 0; i < len(value); i++ {
		b := value[i]
		if b < 0x20 || b == '\\' || b == '"' || b >= utf8.RuneSelf {
			quoted, err := json.Marshal(value)
			if err != nil {
				return nil, false
			}
			return append(dst, quoted...), true
		}
	}
	dst = append(dst, '"')
	dst = append(dst, value...)
	dst = append(dst, '"')
	return dst, true
}

func ensureOpenAIChunkCapacity(dst []byte, extra int) []byte {
	if extra <= 0 || cap(dst)-len(dst) >= extra {
		return dst
	}
	grown := make([]byte, len(dst), len(dst)+extra)
	copy(grown, dst)
	return grown
}

func decimalLenInt64(v int64) int {
	if v == 0 {
		return 1
	}
	n := 0
	if v < 0 {
		n++
		if v == -1<<63 {
			return 20
		}
		v = -v
	}
	for v > 0 {
		n++
		v /= 10
	}
	return n
}

var (
	openAIModelMarker           = []byte("\"model\":")
	openAIToolUseTypeMarker     = []byte("\"type\":\"tool_use\"")
	openAITextContentTypeMarker = []byte("\"type\":\"text\"")
	openAITextDeltaMarker       = []byte("\"type\":\"text_delta\"")
	openAIInputJSONMarker       = []byte("\"type\":\"input_json_delta\"")
	openAIThinkingDeltaMarker   = []byte("\"type\":\"thinking_delta\"")
	openAIStopReasonMarker      = []byte("\"stop_reason\":")
	openAITextMarker            = []byte("\"text\":")
	openAIPartialJSONMarker     = []byte("\"partial_json\":")
	openAIThinkingMarker        = []byte("\"thinking\":")
	openAIIDMarker              = []byte("\"id\":")
	openAINameMarker            = []byte("\"name\":")
	openAIStopQuoted            = []byte("\"stop\"")
	openAIToolCallsQuoted       = []byte("\"tool_calls\"")
	openAILengthQuoted          = []byte("\"length\"")
	openAIContentFilterQuoted   = []byte("\"content_filter\"")
	openAIToolUseQuoted         = []byte("\"tool_use\"")
	openAIEndTurnQuoted         = []byte("\"end_turn\"")
	openAIMaxTokensQuoted       = []byte("\"max_tokens\"")
	openAIRefusalQuoted         = []byte("\"refusal\"")
)

func normalizeOpenAIStopReasonQuoted(quotedStopReason []byte) []byte {
	switch {
	case len(quotedStopReason) == 0,
		bytes.Equal(quotedStopReason, []byte(`""`)),
		bytes.Equal(quotedStopReason, openAIStopQuoted),
		bytes.Equal(quotedStopReason, openAIEndTurnQuoted):
		return openAIStopQuoted
	case bytes.Equal(quotedStopReason, openAIToolUseQuoted):
		return openAIToolCallsQuoted
	case bytes.Equal(quotedStopReason, openAIMaxTokensQuoted):
		return openAILengthQuoted
	case bytes.Equal(quotedStopReason, openAIRefusalQuoted):
		return openAIContentFilterQuoted
	default:
		return quotedStopReason
	}
}

func normalizeOpenAIStopReason(stopReason string) string {
	switch strings.TrimSpace(stopReason) {
	case "", "stop", "end_turn":
		return "stop"
	case "tool_use":
		return "tool_calls"
	case "max_tokens":
		return "length"
	case "refusal":
		return "content_filter"
	default:
		return strings.TrimSpace(stopReason)
	}
}

func extractJSONStringValueAfter(data []byte, marker []byte) ([]byte, bool) {
	idx := bytes.Index(data, marker)
	if idx < 0 {
		return nil, false
	}
	idx += len(marker)
	for idx < len(data) {
		switch data[idx] {
		case ' ', '\t', '\n', '\r':
			idx++
			continue
		}
		break
	}
	if idx >= len(data) || data[idx] != '"' {
		return nil, false
	}
	start := idx
	idx++
	escaped := false
	for idx < len(data) {
		if escaped {
			escaped = false
			idx++
			continue
		}
		switch data[idx] {
		case '\\':
			escaped = true
		case '"':
			return data[start : idx+1], true
		}
		idx++
	}
	return nil, false
}

func estimatedOpenAIChunkPrefixLen(msgID string, created int64, quotedModel []byte) int {
	modelLen := len(quotedModel)
	if modelLen == 0 {
		modelLen = 2
	}
	return len(openAIChunkIDPrefix) + len(msgID) + 2 +
		len(openAIChunkObjectCreated) + decimalLenInt64(created) +
		len(openAIChunkModelPrefix) + modelLen +
		len(openAIChunkChoicesPrefix)
}

func appendOpenAIChunkPrefix(dst []byte, msgID string, created int64, quotedModel []byte) ([]byte, bool) {
	dst = append(dst, openAIChunkIDPrefix...)
	var ok bool
	dst, ok = appendOpenAIJSONString(dst, msgID)
	if !ok {
		return nil, false
	}
	dst = append(dst, openAIChunkObjectCreated...)
	dst = strconv.AppendInt(dst, created, 10)
	dst = append(dst, openAIChunkModelPrefix...)
	if len(quotedModel) > 0 {
		dst = append(dst, quotedModel...)
	} else {
		dst = append(dst, '"', '"')
	}
	dst = append(dst, openAIChunkChoicesPrefix...)
	return dst, true
}

func appendOpenAIChunkMessageStart(dst []byte, msgID string, created int64, quotedModel []byte) ([]byte, bool) {
	dst = ensureOpenAIChunkCapacity(dst, estimatedOpenAIChunkPrefixLen(msgID, created, quotedModel)+len(openAIMessageStartSuffix))
	dst, ok := appendOpenAIChunkPrefix(dst, msgID, created, quotedModel)
	if !ok {
		return nil, false
	}
	dst = append(dst, openAIMessageStartSuffix...)
	return dst, true
}

func appendOpenAIChunkTextWithModel(dst []byte, msgID string, created int64, quotedText []byte, quotedModel []byte) ([]byte, bool) {
	dst = ensureOpenAIChunkCapacity(dst, estimatedOpenAIChunkPrefixLen(msgID, created, quotedModel)+len(openAITextDeltaPrefix)+len(quotedText)+len(openAIContentDeltaSuffix))
	dst, ok := appendOpenAIChunkPrefix(dst, msgID, created, quotedModel)
	if !ok {
		return nil, false
	}
	dst = append(dst, openAITextDeltaPrefix...)
	dst = append(dst, quotedText...)
	dst = append(dst, openAIContentDeltaSuffix...)
	return dst, true
}

func appendOpenAIChunkThinkingWithModel(dst []byte, msgID string, created int64, quotedThinking []byte, quotedModel []byte) ([]byte, bool) {
	dst = ensureOpenAIChunkCapacity(dst, estimatedOpenAIChunkPrefixLen(msgID, created, quotedModel)+len(openAIThinkingDeltaPrefix)+len(quotedThinking)+len(openAIContentDeltaSuffix))
	dst, ok := appendOpenAIChunkPrefix(dst, msgID, created, quotedModel)
	if !ok {
		return nil, false
	}
	dst = append(dst, openAIThinkingDeltaPrefix...)
	dst = append(dst, quotedThinking...)
	dst = append(dst, openAIContentDeltaSuffix...)
	return dst, true
}

func appendOpenAIChunkToolArgsWithModel(dst []byte, msgID string, created int64, quotedArgs []byte, quotedModel []byte, toolIndex int) ([]byte, bool) {
	if toolIndex < 0 {
		toolIndex = 0
	}
	dst = ensureOpenAIChunkCapacity(dst, estimatedOpenAIChunkPrefixLen(msgID, created, quotedModel)+len(openAIToolArgsPrefixHead)+decimalLenInt64(int64(toolIndex))+len(openAIToolArgsPrefixTail)+len(quotedArgs)+len(openAIToolArgsDeltaSuffix))
	dst, ok := appendOpenAIChunkPrefix(dst, msgID, created, quotedModel)
	if !ok {
		return nil, false
	}
	dst = append(dst, openAIToolArgsPrefixHead...)
	dst = strconv.AppendInt(dst, int64(toolIndex), 10)
	dst = append(dst, openAIToolArgsPrefixTail...)
	dst = append(dst, quotedArgs...)
	dst = append(dst, openAIToolArgsDeltaSuffix...)
	return dst, true
}

func appendOpenAIChunkToolStartWithModel(dst []byte, msgID string, created int64, quotedID []byte, quotedName []byte, quotedModel []byte, toolIndex int) ([]byte, bool) {
	if toolIndex < 0 {
		toolIndex = 0
	}
	dst = ensureOpenAIChunkCapacity(dst, estimatedOpenAIChunkPrefixLen(msgID, created, quotedModel)+len(openAIToolStartPrefixHead)+decimalLenInt64(int64(toolIndex))+len(openAIToolStartPrefixMid)+len(quotedID)+len(openAIToolStartNamePrefix)+len(quotedName)+len(openAIToolStartDeltaSuffix))
	dst, ok := appendOpenAIChunkPrefix(dst, msgID, created, quotedModel)
	if !ok {
		return nil, false
	}
	dst = append(dst, openAIToolStartPrefixHead...)
	dst = strconv.AppendInt(dst, int64(toolIndex), 10)
	dst = append(dst, openAIToolStartPrefixMid...)
	dst = append(dst, quotedID...)
	dst = append(dst, openAIToolStartNamePrefix...)
	dst = append(dst, quotedName...)
	dst = append(dst, openAIToolStartDeltaSuffix...)
	return dst, true
}

func appendOpenAIChunkMessageDeltaWithModel(dst []byte, msgID string, created int64, quotedStopReason []byte, quotedModel []byte) ([]byte, bool) {
	dst = ensureOpenAIChunkCapacity(dst, estimatedOpenAIChunkPrefixLen(msgID, created, quotedModel)+len(openAIMessageDeltaPrefix)+len(quotedStopReason)+len(openAIMessageDeltaSuffix))
	dst, ok := appendOpenAIChunkPrefix(dst, msgID, created, quotedModel)
	if !ok {
		return nil, false
	}
	dst = append(dst, openAIMessageDeltaPrefix...)
	dst = append(dst, quotedStopReason...)
	dst = append(dst, openAIMessageDeltaSuffix...)
	return dst, true
}

// appendOpenAIChunkText default to empty model (legacy behaviour).
func appendOpenAIChunkText(dst []byte, msgID string, created int64, quotedText []byte) ([]byte, bool) {
	return appendOpenAIChunkTextWithModel(dst, msgID, created, quotedText, nil)
}
func appendOpenAIChunkThinking(dst []byte, msgID string, created int64, quotedThinking []byte) ([]byte, bool) {
	return appendOpenAIChunkThinkingWithModel(dst, msgID, created, quotedThinking, nil)
}
func appendOpenAIChunkToolArgs(dst []byte, msgID string, created int64, quotedArgs []byte) ([]byte, bool) {
	return appendOpenAIChunkToolArgsWithModel(dst, msgID, created, quotedArgs, nil, 0)
}
func appendOpenAIChunkToolStart(dst []byte, msgID string, created int64, quotedID []byte, quotedName []byte) ([]byte, bool) {
	return appendOpenAIChunkToolStartWithModel(dst, msgID, created, quotedID, quotedName, nil, 0)
}
func appendOpenAIChunkMessageDelta(dst []byte, msgID string, created int64, quotedStopReason []byte) ([]byte, bool) {
	return appendOpenAIChunkMessageDeltaWithModel(dst, msgID, created, quotedStopReason, nil)
}

func appendOpenAIChunkFast(dst []byte, msgID string, created int64, event string, data []byte) ([]byte, bool) {
	return appendOpenAIChunkFastWithHint(dst, msgID, created, event, data, nil, 0)
}

// appendOpenAIChunkFastWithHint is the correctness-aware fast path.
// quotedModel: optional model name to emit on every chunk (nil = legacy, emits "").
// toolIndex: per-chunk tool index, only relevant for tool events (0 = legacy).
func appendOpenAIChunkFastWithHint(dst []byte, msgID string, created int64, event string, data []byte, quotedModel []byte, toolIndex int) ([]byte, bool) {
	switch event {
	case "message_start":
		// Always extract upstream model for the chunk header (may differ from caller hint).
		upstream, ok := extractJSONStringValueAfter(data, openAIModelMarker)
		if !ok {
			return nil, false
		}
		return appendOpenAIChunkMessageStart(dst, msgID, created, upstream)
	case "content_block_start":
		if bytes.Contains(data, openAIToolUseTypeMarker) {
			quotedID, okID := extractJSONStringValueAfter(data, openAIIDMarker)
			quotedName, okName := extractJSONStringValueAfter(data, openAINameMarker)
			if okID && okName {
				return appendOpenAIChunkToolStartWithModel(dst, msgID, created, quotedID, quotedName, quotedModel, toolIndex)
			}
		}
		if bytes.Contains(data, openAITextContentTypeMarker) {
			if quotedText, ok := extractJSONStringValueAfter(data, openAITextMarker); ok && len(quotedText) > 2 {
				return appendOpenAIChunkTextWithModel(dst, msgID, created, quotedText, quotedModel)
			}
		}
	case "content_block_delta":
		switch {
		case bytes.Contains(data, openAITextDeltaMarker):
			if quotedText, ok := extractJSONStringValueAfter(data, openAITextMarker); ok {
				return appendOpenAIChunkTextWithModel(dst, msgID, created, quotedText, quotedModel)
			}
		case bytes.Contains(data, openAIInputJSONMarker):
			if quotedArgs, ok := extractJSONStringValueAfter(data, openAIPartialJSONMarker); ok {
				return appendOpenAIChunkToolArgsWithModel(dst, msgID, created, quotedArgs, quotedModel, toolIndex)
			}
		case bytes.Contains(data, openAIThinkingDeltaMarker):
			if quotedThinking, ok := extractJSONStringValueAfter(data, openAIThinkingMarker); ok {
				return appendOpenAIChunkThinkingWithModel(dst, msgID, created, quotedThinking, quotedModel)
			}
		}
	case "message_delta":
		if quotedStopReason, ok := extractJSONStringValueAfter(data, openAIStopReasonMarker); ok {
			return appendOpenAIChunkMessageDeltaWithModel(dst, msgID, created, normalizeOpenAIStopReasonQuoted(quotedStopReason), quotedModel)
		}
	case "message_stop":
		return nil, false
	}
	return nil, false
}

func buildOpenAIChunkFast(msgID string, created int64, event string, data []byte) ([]byte, bool) {
	return appendOpenAIChunkFast(nil, msgID, created, event, data)
}

func buildOpenAIChunkSlow(msgID string, created int64, event string, data []byte) ([]byte, bool) {
	return buildOpenAIChunkSlowWithHint(msgID, created, event, data, nil, 0)
}

// buildOpenAIChunkSlowWithHint is the JSON-unmarshal fallback path.
// It honours the caller's quotedModel for non-message_start events so every chunk
// emits a populated model field as required by the OpenAI streaming spec.
func buildOpenAIChunkSlowWithHint(msgID string, created int64, event string, data []byte, quotedModel []byte, toolIndex int) ([]byte, bool) {
	if toolIndex < 0 {
		toolIndex = 0
	}
	chunk := newOpenAIChunk(msgID, created)
	choice := &chunk.Choices[0]

	switch event {
	case "message_start":
		var payload openAIMessageStartPayload
		if err := json.Unmarshal(data, &payload); err != nil || payload.Message == nil {
			return nil, false
		}
		choice.Delta.Role = stringPtr("assistant")
		chunk.Model = payload.Message.Model
	case "content_block_start":
		var payload openAIContentBlockStartPayload
		if err := json.Unmarshal(data, &payload); err != nil || payload.ContentBlock == nil {
			return nil, false
		}
		switch payload.ContentBlock.Type {
		case "text":
			if payload.ContentBlock.Text != "" {
				choice.Delta.Content = stringPtr(payload.ContentBlock.Text)
			}
		case "tool_use":
			choice.Delta.ToolCalls = []openAIToolCall{{
				Index: toolIndex,
				ID:    payload.ContentBlock.ID,
				Type:  "function",
				Function: openAIFunction{
					Name:      stringPtr(payload.ContentBlock.Name),
					Arguments: "",
				},
			}}
		default:
			return nil, false
		}
	case "content_block_delta":
		var payload openAIContentBlockDeltaPayload
		if err := json.Unmarshal(data, &payload); err != nil || payload.Delta == nil {
			return nil, false
		}
		switch payload.Delta.Type {
		case "text_delta":
			if payload.Delta.Text == nil {
				return nil, false
			}
			choice.Delta.Content = payload.Delta.Text
		case "input_json_delta":
			if payload.Delta.PartialJSON == nil {
				return nil, false
			}
			choice.Delta.ToolCalls = []openAIToolCall{{
				Index: toolIndex,
				Function: openAIFunction{
					Arguments: *payload.Delta.PartialJSON,
				},
			}}
		case "thinking_delta":
			if payload.Delta.Thinking == nil {
				return nil, false
			}
			choice.Delta.ReasoningContent = payload.Delta.Thinking
		default:
			return nil, false
		}
	case "message_delta":
		var payload openAIMessageDeltaPayload
		if err := json.Unmarshal(data, &payload); err != nil || payload.Delta == nil || payload.Delta.StopReason == nil {
			return nil, false
		}
		mapped := normalizeOpenAIStopReason(*payload.Delta.StopReason)
		choice.FinishReason = stringPtr(mapped)
	case "message_stop":
		return nil, false
	case "content_block_stop":
		return nil, false
	default:
		return nil, false
	}

	if choice.Delta.empty() && choice.FinishReason == nil {
		return nil, false
	}

	// If we are not on a message_start, prefer the caller's model hint so that
	// every chunk carries a populated model field, matching the OpenAI spec.
	if event != "message_start" && len(quotedModel) >= 2 {
		if u, err := unquoteIfQuoted(quotedModel); err == nil && u != "" {
			chunk.Model = u
		}
	}

	bytes, err := json.Marshal(chunk)
	if err != nil {
		return nil, false
	}
	return bytes, true
}

// unquoteIfQuoted strips a leading/trailing double quote (lenient).
func unquoteIfQuoted(in []byte) (string, error) {
	if len(in) >= 2 && in[0] == '"' && in[len(in)-1] == '"' {
		return string(in[1 : len(in)-1]), nil
	}
	return string(in), nil
}

func AppendOpenAIChunk(dst []byte, msgID string, created int64, event string, data []byte) ([]byte, bool) {
	if raw, ok := appendOpenAIChunkFast(dst, msgID, created, event, data); ok {
		return raw, true
	}

	raw, ok := buildOpenAIChunkSlow(msgID, created, event, data)
	if !ok {
		return nil, false
	}
	if len(dst) == 0 && cap(dst) == 0 {
		return raw, true
	}
	dst = ensureOpenAIChunkCapacity(dst, len(raw))
	return append(dst, raw...), true
}

func AppendOpenAIChunkWithHint(dst []byte, msgID string, created int64, event string, data []byte, quotedModel []byte, toolIndex int) ([]byte, bool) {
	if raw, ok := appendOpenAIChunkFastWithHint(dst, msgID, created, event, data, quotedModel, toolIndex); ok {
		return raw, true
	}

	raw, ok := buildOpenAIChunkSlowWithHint(msgID, created, event, data, quotedModel, toolIndex)
	if !ok {
		return nil, false
	}
	if len(dst) == 0 && cap(dst) == 0 {
		return raw, true
	}
	dst = ensureOpenAIChunkCapacity(dst, len(raw))
	return append(dst, raw...), true
}

func BuildOpenAIChunk(msgID string, created int64, event string, data []byte) ([]byte, bool) {
	return AppendOpenAIChunk(nil, msgID, created, event, data)
}

func BuildOpenAIChunkWithHint(msgID string, created int64, event string, data []byte, quotedModel []byte, toolIndex int) ([]byte, bool) {
	return AppendOpenAIChunkWithHint(nil, msgID, created, event, data, quotedModel, toolIndex)
}
