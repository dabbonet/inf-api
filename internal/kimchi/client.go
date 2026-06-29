// Package kimchi is the upstream HTTP client for Kimchi / Cast.ai
// "Serverless Inference" (https://llm.kimchi.dev). It implements
// upstream.UpstreamClient so the shared inf-api dispatcher
// (handler.handlePassthroughProvider → spec.ClientFactory) can pick it up
// through the provider registry.
package kimchi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"orchids-api/internal/config"
	"orchids-api/internal/util"
)

const (
	// defaultLLMBaseURL hosts chat completions + model metadata.
	defaultLLMBaseURL = "https://llm.kimchi.dev"
	// defaultAppBaseURL hosts /api/v1/me (auth/profile lookups live on app, not llm).
	defaultAppBaseURL = "https://app.kimchi.dev"
	// defaultUserAgent mirrors what the official @kimchi-dev/cli sends.
	// The upstream doesn't enforce this but matching it keeps their analytics
	// signals identical to first-party CLI traffic.
	defaultUserAgent = "kimchi/0.1.50"
	maxResponseBody  = 64 * 1024 * 1024
)

// Me mirrors the app.kimchi.dev/api/v1/me response. Only the fields we use
// are decoded; the rest are dropped to avoid struct drift.
type Me struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Name     string `json:"name"`
	Email    string `json:"email"`
}

// ModelInfo is a single row of /v1/models/metadata. We read just the fields
// needed to write into the local store.Model table.
type ModelInfo struct {
	Slug             string   `json:"slug"`
	DisplayName      string   `json:"display_name"`
	Provider         string   `json:"provider"`
	ToolCall         bool     `json:"tool_call"`
	Reasoning        bool     `json:"reasoning"`
	SupportsImages   bool     `json:"supports_images"`
	InputModalities  []string `json:"input_modalities"`
	IsServerless     bool     `json:"is_serverless"`
	IsRoutable       bool     `json:"is_routable"`
	ContextWindow    int      `json:"limits.context_window"`
	MaxOutputTokens  int      `json:"limits.max_output_tokens"`
}

// ModelsMetadataResponse envelopes /v1/models/metadata.
type ModelsMetadataResponse struct {
	Models []ModelInfo `json:"models"`
}

// HTTPError is what we return from SendStream/SendJSON for non-2xx upstream
// responses. The error message carries the body so the load balancer can
// pattern-match on canonical upstream messages.
type HTTPError struct {
	Status  int
	Body    string
	Headers http.Header
}

func (e *HTTPError) Error() string {
	body := strings.TrimSpace(e.Body)
	if body == "" {
		body = "<empty>"
	}
	// Truncate so we don't blow up the in-memory error log on a giant SSE body.
	if len(body) > 256 {
		body = body[:256] + "..."
	}
	return fmt.Sprintf("kimchi upstream %d: %s", e.Status, body)
}

// Client wraps the HTTP transport. Same shape as codebuff.Client / puter.Client.
type Client struct {
	apiKey     string
	llmBaseURL string
	appBaseURL string
	userAgent  string
	httpClient *http.Client
}

// NewClient builds a Client for the given bearer token.
// cfg.ProxyURL / cfg.RequestTimeout / cfg.KimchiBaseURL are honoured.
func NewClient(apiKey string, cfg *config.Config) *Client {
	timeout := 60 * time.Second
	if cfg != nil && cfg.RequestTimeout > 0 {
		timeout = time.Duration(cfg.RequestTimeout) * time.Second
	}
	if timeout < 30*time.Second {
		timeout = 30 * time.Second
	}

	proxyFunc := util.ProxyFuncFromConfig(cfg)
	proxyKey := util.GenerateProxyKeyFromConfig(cfg)

	llmBase := defaultLLMBaseURL
	appBase := defaultAppBaseURL
	if cfg != nil {
		if v := strings.TrimSpace(cfg.KimchiBaseURL); v != "" {
			llmBase = strings.TrimRight(v, "/")
			appBase = strings.TrimRight(strings.Replace(v, "llm.", "app.", 1), "/")
		}
	}

	return &Client{
		apiKey:     apiKey,
		llmBaseURL: llmBase,
		appBaseURL: appBase,
		userAgent:  defaultUserAgent,
		httpClient: util.GetSharedHTTPClient(proxyKey, timeout, proxyFunc),
	}
}

// VerifyAccount pings /api/v1/me on the app host. Used at admin-create time
// (api.HandleAccounts path) to confirm the bearer works and to surface the
// stable user id we'll mirror into Account.ID.
func (c *Client) VerifyAccount(ctx context.Context) (*Me, error) {
	if c == nil || c.apiKey == "" {
		return nil, fmt.Errorf("kimchi: client not initialized")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.appBaseURL+"/api/v1/me", nil)
	if err != nil {
		return nil, fmt.Errorf("kimchi: build me request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kimchi: /api/v1/me: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(body), Headers: resp.Header}
	}
	var me Me
	if err := json.Unmarshal(body, &me); err != nil {
		return nil, fmt.Errorf("kimchi: /api/v1/me decode: %w", err)
	}
	if me.ID == "" {
		return nil, fmt.Errorf("kimchi: /api/v1/me response missing id")
	}
	return &me, nil
}

// FetchModelMetadata pulls the live model catalog. Source URL is exact-match
// against the official @kimchi-dev/cli models.ts::modelsMetadataApi().
func (c *Client) FetchModelMetadata(ctx context.Context) ([]ModelInfo, error) {
	if c == nil || c.apiKey == "" {
		return nil, fmt.Errorf("kimchi: client not initialized")
	}
	url := c.llmBaseURL + "/v1/models/metadata?include_in_cli=true"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("kimchi: build models request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kimchi: models metadata: %w", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1*1024*1024))
	if resp.StatusCode != http.StatusOK {
		return nil, &HTTPError{Status: resp.StatusCode, Body: string(body), Headers: resp.Header}
	}
	var out ModelsMetadataResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("kimchi: models metadata decode: %w", err)
	}
	return out.Models, nil
}

// endpointKind picks which upstream URL the Provider hits.
type endpointKind int

const (
	endpointOpenAI endpointKind = iota // /openai/v1/chat/completions
	endpointAnthropic                  // /anthropic/v1/messages
)

func (c *Client) upstreamURL(kind endpointKind) string {
	switch kind {
	case endpointAnthropic:
		return c.llmBaseURL + "/anthropic/v1/messages"
	default:
		return c.llmBaseURL + "/openai/v1/chat/completions"
	}
}

// SendStream POSTs body upstream and writes each SSE `data:` line verbatim
// using write(). Upstream may be the OpenAI Chat Completions endpoint
// (returns `data: {json}` + `data: [DONE]`) or the Anthropic Messages
// endpoint (returns `event: message_start`, content_block_delta, etc.).
// We do NO translation — passthrough mode per the spec.
//
// Returns *HTTPError on non-2xx, or nil on graceful stream completion.
func (c *Client) SendStream(ctx context.Context, kind endpointKind, body []byte, write func(event string, data []byte)) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.upstreamURL(kind), bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("kimchi: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "text/event-stream")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("kimchi: upstream POST: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
		return &HTTPError{Status: resp.StatusCode, Body: string(b), Headers: resp.Header}
	}
	return forwardSSE(resp.Body, write)
}

// SendJSON POSTs body upstream and returns the raw response body for non-stream
// callers (e.g. /v1/messages with stream:false). We hand back raw bytes so the
// inf-api handler can shape them however it likes without re-marshalling.
func (c *Client) SendJSON(ctx context.Context, kind endpointKind, body []byte) ([]byte, *HTTPError, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.upstreamURL(kind), bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("kimchi: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("kimchi: upstream POST: %w", err)
	}
	defer resp.Body.Close()
	buf, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBody))
	if err != nil {
		return nil, nil, fmt.Errorf("kimchi: read response: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return buf, &HTTPError{Status: resp.StatusCode, Body: string(buf), Headers: resp.Header}, nil
	}
	return buf, nil, nil
}

// forwardSSE walks the upstream body line-by-line, catching `event:` and
// `data:` pairs, and hands each closed event to write(). On `[DONE]` we
// stop. We don't try to parse the SSE JSON — passthrough mode.
func forwardSSE(r io.Reader, write func(event string, data []byte)) error {
	scanner := bufio.NewReaderSize(r, 64*1024)
	var event, data strings.Builder
	for {
		line, err := readSSEline(scanner)
		if err == io.EOF {
			if data.Len() > 0 {
				write(event.String(), []byte(data.String()))
			}
			return nil
		}
		if err != nil {
			return err
		}
		switch {
		case line == "":
			if data.Len() > 0 {
				payload := []byte(data.String())
				write(event.String(), payload)
				if bytes.Equal(payload, []byte("[DONE]")) {
					return nil
				}
			}
			event.Reset()
			data.Reset()
		case strings.HasPrefix(line, "event:"):
			event.Reset()
			event.WriteString(strings.TrimSpace(line[len("event:"):]))
		case strings.HasPrefix(line, "data:"):
			v := strings.TrimSpace(line[len("data:"):])
			if data.Len() > 0 {
				data.WriteByte('\n')
			}
			data.WriteString(v)
		case strings.HasPrefix(line, ":"):
			// SSE comment / keepalive — ignore.
		}
	}
}

// readSSEline reads one logical SSE line, terminated by LF and stripping
// trailing CR. Returns io.EOF at clean stream end.
func readSSEline(r *bufio.Reader) (string, error) {
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		return "", err
	}
	line = strings.TrimRight(line, "\r\n")
	return line, nil
}
