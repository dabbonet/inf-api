package codebuff

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"orchids-api/internal/store"
)

// ModelBlock records a 429 block for a specific account+model.
type ModelBlock struct {
	Model        string    `json:"model"`
	Limit        int       `json:"limit"`
	RecentCount  int       `json:"recentCount"`
	ResetAt      time.Time `json:"resetAt"`
	BlockedAt    time.Time `json:"blockedAt"`
	RetryAfterMs int       `json:"retryAfterMs"`
}

// ModelRateLimit records quota info from a session's rateLimitsByModel.
type ModelRateLimit struct {
	Limit     int       `json:"limit"`
	Remaining int       `json:"remaining"`
	ResetAt   time.Time `json:"resetAt"`
	Window    string    `json:"window"`
}

// StreakInfo holds upstream streak data.
type StreakInfo struct {
	StreakDays int       `json:"streakDays"`
	SyncedAt   time.Time `json:"syncedAt"`
}

// SessionQuotaInfo holds parsed rateLimitsByModel from session creation.
type SessionQuotaInfo struct {
	RateLimitsByModel map[string]ModelRateLimit `json:"rateLimitsByModel"`
	SyncedAt          time.Time                 `json:"syncedAt"`
}

// AccountQuotaStatus is the unified response for a single account.
type AccountQuotaStatus struct {
	AccountID int64             `json:"account_id"`
	Name      string            `json:"name"`
	Streak    *StreakInfo       `json:"streak,omitempty"`
	Session   *SessionQuotaInfo `json:"session,omitempty"`
	Blocks    []ModelBlock      `json:"blocks"`
}

// ModelPoolStatus is the per-model cell in the pool matrix.
type ModelPoolStatus struct {
	Blocked     bool       `json:"blocked"`
	Limit       int        `json:"limit"`
	RecentCount int        `json:"recent_count"`
	Remaining   int        `json:"remaining"`
	ResetAt     time.Time  `json:"reset_at"`
	BlockedAt   *time.Time `json:"blocked_at,omitempty"`
}

// PoolStatus is the response for the pool-status endpoint.
type PoolStatus struct {
	Accounts  []AccountPoolStatus `json:"accounts"`
	AllModels []string            `json:"all_models"`
}

// AccountPoolStatus is the per-account row in the pool matrix.
type AccountPoolStatus struct {
	AccountID int64                      `json:"account_id"`
	Name      string                     `json:"name"`
	Models    map[string]ModelPoolStatus `json:"models"`
}

// QuotaStore manages Redis-backed per-model quota state.
type QuotaStore struct {
	redis  *redis.Client
	prefix string
}

// NewQuotaStore creates a quota store with the given Redis client and key prefix.
func NewQuotaStore(client *redis.Client, prefix string) *QuotaStore {
	if prefix == "" {
		prefix = "codebuff"
	}
	return &QuotaStore{
		redis:  client,
		prefix: prefix,
	}
}

func (qs *QuotaStore) blockKey(accountID int64, model string) string {
	return fmt.Sprintf("%s:quota:block:%d:%s", qs.prefix, accountID, model)
}

func (qs *QuotaStore) streakKey(accountID int64) string {
	return fmt.Sprintf("%s:quota:streak:%d", qs.prefix, accountID)
}

func (qs *QuotaStore) sessionKey(accountID int64) string {
	return fmt.Sprintf("%s:quota:session:%d", qs.prefix, accountID)
}

// IsBlocked returns true if the (account, model) has an active 429 block
// stored in Redis. Block keys auto-expire at resetAt+1min, so a missing key
// means quota is or will be available. Single GET per check; pipeline
// candidates for batch lookups in the request path.
func (qs *QuotaStore) IsBlocked(ctx context.Context, accountID int64, model string) (bool, error) {
	if qs == nil {
		return false, nil
	}
	n, err := qs.redis.Exists(ctx, qs.blockKey(accountID, model)).Result()
	if err != nil {
		return false, err
	}
	return n > 0, nil
}

// RecordBlock stores a 429 block with TTL until resetAt.
func (qs *QuotaStore) RecordBlock(ctx context.Context, accountID int64, block *ModelBlock) error {
	if qs == nil || block == nil {
		return nil
	}
	key := qs.blockKey(accountID, block.Model)
	raw, err := json.Marshal(block)
	if err != nil {
		return err
	}
	ttl := qs.blockTTL(block.ResetAt)
	if err := qs.redis.Set(ctx, key, raw, ttl).Err(); err != nil {
		return err
	}
	slog.Debug("Recorded codebuff model block", "account_id", accountID, "model", block.Model, "ttl", ttl)
	return nil
}

func (qs *QuotaStore) blockTTL(resetAt time.Time) time.Duration {
	if resetAt.IsZero() {
		return 24 * time.Hour
	}
	d := resetAt.Sub(time.Now().UTC())
	if d <= 0 {
		return time.Minute
	}
	return d + time.Minute
}

// RecordStreak stores streak info.
func (qs *QuotaStore) RecordStreak(ctx context.Context, accountID int64, streak *StreakInfo) error {
	if qs == nil || streak == nil {
		return nil
	}
	key := qs.streakKey(accountID)
	raw, err := json.Marshal(streak)
	if err != nil {
		return err
	}
	return qs.redis.Set(ctx, key, raw, 7*24*time.Hour).Err()
}

// RecordSessionQuotas stores rateLimitsByModel from a session response.
func (qs *QuotaStore) RecordSessionQuotas(ctx context.Context, accountID int64, limits map[string]ModelRateLimit) error {
	if qs == nil || len(limits) == 0 {
		return nil
	}
	info := SessionQuotaInfo{
		RateLimitsByModel: limits,
		SyncedAt:          time.Now().UTC(),
	}
	key := qs.sessionKey(accountID)
	raw, err := json.Marshal(info)
	if err != nil {
		return err
	}
	return qs.redis.Set(ctx, key, raw, 7*24*time.Hour).Err()
}

// GetAccountStatus returns the full quota status for one account.
func (qs *QuotaStore) GetAccountStatus(ctx context.Context, accountID int64, name string) (*AccountQuotaStatus, error) {
	if qs == nil {
		return &AccountQuotaStatus{AccountID: accountID, Name: name}, nil
	}
	status := &AccountQuotaStatus{
		AccountID: accountID,
		Name:      name,
		Blocks:    []ModelBlock{},
	}

	// Streak
	streakData, err := qs.redis.Get(ctx, qs.streakKey(accountID)).Result()
	if err == nil {
		var streak StreakInfo
		if err := json.Unmarshal([]byte(streakData), &streak); err == nil {
			status.Streak = &streak
		}
	}

	// Session quotas
	sessData, err := qs.redis.Get(ctx, qs.sessionKey(accountID)).Result()
	if err == nil {
		var sess SessionQuotaInfo
		if err := json.Unmarshal([]byte(sessData), &sess); err == nil {
			status.Session = &sess
		}
	}

	// Blocks - scan for block keys
	pattern := qs.blockKey(accountID, "*")
	iter := qs.redis.Scan(ctx, 0, pattern, 100).Iterator()
	for iter.Next(ctx) {
		raw, err := qs.redis.Get(ctx, iter.Val()).Result()
		if err != nil {
			continue
		}
		var block ModelBlock
		if err := json.Unmarshal([]byte(raw), &block); err == nil {
			status.Blocks = append(status.Blocks, block)
		}
	}
	if err := iter.Err(); err != nil {
		return nil, err
	}

	return status, nil
}

// GetPoolStatus returns the matrix of all accounts x all models.
func (qs *QuotaStore) GetPoolStatus(ctx context.Context, accounts []*store.Account) (*PoolStatus, error) {
	if qs == nil {
		return &PoolStatus{AllModels: allModelIDs()}, nil
	}

	allModels := allModelIDs()
	result := PoolStatus{
		AllModels: allModels,
		Accounts:  make([]AccountPoolStatus, 0, len(accounts)),
	}

	for _, acc := range accounts {
		if acc == nil || !strings.EqualFold(acc.AccountType, "codebuff") {
			continue
		}
		accountStatus := AccountPoolStatus{
			AccountID: acc.ID,
			Name:      acc.Name,
			Models:    make(map[string]ModelPoolStatus),
		}

		// Fetch session quotas
		sessionData, _ := qs.redis.Get(ctx, qs.sessionKey(acc.ID)).Result()
		var sessionInfo *SessionQuotaInfo
		if sessionData != "" {
			var si SessionQuotaInfo
			if err := json.Unmarshal([]byte(sessionData), &si); err == nil {
				sessionInfo = &si
			}
		}

		// Fetch all blocks for this account
		blocks := make(map[string]ModelBlock)
		pattern := qs.blockKey(acc.ID, "*")
		iter := qs.redis.Scan(ctx, 0, pattern, 100).Iterator()
		for iter.Next(ctx) {
			raw, err := qs.redis.Get(ctx, iter.Val()).Result()
			if err != nil {
				continue
			}
			var block ModelBlock
			if err := json.Unmarshal([]byte(raw), &block); err == nil {
				blocks[block.Model] = block
			}
		}

		for _, model := range allModels {
			cell := ModelPoolStatus{
				Blocked:   false,
				Limit:     5,
				Remaining: 5, // assume fully available until proven otherwise
			}
		if sessionInfo != nil {
			if rl, ok := sessionInfo.RateLimitsByModel[model]; ok {
				cell.Limit = rl.Limit
				if rl.Limit > 0 {
					cell.Remaining = rl.Remaining
				}
				cell.ResetAt = rl.ResetAt
				// If the upstream reset window has already passed AND
				// remaining is stuck at zero, auto-recover to limit.
				// This fixes stale quota data between the moment
				// 07:00 UTC passes and the next session create.
				if cell.Remaining == 0 && !cell.ResetAt.IsZero() && time.Now().UTC().After(cell.ResetAt) {
					cell.Remaining = cell.Limit
				}
			}
		}
			if block, ok := blocks[model]; ok {
				cell.Blocked = true
				cell.RecentCount = block.RecentCount
				if block.Limit > 0 {
					cell.Limit = block.Limit
				}
				cell.ResetAt = block.ResetAt
				cell.BlockedAt = &block.BlockedAt
				cell.Remaining = 0
			}
			accountStatus.Models[model] = cell
		}

		result.Accounts = append(result.Accounts, accountStatus)
	}

	return &result, nil
}

// ClearQuotaResetData removes block keys and session quota data at the
// daily 07:00 UTC reset. Streak keys are preserved (they track usage
// across days). One scan of codebuff:quota:*, delete everything except
// codebuff:quota:streak:*.
func (qs *QuotaStore) ClearQuotaResetData(ctx context.Context) error {
	if qs == nil {
		return nil
	}
	pattern := fmt.Sprintf("%s:quota:*", qs.prefix)
	iter := qs.redis.Scan(ctx, 0, pattern, 1000).Iterator()
	streakPrefix := fmt.Sprintf("%s:quota:streak:", qs.prefix)
	var keys []string
	for iter.Next(ctx) {
		key := iter.Val()
		if !strings.HasPrefix(key, streakPrefix) {
			keys = append(keys, key)
		}
	}
	if err := iter.Err(); err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}
	const batch = 100
	for i := 0; i < len(keys); i += batch {
		end := i + batch
		if end > len(keys) {
			end = len(keys)
		}
		if err := qs.redis.Del(ctx, keys[i:end]...).Err(); err != nil {
			return err
		}
	}
	slog.Info("Cleared codebuff quota reset data", "count", len(keys))
	return nil
}

func allModelIDs() []string {
	ids := make([]string, 0, len(ALL_MODELS))
	for _, m := range ALL_MODELS {
		ids = append(ids, m.ID)
	}
	return ids
}

// Parse429Body extracts model block info from a codebuff 429 error.
func Parse429Body(err error) (*ModelBlock, error) {
	if err == nil {
		return nil, nil
	}
	msg := err.Error()
	start := strings.Index(msg, "{")
	if start == -1 {
		return nil, fmt.Errorf("no JSON in error")
	}
	decoder := json.NewDecoder(strings.NewReader(msg[start:]))
	var payload map[string]any
	if decErr := decoder.Decode(&payload); decErr != nil {
		return nil, fmt.Errorf("failed to decode 429 JSON: %w", decErr)
	}

	block := &ModelBlock{
		BlockedAt: time.Now().UTC(),
	}
	if v, ok := payload["model"].(string); ok {
		block.Model = v
	}
	if v, ok := payload["limit"].(float64); ok {
		block.Limit = int(v)
	}
	if v, ok := payload["recentCount"].(float64); ok {
		block.RecentCount = int(v)
	}
	if v, ok := payload["retryAfterMs"].(float64); ok {
		block.RetryAfterMs = int(v)
	}
	if v, ok := payload["resetAt"]; ok {
		if t, perr := parseTime(v); perr == nil {
			block.ResetAt = t
		}
	}
	if block.Model == "" {
		return nil, fmt.Errorf("model missing in 429 payload")
	}
	return block, nil
}

// ParseSessionRateLimits extracts rateLimitsByModel from a session response.
func ParseSessionRateLimits(data map[string]any) (map[string]ModelRateLimit, error) {
	if data == nil {
		return nil, nil
	}
	raw, ok := data["rateLimitsByModel"].(map[string]any)
	if !ok {
		return nil, nil
	}
	result := make(map[string]ModelRateLimit)
	for model, v := range raw {
		mraw, ok := v.(map[string]any)
		if !ok {
			continue
		}
		var mr ModelRateLimit
		if limit, ok := mraw["limit"].(float64); ok {
			mr.Limit = int(limit)
		}
		if remaining, ok := mraw["remaining"].(float64); ok {
			mr.Remaining = int(remaining)
		}
		if window, ok := mraw["window"].(string); ok {
			mr.Window = window
		}
		if resetAt, ok := mraw["resetAt"]; ok {
			if t, err := parseTime(resetAt); err == nil {
				mr.ResetAt = t
			}
		}
		result[model] = mr
	}
	return result, nil
}

// ParseStreak extracts streak info from GetStreak response.
func ParseStreak(data map[string]any) *StreakInfo {
	if data == nil {
		return nil
	}
	streak := &StreakInfo{SyncedAt: time.Now().UTC()}
	if v, ok := data["streakDays"].(float64); ok {
		streak.StreakDays = int(v)
	}
	if v, ok := data["streakDays"].(int); ok {
		streak.StreakDays = v
	}
	return streak
}

func parseTime(v any) (time.Time, error) {
	switch t := v.(type) {
	case string:
		layouts := []string{
			time.RFC3339,
			time.RFC3339Nano,
			"2006-01-02T15:04:05Z",
			"2006-01-02T15:04:05.000Z",
		}
		for _, layout := range layouts {
			if parsed, err := time.Parse(layout, t); err == nil {
				return parsed.UTC(), nil
			}
		}
		return time.Time{}, fmt.Errorf("unrecognized time string: %s", t)
	case float64:
		return time.Unix(int64(t), 0).UTC(), nil
	case int64:
		return time.Unix(t, 0).UTC(), nil
	case int:
		return time.Unix(int64(t), 0).UTC(), nil
	}
	return time.Time{}, fmt.Errorf("unsupported time type %T", v)
}
