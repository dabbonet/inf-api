package codebuff

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"orchids-api/internal/config"
	"orchids-api/internal/util"
)

const (
	codebuffJSONUserAgent     = "Bun/1.3.11"
	freebuffCLIUserAgent      = "Freebuff-CLI/0.0.105"
	chatCompletionsUserAgent  = "ai-sdk/openai-compatible/0.0.0-test/codebuff ai-sdk/provider-utils/3.0.20 runtime/browser"
	defaultCodebuffAPIURL     = "https://www.codebuff.com"
	defaultZeroclickAPIURL    = "https://zeroclick.dev"
	defaultRequestTimeout     = 60 * time.Second
	maxQueuePollDelay         = 2 * time.Second
	minQueuePollDelay         = 250 * time.Millisecond
)

// Client wraps the HTTP transport for codebuff.com APIs.
type Client struct {
	apiKey     string
	baseURL    string
	httpClient *http.Client
	config     *config.Config
}

// NewClient creates a codebuff HTTP client.
func NewClient(apiKey string, cfg *config.Config) *Client {
	timeout := defaultRequestTimeout
	if cfg != nil && cfg.RequestTimeout > 0 {
		timeout = time.Duration(cfg.RequestTimeout) * time.Second
	}
	if timeout < 30*time.Second {
		timeout = 30 * time.Second
	}

	proxyFunc := http.ProxyFromEnvironment
	proxyKey := "direct"
	if cfg != nil {
		proxyFunc = util.ProxyFuncFromConfig(cfg)
		proxyKey = util.GenerateProxyKeyFromConfig(cfg)
	}

	baseURL := defaultCodebuffAPIURL
	if cfg != nil && cfg.CodebuffBaseURL != "" {
		baseURL = strings.TrimSpace(cfg.CodebuffBaseURL)
		baseURL = strings.TrimRight(baseURL, "/")
	}

	return &Client{
		apiKey:     apiKey,
		baseURL:    baseURL,
		httpClient: util.GetSharedHTTPClient(proxyKey, timeout, proxyFunc),
		config:     cfg,
	}
}

// Close is a no-op because we use the shared HTTP client pool.
func (c *Client) Close() {}

func (c *Client) headers(jsonBody bool, userAgent string, extra map[string]string) http.Header {
	h := http.Header{}
	h.Set("Accept", "*/*")
	h.Set("Connection", "keep-alive")
	h.Set("Host", hostHeader(c.baseURL))
	h.Set("User-Agent", userAgent)
	if c.apiKey != "" {
		h.Set("Authorization", "Bearer "+c.apiKey)
	}
	if jsonBody {
		h.Set("Content-Type", "application/json")
	}
	for k, v := range extra {
		h.Set(k, v)
	}
	return h
}

func (c *Client) doJSON(ctx context.Context, method, path string, body map[string]any, h http.Header) (map[string]any, error) {
	url := c.baseURL + path
	var bodyReader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("failed to marshal request body: %w", err)
		}
		bodyReader = bytes.NewReader(raw)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}
	for k, vv := range h {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, NewNetworkError(method, url, err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode >= 400 {
		return nil, NewError(resp, respBody, "Codebuff request failed")
	}
	if len(respBody) == 0 {
		return map[string]any{}, nil
	}
	var result map[string]any
	if err := json.Unmarshal(respBody, &result); err != nil {
		return nil, fmt.Errorf("failed to decode JSON response: %w", err)
	}
	return result, nil
}

// GetSession calls GET /api/v1/freebuff/session.
func (c *Client) GetSession(ctx context.Context, instanceID string) (map[string]any, error) {
	extra := map[string]string{}
	if instanceID != "" {
		extra["x-freebuff-instance-id"] = instanceID
	}
	return c.doJSON(ctx, http.MethodGet, "/api/v1/freebuff/session", nil, c.headers(false, codebuffJSONUserAgent, extra))
}

// CreateSession calls POST /api/v1/freebuff/session.
// The returned map contains the raw upstream response (including rateLimitsByModel).
func (c *Client) CreateSession(ctx context.Context, model string) (*Session, map[string]any, error) {
	data, err := c.doJSON(ctx, http.MethodPost, "/api/v1/freebuff/session", nil, c.headers(false, codebuffJSONUserAgent, map[string]string{"x-freebuff-model": model}))
	if err != nil {
		return nil, nil, err
	}
	if status, _ := data["status"].(string); status == "queued" {
		return c.waitForActiveSession(ctx, data, model)
	}
	return sessionFromData(data, model), data, nil
}

// DeleteSession calls DELETE /api/v1/freebuff/session.
func (c *Client) DeleteSession(ctx context.Context) error {
	_, err := c.doJSON(ctx, http.MethodDelete, "/api/v1/freebuff/session", nil, c.headers(false, codebuffJSONUserAgent, nil))
	return err
}

// GetStreak calls GET /api/v1/freebuff/streak.
func (c *Client) GetStreak(ctx context.Context) (map[string]any, error) {
	return c.doJSON(ctx, http.MethodGet, "/api/v1/freebuff/streak", nil, c.headers(false, codebuffJSONUserAgent, nil))
}

// RequestAds calls POST /api/v1/ads.
func (c *Client) RequestAds(ctx context.Context, provider string, messages []map[string]any, surface string) (map[string]any, error) {
	body := map[string]any{
		"provider":  provider,
		"messages":  adMessages(messages),
		"sessionId": sessionID(c.config),
		"device": map[string]any{
			"os":       osName(c.config),
			"timezone": timezone(c.config),
			"locale":   locale(c.config),
		},
		"userAgent": browserUserAgent,
	}
	if surface != "" {
		body["surface"] = surface
	}
	return c.doJSON(ctx, http.MethodPost, "/api/v1/ads", body, c.headers(true, freebuffCLIUserAgent, nil))
}

// ReportZeroclickImpressions calls POST /api/v2/impressions on zeroclick.
func (c *Client) ReportZeroclickImpressions(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}
	zcURL := defaultZeroclickAPIURL
	if c.config != nil && c.config.ZeroclickBaseURL != "" {
		zcURL = strings.TrimRight(c.config.ZeroclickBaseURL, "/")
	}
	url := zcURL + "/api/v2/impressions"
	raw, _ := json.Marshal(map[string]any{"ids": ids})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "*/*")
	req.Header.Set("User-Agent", codebuffJSONUserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return NewNetworkError("POST", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 8192))
		return &Error{Message: fmt.Sprintf("Zeroclick impression failed: %d %s", resp.StatusCode, string(body)), StatusCode: resp.StatusCode}
	}
	return nil
}

// ReportCodebuffImpression calls POST /api/v1/ads/impression.
func (c *Client) ReportCodebuffImpression(ctx context.Context, impURL string) error {
	if impURL == "" {
		return nil
	}
	_, err := c.doJSON(ctx, http.MethodPost, "/api/v1/ads/impression", map[string]any{"impUrl": impURL, "mode": "LITE"}, c.headers(true, freebuffCLIUserAgent, nil))
	return err
}

// StartRun calls POST /api/v1/agent-runs with action=START.
func (c *Client) StartRun(ctx context.Context, agentID string, ancestorRunIDs []string) (string, error) {
	body := map[string]any{
		"action":         "START",
		"agentId":        agentID,
		"ancestorRunIds": ancestorRunIDs,
	}
	data, err := c.doJSON(ctx, http.MethodPost, "/api/v1/agent-runs", body, c.headers(true, codebuffJSONUserAgent, nil))
	if err != nil {
		return "", err
	}
	runID, _ := data["runId"].(string)
	if runID == "" {
		return "", &Error{Message: fmt.Sprintf("Codebuff run id missing: %v", data), StatusCode: 502}
	}
	return runID, nil
}

// RecordRunStep calls POST /api/v1/agent-runs/{runID}/steps.
func (c *Client) RecordRunStep(ctx context.Context, runID string, stepNumber int, childRunIDs []string, messageID, startTime string) error {
	body := map[string]any{
		"stepNumber":  stepNumber,
		"credits":     0,
		"childRunIds": childRunIDs,
		"messageId":   messageID,
		"status":      "completed",
		"startTime":   startTime,
	}
	_, err := c.doJSON(ctx, http.MethodPost, fmt.Sprintf("/api/v1/agent-runs/%s/steps", runID), body, c.headers(true, codebuffJSONUserAgent, nil))
	return err
}

// FinishRun calls POST /api/v1/agent-runs with action=FINISH.
func (c *Client) FinishRun(ctx context.Context, runID string, totalSteps int) error {
	body := map[string]any{
		"action":       "FINISH",
		"runId":        runID,
		"status":       "completed",
		"totalSteps":   totalSteps,
		"directCredits": 0,
		"totalCredits": 0,
	}
	_, err := c.doJSON(ctx, http.MethodPost, "/api/v1/agent-runs", body, c.headers(true, codebuffJSONUserAgent, nil))
	return err
}

// ChatCompletions opens a streaming POST to /api/v1/chat/completions and returns
// the response body (an SSE stream) that the caller must close.
func (c *Client) ChatCompletions(ctx context.Context, payload map[string]any) (io.ReadCloser, error) {
	url := c.baseURL + "/api/v1/chat/completions"
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal chat payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("failed to create chat request: %w", err)
	}
	for k, vv := range c.headers(true, chatCompletionsUserAgent, nil) {
		for _, v := range vv {
			req.Header.Add(k, v)
		}
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, NewNetworkError("POST", url, err)
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		return nil, NewError(resp, body, "Codebuff chat failed")
	}
	return resp.Body, nil
}

// ValidateAgents calls POST /api/agents/validate.
func (c *Client) ValidateAgents(ctx context.Context) error {
	_, err := c.doJSON(ctx, http.MethodPost, "/api/agents/validate", AgentValidationPayload(), c.headers(true, codebuffJSONUserAgent, map[string]string{}))
	return err
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func (c *Client) waitForActiveSession(ctx context.Context, data map[string]any, model string) (*Session, map[string]any, error) {
	instanceID, _ := data["instanceId"].(string)
	if instanceID == "" {
		return nil, nil, &Error{Message: fmt.Sprintf("Freebuff queued session id missing: %v", data), StatusCode: 502}
	}

	deadline, hasDeadline := ctx.Deadline()
	if !hasDeadline {
		deadline = time.Now().Add(defaultRequestTimeout)
	}
	attempts := 0
	for {
		status, _ := data["status"].(string)
		if status != "queued" {
			break
		}
		if time.Now().After(deadline) {
			return nil, nil, &Error{Message: fmt.Sprintf("Freebuff session did not become active before timeout: %v", data), StatusCode: 502}
		}
		if attempts > 0 {
			select {
			case <-ctx.Done():
				return nil, nil, ctx.Err()
			case <-time.After(queuePollDelay(data["estimatedWaitMs"])):
			}
		}
		var err error
		data, err = c.GetSession(ctx, instanceID)
		if err != nil {
			return nil, nil, err
		}
		attempts++
	}
	return sessionFromData(data, model), data, nil
}

func sessionFromData(data map[string]any, model string) *Session {
	instanceID, _ := data["instanceId"].(string)
	if instanceID == "" {
		// Defensive: if the upstream returns active but no instanceId, treat as error.
		return nil
	}
	expiresAt, _ := data["expiresAt"].(string)
	remainingMs := 0
	if v, ok := data["remainingMs"].(float64); ok {
		remainingMs = int(v)
	}
	if v, ok := data["remainingMs"].(int); ok {
		remainingMs = v
	}
	return &Session{
		InstanceID: instanceID,
		Model:      model,
		ExpiresAt:  expiresAt,
		RemainingMs: remainingMs,
	}
}

func hostHeader(urlStr string) string {
	u, err := url.Parse(urlStr)
	if err != nil {
		return "www.codebuff.com"
	}
	if u.Host != "" {
		return u.Host
	}
	return "www.codebuff.com"
}

func queuePollDelay(estimatedWaitMs any) time.Duration {
	if v, ok := estimatedWaitMs.(float64); ok && v > 0 {
		d := time.Duration(v) * time.Millisecond
		if d < minQueuePollDelay {
			return minQueuePollDelay
		}
		if d > maxQueuePollDelay {
			return maxQueuePollDelay
		}
		return d
	}
	return minQueuePollDelay
}

func adMessages(messages []map[string]any) []map[string]string {
	result := make([]map[string]string, 0, len(messages))
	for _, m := range messages {
		role, _ := m["role"].(string)
		if role == "developer" {
			role = "system"
		}
		content := adMessageContent(m["content"])
		result = append(result, map[string]string{"role": role, "content": content})
	}
	return result
}

func adMessageContent(content any) string {
	switch v := content.(type) {
	case string:
		return v
	case nil:
		return ""
	case []any:
		parts := make([]string, 0, len(v))
		for _, part := range v {
			p, ok := part.(map[string]any)
			if !ok {
				continue
			}
			if text, ok := p["text"].(string); ok {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if text, ok := v["text"].(string); ok {
			return text
		}
	}
	return ""
}

func sessionID(cfg *config.Config) string {
	if cfg != nil && cfg.CodebuffSessionID != "" {
		return cfg.CodebuffSessionID
	}
	return ""
}

func osName(cfg *config.Config) string {
	if cfg != nil && cfg.CodebuffOS != "" {
		return cfg.CodebuffOS
	}
	return "windows"
}

func timezone(cfg *config.Config) string {
	if cfg != nil && cfg.CodebuffTimezone != "" {
		return cfg.CodebuffTimezone
	}
	return "Asia/Shanghai"
}

func locale(cfg *config.Config) string {
	if cfg != nil && cfg.CodebuffLocale != "" {
		return cfg.CodebuffLocale
	}
	return "zh-CN"
}

const browserUserAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36"
