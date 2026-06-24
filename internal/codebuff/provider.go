package codebuff

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"orchids-api/internal/config"
	"orchids-api/internal/debug"
	"orchids-api/internal/store"
	"orchids-api/internal/upstream"
)

// Provider implements handler.UpstreamClient for codebuff.com.
type Provider struct {
	client       *Client
	sessionCache *SessionCache
	config       *config.Config
	account      *store.Account
	quotaStore   *QuotaStore
	telemetry    *TelemetryStore
}

// NewFromAccount creates a codebuff Provider from a store.Account.
// This matches the signature expected by handler.buildAccountClient.
func NewFromAccount(acc *store.Account, cfg *config.Config) *Provider {
	token := ResolveAuthToken(acc)
	if token == "" {
		return nil
	}
	return &Provider{
		client:       NewClient(token, cfg),
		config:       cfg,
		account:      acc,
		sessionCache: nil, // populated lazily from Redis
	}
}

// ResolveAuthToken extracts the bearer token that should be sent to codebuff
// upstream. Order of preference:
//  1. ClientCookie (full bearer)
//  2. SessionCookie
//  3. RefreshToken
//  4. Token — only if it does NOT end in "..." (which signals a truncated preview
//     written by truncateAccountDisplayToken). Truncated previews are rejected
//     because upstream will return 401.
func ResolveAuthToken(acc *store.Account) string {
	if acc == nil {
		return ""
	}
	for _, value := range []string{acc.ClientCookie, acc.SessionCookie, acc.RefreshToken} {
		if token := strings.TrimSpace(value); token != "" {
			return token
		}
	}
	if token := strings.TrimSpace(acc.Token); token != "" {
		if strings.HasSuffix(token, "...") {
			return ""
		}
		return token
	}
	return ""
}

// SetRedisClient injects the Redis client used for session caching.
// Called during handler initialisation after the store is ready.
func (p *Provider) SetRedisClient(client *redis.Client) {
	if client == nil {
		return
	}
	prefix := "codebuff"
	if p.config != nil && p.config.RedisPrefix != "" {
		prefix = p.config.RedisPrefix + ":codebuff"
	}
	p.sessionCache = NewSessionCache(client, prefix)
}

// SetQuotaStore injects the quota store used for per-model block tracking.
func (p *Provider) SetQuotaStore(qs *QuotaStore) {
	p.quotaStore = qs
}

// SetTelemetryStore injects the telemetry store used to record request
// counters, 429 occurrences, and token usages per account and model.
func (p *Provider) SetTelemetryStore(ts *TelemetryStore) {
	p.telemetry = ts
}

// SendRequestWithPayload is the core upstream request handler.
func (p *Provider) SendRequestWithPayload(
	ctx context.Context,
	req upstream.UpstreamRequest,
	onMessage func(upstream.SSEMessage),
	logger *debug.Logger,
) error {
	if p == nil || p.client == nil {
		return fmt.Errorf("codebuff provider is nil")
	}

	// Resolve model.
	model, err := ResolveModel(req.Model)
	if err != nil {
		return err
	}

	// Acquire session (with retries for waiting_room_required).
	const maxRetries = 2
	var sess *Session
	var sessData map[string]any
	for attempt := 0; attempt <= maxRetries; attempt++ {
		sess, sessData, err = p.acquireSession(ctx, model.SessionID())
		if err == nil {
			break
		}
		if IsWaitingRoomRequired(err) && attempt < maxRetries {
			slog.Warn("codebuff session superseded; retrying", "attempt", attempt+1, "error", err)
			continue
		}
		return fmt.Errorf("codebuff session acquisition failed: %w", err)
	}
	if sess == nil {
		return fmt.Errorf("codebuff session is nil after acquisition")
	}
	if sessData != nil {
		p.recordSessionQuotas(sessData)
	}

	// Request ads (best-effort, non-blocking).
	go p.requestAds(ctx, req)

	// Start run chain.
	run, err := StartRunChain(ctx, p.client, model)
	if err != nil {
		return fmt.Errorf("codebuff run chain failed: %w", err)
	}

	// Build payload with Buffy injection.
	clientID := ""
	if p.config != nil {
		clientID = p.config.CodebuffClientID
	}
	payload := BuildPayload(req, sess, run, clientID)

	if logger != nil {
		logger.LogUpstreamRequest(p.client.baseURL+"/api/v1/chat/completions", map[string]string{"channel": "codebuff"}, nil)
	}

	// Execute chat.
	if req.Stream {
		if req.RawSSEWriter != nil {
			return p.streamChatRaw(ctx, payload, run, req.RawSSEWriter, logger, req.Model)
		}
		return p.streamChat(ctx, payload, run, onMessage, logger, req.Model)
	}
	return p.completeChat(ctx, payload, run, onMessage, logger, req.Model)
}

func (p *Provider) acquireSession(ctx context.Context, model string) (*Session, map[string]any, error) {
	if p.sessionCache != nil {
		return p.sessionCache.EnsureSession(ctx, p.client, p.client.apiKey, model)
	}
	// Fallback: create a new session every time (no Redis).
	return p.client.CreateSession(ctx, model)
}

func (p *Provider) recordSessionQuotas(data map[string]any) {
	if p.quotaStore == nil || p.account == nil || data == nil {
		return
	}
	limits, err := ParseSessionRateLimits(data)
	if err != nil || len(limits) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.quotaStore.RecordSessionQuotas(ctx, p.account.ID, limits)
}

// recordTelemetry increments per-account/per-model counters in the TelemetryStore.
// is429 must be true only when the request was rejected by a 429 response.
func (p *Provider) recordTelemetry(requestedModel string, is429 bool, tokens int, latencyMs int64) {
	if p == nil || p.telemetry == nil || p.account == nil {
		return
	}
	resolved, _ := ResolveModel(requestedModel)
	model := requestedModel
	if resolved != nil {
		model = resolved.UpstreamID()
	}
	if model == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	p.telemetry.RecordRequest(ctx, p.account.ID, model, is429, tokens, latencyMs)
}

func (p *Provider) recordBlockIf429(err error, requestedModel string) {
	if p.quotaStore == nil || p.account == nil {
		return
	}
	cbErr, ok := err.(*Error)
	if !ok || cbErr.StatusCode != 429 {
		return
	}
	block, parseErr := Parse429Body(err)
	if parseErr != nil || block == nil {
		return
	}
	if block.Model == "" {
		block.Model = requestedModel
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_ = p.quotaStore.RecordBlock(ctx, p.account.ID, block)
}

func (p *Provider) requestAds(ctx context.Context, req upstream.UpstreamRequest) {
	providers := []string{"gravity", "zeroclick"}
	if p.config != nil && len(p.config.CodebuffAdProviders) > 0 {
		providers = p.config.CodebuffAdProviders
	}
	chain := NewAdChain(p.client, providers)

	messages := make([]map[string]any, 0, len(req.Messages))
	for _, m := range req.Messages {
		messages = append(messages, map[string]any{
			"role":    m.Role,
			"content": encodeMessageContent(m),
		})
	}
	chain.Request(ctx, messages, "")
}

func (p *Provider) streamChat(
	ctx context.Context,
	payload map[string]any,
	run *Run,
	onMessage func(upstream.SSEMessage),
	logger *debug.Logger,
	requestedModel string,
) error {
	start := time.Now()
	body, err := p.client.ChatCompletions(ctx, payload)
	if err != nil {
		p.recordBlockIf429(err, requestedModel)
		p.recordTelemetry(requestedModel, true, 0, time.Since(start).Milliseconds())
		return err
	}
	defer body.Close()

	// Raw SSE passthrough: forward upstream lines directly to client.
	parser := NewStreamParser(body)
	defer parser.Close()

	messageID := ""
	for {
		msgs, err := parser.Next()
		if err != nil {
			if err == io.EOF {
				break
			}
			go FinalizeRun(context.Background(), p.client, run, messageID)
			p.recordTelemetry(requestedModel, true, 0, time.Since(start).Milliseconds())
			return fmt.Errorf("codebuff stream error: %w", err)
		}
		for _, msg := range msgs {
			if onMessage != nil {
				onMessage(msg)
			}
			if msg.Type == "model.finish" {
				go FinalizeRun(context.Background(), p.client, run, messageID)
				p.recordTelemetry(requestedModel, false, 0, time.Since(start).Milliseconds())
				return nil
			}
		}
	}
	go FinalizeRun(context.Background(), p.client, run, messageID)
	p.recordTelemetry(requestedModel, false, 0, time.Since(start).Milliseconds())
	return nil
}

// streamChatRaw forwards raw SSE lines from codebuff upstream directly to the
// client, matching freebuff2api's passthrough behavior exactly. No parsing,
// no format conversion, no tool name normalization.
func (p *Provider) streamChatRaw(
	ctx context.Context,
	payload map[string]any,
	run *Run,
	writeSSE func(event string, data []byte),
	logger *debug.Logger,
	requestedModel string,
) error {
	start := time.Now()
	body, err := p.client.ChatCompletions(ctx, payload)
	if err != nil {
		p.recordBlockIf429(err, requestedModel)
		p.recordTelemetry(requestedModel, true, 0, time.Since(start).Milliseconds())
		return err
	}
	defer body.Close()

	messageID := ""
	sawToolCallsFinish := false
	scanner := bufio.NewScanner(body)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		if strings.HasPrefix(string(line), "data: ") {
			data := line[6:]
			if string(data) == "[DONE]" {
				writeSSE("", []byte("[DONE]"))
				break
			}
			// Codebuff sends finish_reason: "tool_calls" then finish_reason: "stop".
			// The trailing "stop" makes opencode end the turn. Suppress it.
			if sawToolCallsFinish && bytes.Contains(data, []byte(`"finish_reason":"stop"`)) {
				continue
			}
			writeSSE("", data)
			// Track if we already sent tool_calls finish.
			if !sawToolCallsFinish && bytes.Contains(data, []byte(`"finish_reason":"tool_calls"`)) {
				sawToolCallsFinish = true
			}
			// Try to extract messageID from chunk for run finalization.
			if messageID == "" {
				var chunk struct {
					ID string `json:"id"`
				}
				if json.Unmarshal(data, &chunk) == nil && chunk.ID != "" {
					messageID = chunk.ID
				}
			}
		} else if strings.HasPrefix(string(line), "event: ") {
			// Some SSE streams use event: lines.
			event := string(line[7:])
			writeSSE(event, nil)
		}
	}

	go FinalizeRun(context.Background(), p.client, run, messageID)
	p.recordTelemetry(requestedModel, false, 0, time.Since(start).Milliseconds())
	return nil
}

func (p *Provider) completeChat(
	ctx context.Context,
	payload map[string]any,
	run *Run,
	onMessage func(upstream.SSEMessage),
	logger *debug.Logger,
	requestedModel string,
) error {
	start := time.Now()
	body, err := p.client.ChatCompletions(ctx, payload)
	if err != nil {
		p.recordBlockIf429(err, requestedModel)
		p.recordTelemetry(requestedModel, true, 0, time.Since(start).Milliseconds())
		return err
	}
	defer body.Close()

	var resp struct {
		ID      string `json:"id"`
		Choices []struct {
			Message struct {
				Role            string `json:"role"`
				Content         string `json:"content"`
				ReasoningContent string `json:"reasoning_content"`
			} `json:"message"`
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
			ReasoningTokens  int `json:"reasoning_tokens"`
		} `json:"usage"`
	}
	if err := json.NewDecoder(body).Decode(&resp); err != nil {
		p.recordTelemetry(requestedModel, false, 0, time.Since(start).Milliseconds())
		return fmt.Errorf("codebuff completion decode error: %w", err)
	}
	p.recordTelemetry(requestedModel, false, resp.Usage.TotalTokens, time.Since(start).Milliseconds())

	messageID := resp.ID
	var content, reasoning, finishReason string
	if len(resp.Choices) > 0 {
		content = resp.Choices[0].Message.Content
		reasoning = resp.Choices[0].Message.ReasoningContent
		finishReason = resp.Choices[0].FinishReason
	}

	if onMessage != nil {
		if content != "" {
			onMessage(upstream.SSEMessage{Type: "model.text-start", Event: map[string]any{}})
			onMessage(upstream.SSEMessage{Type: "model.text-delta", Event: map[string]any{"delta": content}})
			onMessage(upstream.SSEMessage{Type: "model.text-end", Event: map[string]any{}})
		}
		if reasoning != "" {
			onMessage(upstream.SSEMessage{Type: "model.thinking", Event: map[string]any{"delta": reasoning}})
		}
		onMessage(upstream.SSEMessage{
			Type: "model.finish",
			Event: map[string]any{
				"finishReason": finishReason,
				"usage": map[string]int{
					"inputTokens":  resp.Usage.PromptTokens,
					"outputTokens": resp.Usage.CompletionTokens,
					"totalTokens":  resp.Usage.TotalTokens,
				},
			},
		})
	}

	go FinalizeRun(context.Background(), p.client, run, messageID)
	return nil
}

// Close is a no-op because we use the shared HTTP client pool.
func (p *Provider) Close() {}

// BuildChunkRewriter returns a function that rewrites OpenAI SSE chunks emitted
// by the streamHandler into a codebuff-friendly shape: replaces the upstream
// "msg_<ms>" identifier (Anthropic-style) with an OpenAI-compatible
// "chatcmpl-<hex>" identifier. Implements handler.ChunkRewriterInstaller.
func (p *Provider) BuildChunkRewriter() func([]byte) []byte {
	cr := NewChunkRewriter()
	return cr.RewriteLine
}

// ---------------------------------------------------------------------------
// Completion accumulator (non-streaming path)
// ---------------------------------------------------------------------------

// CompletionAccumulator gathers SSE events into a single completion response.
type CompletionAccumulator struct {
	MessageID     string
	Content       string
	Reasoning     string
	FinishReason  string
	Usage         map[string]int
	ToolCalls     []map[string]any
	hasTextStart  bool
	hasTextEnd    bool
}

// NewCompletionAccumulator creates an accumulator for non-streaming responses.
func NewCompletionAccumulator(model string) *CompletionAccumulator {
	return &CompletionAccumulator{}
}

// Add processes an upstream.SSEMessage and updates internal state.
func (ca *CompletionAccumulator) Add(msg upstream.SSEMessage) {
	switch msg.Type {
	case "model.text-delta":
		if delta, ok := msg.Event["delta"].(string); ok {
			ca.Content += delta
		}
	case "model.thinking":
		if delta, ok := msg.Event["delta"].(string); ok {
			ca.Reasoning += delta
		}
	case "model.tool-input-start":
		ca.ToolCalls = append(ca.ToolCalls, map[string]any{
			"id":       msg.Event["id"],
			"type":     "function",
			"function": map[string]any{"name": msg.Event["toolName"], "arguments": ""},
		})
	case "model.tool-input-delta":
		if len(ca.ToolCalls) > 0 {
			last := ca.ToolCalls[len(ca.ToolCalls)-1]
			fn, _ := last["function"].(map[string]any)
			if fn != nil {
				args, _ := fn["arguments"].(string)
				fn["arguments"] = args + msg.Event["delta"].(string)
			}
		}
	case "model.finish":
		if fr, ok := msg.Event["finishReason"].(string); ok {
			ca.FinishReason = fr
		}
		if usage, ok := msg.Event["usage"].(map[string]int); ok {
			ca.Usage = usage
		}
	}
}

// ToMessages returns the accumulated completion as a sequence of SSE messages
// that the proxy handler can forward to the client.
func (ca *CompletionAccumulator) ToMessages() []upstream.SSEMessage {
	var msgs []upstream.SSEMessage
	if ca.Content != "" {
		msgs = append(msgs, upstream.SSEMessage{Type: "model.text-start", Event: map[string]any{}})
		msgs = append(msgs, upstream.SSEMessage{Type: "model.text-delta", Event: map[string]any{"delta": ca.Content}})
		msgs = append(msgs, upstream.SSEMessage{Type: "model.text-end", Event: map[string]any{}})
	}
	if ca.Reasoning != "" {
		msgs = append(msgs, upstream.SSEMessage{Type: "model.thinking", Event: map[string]any{"delta": ca.Reasoning}})
	}
	for _, tc := range ca.ToolCalls {
		msgs = append(msgs, upstream.SSEMessage{
			Type: "model.tool-input-start",
			Event: map[string]any{
				"id":       tc["id"],
				"toolName": tc["function"].(map[string]any)["name"],
			},
		})
		msgs = append(msgs, upstream.SSEMessage{
			Type: "model.tool-input-delta",
			Event: map[string]any{
				"id":    tc["id"],
				"delta": tc["function"].(map[string]any)["arguments"],
			},
		})
	}
	msgs = append(msgs, upstream.SSEMessage{
		Type: "model.finish",
		Event: map[string]any{
			"finishReason": ca.FinishReason,
			"usage":        ca.Usage,
		},
	})
	return msgs
}
