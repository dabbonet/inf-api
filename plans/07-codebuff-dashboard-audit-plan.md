# Plan 07: Codebuff Telemetry Dashboard — Full Audit & Improvement Plan

**Status:** PLANNED
**Date:** 2026-06-26

## Overview

The Codebuff telemetry dashboard (`/admin/?tab=codebuff`) is a rich card-based UI showing per-account quota, requests, 429s, tokens, latency, and throughput. The UI design is excellent — vertically stacked cards, inline progress bars, countdown timer, model detail rows. However, 3 bugs produce incorrect data and several impactful features are missing. This plan audits every layer of the telemetry pipeline and prioritizes fixes + enhancements.

## Pipeline map

```
provider.go              telemetry.go          codebuff_metrics.go    codebuff.js
(recordTelemetry)  ──→   (Redis hash fields)  ──→  GET /api/codebuff  ──→  renderStats()
RecordSessionQuotas ──→   quotastore.go        ──→  /metrics            ──→  renderCards()
                                                     ──→  GET /api/codebuff  ──→  renderCards()
recordBlockIf429    ──→                           ──→  /pool-status
handleCodebuffSync  ──→  quotastore.go +         ──→  POST /accounts/   ──→  syncQuota()
                          client.GetSession/             {id}/codebuff-sync
                          GetStreak
```

---

## BUGS (must fix)

### Bug 1: Tokens always 0 on streaming paths

**Severity:** HIGH — the dominant traffic path never records token usage.

**Root cause:** `provider.go` streaming paths call `recordTelemetry()` with hardcoded `tokens=0`:

| Path | Line | `is429` | `tokens` |
|------|------|---------|----------|
| `streamChat` on error | 228 | `true` | `0` |
| `streamChat` on stream error | 245 | `true` | `0` |
| `streamChat` on `model.finish` | 254 | `false` | `0` |
| `streamChat` on EOF | 260 | `false` | `0` |
| `streamChatRaw` on error | 279 | `true` | `0` |
| `streamChatRaw` on done | 325 | `false` | `0` |
| `completeChat` on success | 367 | `false` | `resp.Usage.TotalTokens` ✅ |

Only `completeChat` (non-streaming, rare path) passes real token counts. Streaming is 99%+ of traffic → tokens in the dashboard are effectively always 0.

**Fix:** Use the existing `internal/tiktoken/tokenizer.go` (`Estimator`) to accumulate token estimates from SSE text/content deltas in streaming paths:

1. In `streamChat`: before the parse loop, create an `Estimator`. On each SSE chunk with text delta, call `estimator.Add(deltaText)`. When done, pass `estimator.Count()` to `recordTelemetry`.
2. In `streamChatRaw`: track accumulated text from `data:` chunks containing `"delta"` or `"content"`. Use `estimator.AddBytes()` for the raw SSE data chunks, or better, JSON-decode and extract text fields.
3. The `Estimator` produces approximate (~90-95% accurate) token counts — far better than 0.
4. Optionally: extract `usage` from the final SSE chunk if Codebuff sends it (check during Phase 1 investigation).

**Files to change:** `internal/codebuff/provider.go`

---

### Bug 2: RPM is a lifetime average, not a rolling window

**Severity:** HIGH — RPM decays to near-zero as time passes.

**Root cause:** Two places compute RPM identically — both use `(last_unix - first_unix)` as the denominator:

- `totalAcross()` in `codebuff.js:60`: `rpm = reqs / ((newest - oldest) / 60000)`
- `buildCard()` in `codebuff.js:154-155`: `accRpm = reqs / ((last_used - first_used) / 60000)`

`first_unix` is set once via `HSETNX` in `telemetry.go:61` and never resets within the 24h TTL. This is a lifetime average:
- Hour 1: RPM ≈ actual rate ✅
- Hour 6: RPM ≈ actual/6
- Hour 24: RPM ≈ actual/24
- After 30 days: RPM ≈ 0 regardless of activity

**Fix:** Replace with a true rolling 60-second window:

**Backend (Go):**
1. Store per-request timestamps in a Redis sorted set: `ZADD telemetry:times:<account_id> <now_unix> <unique_request_id>`.
2. On `GetAccountsMetrics`, compute rolling RPM: `ZCOUNT telemetry:times:<account_id> (now-60) now`.
3. Cleanup: `ZREMRANGEBYSCORE telemetry:times:<account_id> -inf (now-120)` to keep the set small.
4. Add `rpm` field to `ModelMetrics` + `AccountMetrics` structs.

**Frontend (JS):**
1. Use server-provided `rpm` field instead of client-side division.
2. Remove the `first_unix` / `last_unix` denominator math from `totalAcross()` and `buildCard()`.

**Alternative (simpler, less accurate):** Store minute-bucket counters: `HINCRBY telemetry:bucket:<account_id> <YYYYMMDDHHMM> 1` with 2h TTL. On poll, sum the current minute's bucket. Slightly coarser but much simpler.

**Files to change:** `internal/codebuff/telemetry.go`, `web/static/js/codebuff.js`

---

### Bug 3: Sync quota data may be stale or incomplete

**Severity:** MEDIUM — quota bars may not reflect reality.

**Root cause:** `handleCodebuffSync` (`codebuff_quota.go:35-69`) calls:
1. `client.GetStreak()` — `/api/v1/freebuff/streak` → stores streak days only.
2. `client.GetSession(ctx, "")` — `/api/v1/freebuff/session` GET → parses `rateLimitsByModel`.

Issue: the GET session endpoint may return a different shape than the POST/create session response. The `rateLimitsByModel` field is known to be present in POST responses (session creation) but may be absent or differently structured in GET responses (existing session retrieval).

Additionally, `GetSession` on the current active session may return stale data if `EnsureSession` (in `session.go`) reused a cached session from Redis rather than fetching fresh from upstream.

**Investigation needed (Phase 1):**
1. Dump the raw JSON from `GET /api/v1/freebuff/session` during sync.
2. Dump the raw JSON from `GET /api/v1/freebuff/streak`.
3. Compare against POST session creation response.
4. Check for fields we're not parsing: `remainingCallsToday`, `dailyUsage`, `quota`, `usageToday`.

**Likely fix (depends on investigation):**
- If `rateLimitsByModel` is present in GET: no fix needed, data is fresh on each session refresh cycle.
- If absent in GET: store quota data from the POST/create response instead (already captured in `recordSessionQuotas` in `provider.go:147-158`). Use that as the authoritative quota source.
- Add a self-counting daily request counter as backup: `INCR codebuff:daily:requests:<account_id>:<YYYY-MM-DD>` with 48h TTL on every proxied request.

**Files to change:** `internal/api/codebuff_quota.go`, `internal/codebuff/quotastore.go` (possibly)

---

## FEATURES

### Feature 1: Time-range filters (1h / 6h / 24h / today)

**Value:** HIGH — currently all stats are "lifetime since first request."

**Design:**
- Add a filter bar with buttons: `1h` | `6h` | `24h` | `Today` | `All`
- On selection, re-fetch metrics with a `?range=1h` query param.
- Backend computes stats scoped to the time window.

**Implementation:**
1. Store per-minute bucket counters in Redis:
   ```
   HINCRBY telemetry:bucket:<account_id>:<model>:<unix_minute> requests 1
   HINCRBY telemetry:bucket:<account_id>:<model>:<unix_minute> tokens <n>
   HINCRBY telemetry:bucket:<account_id>:<model>:<unix_minute> latency_ms <n>
   EXPIRE telemetry:bucket:<account_id>:<model>:<unix_minute> 86400
   ```
2. `GetAccountsMetrics` accepts optional `range` parameter.
3. When `range` is set, scan minute buckets in the window, sum them.
4. When `range` is `all` (default), fall back to existing `HGETALL` on the aggregate hash.
5. Frontend tracks selected range, passes it in the fetch URL.
6. RPM computed server-side from sorted-set timestamps within the window.

**Files to change:** `internal/codebuff/telemetry.go`, `internal/api/codebuff_metrics.go`, `web/static/js/codebuff.js`, `web/templates/pages/codebuff.html`

---

### Feature 2: Auto quota sync (periodic + on first load)

**Value:** MEDIUM — currently sync is manual button-click only.

**Design:**
- On page load, auto-trigger `syncQuota()` (after 1s delay to let initial data load).
- Every 5 minutes, auto-sync in background.
- Show "Last synced: 3m ago" below the Sync button.
- Keep manual sync button for on-demand refreshes.

**Implementation:**
1. In `codebuff.js`, add `setInterval(() => syncQuota(), 300000)` alongside the existing 30s polling.
2. On `DOMContentLoaded` or after first `loadCodebuffData()`, call `syncQuota()` with a 1s delay.
3. Track `lastSyncTime` in JS, display in a new `<span>` next to the sync button.

**Files to change:** `web/static/js/codebuff.js`, `web/templates/pages/codebuff.html`

---

### Feature 3: Per-model token breakdown

**Value:** MEDIUM — model detail rows show requests and 429s but not tokens.

**Design:**
- Add a "Tokens" column to the model detail table (between "429s" and "T/s").
- Server already returns per-model token counts in `ModelMetrics`, just not rendered.

**Implementation:**
1. In `buildModelRow()`, read `mm.tokens` and render it as a new `<div class="m-cell m-num">`.
2. Add "Tokens" header to `.model-table-head`.
3. Adjust grid columns from `1.4fr 1.4fr repeat(4, 0.7fr)` to `1.4fr 1.4fr repeat(5, 0.7fr)`.

**Files to change:** `web/static/js/codebuff.js`, `web/templates/pages/codebuff.html`

---

### Feature 4: Sparklines (15-minute request rate)

**Value:** MEDIUM — the code comment on line 1 says "vertically rich cards layout with sparklines" but none exist.

**Design:**
- Tiny inline SVG sparkline (120px × 24px) under each account card showing request rate over the last 15 minutes.
- Data source: keep last 30 data points in JS memory (one per 30s poll = 15 min).
- No server-side changes needed.

**Implementation:**
1. Maintain a `sparkData` map: `{ account_id → [reqs_at_poll_N, reqs_at_poll_N-1, ..., reqs_at_poll_0] }`.
2. On each poll, push current `total.requests` value, keep max 30 entries.
3. Render SVG polyline connecting normalized values.
4. Style: thin line (1px), muted color (#2563eb), subtle fill below.

**Files to change:** `web/static/js/codebuff.js`

---

### Feature 5: Request success/failure breakdown

**Value:** MEDIUM — only 429 errors are tracked. Network errors, timeouts, upstream 5xx are invisible.

**Design:**
- Add `errors_total` field to telemetry hash (distinct from `errors_429`).
- Show in banner: "Errors: 12 (2.1%)"
- Show per-card: error count + percentage.

**Implementation:**
1. In `telemetry.go` `RecordRequest`: add `isError` parameter (true for non-429 failures). Increment `errors_total` hash field.
2. In `provider.go`: pass `isError=true` to `recordTelemetry` on non-429 error paths (already identified in Bug 1 table — lines 228, 245, 279).
3. In `ModelMetrics`: add `ErrorsTotal int64`.
4. In frontend: render in banner stats and card stats.

**Files to change:** `internal/codebuff/telemetry.go`, `internal/codebuff/provider.go`, `web/static/js/codebuff.js`, `web/templates/pages/codebuff.html`

---

### Feature 6: Latency percentiles (P50, P95)

**Value:** MEDIUM — current "Avg Latency" masks tail latency spikes.

**Design:**
- Show P50 and P95 alongside average: "Avg 340ms | P50 280ms | P95 890ms".
- Use HDR Histogram approximation in Redis.

**Simple approach:**
1. Track `max_latency_ms` and `min_latency_ms` hash fields per model.
2. Store latency samples in a Redis sorted set: `ZADD telemetry:latency:<account_id>:<model> <latency_ms> <request_id>`.
3. On metrics poll, compute P50/P95 from the sorted set: fetch cardinality, then `ZRANGE` by index.
4. Cap sorted set at 1000 entries, clean stale entries older than 2h.

**Files to change:** `internal/codebuff/telemetry.go`, `internal/api/codebuff_metrics.go`, `web/static/js/codebuff.js`

---

### Feature 7: Recent error log

**Value:** LOW — useful for debugging failing accounts.

**Design:**
- Last 50 errors stored per account in Redis list.
- Displayed in expandable section below each card (collapsed by default).
- Each entry: timestamp, model, error type (429 / network / timeout / upstream_5xx), message preview.

**Implementation:**
1. On error in `provider.go`, push to Redis list: `LPUSH codebuff:errors:<account_id> <json_blob>`, `LTRIM codebuff:errors:<account_id> 0 49`.
2. Add `GET /api/codebuff/errors?account_id=N` endpoint.
3. Frontend: fetch on card expand, render as scrollable log.

**Files to change:** `internal/codebuff/telemetry.go`, `internal/api/codebuff_metrics.go`, `internal/codebuff/provider.go`, `web/static/js/codebuff.js`

---

## UI/UX POLISH

### Polish 1: Refresh interval selector

**Value:** LOW — hardcoded 30s polling.
**Effort:** Trivial.

Dropdown next to Refresh button: `10s | 30s | 60s | 5min | off`. Clears and resets `setInterval`.

**Files to change:** `web/static/js/codebuff.js`, `web/templates/pages/codebuff.html`

---

### Polish 2: Status timeline heatmap

**Value:** LOW — spot activity patterns across hours of the day.
**Effort:** Medium.

A 24-column mini-grid under each card. Each column = 1 hour of the day. Green = had traffic, yellow = had 429s, red = blocked during that hour, grey = idle.

**Implementation:**
Store per-hour activity flags in Redis hash: `HSET telemetry:hourly:<account_id> <0..23> <status_bits>` where status bits encode: 1=traffic, 2=429s, 4=blocked.

**Files to change:** `internal/codebuff/telemetry.go`, `web/static/js/codebuff.js`

---

### Polish 3: Dark mode toggle

**Value:** LOW.
**Effort:** Low.

1. Detect `prefers-color-scheme: dark` media query for auto mode.
2. Manual toggle stored in `localStorage`.
3. CSS variables swap in a `.dark` class on `<html>`.

**Files to change:** `web/templates/pages/codebuff.html`

---

### Polish 4: Token throughput stat clarification

**Value:** LOW — `tokens_per_s` metric is misleading.
**Effort:** Trivial.

Current formula: `total_tokens / (total_latency / 1000)`. For streaming, `total_latency` is wall clock including network wait. Clarify label to "tokens/s (wall)" or switch to internal processing rate once we have per-chunk timing.

**Files to change:** `web/static/js/codebuff.js`

---

### Polish 5: Aggregate account comparison mode

**Value:** LOW — useful with 5+ accounts.
**Effort:** High.

Checkbox next to account filter: "Compare selected". When 2+ accounts are checked, render a merged comparison table: rows = models, cols = accounts, cells = requests/tokens/429s/rpm.

**Files to change:** `web/static/js/codebuff.js`, `web/templates/pages/codebuff.html`

---

## REDIS SCALE NOTE

**Current:** `GetAccountsMetrics()` in `telemetry.go:89-133` calls `HGetAll` for every model × every account. With 7 accounts × 10 models = 70 sequential `HGETALL` calls per 30s poll. This is fine at current scale.

**After adding time buckets:** Instead of sequential calls, use `redis.Pipelined()` to batch all bucket fetches in one round-trip. This will be implemented as part of Feature 1.

---

## EXECUTION PLAN

### Phase 0 — Investigation (30 min)

| Step | Description |
|------|-------------|
| I.1 | Add temporary log line in `handleCodebuffSync` dumping raw JSON from `GetSession` and `GetStreak` responses |
| I.2 | Add temporary log line in `recordSessionQuotas` dumping `rateLimitsByModel` from session creation |
| I.3 | Deploy, trigger sync, inspect logs to determine available quota fields |
| I.4 | Confirm Bug 3 root cause based on findings — update fix plan accordingly |

### Phase 1 — Bug fixes (4-6 hours)

| Step | Description | Files |
|------|-------------|-------|
| 1.1 | Add tiktoken `Estimator` accumulation to `streamChat` path | `provider.go` |
| 1.2 | Add tiktoken `Estimator` accumulation to `streamChatRaw` path | `provider.go` |
| 1.3 | Implement rolling RPM via Redis sorted sets | `telemetry.go` |
| 1.4 | Add `rpm` field to `ModelMetrics` + `AccountMetrics` | `telemetry.go` |
| 1.5 | Update `codebuff.js` to use server-provided `rpm`, remove client-side window math | `codebuff.js` |
| 1.6 | Fix sync quota data freshness (exact fix depends on Phase 0) | `codebuff_quota.go` |
| 1.7 | Add daily self-counting request counter for quota fallback | `telemetry.go`, `codebuff_quota.go` |
| 1.8 | Build + test all changes | — |

### Phase 2 — Time filters (3-4 hours)

| Step | Description | Files |
|------|-------------|-------|
| 2.1 | Add per-minute bucket counter writes to `RecordRequest` | `telemetry.go` |
| 2.2 | Add `range` parameter to `GetAccountsMetrics` | `telemetry.go` |
| 2.3 | Implement bucket summation for time-windowed queries | `telemetry.go` |
| 2.4 | Add `?range=` param to `HandleCodebuffMetrics` | `codebuff_metrics.go` |
| 2.5 | Add time-range filter buttons to UI | `codebuff.html`, `codebuff.js` |
| 2.6 | Add auto quota sync (Feature 2) | `codebuff.js`, `codebuff.html` |

### Phase 3 — Polish (4-6 hours)

| Step | Description | Files |
|------|-------------|-------|
| 3.1 | Per-model token breakdown (Feature 3) | `codebuff.js`, `codebuff.html` |
| 3.2 | Success/failure ratio (Feature 5) | `telemetry.go`, `provider.go`, `codebuff.js` |
| 3.3 | Sparlines — 15-min request rate (Feature 4) | `codebuff.js` |
| 3.4 | Refresh interval selector (Polish 1) | `codebuff.js`, `codebuff.html` |
| 3.5 | Token throughput stat clarification (Polish 4) | `codebuff.js` |

### Phase 4 — Deferred (nice-to-haves, 4-6 hours)

| Step | Description |
|------|-------------|
| 4.1 | Latency percentiles P50/P95 (Feature 6) |
| 4.2 | Recent error log (Feature 7) |
| 4.3 | Status timeline heatmap (Polish 2) |
| 4.4 | Dark mode toggle (Polish 3) |
| 4.5 | Account comparison mode (Polish 5) |

---

## VERIFICATION

After each phase, verify:
- `go build ./...` compiles clean
- `go test ./internal/codebuff/...` passes
- `go vet ./...` clean
- Dashboard loads without JS errors in console
- Tokens increment on streaming requests (check Redis hash fields)
- RPM reflects actual request rate (not decaying)
- Sync quota updates correctly after manual sync
- Time range filter shows correct scoped stats

---

## Files inventory

| File | Current lines | Changes expected |
|------|--------------|-----------------|
| `internal/codebuff/provider.go` | 508 | +20 (token estimator, error tracking) |
| `internal/codebuff/telemetry.go` | 181 | +80 (RPM, time buckets, error logging) |
| `internal/api/codebuff_metrics.go` | 47 | +15 (range param, error endpoint) |
| `internal/api/codebuff_quota.go` | 108 | +10 (sync fix) |
| `web/static/js/codebuff.js` | 374 | +120 (sparklines, time filter, auto-sync, RPM, errors, per-model tokens, refresh selector) |
| `web/templates/pages/codebuff.html` | 376 | +40 (time filter bar, tokens column, refresh selector, dark mode vars) |
