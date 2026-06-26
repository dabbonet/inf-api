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
// isError marks any non-429 failure (network, upstream 5xx, decode); the
// errors_total counter is incremented for failure-rate calculations.
func (ts *TelemetryStore) RecordRequest(ctx context.Context, accountID int64, model string, is429 bool, isError bool, tokens int, latencyMs int64) {
	if ts == nil || ts.redis == nil || accountID == 0 || model == "" {
		return
	}
	now := time.Now().Unix()

	// Per-minute bucket counters — feed Feature 1 / time-range scoped metrics.
	// Each bucket is a Redis hash with fields requests, errors_429, errors_total,
	// tokens, latency_ms, wall_ms. 24h TTL covers the longest available range
	// button ("Today"); older buckets naturally roll forward and are pruned by
	// Redis once the TTL elapses.
	bucketKey := ts.bucketKey(accountID, model, now)
	tokensI := int64(tokens)
	pipe := ts.redis.TxPipeline()
	pipe.HIncrBy(ctx, bucketKey, "requests", 1)
	if is429 {
		pipe.HIncrBy(ctx, bucketKey, "errors_429", 1)
	}
	if isError {
		pipe.HIncrBy(ctx, bucketKey, "errors_total", 1)
	}
	if tokens > 0 {
		pipe.HIncrBy(ctx, bucketKey, "tokens", tokensI)
	}
	if latencyMs > 0 {
		pipe.HIncrBy(ctx, bucketKey, "latency_ms", latencyMs)
		pipe.HIncrBy(ctx, bucketKey, "wall_ms", latencyMs)
	}
	pipe.Expire(ctx, bucketKey, 24*time.Hour)

	// Aggregate lifetime counters. first_unix remains HSetNX'd so lifetime
	// averages stay meaningful on the cards that still use them.
	key := ts.accountKey(accountID, model)
	pipe.HIncrBy(ctx, key, "requests", 1)
	pipe.HIncrBy(ctx, key, "last_unix", now)
	if is429 {
		pipe.HIncrBy(ctx, key, "errors_429", 1)
	}
	if isError {
		pipe.HIncrBy(ctx, key, "errors_total", 1)
	}
	if tokens > 0 {
		pipe.HIncrBy(ctx, key, "tokens", tokensI)
	}
	if latencyMs > 0 {
		pipe.HIncrBy(ctx, key, "latency_ms", latencyMs)
		pipe.HIncrBy(ctx, key, "wall_ms", latencyMs)
	}
	pipe.HSetNX(ctx, key, "first_unix", now)
	pipe.Expire(ctx, key, 24*time.Hour)

	// Rolling-RPM sorted set: score == unix-second of the request, member == request id.
	// Backed by timestamp entries that count the requests in the last 60 seconds
	// and that get garbage-collected on every read (ZREMRANGEBYSCORE).
	pipe.ZAdd(ctx, ts.timestampKey(accountID), redis.Z{Score: float64(now), Member: fmt.Sprintf("%d-%d", now, accountID)})
	pipe.ZRemRangeByScore(ctx, ts.timestampKey(accountID), "-inf", fmt.Sprintf("(%d", now-120))
	pipe.Expire(ctx, ts.timestampKey(accountID), 2*time.Hour)

	// Per-account daily request counter — used by handleCodebuffSync as an
	// authoritative fallback when upstream's rateLimitsByModel is stale or
	// missing from the GET /api/v1/freebuff/session response. 48h TTL covers
	// the current Pacific-day window plus one full day of grace for clock skew.
	today := nowDateUTC(now)
	pipe.Incr(ctx, ts.dailyKey(accountID, today))
	pipe.Expire(ctx, ts.dailyKey(accountID, today), 48*time.Hour)

	_, _ = pipe.Exec(ctx)
}

// timestampKey is the Redis sorted-set key holding per-second request timestamps
// for a given codebuff account, used to compute rolling RPM.
func (ts *TelemetryStore) timestampKey(accountID int64) string {
	return fmt.Sprintf("%s:telemetry:times:%d", ts.prefix, accountID)
}

// dailyKey is the Redis key holding today's request count for a given
// codebuff account, used as the fallback for QuotaSync freshness.
func (ts *TelemetryStore) dailyKey(accountID int64, day string) string {
	return fmt.Sprintf("%s:telemetry:daily:%d:%s", ts.prefix, accountID, day)
}

// bucketKey is the Redis hash key holding one minute's worth of counters
// for a given (account, model) pair. Used by time-range UI to scope stats.
func (ts *TelemetryStore) bucketKey(accountID int64, model string, unixSec int64) string {
	minute := unixSec - (unixSec % 60)
	return fmt.Sprintf("%s:telemetry:bucket:%d:%s:%d", ts.prefix, accountID, model, minute)
}

// DailyRequests returns today's proxied request count for the given account
// (UTC date). Used by handleCodebuffSync as an authoritative daily figure
// independent of upstream rate-limit responses.
func (ts *TelemetryStore) DailyRequests(ctx context.Context, accountID int64) (int64, error) {
	if ts == nil || ts.redis == nil || accountID == 0 {
		return 0, nil
	}
	v, err := ts.redis.Get(ctx, ts.dailyKey(accountID, nowDateUTC(time.Now().Unix()))).Int64()
	if err == redis.Nil {
		return 0, nil
	}
	return v, err
}

// nowDateUTC returns YYYY-MM-DD in UTC for the given unix-second. Extracted as
// a tiny helper so the date string is stable across calls within the same
// second for a given request.
func nowDateUTC(unixSec int64) string {
	return time.Unix(unixSec, 0).UTC().Format("2006-01-02")
}

// ModelMetrics is the per-model telemetry summary returned to the dashboard.
type ModelMetrics struct {
	Requests     int64   `json:"requests"`
	Errors429    int64   `json:"errors_429"`
	ErrorsTotal  int64   `json:"errors_total"`
	Tokens       int64   `json:"tokens"`
	LatencyMs    int64   `json:"latency_ms"`
	WallMs       int64   `json:"wall_ms"`
	AvgLatencyMs int64   `json:"avg_latency_ms"`
	TokensPerS   float64 `json:"tokens_per_s"`
	LastUsed     int64   `json:"last_used"`
	FirstUsed    int64   `json:"first_used"`
	RPM          float64 `json:"rpm"` // requests served in the last 60s
}

// AccountMetrics is the per-account telemetry summary returned to the dashboard.
type AccountMetrics struct {
	AccountID int64                    `json:"account_id"`
	Name      string                   `json:"name"`
	Total     ModelMetrics             `json:"total"`
	Models    map[string]ModelMetrics  `json:"models"`
	RPM       float64                  `json:"rpm"` // requests served in the last 60s by the account
}

// GetAccountsMetrics reads all telemetry counters for the given codebuff
// account IDs and aggregates them per-account and per-model.
//
// The returned RPM fields are computed from a Redis sorted set of recent
// request timestamps, not from the lifetime first_unix/last_unix window,
// so they stay accurate even as the underlying TTL rolls over.
//
// When `RangeSeconds` is set, the aggregates are computed by summing the
// per-minute buckets in the window (Feature 1) and lifetime aggregates
// are skipped. When RangeSeconds is 0, the lifetime aggregate hash is
// returned and RPM comes from the rolling set.
func (ts *TelemetryStore) GetAccountsMetrics(ctx context.Context, accounts []AccountRef) ([]AccountMetrics, error) {
	return ts.GetAccountsMetricsInRange(ctx, accounts, 0)
}

// GetAccountsMetricsInRange is the entrypoint respecting Feature 1's
// ?range= query parameter. RangeSeconds == 0 is "all-time" (legacy path).
// RangeSeconds > 0 sums the per-minute buckets in [now-RangeSeconds, now].
func (ts *TelemetryStore) GetAccountsMetricsInRange(ctx context.Context, accounts []AccountRef, rangeSeconds int64) ([]AccountMetrics, error) {
	if ts == nil || ts.redis == nil {
		return []AccountMetrics{}, nil
	}

	out := make([]AccountMetrics, 0, len(accounts))
	models := allModelIDs()

	for _, acc := range accounts {
		am := AccountMetrics{
			AccountID: acc.ID,
			Name:      acc.Name,
			Models:    make(map[string]ModelMetrics),
		}

		total := ModelMetrics{}

		for _, m := range models {
			var mm ModelMetrics
			if rangeSeconds > 0 {
				var err error
				mm, err = ts.sumBucketsForRange(ctx, acc.ID, m, rangeSeconds)
				if err != nil || (mm.Requests == 0 && mm.Errors429 == 0 && mm.Tokens == 0) {
					continue
				}
			} else {
				key := ts.accountKey(acc.ID, m)
				raw, err := ts.redis.HGetAll(ctx, key).Result()
				if err != nil || len(raw) == 0 {
					continue
				}
				mm = parseModelMetrics(raw)
			}

			am.Models[m] = mm
			total.Requests += mm.Requests
			total.Errors429 += mm.Errors429
			total.ErrorsTotal += mm.ErrorsTotal
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

		// Compute rolling 60s RPM from sorted-set timestamps. Bucket-scoped
		// queries still expose the rolling RPM since per-minute buckets would
		// lag a 60-second window.
		rpm, err := ts.RollingRPM(ctx, acc.ID, 60)
		if err == nil {
			am.RPM = rpm
		}

		out = append(out, am)
	}

	return out, nil
}

// sumBucketsForRange sums the per-minute bucket counters for the given
// (account, model) pair in the last `rangeSeconds` seconds. Uses pipelining
// so a 24h range is at most 1440 HGETALL calls in one round-trip.
func (ts *TelemetryStore) sumBucketsForRange(ctx context.Context, accountID int64, model string, rangeSeconds int64) (ModelMetrics, error) {
	if rangeSeconds <= 0 {
		return ModelMetrics{}, nil
	}
	now := time.Now().Unix()
	startMinute := (now - rangeSeconds) - ((now - rangeSeconds) % 60)
	endMinute := now - (now % 60)

	pipe := ts.redis.Pipeline()
	cmds := make([]*redis.MapStringStringCmd, 0, (endMinute-startMinute)/60+1)
	for m := startMinute; m <= endMinute; m += 60 {
		key := ts.bucketKey(accountID, model, m)
		cmds = append(cmds, pipe.HGetAll(ctx, key))
	}
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return ModelMetrics{}, err
	}

	out := ModelMetrics{}
	for _, c := range cmds {
		v, err := c.Result()
		if err != nil || len(v) == 0 {
			continue
		}
		out.Requests += parseInt(v["requests"])
		out.Errors429 += parseInt(v["errors_429"])
		out.ErrorsTotal += parseInt(v["errors_total"])
		out.Tokens += parseInt(v["tokens"])
		out.LatencyMs += parseInt(v["latency_ms"])
		out.WallMs += parseInt(v["wall_ms"])
		if last := parseInt(v["last_unix"]); last > out.LastUsed {
			out.LastUsed = last
		}
	}
	out.AvgLatencyMs = avgLat(out.Requests, out.LatencyMs)
	out.TokensPerS = tokensPerSecond(out.Tokens, out.LatencyMs)
	// Range-scoped metrics are not "lifetime" — RPM is computed by the
	// caller from the rolling set; FirstUsed is left as 0 for the UI.
	return out, nil
}

// RollingRPM returns the number of requests served by the given account in
// the last `windowSeconds` seconds, computed from the per-second timestamp
// sorted set. The sorted set itself is pruned to discard entries older than
// 120s on every observation, keeping it small in steady state.
func (ts *TelemetryStore) RollingRPM(ctx context.Context, accountID int64, windowSeconds int) (float64, error) {
	if ts == nil || ts.redis == nil || accountID == 0 {
		return 0, nil
	}
	now := time.Now().Unix()
	from := fmt.Sprintf("(%d", now-int64(windowSeconds))
	to := fmt.Sprintf("%d", now)

	// ZCOUNT is O(log N); also opportunistically prune stale timestamps.
	if _, err := ts.redis.ZRemRangeByScore(ctx, ts.timestampKey(accountID), "-inf", fmt.Sprintf("(%d", now-120)).Result(); err != nil {
		// Best-effort; nuke and continue on error.
		_ = ts.redis.Del(ctx, ts.timestampKey(accountID)).Err()
	}
	n, err := ts.redis.ZCount(ctx, ts.timestampKey(accountID), from, to).Result()
	if err != nil {
		return 0, err
	}
	if windowSeconds <= 0 {
		return 0, nil
	}
	return float64(n) / (float64(windowSeconds) / 60.0), nil
}

func parseModelMetrics(raw map[string]string) ModelMetrics {
	mm := ModelMetrics{
		Requests:    parseInt(raw["requests"]),
		Errors429:   parseInt(raw["errors_429"]),
		ErrorsTotal: parseInt(raw["errors_total"]),
		Tokens:      parseInt(raw["tokens"]),
		LatencyMs:   parseInt(raw["latency_ms"]),
		LastUsed:    parseInt(raw["last_unix"]),
		FirstUsed:   parseInt(raw["first_unix"]),
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
