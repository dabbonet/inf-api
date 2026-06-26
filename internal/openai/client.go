package openai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"orchids-api/internal/config"
	"orchids-api/internal/debug"
	"orchids-api/internal/store"
	"orchids-api/internal/upstream"
	"orchids-api/internal/util"
)

// Client is a generic OpenAI-compatible HTTP client.
// It can be embedded by channel-specific wrappers which only customize
// the base URL, default model, and channel tag.
type Client struct {
	channel       string
	baseURL       string
	apiKey        string
	defaultModel  string
	httpClient    *http.Client
	sharedClient  bool
	userAgent     string
}

// NewClient builds an OpenAI-compatible client from a store account.
// baseURL is the upstream root (e.g. "https://api.example.com/v1"); it must
// not end with a slash. defaultModel is the model used when the caller does
// not supply one.
func NewClient(channel, baseURL, defaultModel string, acc *store.Account, cfg *config.Config) *Client {
	timeout := 5 * time.Minute
	if cfg != nil && cfg.RequestTimeout > 0 {
		timeout = time.Duration(cfg.RequestTimeout) * time.Second
		if timeout < 30*time.Second {
			timeout = 30 * time.Second
		}
	}

	proxyFunc := http.ProxyFromEnvironment
	proxyKey := "direct"
	if cfg != nil {
		proxyFunc = util.ProxyFuncFromConfig(cfg)
		proxyKey = util.GenerateProxyKeyFromConfig(cfg)
	}

	return &Client{
		channel:      channel,
		baseURL:      strings.TrimRight(baseURL, "/"),
		apiKey:       ResolveAPIKey(acc),
		defaultModel: defaultModel,
		httpClient:   util.GetSharedHTTPClient(proxyKey, timeout, proxyFunc),
		sharedClient: true,
		userAgent:    "Mozilla/5.0 (compatible; orchids-api/1.0; +" + channel + ")",
	}
}

// ResolveAPIKey returns the bearer token for an account.
// Preference order: Token, RefreshToken, ClientCookie.
// Returns "" if none is set.
func ResolveAPIKey(acc *store.Account) string {
	if acc == nil {
		return ""
	}
	for _, v := range []string{acc.Token, acc.RefreshToken, acc.ClientCookie} {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		return v
	}
	return ""
}

// Channel returns the channel tag (lowercase, e.g. "puter").
func (c *Client) Channel() string { return c.channel }

// BaseURL returns the upstream root (no trailing slash).
func (c *Client) BaseURL() string { return c.baseURL }

// APIKey returns the bearer token in use (empty if not configured).
func (c *Client) APIKey() string { return c.apiKey }

// Close releases the underlying HTTP transport if owned.
// When using the shared client pool this is a no-op.
func (c *Client) Close() {
	if c == nil || c.sharedClient || c.httpClient == nil || c.httpClient.Transport == nil {
		return
	}
	if closer, ok := c.httpClient.Transport.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// SendRequestWithPayload implements handler.UpstreamClient.
// It translates an internal upstream.UpstreamRequest into an OpenAI
// Chat Completions request, streams the upstream response, and emits
// upstream.SSEMessage events on onMessage.
func (c *Client) SendRequestWithPayload(
	ctx context.Context,
	req upstream.UpstreamRequest,
	onMessage func(upstream.SSEMessage),
	logger *debug.Logger,
) error {
	if c == nil {
		return fmt.Errorf("openai client is nil")
	}
	if strings.TrimSpace(c.apiKey) == "" {
		return fmt.Errorf("missing %s api key", c.channel)
	}

	modelID := strings.TrimSpace(req.Model)
	if modelID == "" {
		modelID = c.defaultModel
	}

	body, err := c.buildChatBody(modelID, req)
	if err != nil {
		return fmt.Errorf("failed to marshal openai request: %w", err)
	}
	if logger != nil {
		logger.LogUpstreamRequest(c.baseURL+"/chat/completions", map[string]string{"channel": c.channel}, body)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("failed to create openai request: %w", err)
	}
	c.applyHeaders(httpReq, req.Stream)

	resp, err := c.httpClient.Do(httpReq)
	if err != nil {
		return fmt.Errorf("failed to send openai request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return fmt.Errorf("%s API error: status=%d, body=%s", c.channel, resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	// Non-stream responses return a plain JSON ChatCompletion object.
	// Stream responses return SSE text/event-stream.
	if !req.Stream {
		return c.consumeJSON(ctx, modelID, resp.Body, onMessage)
	}

	stream := NewStreamParser(resp.Body)
	return c.consumeStream(ctx, modelID, stream, onMessage)
}

func (c *Client) buildChatBody(modelID string, req upstream.UpstreamRequest) ([]byte, error) {
	messages := promptToOpenAIMessages(req.System, req.Messages)
	body := ChatRequest{
		Model:       modelID,
		Messages:    messages,
		Stream:      req.Stream,
		User:        strings.TrimSpace(req.TraceID),
		StreamOptions: &StreamOptions{IncludeUsage: true},
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func (c *Client) applyHeaders(r *http.Request, stream bool) {
	r.Header.Set("Authorization", "Bearer "+c.apiKey)
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("Accept", "application/json")
	if stream {
		r.Header.Set("Accept-Encoding", "gzip, deflate, br")
	}
	r.Header.Set("User-Agent", c.userAgent)
	r.Header.Set("Cache-Control", "no-cache")
	r.Header.Set("Connection", "keep-alive")
}

// consumeJSON handles a non-stream JSON ChatCompletion response from the
// upstream. It parses the JSON, extracts the message content, and emits
// the appropriate SSE events so the proxy can forward them correctly.
func (c *Client) consumeJSON(
	ctx context.Context,
	modelID string,
	body io.Reader,
	onMessage func(upstream.SSEMessage),
) error {
	raw, err := io.ReadAll(io.LimitReader(body, 10<<20))
	if err != nil {
		return fmt.Errorf("failed to read upstream response body: %w", err)
	}

	// Some providers return an error envelope even on 200.
	var errEnv ErrorEnvelope
	if json.Unmarshal(raw, &errEnv) == nil && errEnv.Error.Message != "" {
		return fmt.Errorf("%s upstream error: %s", c.channel, errEnv.Error.Message)
	}

	var resp ChatResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		return fmt.Errorf("failed to parse upstream JSON response: %w (body: %s)", err, strings.TrimSpace(string(raw))[:min(200, len(raw))])
	}

	if onMessage == nil {
		return nil
	}

	// Emit model.conversation_id if upstream provided an ID.
	if resp.ID != "" {
		onMessage(upstream.SSEMessage{
			Type:  "model.conversation_id",
			Event: map[string]interface{}{"id": resp.ID},
		})
	}

	content := ""
	if len(resp.Choices) > 0 {
		rawContent := resp.Choices[0].Message.Content
		if len(rawContent) > 0 {
			// Content can be a JSON string or a structured array.
			// Try string first.
			var s string
			if json.Unmarshal(rawContent, &s) == nil {
				content = s
			} else {
				// Try []TextPart (multimodal).
				var parts []TextPart
				if json.Unmarshal(rawContent, &parts) == nil {
					for _, p := range parts {
						if p.Type == "text" && p.Text != "" {
							content += p.Text
						}
					}
				}
			}
		}
		if resp.Choices[0].FinishReason != nil {
			if reason := *resp.Choices[0].FinishReason; reason == "stop" {
				// "stop" maps to "end_turn" internally.
			}
		}
	}

	if content != "" {
		onMessage(upstream.SSEMessage{
			Type:  "model.text-start",
			Event: map[string]interface{}{},
		})
		onMessage(upstream.SSEMessage{
			Type:  "model.text-delta",
			Event: map[string]interface{}{"delta": content},
		})
		onMessage(upstream.SSEMessage{
			Type:  "model.text-end",
			Event: map[string]interface{}{},
		})
	}

	// Emit finish with usage.
	usage := map[string]int{}
	if resp.Usage != nil {
		usage["inputTokens"] = resp.Usage.PromptTokens
		usage["outputTokens"] = resp.Usage.CompletionTokens
		usage["input_tokens"] = resp.Usage.PromptTokens
		usage["output_tokens"] = resp.Usage.CompletionTokens
	}
	onMessage(upstream.SSEMessage{
		Type: "model.finish",
		Event: map[string]interface{}{
			"finishReason": "end_turn",
			"usage":        usage,
		},
	})

	return nil
}

func (c *Client) consumeStream(
	ctx context.Context,
	modelID string,
	stream *StreamParser,
	onMessage func(upstream.SSEMessage),
) error {
	if onMessage == nil {
		// Drain the stream so the upstream connection can be reused.
		for {
			if _, err := stream.Next(ctx); err != nil {
				return nil
			}
		}
	}

	// Emit text-start once, before the first text delta.
	emittedTextStart := false
	emittedTextEnd := false
	inputTokens, outputTokens := 0, 0
	finalStopReason := "stop"

	for {
		chunk, err := stream.Next(ctx)
		if err != nil {
			if err == io.EOF {
				finalStopReason = pickStopReason(finalStopReason)
				if !emittedTextEnd {
					onMessage(upstream.SSEMessage{
						Type:  "model.text-end",
						Event: map[string]interface{}{},
					})
				}
				onMessage(upstream.SSEMessage{
					Type: "model.finish",
					Event: map[string]interface{}{
						"finishReason": finalStopReason,
						"usage": map[string]int{
							"inputTokens":   inputTokens,
							"outputTokens":  outputTokens,
							"input_tokens":  inputTokens,
							"output_tokens": outputTokens,
						},
					},
				})
				return nil
			}
			return err
		}

		if chunk.DeltaContent != "" {
			if !emittedTextStart {
				emittedTextStart = true
				onMessage(upstream.SSEMessage{
					Type:  "model.text-start",
					Event: map[string]interface{}{},
				})
			}
			onMessage(upstream.SSEMessage{
				Type: "model.text-delta",
				Event: map[string]interface{}{
					"delta": chunk.DeltaContent,
				},
			})
		}

		for _, tc := range chunk.DeltaToolCalls {
			if tc.ID != "" {
				onMessage(upstream.SSEMessage{
					Type: "model.tool-input-start",
					Event: map[string]interface{}{
						"id":       tc.ID,
						"toolName": tc.Function.Name,
					},
				})
			}
			if tc.Function.Arguments != "" {
				id := tc.ID
				if id == "" {
					id = inferToolCallID(stream, len(chunk.DeltaToolCalls))
				}
				onMessage(upstream.SSEMessage{
					Type: "model.tool-input-delta",
					Event: map[string]interface{}{
						"id":    id,
						"delta": tc.Function.Arguments,
					},
				})
			}
		}

		if chunk.FinishReason != "" {
			finalStopReason = mapFinishReason(chunk.FinishReason)
			if emittedTextStart && !emittedTextEnd {
				emittedTextEnd = true
				onMessage(upstream.SSEMessage{
					Type:  "model.text-end",
					Event: map[string]interface{}{},
				})
			}
		}

		if chunk.Usage != nil {
			inputTokens = chunk.Usage.PromptTokens
			outputTokens = chunk.Usage.CompletionTokens
		}
	}
}

func pickStopReason(current string) string {
	if current == "" || current == "stop" {
		return "end_turn"
	}
	return current
}

// mapFinishReason translates an OpenAI finish_reason into the project's
// internal stop-reason vocabulary used by the stream handler.
func mapFinishReason(reason string) string {
	switch strings.ToLower(strings.TrimSpace(reason)) {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "content_filter"
	case "insufficient_quota", "quota_exceeded":
		return "quota"
	default:
		return "end_turn"
	}
}

func inferToolCallID(stream *StreamParser, fallback int) string {
	if stream == nil {
		return fmt.Sprintf("call_%d", fallback)
	}
	for idx, buf := range stream.toolCallBuffers {
		if buf.id != "" {
			return buf.id
		}
		_ = idx
	}
	return fmt.Sprintf("call_%d", fallback)
}
