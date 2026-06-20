package codebuff

import (
	"context"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"
)

// TelemetryStore records real-time per-account/per-model counters in Redis
// so the dashboard can show usage, 429s, and token counts without manually
// tracking them in memory.
type TelemetryStore struct {
	redis  *redis.Client
	prefix string
}

// NewTelemetryStore creates a telemetry store with a 24h TTL so that counters
// automatically reset alongside Codebuff's own daily quota reset (7 UTC).
func NewTelemetryStore(client *redis.Client, prefix string) *TelemetryStore {
	if prefix == "" {
		prefix = "codebuff"
	}
	return &TelemetryStore{
		redis:  client,
		prefix: prefix,
	}
}

func (ts *TelemetryStore) accountKey(accountID int64, model string) string {
	return fmt.Sprintf("%s:telemetry:%d:%s", ts.prefix, accountID, model)
}

// RecordRequest records the outcome of a single chat request to telemetry.
// is429 indicates a rate-limit response. Tokens/latency are optional.
// Pass latencyMs = end-start wall time for speed metrics.
func (ts *TelemetryStore) RecordRequest(ctx context.Context, accountID int64, model string, is429 bool, tokens int, latencyMs int64) {
	if ts == nil || ts.redis == nil || accountID == 0 || model == "" {
		return
	}
	key := ts.accountKey(accountID, model)
	ttl := 24 * time.Hour
	now := time.Now().Unix()

	pipe := ts.redis.TxPipeline()
	pipe.HIncrBy(ctx, key, "requests", 1)
	pipe.HIncrBy(ctx, key, "last_unix", now)
	if is429 {
		pipe.HIncrBy(ctx, key, "errors_429", 1)
	}
	if tokens > 0 {
		pipe.HIncrBy(ctx, key, "tokens", int64(tokens))
	}
	if latencyMs > 0 {
		pipe.HIncrBy(ctx, key, "latency_ms", latencyMs)
		// Wall-time spent serving requests for this model/account.
		pipe.HIncrBy(ctx, key, "wall_ms", latencyMs)
	}
	// First time we ever recorded an event for this model — capture window start.
	pipe.HSetNX(ctx, key, "first_unix", now)
	pipe.Expire(ctx, key, ttl)

	_, _ = pipe.Exec(ctx)
}

// ModelMetrics is the per-model telemetry summary returned to the dashboard.
type ModelMetrics struct {
	Requests     int64   `json:"requests"`
	Errors429    int64   `json:"errors_429"`
	Tokens       int64   `json:"tokens"`
	LatencyMs    int64   `json:"latency_ms"`
	AvgLatencyMs int64   `json:"avg_latency_ms"`
	TokensPerS   float64 `json:"tokens_per_s"`
	LastUsed     int64   `json:"last_used"`
	FirstUsed    int64   `json:"first_used"`
}

// AccountMetrics is the per-account telemetry summary returned to the dashboard.
type AccountMetrics struct {
	AccountID int64                    `json:"account_id"`
	Name      string                   `json:"name"`
	Total     ModelMetrics            `json:"total"`
	Models    map[string]ModelMetrics `json:"models"`
}

// GetAccountsMetrics reads all telemetry counters for the given codebuff
// account IDs and aggregates them per-account and per-model.
func (ts *TelemetryStore) GetAccountsMetrics(ctx context.Context, accounts []AccountRef) ([]AccountMetrics, error) {
	if ts == nil || ts.redis == nil {
		return []AccountMetrics{}, nil
	}

	out := make([]AccountMetrics, 0, len(accounts))

	for _, acc := range accounts {
		am := AccountMetrics{
			AccountID: acc.ID,
			Name:      acc.Name,
			Models:    make(map[string]ModelMetrics),
		}

		models := allModelIDs()
		total := ModelMetrics{}

		for _, m := range models {
			key := ts.accountKey(acc.ID, m)
			raw, err := ts.redis.HGetAll(ctx, key).Result()
			if err != nil || len(raw) == 0 {
				continue
			}
			mm := parseModelMetrics(raw)
			am.Models[m] = mm
			total.Requests += mm.Requests
			total.Errors429 += mm.Errors429
			total.Tokens += mm.Tokens
			total.LatencyMs += mm.LatencyMs
			if mm.LastUsed > total.LastUsed {
				total.LastUsed = mm.LastUsed
			}
			if total.FirstUsed == 0 || (mm.FirstUsed > 0 && mm.FirstUsed < total.FirstUsed) {
				total.FirstUsed = mm.FirstUsed
			}
		}

		total.AvgLatencyMs = avgLat(total.Requests, total.LatencyMs)
		total.TokensPerS = tokensPerSecond(total.Tokens, total.LatencyMs)
		am.Total = total
		out = append(out, am)
	}

	return out, nil
}

func parseModelMetrics(raw map[string]string) ModelMetrics {
	mm := ModelMetrics{
		Requests:  parseInt(raw["requests"]),
		Errors429: parseInt(raw["errors_429"]),
		Tokens:    parseInt(raw["tokens"]),
		LatencyMs: parseInt(raw["latency_ms"]),
		LastUsed:  parseInt(raw["last_unix"]),
		FirstUsed: parseInt(raw["first_unix"]),
	}
	mm.AvgLatencyMs = avgLat(mm.Requests, mm.LatencyMs)
	mm.TokensPerS = tokensPerSecond(mm.Tokens, mm.LatencyMs)
	return mm
}

func avgLat(reqs, latencyMs int64) int64 {
	if reqs == 0 {
		return 0
	}
	return latencyMs / reqs
}

func tokensPerSecond(tokens, latencyMs int64) float64 {
	if latencyMs == 0 {
		return 0
	}
	return float64(tokens) / (float64(latencyMs) / 1000.0)
}

// AccountRef is a minimal interface to look up account names/IDs.
type AccountRef struct {
	ID   int64
	Name string
}

func parseInt(s string) int64 {
	var n int64
	if s == "" {
		return 0
	}
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int64(c-'0')
	}
	return n
}
