package handler

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	rtdebug "runtime/debug"
	"strconv"
	"strings"
	"time"

	"github.com/goccy/go-json"
	stdjson "encoding/json"

	"orchids-api/internal/adapter"
	"orchids-api/internal/audit"
	"orchids-api/internal/config"
	"orchids-api/internal/debug"
	apperrors "orchids-api/internal/errors"
	"orchids-api/internal/loadbalancer"
	"orchids-api/internal/logutil"
	"orchids-api/internal/prompt"
	"orchids-api/internal/provider"
	appreq "orchids-api/internal/req"
	"orchids-api/internal/store"
	"orchids-api/internal/tokencache"
	"orchids-api/internal/upstream"
	"orchids-api/internal/util"
)

// ClientFactory creates an upstream client for a given account.
// Used to decouple provider-specific client construction from the handler.
type ClientFactory func(acc *store.Account, cfg *config.Config) upstream.UpstreamClient

type Handler struct {
	config        *config.Config
	client        upstream.UpstreamClient
	clientFactory ClientFactory
	clientCache   *accountClientCache
	loadBalancer  *loadbalancer.LoadBalancer
	connTracker   loadbalancer.ConnTracker
	tokenCache    tokencache.Cache
	promptCache   tokencache.PromptCache
	auditLogger   audit.Logger
	specs         *provider.Registry

	sessionStore SessionStore
	dedupStore   DedupStore
}

type ClaudeRequest = appreq.Request

type toolCall struct {
	id    string
	name  string
	input string
}

type openAINonStreamToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAINonStreamMessage struct {
	Role      string                    `json:"role"`
	Content   interface{}               `json:"content"`
	ToolCalls []openAINonStreamToolCall `json:"tool_calls,omitempty"`
}

type openAINonStreamChoice struct {
	Index        int                    `json:"index"`
	Message      openAINonStreamMessage `json:"message"`
	FinishReason *string                `json:"finish_reason"`
}

type openAINonStreamUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type openAINonStreamResponse struct {
	ID      string                  `json:"id"`
	Object  string                  `json:"object"`
	Created int64                   `json:"created"`
	Model   string                  `json:"model"`
	Choices []openAINonStreamChoice `json:"choices"`
	Usage   openAINonStreamUsage    `json:"usage"`
}

func cloneSSEMessage(msg upstream.SSEMessage) upstream.SSEMessage {
	cloned := msg
	if msg.Event != nil {
		cloned.Event = make(map[string]interface{}, len(msg.Event))
		for k, v := range msg.Event {
			cloned.Event[k] = v
		}
	}
	if msg.Raw != nil {
		cloned.Raw = make(map[string]interface{}, len(msg.Raw))
		for k, v := range msg.Raw {
			cloned.Raw[k] = v
		}
	}
	if len(msg.RawJSON) > 0 {
		cloned.RawJSON = append(json.RawMessage(nil), msg.RawJSON...)
	}
	return cloned
}

const keepAliveInterval = 15 * time.Second
const maxRequestBytes = 50 * 1024 * 1024 // 50MB
const duplicateWindow = 2 * time.Second
const duplicateCleanupWindow = 10 * time.Second

type recentRequest struct {
	last     time.Time
	inFlight int
}

func NewWithLoadBalancer(cfg *config.Config, lb *loadbalancer.LoadBalancer) *Handler {
	h := &Handler{
		config:       cfg,
		loadBalancer: lb,
		connTracker:  loadbalancer.NewMemoryConnTracker(),
		clientCache:  newAccountClientCache(),
		specs:        provider.NewRegistry(),
		sessionStore: NewMemorySessionStore(30*time.Minute, 1024),
		dedupStore:   NewMemoryDedupStore(duplicateWindow, duplicateCleanupWindow),
		auditLogger:  audit.NewNopLogger(),
	}

	return h
}

func (h *Handler) SetTokenCache(cache tokencache.Cache) {
	h.tokenCache = cache
}

func (h *Handler) SetPromptCache(cache tokencache.PromptCache) {
	h.promptCache = cache
}

// SetSessionStore replaces the default in-memory session store.
func (h *Handler) SetSessionStore(ss SessionStore) {
	h.sessionStore = ss
}

// SetDedupStore replaces the default in-memory dedup store.
func (h *Handler) SetDedupStore(ds DedupStore) {
	h.dedupStore = ds
}

// SetAuditLogger replaces the default nop audit logger.
func (h *Handler) SetAuditLogger(al audit.Logger) {
	h.auditLogger = al
}

// SetClientFactory sets the factory used by selectAccount to create provider-specific clients.
func (h *Handler) SetClientFactory(f ClientFactory) {
	h.clientFactory = f
}

// SetSpecs replaces the provider registry. Existing entries are not retained.
func (h *Handler) SetSpecs(specs *provider.Registry) {
	h.specs = specs
}

// RegisterSpec adds a single provider spec to the handler's registry.
func (h *Handler) RegisterSpec(s provider.Spec) {
	if h.specs == nil {
		h.specs = provider.NewRegistry()
	}
	h.specs.Register(s)
}

// ResolveSpec returns the provider spec for a request, looking up by URL path
// prefix first, then by channel name. Returns false if no spec matches.
func (h *Handler) ResolveSpec(r *http.Request, channel string) (provider.Spec, bool) {
	if h.specs != nil {
		if s, ok := h.specs.GetByPathPrefix(r.URL.Path); ok {
			return s, true
		}
		if channel != "" {
			if s, ok := h.specs.GetByName(channel); ok {
				return s, true
			}
		}
	}
	return provider.Spec{}, false
}

// SpecByName returns the spec registered for the given channel name (case-insensitive).
// This is the dispatch key used by selectAccount's ClientFactory callback.
func (h *Handler) SpecByName(name string) (provider.Spec, bool) {
	if h.specs == nil {
		return provider.Spec{}, false
	}
	return h.specs.GetByName(name)
}

func (h *Handler) computeRequestHash(r *http.Request, body []byte) string {
	hasher := sha256.New()
	hasher.Write([]byte(r.URL.Path))
	hasher.Write([]byte{0})
	if auth := r.Header.Get("Authorization"); auth != "" {
		hasher.Write([]byte(auth))
	}
	hasher.Write([]byte{0})
	hasher.Write(body)
	return hex.EncodeToString(hasher.Sum(nil))
}

func ownsFinalSSELifecycle(client upstream.UpstreamClient) bool {
	owner, ok := client.(upstream.FinalSSELifecycleOwner)
	return ok && owner.OwnsFinalSSELifecycle()
}

func mapStopReasonToOpenAIFinishReason(stopReason string) *string {
	switch strings.TrimSpace(stopReason) {
	case "", "end_turn", "stop":
		reason := "stop"
		return &reason
	case "tool_use":
		reason := "tool_calls"
		return &reason
	case "max_tokens":
		reason := "length"
		return &reason
	case "refusal":
		reason := "content_filter"
		return &reason
	default:
		reason := stopReason
		return &reason
	}
}

func buildOpenAINonStreamResponse(sh *streamHandler, model string, stopReason string) openAINonStreamResponse {
	textParts := make([]string, 0, len(sh.contentBlocks))
	toolCalls := make([]openAINonStreamToolCall, 0)

	for i := range sh.contentBlocks {
		blockType, _ := sh.contentBlocks[i]["type"].(string)
		switch blockType {
		case "text":
			if builder, ok := sh.textBlockBuilders[i]; ok {
				if text := builder.String(); text != "" {
					textParts = append(textParts, text)
					continue
				}
			}
			if text, ok := sh.contentBlocks[i]["text"].(string); ok && text != "" {
				textParts = append(textParts, text)
			}
		case "tool_use":
			call := openAINonStreamToolCall{
				Type: "function",
			}
			if id, ok := sh.contentBlocks[i]["id"].(string); ok {
				call.ID = id
			}
			if name, ok := sh.contentBlocks[i]["name"].(string); ok {
				call.Function.Name = name
			}
			switch input := sh.contentBlocks[i]["input"].(type) {
			case string:
				call.Function.Arguments = input
			case nil:
				call.Function.Arguments = "{}"
			default:
				raw, err := json.Marshal(input)
				if err != nil {
					call.Function.Arguments = "{}"
				} else {
					call.Function.Arguments = string(raw)
				}
			}
			toolCalls = append(toolCalls, call)
		}
	}

	content := strings.Join(textParts, "")
	if strings.TrimSpace(content) == "" && len(toolCalls) > 0 {
		content = ""
	}

	message := openAINonStreamMessage{
		Role:    "assistant",
		Content: content,
	}
	if len(toolCalls) > 0 {
		message.ToolCalls = toolCalls
	}

	return openAINonStreamResponse{
		ID:      sh.msgID,
		Object:  "chat.completion",
		Created: sh.startTime.Unix(),
		Model:   model,
		Choices: []openAINonStreamChoice{{
			Index:        0,
			Message:      message,
			FinishReason: mapStopReasonToOpenAIFinishReason(stopReason),
		}},
		Usage: openAINonStreamUsage{
			PromptTokens:     sh.inputTokens,
			CompletionTokens: sh.outputTokens,
			TotalTokens:      sh.inputTokens + sh.outputTokens,
		},
	}
}

func upstreamMessageHandler(sh *streamHandler) func(upstream.SSEMessage) {
	return func(msg upstream.SSEMessage) {
		sh.handleMessage(msg)
	}
}

func (h *Handler) computeSemanticRequestHash(r *http.Request, req ClaudeRequest) string {
	if lastUserIsToolResultFollowup(req.Messages) {
		return ""
	}
	userText := normalizeTopicText(extractUserText(req.Messages))
	if userText == "" {
		return ""
	}
	if len(userText) > 4096 {
		userText = userText[:4096]
	}

	mode := "chat"
	if isTopicClassifierRequest(req) {
		mode = "topic_classifier"
	} else if isTitleGenerationRequest(req) {
		mode = "title_generation"
	} else if ok, _ := isCommandPrefixRequest(req); ok {
		mode = "command_prefix"
	}

	hasher := sha256.New()
	hasher.Write([]byte(r.URL.Path))
	hasher.Write([]byte{0})
	if auth := r.Header.Get("Authorization"); auth != "" {
		hasher.Write([]byte(auth))
	}
	hasher.Write([]byte{0})
	hasher.Write([]byte(strings.ToLower(strings.TrimSpace(req.Model))))
	hasher.Write([]byte{0})
	hasher.Write([]byte(strings.ToLower(strings.TrimSpace(conversationKeyForRequest(r, req)))))
	hasher.Write([]byte{0})
	hasher.Write([]byte(mode))
	hasher.Write([]byte{0})
	if req.Stream {
		hasher.Write([]byte{1})
	} else {
		hasher.Write([]byte{0})
	}
	hasher.Write([]byte{0})
	hasher.Write([]byte(userText))
	return hex.EncodeToString(hasher.Sum(nil))
}

func shortRequestTrace(hash string) string {
	hash = strings.TrimSpace(hash)
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}

func (h *Handler) registerRequest(hash string) (bool, bool) {
	return h.dedupStore.Register(context.Background(), hash)
}

func (h *Handler) finishRequest(hash string) {
	h.dedupStore.Finish(context.Background(), hash)
}

func stainlessRetryCount(r *http.Request) int {
	if r == nil {
		return 0
	}
	raw := strings.TrimSpace(r.Header.Get("X-Stainless-Retry-Count"))
	if raw == "" {
		return 0
	}
	count, err := strconv.Atoi(raw)
	if err != nil || count < 0 {
		return 0
	}
	return count
}

func writeRetryDedupError(w http.ResponseWriter, inFlight bool) {
	status := http.StatusConflict
	code := apperrors.CodeInvalidRequest
	message := "Automatic retry suppressed because an identical request was already handled recently."
	if inFlight {
		status = http.StatusTooManyRequests
		code = apperrors.CodeOverloaded
		message = "Automatic retry suppressed because an identical request is still in progress. Retry again shortly."
		w.Header().Set("Retry-After", "1")
	}
	apperrors.New(code, message, status).WriteResponse(w)
}

func (h *Handler) writeDuplicateResponse(w http.ResponseWriter, req ClaudeRequest, responseFormat adapter.ResponseFormat) {
	if req.Stream {
		if responseFormat == adapter.FormatOpenAI {
			w.Header().Set("Content-Type", "text/event-stream")
			w.Header().Set("Cache-Control", "no-cache")
			w.Header().Set("Connection", "keep-alive")

			type openAIStreamChoice struct {
				Index int `json:"index"`
				Delta struct {
					Role string `json:"role,omitempty"`
				} `json:"delta"`
				FinishReason *string `json:"finish_reason,omitempty"`
			}
			type openAIStreamChunk struct {
				ID      string               `json:"id"`
				Object  string               `json:"object"`
				Created int64                `json:"created"`
				Model   string               `json:"model"`
				Choices []openAIStreamChoice `json:"choices"`
			}
			startChunk := openAIStreamChunk{
				ID:      "dup",
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   req.Model,
				Choices: []openAIStreamChoice{{
					Index: 0,
					Delta: struct {
						Role string `json:"role,omitempty"`
					}{Role: "assistant"},
				}},
			}
			stopReason := "stop"
			stopChunk := struct {
				ID      string `json:"id"`
				Object  string `json:"object"`
				Created int64  `json:"created"`
				Model   string `json:"model"`
				Choices []struct {
					Index        int            `json:"index"`
					Delta        map[string]any `json:"delta"`
					FinishReason *string        `json:"finish_reason,omitempty"`
				} `json:"choices"`
			}{
				ID:      "dup",
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   req.Model,
				Choices: []struct {
					Index        int            `json:"index"`
					Delta        map[string]any `json:"delta"`
					FinishReason *string        `json:"finish_reason,omitempty"`
				}{{
					Index:        0,
					Delta:        map[string]any{},
					FinishReason: &stopReason,
				}},
			}
			rawStart, _ := json.Marshal(startChunk)
			rawStop, _ := json.Marshal(stopChunk)
			_ = writeOpenAIFrame(w, rawStart)
			_ = writeOpenAIFrame(w, rawStop)
			_, _ = w.Write(sseDoneLineBytes)
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		msgStart, _ := marshalSSEMessageStartBytes("dup", req.Model, 0, 0)
		_ = writeSSEFrameBytes(w, "message_start", msgStart)
		_ = writeSSEFrameBytes(w, "message_stop", sseMessageStopBytes)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	if responseFormat == adapter.FormatOpenAI {
		emptyMsg := openAINonStreamMessage{
			Role:    "assistant",
			Content: "",
		}
		stopReason := "stop"
		resp := openAINonStreamResponse{
			ID:      "dup",
			Object:  "chat.completion",
			Created: time.Now().Unix(),
			Model:   req.Model,
			Choices: []openAINonStreamChoice{{
				Index:        0,
				Message:      emptyMsg,
				FinishReason: &stopReason,
			}},
			Usage: openAINonStreamUsage{},
		}
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			slog.Error("Failed to write duplicate response", "error", err)
		}
		return
	}
	if err := json.NewEncoder(w).Encode(struct {
		Type     string `json:"type"`
		Deduped  bool   `json:"deduped"`
		Message  string `json:"message"`
		Model    string `json:"model"`
		Streamed bool   `json:"streamed"`
	}{
		Type:     "duplicate_request",
		Deduped:  true,
		Message:  "duplicate request suppressed",
		Model:    req.Model,
		Streamed: false,
	}); err != nil {
		slog.Error("Failed to write duplicate response", "error", err)
	}
}

func (h *Handler) HandleMessages(w http.ResponseWriter, r *http.Request) {
	startTime := time.Now()
	streamingStarted := false

	defer func() {
		if err := recover(); err != nil {
			stack := string(rtdebug.Stack())
			slog.Error("Panic in HandleMessages", "error", err, "stack", stack)
			if streamingStarted {
				fmt.Fprintf(w, "event: error\ndata: {\"type\":\"error\",\"error\":{\"type\":\"server_error\",\"message\":\"Internal Server Error\"}}\n\n")
				if f, ok := w.(http.Flusher); ok {
					f.Flush()
				}
			} else {
				apperrors.New("server_error", "Internal Server Error", http.StatusInternalServerError).WriteResponse(w)
			}
		}
	}()

	if r.Method != http.MethodPost {
		apperrors.New("invalid_request_error", "Method not allowed", http.StatusMethodNotAllowed).WriteResponse(w)
		return
	}

	var req ClaudeRequest
	if maxRequestBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, maxRequestBytes)
	}
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		if maxRequestBytes > 0 {
			var maxErr *http.MaxBytesError
			if errors.As(err, &maxErr) {
				apperrors.New("invalid_request_error", "Request body too large", http.StatusRequestEntityTooLarge).WriteResponse(w)
				return
			}
		}
		apperrors.New("invalid_request_error", "Invalid request body", http.StatusBadRequest).WriteResponse(w)
		return
	}

	// Fast path for codebuff: bypass all type conversions, match freebuff2api
	// exactly — parse only model name, stream raw body + raw messages upstream.
	if channelFromPath(r.URL.Path) == "codebuff" {
		h.handleCodebuffDirect(w, r, bodyBytes, startTime)
		return
	}

	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		apperrors.New("invalid_request_error", "Invalid request body", http.StatusBadRequest).WriteResponse(w)
		return
	}

	// Extract raw OpenAI messages/system for codebuff passthrough.
	var rawBody struct {
		Messages stdjson.RawMessage `json:"messages"`
		System   stdjson.RawMessage `json:"system"`
	}
	_ = stdjson.Unmarshal(bodyBytes, &rawBody)
	responseFormat := adapter.DetectResponseFormat(r.URL.Path)

	// Initialize debug log
	logger := debug.New(h.config.DebugEnabled, h.config.DebugLogSSE)
	defer logger.Close()
	verboseDiagnostics := logutil.VerboseDiagnosticsEnabled()

	// 1. Log incoming Claude requests
	logger.LogIncomingRequest(req)

	reqHash := h.computeRequestHash(r, bodyBytes)
	semanticHash := h.computeSemanticRequestHash(r, req)
	bypassDedup := hasInterruptedRetryMarker(req.Messages)
	traceID := shortRequestTrace(reqHash)
	retryCount := stainlessRetryCount(r)
	if verboseDiagnostics {
		slog.Debug("Request fingerprint", "trace_id", traceID, "hash", reqHash, "semantic_hash", semanticHash, "path", r.URL.Path, "content_length", len(bodyBytes), "retry", retryCount, "bypass_dedup", bypassDedup)
	}

	registeredKeys := []string{}
	if !bypassDedup {
		exactKey := "exact:" + reqHash
		if dup, inFlight := h.registerRequest(exactKey); dup {
			if retryCount > 0 {
				slog.Warn("Duplicate retry request rejected", "hash", reqHash, "in_flight", inFlight, "path", r.URL.Path, "user_agent", r.UserAgent(), "retry_count", retryCount)
				logger.LogEarlyExit("duplicate_retry_request", map[string]interface{}{
					"hash":        exactKey,
					"in_flight":   inFlight,
					"path":        r.URL.Path,
					"kind":        "exact",
					"retry_count": retryCount,
				})
				writeRetryDedupError(w, inFlight)
				return
			}
			slog.Warn("Duplicate request suppressed", "hash", reqHash, "in_flight", inFlight, "path", r.URL.Path, "user_agent", r.UserAgent())
			logger.LogEarlyExit("duplicate_request", map[string]interface{}{
				"hash":      exactKey,
				"in_flight": inFlight,
				"path":      r.URL.Path,
				"kind":      "exact",
			})
			h.writeDuplicateResponse(w, req, responseFormat)
			return
		}
		registeredKeys = append(registeredKeys, exactKey)

		if semanticHash != "" {
			semanticKey := "semantic:" + semanticHash
			if dup, inFlight := h.registerRequest(semanticKey); dup {
				for i := len(registeredKeys) - 1; i >= 0; i-- {
					h.finishRequest(registeredKeys[i])
				}
				if retryCount > 0 {
					slog.Warn("Semantic duplicate retry request rejected", "hash", semanticHash, "in_flight", inFlight, "path", r.URL.Path, "user_agent", r.UserAgent(), "retry_count", retryCount)
					logger.LogEarlyExit("duplicate_retry_request", map[string]interface{}{
						"hash":        semanticKey,
						"in_flight":   inFlight,
						"path":        r.URL.Path,
						"kind":        "semantic",
						"retry_count": retryCount,
					})
					writeRetryDedupError(w, inFlight)
					return
				}
				slog.Warn("Semantic duplicate request suppressed", "hash", semanticHash, "in_flight", inFlight, "path", r.URL.Path, "user_agent", r.UserAgent())
				logger.LogEarlyExit("duplicate_request", map[string]interface{}{
					"hash":      semanticKey,
					"in_flight": inFlight,
					"path":      r.URL.Path,
					"kind":      "semantic",
				})
				h.writeDuplicateResponse(w, req, responseFormat)
				return
			}
			registeredKeys = append(registeredKeys, semanticKey)
		}
	}
	defer func() {
		for i := len(registeredKeys) - 1; i >= 0; i-- {
			h.finishRequest(registeredKeys[i])
		}
	}()

	// ...
	if ok, command := isCommandPrefixRequest(req); ok {
		if verboseDiagnostics {
			slog.Debug("Handling command prefix request", "command", command)
		}
		prefix := detectCommandPrefix(command)
		logger.LogEarlyExit("command_prefix", map[string]interface{}{
			"command": command,
			"prefix":  prefix,
		})
		writeCommandPrefixResponse(w, req, responseFormat, prefix, startTime, logger)
		return
	}

	if isTopicClassifierRequest(req) {
		if verboseDiagnostics {
			slog.Debug("Handling topic classifier request locally")
		}
		logger.LogEarlyExit("topic_classifier", map[string]interface{}{
			"mode": "local",
		})
		writeTopicClassifierResponse(w, req, responseFormat, startTime, logger)
		return
	}

	if isTitleGenerationRequest(req) {
		title := generateTopicTitle(extractUserText(req.Messages))
		if verboseDiagnostics {
			slog.Debug("Handling title generation request locally", "title", title)
		}
		logger.LogEarlyExit("title_generation", map[string]interface{}{
			"mode":  "local",
			"title": title,
		})
		writeTitleGenerationResponse(w, req, responseFormat, startTime, logger)
		return
	}

	cacheStrategy := h.config.CacheStrategy
	if cacheStrategy != "" && cacheStrategy != "none" {
		applyCacheStrategy(&req, cacheStrategy)
	}

	// Debug: log all headers
	if verboseDiagnostics {
		for k, v := range r.Header {
			slog.Debug("Incoming header V2 CHECK", "key", k, "value", v)
		}
	}

	// Context and Conversation Key
	conversationKey := conversationKeyForRequest(r, req)
	if verboseDiagnostics {
		slog.Debug("Request dispatch initialized", "trace_id", traceID, "path", r.URL.Path, "conversation_id", conversationKey, "model", req.Model, "stream", req.Stream)
	}

	forcedChannel := channelFromPath(r.URL.Path)
	validatedModel, err := h.validateModelAvailability(r.Context(), req.Model, forcedChannel)
	if err != nil {
		apperrors.New("invalid_request_error", err.Error(), http.StatusBadRequest).WriteResponse(w)
		return
	}
	targetChannel := strings.TrimSpace(forcedChannel)
	if targetChannel == "" && validatedModel != nil {
		targetChannel = strings.TrimSpace(validatedModel.Channel)
	}
	effectiveWorkdir, prevWorkdir, workdirChanged := h.resolveWorkdir(r, req, conversationKey)
	if workdirChanged {
		slog.Warn("A change in the work directory has been detected and the history has been cleared.", "prev", prevWorkdir, "next", effectiveWorkdir, "session", conversationKey)
		req.Messages = resetMessagesForNewWorkdir(req.Messages)
		if conversationKey != "" {
			h.sessionStore.DeleteSession(r.Context(), conversationKey)
		}
	}
	if isCurrentWorkdirRequest(req) {
		logger.LogEarlyExit("current_workdir", map[string]interface{}{
			"mode":    "local",
			"workdir": effectiveWorkdir,
			"path":    r.URL.Path,
		})
		writeCurrentWorkdirResponse(w, req, responseFormat, effectiveWorkdir, startTime, logger)
		return
	}
	if isSuggestionMode(req.Messages) {
		suggestion := buildLocalSuggestion(req.Messages)
		if verboseDiagnostics {
			slog.Debug("Handling suggestion mode request locally", "suggestion", suggestion)
		}
		logger.LogEarlyExit("suggestion_mode", map[string]interface{}{
			"mode":       "local",
			"suggestion": suggestion,
		})
		writeSuggestionModeResponse(w, req, responseFormat, startTime, logger)
		return
	}

	isPuterRequest := strings.EqualFold(targetChannel, "puter")
	suggestionMode := isSuggestionMode(req.Messages)
	noThinking := suggestionMode || h.config.SuppressThinking
	gateNoTools := false
	toolGateMessage := ""
	suppressThinking := noThinking
	if suggestionMode {
		gateNoTools = true
		toolGateMessage = buildToolGateMessage(req.Messages, true)
	}
	if lastUserIsToolResultFollowup(req.Messages) {
		if isPuterRequest {
			if verboseDiagnostics {
				slog.Debug("tool_gate: keeping tools for puter tool_result follow-up")
			}
		} else {
			gateNoTools = true
			toolGateMessage = buildToolGateMessage(req.Messages, suggestionMode)
			if verboseDiagnostics {
				slog.Debug("tool_gate: disabled tools for tool_result-only follow-up")
			}
		}
	}
	effectiveTools := req.Tools
	if gateNoTools {
		effectiveTools = nil
		if verboseDiagnostics {
			slog.Debug("tool_gate: disabled tools")
		}
	}

	// Initial Selection
	failedAccountIDs := []int64{}
	failedAccountSet := make(map[int64]struct{})

	apiClient, currentAccount, err := h.selectAccountWithOptions(r.Context(), targetChannel, forcedChannel != "", failedAccountIDs, accountSelectionOptions{
		ModelID: strings.TrimSpace(req.Model),
	})
	if err != nil {
		slog.Error("selectAccount failed", "error", err, "channel", targetChannel)
		logger.LogEarlyExit("select_account_failed", map[string]interface{}{
			"error":   err.Error(),
			"model":   req.Model,
			"channel": targetChannel,
		})
		apperrors.New("overloaded_error", err.Error(), http.StatusServiceUnavailable).WriteResponse(w)
		return
	}
	if verboseDiagnostics {
		slog.Debug("Checkpoint: selectAccount success")
	}

	// Capture an account snapshot
	var accountSnapshot *store.Account
	if currentAccount != nil {
		snap := *currentAccount
		accountSnapshot = &snap
	}

	if isPuterRequest || (currentAccount != nil && strings.EqualFold(currentAccount.AccountType, "puter")) {
		isPuterRequest = true
	}
	isPassthroughRequest := isPuterRequest
	if isPassthroughRequest {
		if verboseDiagnostics {
			slog.Debug("Checkpoint: passthrough, skip context trimming", "channel", "puter")
		}
	} else {
		if verboseDiagnostics {
			slog.Debug("Checkpoint: skip context trimming")
		}
		if err := appreq.SanitizeSystemItems(h.config)(&req); err != nil {
			slog.Warn("Failed to sanitize system items", "error", err)
		}
	}
	if isPuterRequest {
		if err := appreq.SanitizeSystemItemsPuter(h.config)(&req); err != nil {
			slog.Warn("Failed to sanitize puter system items", "error", err)
		} else if verboseDiagnostics {
			slog.Debug("puter: sanitized forwarded system items")
		}
		req.Messages = sanitizePuterMessages(req.Messages)
	}
	if verboseDiagnostics {
		slog.Debug("Checkpoint: message processing done")
	}

	// Manually manage the connection count. When switching accounts, you need to release the old account and obtain a new account.
	trackedAccountID := int64(0)
	trackedAccountID = h.acquireTrackedAccount(currentAccount)
	defer func() {
		h.releaseTrackedAccount(trackedAccountID)
	}()

	// build prompt (V2 Markdown format)
	startBuild := time.Now()
	if verboseDiagnostics {
		slog.Debug("Starting prompt build...", "conversation_id", conversationKey)
	}
	// Mapping model
	mappedModel := mapModel(req.Model)
	if isPuterRequest {
		mappedModel = strings.TrimSpace(req.Model)
	} else if currentAccount != nil {
		at := strings.ToLower(strings.TrimSpace(currentAccount.AccountType))
		if at == "codebuff" {
			mappedModel = strings.TrimSpace(req.Model)
		}
	}

	var promptHistory []map[string]string
	var builtPrompt string
	type promptMetaType struct {
		Profile    string
		NoThinking bool
	}
	var promptMeta promptMetaType
	if isPuterRequest {
		builtPrompt = strings.TrimSpace(extractUserText(req.Messages))
		if builtPrompt == "" {
			builtPrompt = "puter request"
		}
		promptMeta = promptMetaType{
			Profile:    "puter",
			NoThinking: noThinking,
		}
	} else {
		builtPrompt = strings.TrimSpace(extractUserText(req.Messages))
		if builtPrompt == "" {
			builtPrompt = "request"
		}
		promptMeta = promptMetaType{
			Profile:    "generic",
			NoThinking: noThinking,
		}
	}
	noThinking = promptMeta.NoThinking
	suppressThinking = promptMeta.NoThinking
	buildDuration := time.Since(startBuild)
	if verboseDiagnostics {
		slog.Debug("Prompt build completed", "duration", buildDuration)
	}

	if verboseDiagnostics {
		slog.Debug("Model mapping", "original", req.Model, "mapped", mappedModel)
	}

	isStream := req.Stream

	if isStream {
		// Set SSE response headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		if _, ok := w.(http.Flusher); !ok {
			apperrors.New("api_error", "Streaming not supported by underlying connection", http.StatusInternalServerError).WriteResponse(w)
			return
		}
		streamingStarted = true
	} else {
		w.Header().Set("Content-Type", "application/json")
	}

	// Status management
	// msgID is now managed by streamHandler

	var chatHistory []interface{}
	upstreamMessages := append([]prompt.Message(nil), req.Messages...)

	// Pre-allocate chatHistory
	chatHistory = make([]interface{}, len(promptHistory))
	for i := range promptHistory {
		chatHistory[i] = promptHistory[i]
	}

	if gateNoTools {
		builtPrompt = injectToolGate(builtPrompt, toolGateMessage)
	}

	// 2. Record the converted prompt
	if verboseDiagnostics {
		slog.Debug("Checkpoint: LogConvertedPrompt")
	}
	logger.LogConvertedPrompt(builtPrompt)

	breakdown := estimateInputTokenBreakdown(builtPrompt, promptHistory, effectiveTools)
	breakdownProfile := promptMeta.Profile
	if verboseDiagnostics {
		slog.Debug(
			"Input token breakdown (estimated)",
			"prompt_profile", breakdownProfile,
			"base_prompt_tokens", breakdown.BasePromptTokens,
			"system_context_tokens", breakdown.SystemContextTokens,
			"history_tokens", breakdown.HistoryTokens,
			"tools_tokens", breakdown.ToolsTokens,
			"estimated_total_input_tokens", breakdown.Total,
		)
	}
	logger.LogInputTokenBreakdown(
		breakdownProfile,
		breakdown.BasePromptTokens,
		breakdown.SystemContextTokens,
		breakdown.HistoryTokens,
		breakdown.ToolsTokens,
		breakdown.Total,
	)

	// Token count (for front usage display)
	inputTokens := breakdown.Total
	if inputTokens <= 0 {
		inputTokens = h.estimateInputTokens(r.Context(), req.Model, builtPrompt)
	}

	var cacheReadTokens, cacheCreationTokens int
	if h.config.EnableTokenCache && h.promptCache != nil {
		sysText := ""
		if len(req.System) > 0 {
			if sysBytes, err := json.Marshal(req.System); err == nil {
				sysText = string(sysBytes)
			}
		}
		toolsText := ""
		if len(effectiveTools) > 0 {
			if toolsBytes, err := json.Marshal(effectiveTools); err == nil {
				toolsText = string(toolsBytes)
			}
		}

		rTokens, crTokens := h.promptCache.CheckPromptCache(
			h.config.TokenCacheStrategy,
			breakdown.SystemContextTokens,
			breakdown.ToolsTokens,
			sysText,
			toolsText,
		)
		cacheReadTokens = rTokens
		cacheCreationTokens = crTokens

		// Subtract cacheReadTokens from the base inputTokens
		// if simulating prompt caching billing behavior
		if inputTokens >= cacheReadTokens {
			inputTokens -= cacheReadTokens
		}
	}

	sh := newStreamHandler(
		h.config, w, logger, suppressThinking, isStream, responseFormat, effectiveWorkdir,
	)
	allowedToolNames := []string(nil)
	allowedToolNames = validationAllowedToolNames(effectiveTools, req.Tools, false)
	sh.setAllowedToolNames(allowedToolNames)
	if len(req.Tools) > 0 {
		sh.setClientTools(req.Tools)
	} else if len(effectiveTools) > 0 {
		sh.setClientTools(effectiveTools)
	}
	sh.setDisallowToolCalls(gateNoTools)
	sh.seedSideEffectDedupFromMessages(upstreamMessages)
	sh.setUsageTokens(inputTokens, -1)
	sh.setCacheTokens(cacheReadTokens, cacheCreationTokens)
	if cr, ok := apiClient.(upstream.ChunkRewriterInstaller); ok {
		sh.SetChunkRewriter(cr.BuildChunkRewriter())
	}
	sh.onConversationID = func(id string) {
		if conversationKey == "" {
			return
		}
		h.sessionStore.SetConvID(r.Context(), conversationKey, id)
		h.sessionStore.Touch(r.Context(), conversationKey)
		if verboseDiagnostics {
			slog.Debug("ConversationID captured", "key", conversationKey, "id", id)
		}
	}
	defer sh.release()

	sh.setModelHint(req.Model)
	// For codebuff raw SSE passthrough, skip Anthropic-format lifecycle events.
	if targetChannel == "codebuff" && isStream {
		sh.mu.Lock()
		sh.hasReturn = true
		sh.mu.Unlock()
	} else {
		sh.writeSSEMessageStart(req.Model, inputTokens, 0)
	}

	if verboseDiagnostics {
		slog.Debug("New request received")
	}

	// KeepAlive
	var keepAliveStop chan struct{}
	if isStream {
		keepAliveStop = make(chan struct{})
		defer close(keepAliveStop)
		ticker := time.NewTicker(keepAliveInterval)
		go func() {
			defer ticker.Stop()
			for {
				select {
				case <-ticker.C:
					sh.mu.Lock()
					done := sh.hasReturn
					sh.mu.Unlock()
					if done {
						return
					}
					sh.writeKeepAlive()
				case <-keepAliveStop:
					return
				case <-r.Context().Done():
					return
				}
			}
		}()
	}

	// Main execution
	run := func() {
		chatSessionID := "chat_" + randomSessionID()
		maxRetries := h.config.MaxRetries
		if maxRetries < 0 {
			maxRetries = 0
		}
		retryDelay := time.Duration(h.config.RetryDelay) * time.Millisecond
		retriesRemaining := maxRetries

		payloadMessages := upstreamMessages
		payloadSystem := req.System

		// For codebuff passthrough: forward raw SSE directly to client.
		var rawSSEWriter func(event string, data []byte)
		if targetChannel == "codebuff" && isStream {
			rawFlusher, _ := w.(http.Flusher)
			var rawSawToolCallsFinish bool
			rawSSEWriter = func(event string, data []byte) {
				if string(data) == "[DONE]" {
					fmt.Fprintf(w, "data: [DONE]\n\n")
					if rawFlusher != nil {
						rawFlusher.Flush()
					}
					return
				}
				if rawSawToolCallsFinish {
					hasStop := bytes.Contains(data, []byte(`"finish_reason":"stop"`))
					slog.Debug("rawSSE stop check", "has_stop", hasStop, "len", len(data), "snip", string(data[0:min(len(data),80)]))
					if hasStop {
						slog.Debug("suppressing stop chunk in rawSSEWriter")
						return
					}
				}
				fmt.Fprintf(w, "data: %s\n\n", data)
				if rawFlusher != nil {
					rawFlusher.Flush()
				}
				if !rawSawToolCallsFinish && bytes.Contains(data, []byte(`"finish_reason":"tool_calls"`)) {
					slog.Debug("detected tool_calls in rawSSEWriter")
					rawSawToolCallsFinish = true
				}
			}
		}

		upstreamReq := upstream.UpstreamRequest{
			Prompt:            builtPrompt,
			ChatHistory:       chatHistory,
			Workdir:           effectiveWorkdir,
			Model:             mappedModel,
			Stream:            req.Stream,
			Messages:          payloadMessages,
			System:            payloadSystem.ToPrompt(),
			Tools:             effectiveTools,
			ToolChoice:        req.ToolChoice,
			NoTools:           gateNoTools,
			NoThinking:        noThinking,
			TraceID:           traceID,
			ChatSessionID:     chatSessionID,
			ProjectID:         "",
			IsFirstPrompt:     false,
			DirectSSE:         nil,
			RawOpenAIMessages: rawBody.Messages,
			RawOpenAISystem:   rawBody.System,
			RawBody:           bodyBytes,
			RawSSEWriter:      rawSSEWriter,
		}
		primaryHandler := upstreamMessageHandler(sh)
		var attempt int
		for {
			sh.resetRoundState()
			var err error
			upstreamReq.Attempt = attempt + 1
			accountID := int64(0)
			accountType := ""
			accountName := ""
			if currentAccount != nil {
				accountID = currentAccount.ID
				accountType = currentAccount.AccountType
				accountName = currentAccount.Name
			}
			if verboseDiagnostics {
				slog.Debug(
					"Calling upstream client",
					"trace_id", traceID,
					"attempt", upstreamReq.Attempt,
					"max_attempts", maxRetries+1,
					"channel", targetChannel,
					"model", mappedModel,
					"conversation_id", conversationKey,
					"chat_session_id", chatSessionID,
					"account_id", accountID,
					"account_type", accountType,
					"account_name", accountName,
				)
			}

			if verboseDiagnostics {
				slog.Debug("Using SendRequestWithPayload")
			}
			err = apiClient.SendRequestWithPayload(r.Context(), upstreamReq, primaryHandler, logger)
			if verboseDiagnostics {
				slog.Debug("Upstream client returned", "trace_id", traceID, "attempt", upstreamReq.Attempt, "error", err)
			}

			if err == nil {
				sh.forceFinishIfMissing()
				if verboseDiagnostics {
					slog.Debug("Upstream attempt completed", "trace_id", traceID, "attempt", upstreamReq.Attempt)
				}
				break
			}
			errStr := err.Error()
			errClass := classifyUpstreamError(errStr)
			if sh.hasAnyOutput() {
				slog.Warn("Upstream failed after partial output, skip retry to avoid duplicated token billing", "trace_id", traceID, "attempt", upstreamReq.Attempt, "error", err)
				sh.finishResponse("end_turn")
				return
			}

			// Check for non-retriable errors
			slog.Error("Request error", "trace_id", traceID, "attempt", upstreamReq.Attempt, "error", err, "category", errClass.Category, "retryable", errClass.Retryable)
			// Mark account status
			if currentAccount != nil && h.loadBalancer != nil && h.loadBalancer.Store != nil {
				if status := classifyAccountStatus(errStr); status != "" {
					if !errClass.Retryable || errClass.Category == "auth" || errClass.Category == "auth_blocked" || status == "403" || status == "429" || status == "402" {
						if verboseDiagnostics {
							slog.Debug("Mark account status", "account_id", currentAccount.ID, "status", status, "category", errClass.Category)
						}
						h.loadBalancer.MarkAccountStatus(r.Context(), currentAccount, status)
					}
				}
			}

			if !errClass.Retryable {
				slog.Error("Aborting retries for non-retriable error", "error", err, "category", errClass.Category)
				if errClass.Category == "auth_blocked" || errClass.Category == "auth" {
					sh.InjectAuthError(errClass.Category, errStr)
				} else if errClass.Category != "canceled" {
					sh.InjectUpstreamError(errStr)
				}
				if errClass.Category == "canceled" {
					sh.finishResponse("end_turn")
					return
				}
				sh.finishResponse("end_turn")
				return
			}

			if r.Context().Err() != nil {
				sh.finishResponse("end_turn")
				return
			}
			if retriesRemaining <= 0 {
				if currentAccount != nil && h.loadBalancer != nil {
					slog.Error("Account request failed, max retries reached", "account", currentAccount.Name)
				}
				if errClass.Category == "auth" || errClass.Category == "auth_blocked" {
					sh.InjectAuthError(errClass.Category, errStr)
				} else {
					sh.InjectRetryExhaustedError(errStr)
				}
				sh.finishResponse("end_turn")
				return
			}
			retriesRemaining--
			slog.Warn(
				"Retrying upstream request without prior output",
				"trace_id", traceID,
				"attempt", upstreamReq.Attempt,
				"category", errClass.Category,
				"switch_account", errClass.SwitchAccount,
				"retries_remaining", retriesRemaining,
			)
			if errClass.SwitchAccount && currentAccount != nil && h.loadBalancer != nil {
				prevClient := apiClient
				prevAccount := currentAccount
				if _, ok := failedAccountSet[currentAccount.ID]; !ok {
					failedAccountSet[currentAccount.ID] = struct{}{}
					failedAccountIDs = append(failedAccountIDs, currentAccount.ID)
				}
				slog.Warn("Account request failed, switching account", "account", currentAccount.Name, "unsuccessful_attempts", len(failedAccountIDs))

				// Release the connection count of the old account
				if trackedAccountID != 0 {
					h.releaseTrackedAccount(trackedAccountID)
					trackedAccountID = 0
				}

				nextClient, nextAccount, retryErr := h.selectAccountWithOptions(r.Context(), targetChannel, forcedChannel != "", failedAccountIDs, accountSelectionOptions{
					ModelID: upstreamReq.Model,
				})
				if retryErr == nil {
					apiClient = nextClient
					currentAccount = nextAccount
					if currentAccount != nil {
						trackedAccountID = h.acquireTrackedAccount(currentAccount)
						if verboseDiagnostics {
							slog.Debug("Switched to account", "account", currentAccount.Name)
						}
					} else {
						if verboseDiagnostics {
							slog.Debug("Switched to default upstream config")
						}
					}
				} else {
					if shouldRetryCurrentAccountWhenNoAlternative(errClass.Category) && prevAccount != nil {
						apiClient = prevClient
						currentAccount = prevAccount
						trackedAccountID = h.acquireTrackedAccount(currentAccount)
						slog.Warn(
							"No alternate accounts available; retrying current account",
							"trace_id", traceID,
							"attempt", upstreamReq.Attempt,
							"account_id", currentAccount.ID,
							"category", errClass.Category,
							"retry_error", retryErr,
						)
					} else {
						slog.Error("No more accounts available", "error", retryErr)
						sh.InjectNoAvailableAccountError(errStr, retryErr)
						sh.finishResponse("end_turn")
						return
					}
				}
			}
			if retryDelay > 0 {
				delay := computeRetryDelay(retryDelay, attempt+1, errClass.Category)
				if delay > 0 && !util.SleepWithContext(r.Context(), delay) {
					sh.finishResponse("end_turn")
					return
				}
			}
			attempt++
		}
	}

	run()

	// ensure final response
	if !sh.hasReturn {
		sh.finishResponse("end_turn")
	}

	if !isStream {
		stopReason := sh.finalStopReason
		if stopReason == "" {
			stopReason = "end_turn"
		}

		for i := range sh.contentBlocks {
			blockType, _ := sh.contentBlocks[i]["type"].(string)
			switch blockType {
			case "text":
				if builder, ok := sh.textBlockBuilders[i]; ok {
					sh.contentBlocks[i]["text"] = builder.String()
				} else if _, ok := sh.contentBlocks[i]["text"]; !ok {
					sh.contentBlocks[i]["text"] = ""
				}
			case "thinking":
				if builder, ok := sh.thinkingBlockBuilders[i]; ok {
					sh.contentBlocks[i]["thinking"] = builder.String()
				} else if _, ok := sh.contentBlocks[i]["thinking"]; !ok {
					sh.contentBlocks[i]["thinking"] = ""
				}
			}
		}

		if len(sh.contentBlocks) == 0 && sh.responseText.Len() > 0 {
			sh.contentBlocks = append(sh.contentBlocks, map[string]interface{}{
				"type": "text",
				"text": sh.responseText.String(),
			})
		}
		if sh.contentBlocks == nil {
			sh.contentBlocks = make([]map[string]interface{}, 0)
		}

		var response interface{}
		if responseFormat == adapter.FormatOpenAI {
			response = buildOpenAINonStreamResponse(sh, req.Model, stopReason)
		} else {
			anthropicResponse := map[string]interface{}{
				"id":            sh.msgID,
				"type":          "message",
				"role":          "assistant",
				"content":       sh.contentBlocks,
				"model":         req.Model,
				"stop_reason":   stopReason,
				"stop_sequence": nil,
				"usage": map[string]int{
					"input_tokens":  sh.inputTokens,
					"output_tokens": sh.outputTokens,
				},
			}
			response = anthropicResponse
		}

		if err := json.NewEncoder(w).Encode(response); err != nil {
			slog.Error("Failed to write JSON response", "error", err)
		}

	}

	// Sync state and update stats using helpers
	h.syncWarpState(currentAccount, apiClient, accountSnapshot)
	h.updateAccountStats(currentAccount, sh.inputTokens, sh.outputTokens)

	// Audit log
	if h.auditLogger != nil {
		accountID := int64(0)
		channel := forcedChannel
		if currentAccount != nil {
			accountID = currentAccount.ID
			if channel == "" {
				channel = currentAccount.AccountType
			}
		}
		status := "success"
		if sh.finalStopReason == "" && !sh.hasReturn {
			status = "error"
		}
		h.auditLogger.Log(r.Context(), audit.Event{
			Action:    "chat_request",
			AccountID: accountID,
			Model:     req.Model,
			Channel:   channel,
			ClientIP:  r.RemoteAddr,
			UserAgent: r.UserAgent(),
			Duration:  time.Since(startTime).Milliseconds(),
			Status:    status,
			Metadata: map[string]interface{}{
				"input_tokens":  sh.inputTokens,
				"output_tokens": sh.outputTokens,
				"stream":        isStream,
			},
		})
	}
}

func randomSessionID() string {
	b := make([]byte, 6)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// handleCodebuffDirect is a pure passthrough handler for codebuff that matches
// freebuff2api's approach exactly. It parses only the model name from the
// request body, then forwards the raw body + raw messages upstream with minimal
// modification — no ClaudeRequest, no prompt.Message, no ContentBlock, no
// SystemItems, no cache_control. This eliminates all intermediate type
// conversions that freebuff2api does not perform.
func (h *Handler) handleCodebuffDirect(w http.ResponseWriter, r *http.Request, bodyBytes []byte, startTime time.Time) {
	// Extract minimal fields from raw body — just model, stream, messages, system.
	var rawBody struct {
		Model    string             `json:"model"`
		Stream   bool               `json:"stream"`
		Messages stdjson.RawMessage `json:"messages"`
		System   stdjson.RawMessage `json:"system"`
	}
	if err := stdjson.Unmarshal(bodyBytes, &rawBody); err != nil {
		apperrors.New("invalid_request_error", "Invalid request body", http.StatusBadRequest).WriteResponse(w)
		return
	}

	// Debug logger
	logger := debug.New(h.config.DebugEnabled, h.config.DebugLogSSE)
	defer logger.Close()

	// Determine channel from URL path
	forcedChannel := channelFromPath(r.URL.Path)
	targetChannel := strings.TrimSpace(forcedChannel)
	if targetChannel == "" {
		targetChannel = "codebuff"
	}

	// Validate model availability
	validatedModel, err := h.validateModelAvailability(r.Context(), rawBody.Model, forcedChannel)
	if err != nil {
		apperrors.New("invalid_request_error", err.Error(), http.StatusBadRequest).WriteResponse(w)
		return
	}
	mappedModel := rawBody.Model
	if validatedModel != nil && validatedModel.ModelID != "" {
		mappedModel = validatedModel.ModelID
	}

	// Select account
	var failedAccountIDs []int64
	apiClient, currentAccount, err := h.selectAccountWithOptions(r.Context(), targetChannel, true, failedAccountIDs, accountSelectionOptions{
		ModelID: mappedModel,
	})
	if err != nil {
		apperrors.New("server_error", fmt.Sprintf("No available accounts: %v", err), http.StatusServiceUnavailable).WriteResponse(w)
		return
	}

	slog.Debug("codebuff direct passthrough",
		"channel", targetChannel,
		"requested_model", rawBody.Model,
		"mapped_model", mappedModel,
		"account_id", currentAccount.ID,
		"stream", rawBody.Stream,
	)

	// Set SSE headers
	if rawBody.Stream {
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
	}

	// Raw SSE writer — forwards upstream SSE directly to client, suppressing
	// trailing finish_reason:"stop" after finish_reason:"tool_calls".
	var rawSSEWriter func(event string, data []byte)
	if rawBody.Stream {
		rawFlusher, _ := w.(http.Flusher)
		var rawSawToolCallsFinish bool
		rawSSEWriter = func(event string, data []byte) {
			if string(data) == "[DONE]" {
				fmt.Fprintf(w, "data: [DONE]\n\n")
				if rawFlusher != nil {
					rawFlusher.Flush()
				}
				return
			}
			if rawSawToolCallsFinish && bytes.Contains(data, []byte(`"finish_reason":"stop"`)) {
				slog.Debug("suppressing stop chunk in codebuff direct passthrough", "len", len(data))
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", data)
			if rawFlusher != nil {
				rawFlusher.Flush()
			}
			if !rawSawToolCallsFinish && bytes.Contains(data, []byte(`"finish_reason":"tool_calls"`)) {
				rawSawToolCallsFinish = true
			}
		}
	}

	// Build minimal upstream request with raw body — no type conversions.
	upstreamReq := upstream.UpstreamRequest{
		Model:             mappedModel,
		Stream:            rawBody.Stream,
		RawBody:           bodyBytes,
		RawOpenAIMessages: rawBody.Messages,
		RawOpenAISystem:   rawBody.System,
		RawSSEWriter:      rawSSEWriter,
		TraceID:           fmt.Sprintf("codebuff-%x", time.Now().UnixNano()),
	}

	// Call provider — SSE passthrough only
	if err := apiClient.SendRequestWithPayload(r.Context(), upstreamReq, nil, logger); err != nil {
		slog.Error("codebuff direct passthrough failed", "error", err)
	}

	// Log audit event
	auditStatus := "success"
	h.auditLogger.Log(r.Context(), audit.Event{
		Action:    "chat_request",
		AccountID: currentAccount.ID,
		Model:     mappedModel,
		Channel:   targetChannel,
		ClientIP:  r.RemoteAddr,
		UserAgent: r.UserAgent(),
		Duration:  time.Since(startTime).Milliseconds(),
		Status:    auditStatus,
	})
}
