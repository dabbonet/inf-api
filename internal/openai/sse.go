package openai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
)

// StreamChunk is one OpenAI streaming chunk translated into upstream-friendly pieces.
// Callers receive a series of chunks; an empty Choices+Usage pair means the stream ended
// gracefully (the upstream emitted data: [DONE] or the connection closed).
type StreamChunk struct {
	// ID/Model come from the first chunk and are stable for the stream.
	ID    string
	Model string

	// DeltaContent is incremental assistant text.
	DeltaContent string

	// DeltaToolCalls carries tool-call deltas (id/name/arguments).
	DeltaToolCalls []ToolCall

	// FinishReason is set on the final chunk for a choice (typically "stop",
	// "length", "tool_calls", or "content_filter").
	FinishReason string

	// Usage is populated on the final chunk when the upstream includes it
	// (requires stream_options.include_usage=true).
	Usage *Usage
}

// StreamParser consumes an OpenAI-compatible SSE byte stream and yields StreamChunks.
// It is safe to use from a single goroutine; it owns the underlying reader.
type StreamParser struct {
	reader  *bufio.Reader
	pending bytes.Buffer

	// toolCallBuffers accumulates incremental deltas for each tool call.
	// The OpenAI streaming protocol emits tool_calls[].function.arguments
	// character-by-character across many chunks; we coalesce by index.
	toolCallBuffers map[int]*toolCallBuffer
}

type toolCallBuffer struct {
	id        string
	name      string
	arguments strings.Builder
}

// NewStreamParser wraps an io.Reader with an OpenAI SSE parser.
func NewStreamParser(r io.Reader) *StreamParser {
	return &StreamParser{
		reader:          bufio.NewReaderSize(r, 64*1024),
		toolCallBuffers: make(map[int]*toolCallBuffer),
	}
}

// Next reads the next chunk from the stream. io.EOF is returned when the stream
// is complete (data: [DONE] was seen or the connection closed cleanly).
// A non-nil error is returned only on I/O or JSON parse failures.
func (p *StreamParser) Next(ctx context.Context) (*StreamChunk, error) {
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		line, err := p.readLine()
		if err != nil {
			if err == io.EOF {
				return nil, io.EOF
			}
			return nil, err
		}

		// SSE comment lines start with ':' — zenmux sends ": ZENMUX PROCESSING"
		// keep-alives. Per the SSE spec, lines beginning with ':' must be ignored.
		if strings.HasPrefix(line, ":") {
			continue
		}

		// OpenAI uses "data: " prefix. We accept "data:" without space too.
		var payload string
		switch {
		case strings.HasPrefix(line, "data: "):
			payload = strings.TrimPrefix(line, "data: ")
		case strings.HasPrefix(line, "data:"):
			payload = strings.TrimPrefix(line, "data:")
		default:
			// Other SSE fields (event:, id:, retry:) are ignored.
			continue
		}

		payload = strings.TrimSpace(payload)
		if payload == "" {
			continue
		}
		if payload == "[DONE]" {
			return nil, io.EOF
		}

		var resp ChatResponse
		if err := json.Unmarshal([]byte(payload), &resp); err != nil {
			return nil, fmt.Errorf("openai stream: invalid chunk payload: %w", err)
		}

		chunk := &StreamChunk{
			ID:    resp.ID,
			Model: resp.Model,
		}
		if len(resp.Choices) > 0 {
			c := resp.Choices[0]
			if c.Delta != nil {
				chunk.DeltaContent = extractDeltaContent(c.Delta)
				chunk.DeltaToolCalls = p.coalesceToolCalls(c.Delta.ToolCalls)
			}
			if c.FinishReason != nil {
				chunk.FinishReason = *c.FinishReason
			}
		}
		if resp.Usage != nil {
			chunk.Usage = resp.Usage
		}
		return chunk, nil
	}
}

// FinalToolCalls returns the fully assembled tool calls (one per index).
// Call this after the stream has ended and you've collected all deltas.
func (p *StreamParser) FinalToolCalls() []ToolCall {
	if len(p.toolCallBuffers) == 0 {
		return nil
	}
	maxIdx := -1
	for idx := range p.toolCallBuffers {
		if idx > maxIdx {
			maxIdx = idx
		}
	}
	out := make([]ToolCall, 0, maxIdx+1)
	for i := 0; i <= maxIdx; i++ {
		buf, ok := p.toolCallBuffers[i]
		if !ok {
			continue
		}
		tc := ToolCall{
			ID:   buf.id,
			Type: "function",
		}
		tc.Function.Name = buf.name
		tc.Function.Arguments = buf.arguments.String()
		out = append(out, tc)
	}
	return out
}

func (p *StreamParser) readLine() (string, error) {
	for {
		if p.pending.Len() > 0 {
			if i := bytes.IndexByte(p.pending.Bytes(), '\n'); i >= 0 {
				line := p.pending.Next(i)
				p.pending.Next(1) // consume '\n'
				return strings.TrimRight(string(line), "\r"), nil
			}
		}

		buf := make([]byte, 4096)
		n, err := p.reader.Read(buf)
		if n > 0 {
			p.pending.Write(buf[:n])
		}
		if err != nil {
			if p.pending.Len() > 0 {
				line := p.pending.String()
				p.pending.Reset()
				return strings.TrimRight(line, "\r"), err
			}
			return "", err
		}
	}
}

func extractDeltaContent(d *ChatMessage) string {
	if d == nil || len(d.Content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(d.Content, &s); err == nil {
		return s
	}
	// Some upstreams embed content as an array even in deltas; we extract text parts.
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(d.Content, &parts); err == nil {
		var b strings.Builder
		for _, p := range parts {
			if p.Type == "text" {
				b.WriteString(p.Text)
			}
		}
		return b.String()
	}
	return ""
}

func (p *StreamParser) coalesceToolCalls(deltas []ToolCall) []ToolCall {
	if len(deltas) == 0 {
		return nil
	}
	out := make([]ToolCall, 0, len(deltas))
	for _, d := range deltas {
		idx := len(out)
		buf, ok := p.toolCallBuffers[idx]
		if !ok {
			buf = &toolCallBuffer{}
			p.toolCallBuffers[idx] = buf
		}
		if d.ID != "" {
			buf.id = d.ID
		}
		if d.Function.Name != "" {
			buf.name = d.Function.Name
		}
		if d.Function.Arguments != "" {
			buf.arguments.WriteString(d.Function.Arguments)
		}
		out = append(out, d)
	}
	return out
}
