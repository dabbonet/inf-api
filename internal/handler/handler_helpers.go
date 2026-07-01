package handler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	apperrors "orchids-api/internal/errors"
	"orchids-api/internal/codebuff"
	"orchids-api/internal/store"
	"orchids-api/internal/upstream"

	"github.com/redis/go-redis/v9"
)

func (h *Handler) resolveWorkdir(r *http.Request, req ClaudeRequest, conversationKey string) (string, string, bool) {
	prevWorkdir := ""
	if conversationKey != "" {
		prevWorkdir, _ = h.sessionStore.GetWorkdir(r.Context(), conversationKey)
	}

	dynamicWorkdir, source := extractWorkdirFromRequest(r, req)

	hasExplicitSession := req.ConversationID != "" ||
		headerValue(r, "X-Conversation-Id", "X-Session-Id", "X-Thread-Id", "X-Chat-Id") != "" ||
		(req.Metadata != nil && metadataString(req.Metadata,
			"conversation_id", "conversationId",
			"session_id", "sessionId",
			"thread_id", "threadId",
			"chat_id", "chatId",
		) != "")

	if dynamicWorkdir == "" && hasExplicitSession && prevWorkdir != "" {
		dynamicWorkdir = prevWorkdir
		source = "session"
		slog.Debug("Recovered workdir from session", "workdir", dynamicWorkdir, "session", conversationKey)
	}

	if dynamicWorkdir != "" && conversationKey != "" {
		h.sessionStore.SetWorkdir(r.Context(), conversationKey, dynamicWorkdir)
		h.sessionStore.Touch(r.Context(), conversationKey)
	}

	if dynamicWorkdir != "" {
		slog.Debug("Using dynamic workdir", "workdir", dynamicWorkdir, "source", source)
	}
	rawPrev := strings.TrimSpace(prevWorkdir)
	rawNext := strings.TrimSpace(dynamicWorkdir)
	normalizedPrev := ""
	normalizedNext := ""
	if rawPrev != "" {
		normalizedPrev = filepath.Clean(rawPrev)
	}
	if rawNext != "" {
		normalizedNext = filepath.Clean(rawNext)
	}
	changed := normalizedPrev != "" && normalizedNext != "" && normalizedPrev != normalizedNext
	return dynamicWorkdir, prevWorkdir, changed
}

// selectAccount logic extracted from HandleMessages
func (h *Handler) selectAccount(ctx context.Context, targetChannel string, channelRequired bool, failedAccountIDs []int64, modelID ...string) (upstream.UpstreamClient, *store.Account, error) {
	return h.selectAccountWithOptions(ctx, targetChannel, channelRequired, failedAccountIDs, accountSelectionOptions{
		ModelID: firstString(modelID...),
	})
}

type accountSelectionOptions struct {
	ModelID string
}

func (h *Handler) selectAccountWithOptions(ctx context.Context, targetChannel string, channelRequired bool, failedAccountIDs []int64, opts accountSelectionOptions) (upstream.UpstreamClient, *store.Account, error) {
	if h.loadBalancer != nil {
		if targetChannel != "" {
			slog.Debug("Account channel selection", "channel", targetChannel, "channel_required", channelRequired)
		}
		account, err := h.selectAccountRecordWithOptions(ctx, targetChannel, failedAccountIDs, opts)
		if err != nil {
			if channelRequired {
				return nil, nil, err
			}
			if h.client != nil {
				slog.Debug("Load balancer: no available accounts for channel, using default config", "channel", targetChannel)
				return h.client, nil, nil
			}
			return nil, nil, err
		}
		client := h.getOrCreateAccountClient(account)
		if client == nil {
			return nil, nil, errors.New("no client configured")
		}
		return client, account, nil
	} else if h.client != nil {
		return h.client, nil, nil
	}
	return nil, nil, errors.New("no client configured")
}

func (h *Handler) selectAccountRecord(ctx context.Context, targetChannel string, failedAccountIDs []int64, modelID string) (*store.Account, error) {
	return h.selectAccountRecordWithOptions(ctx, targetChannel, failedAccountIDs, accountSelectionOptions{ModelID: modelID})
}

func (h *Handler) selectAccountRecordWithOptions(ctx context.Context, targetChannel string, failedAccountIDs []int64, opts accountSelectionOptions) (*store.Account, error) {
	if h == nil || h.loadBalancer == nil {
		return nil, errors.New("load balancer not configured")
	}

	// Quota-aware filter: never pick an account whose (acct, model) has an
	// active 429 block in QuotaStore. The block key auto-expires at
	// resetAt+1min, so a missing key means quota is available again. This is
	// the SOURCE OF TRUTH for "blocked until resetAt" — load balancer's
	// StatusCode cooldown is wrong shape (per-account, 1 minute) for freebuff.
	quotaFilter := func(acc *store.Account) bool {
		if h.quotaStore == nil || acc == nil || opts.ModelID == "" {
			return true
		}
		if !strings.EqualFold(targetChannel, "codebuff") {
			return true
		}
		blocked, err := h.quotaStore.IsBlocked(ctx, acc.ID, opts.ModelID)
		if err != nil {
			return true // fail-open: never wedge selection on a Redis hiccup
		}
		return !blocked
	}

	// Reactive model-session affinity: if the caller knows which model is needed
	// AND we're routing to codebuff, scan existing session caches to find the
	// account that already holds an active upstream session for this model.
	// One Redis EXISTS per account; no writes, no proactive assignment.
	if opts.ModelID != "" && strings.EqualFold(targetChannel, "codebuff") {
		tAffinityStart := time.Now()
		acc := h.trySessionAffinity(ctx, opts.ModelID, failedAccountIDs, quotaFilter)
		slog.Debug("DEBUG_LATENCY trySessionAffinity", "ms", time.Since(tAffinityStart).Milliseconds(), "matched", acc != nil)
		if acc != nil {
			return acc, nil
		}
	}

	tLBStart := time.Now()
	out, lbErr := h.loadBalancer.GetNextAccountExcludingByChannelWithTrackerFilter(ctx, failedAccountIDs, targetChannel, h.connTracker, quotaFilter)
	slog.Debug("DEBUG_LATENCY loadbalancer.GetNext", "ms", time.Since(tLBStart).Milliseconds())
	return out, lbErr
}

// trySessionAffinity checks whether any codebuff account already has an
// active Redis cache entry for the given model. If one exists AND the
// account is not in failedAccountIDs, it returns that account — meaning
// the next chat for this model will reuse the existing session instead of
// creating a new one. Purely reactive: reads existing cache, writes nothing.
func (h *Handler) trySessionAffinity(ctx context.Context, model string, failedIDs []int64, quotaFilter func(*store.Account) bool) *store.Account {
	redisClient := h.loadBalancer.Store.RedisClient()
	if redisClient == nil {
		return nil
	}
	// Build the session-key prefix: RedisPrefix trimmed + ":codebuff"
	prefix := "codebuff"
	if h.config != nil && h.config.RedisPrefix != "" {
		prefix = strings.TrimSuffix(h.config.RedisPrefix, ":") + ":codebuff"
	}

	// Get all accounts, filter to codebuff only, scan sessions on-demand.
	all, err := h.loadBalancer.Store.ListAccounts(ctx)
	if err != nil || len(all) == 0 {
		return nil
	}

	// As a cheap pre-filter, compute token hashes and session keys for all
	// codebuff accounts, then pipeline all EXISTS commands in one round-trip.
	type candidate struct {
		account *store.Account
		key     string
		cmd     *redis.IntCmd
	}
	candidates := make([]candidate, 0, len(all))
	pipe := redisClient.Pipeline()
	for _, acc := range all {
		if acc == nil || !strings.EqualFold(acc.AccountType, "codebuff") {
			continue
		}
		token := codebuff.ResolveAuthToken(acc)
		if token == "" {
			continue
		}
		key := fmt.Sprintf("%s:session:%s:%s", prefix, codebuff.HashToken(token), model)
		candidates = append(candidates, candidate{account: acc, key: key, cmd: pipe.Exists(ctx, key)})
	}
	if len(candidates) == 0 {
		return nil
	}
	_, _ = pipe.Exec(ctx)

	// Walk candidates in account-ID order (stable). Return the first match
	// that is healthy and not in the failed set.
	for _, c := range candidates {
		n, _ := c.cmd.Result()
		if n == 0 {
			continue
		}
		// Skip accounts that were already rejected in the current retry loop.
		excluded := false
		for _, id := range failedIDs {
			if c.account.ID == id {
				excluded = true
				break
			}
		}
		if excluded {
			continue
		}
		if !c.account.Enabled {
			continue
		}
		// Quota-aware: skip accounts with active 429 block on this model.
		if quotaFilter != nil && !quotaFilter(c.account) {
			continue
		}
		return c.account
	}
	return nil
}

func firstString(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func (h *Handler) acquireTrackedAccount(acc *store.Account) int64 {
	if acc == nil || acc.ID == 0 {
		return 0
	}
	if h != nil && h.connTracker != nil {
		h.connTracker.Acquire(acc.ID)
		return acc.ID
	}
	if h != nil && h.loadBalancer != nil {
		h.loadBalancer.AcquireConnection(acc.ID)
		return acc.ID
	}
	return 0
}

func (h *Handler) releaseTrackedAccount(accountID int64) {
	if accountID == 0 {
		return
	}
	if h != nil && h.connTracker != nil {
		h.connTracker.Release(accountID)
		return
	}
	if h != nil && h.loadBalancer != nil {
		h.loadBalancer.ReleaseConnection(accountID)
	}
}

func (h *Handler) validateModelAvailability(ctx context.Context, modelID, forcedChannel string) (*store.Model, error) {
	if h == nil || h.loadBalancer == nil || h.loadBalancer.Store == nil {
		return nil, nil
	}
	modelID = strings.TrimSpace(modelID)
	if modelID == "" {
		return nil, nil
	}
	var m *store.Model
	var err error
	if forcedChannel != "" {
		m, err = h.loadBalancer.Store.GetModelByChannelAndModelID(ctx, forcedChannel, modelID)
	} else {
		m, err = h.loadBalancer.Store.GetModelByModelID(ctx, modelID)
	}
	if err != nil || m == nil {
		return nil, fmt.Errorf("model not found")
	}
	if !m.Status.Enabled() {
		return nil, fmt.Errorf("model not available")
	}
	if forcedChannel != "" {
		mChannel := strings.TrimSpace(m.Channel)
		if !sameModelChannel(mChannel, forcedChannel) {
			return nil, fmt.Errorf("model not found")
		}
	}
	return m, nil
}

func sameModelChannel(a, b string) bool {
	normalize := func(value string) string {
		value = strings.ToLower(strings.TrimSpace(value))
		value = strings.ReplaceAll(value, "_", "-")
		value = strings.ReplaceAll(value, " ", "-")
		return value
	}
	return normalize(a) == normalize(b)
}

func (h *Handler) updateAccountStats(account *store.Account, inputTokens, outputTokens int) {
	if account == nil || h.loadBalancer == nil || h.loadBalancer.Store == nil {
		return
	}
	go func(accountID int64, inputTokens, outputTokens int) {
		usage := float64(inputTokens + outputTokens)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := h.loadBalancer.Store.IncrementAccountStats(ctx, accountID, usage, 1); err != nil {
			slog.Error("Failed to update account stats", "account_id", accountID, "error", err)
		}
	}(account.ID, inputTokens, outputTokens)
}

func (h *Handler) syncWarpState(account *store.Account, client upstream.UpstreamClient, snapshot *store.Account) {
}

type upstreamErrorClass = apperrors.UpstreamErrorClass

func classifyUpstreamError(errStr string) upstreamErrorClass {
	return apperrors.ClassifyUpstreamError(errStr)
}

func computeRetryDelay(base time.Duration, attempt int, category string) time.Duration {
	if base <= 0 {
		return 0
	}
	if attempt < 1 {
		attempt = 1
	}
	if attempt > 4 {
		attempt = 4
	}
	delay := base * time.Duration(1<<(attempt-1))
	if category == "rate_limit" && delay < 2*time.Second {
		delay = 2 * time.Second
	}
	if delay > 30*time.Second {
		delay = 30 * time.Second
	}
	return delay
}

func shouldRetryCurrentAccountWhenNoAlternative(category string) bool {
	switch strings.TrimSpace(category) {
	case "network", "timeout", "server", "model_unavailable", "unknown":
		return true
	default:
		return false
	}
}
