# Plan: Codebuff Quota Telemetry & Rate-Limit Discovery

> **Status**: PLANNED — awaiting approval  
> **Created**: 2026-06-20  
> **Tracking Period**: 2–3 days (starting after next 07:00 UTC reset)  
> **Goal**: Empirically measure codebuff's real quota model (sessions vs LLM calls) by tracking every event across all accounts.

---

## 1. Background

The codebuff free tier has a quota system, but the exact limits are unclear from documentation alone. The developer mentioned on Twitter:

- **5 free hour-long sessions** per day
- **~4,000 LLM calls** per day (one user prompt may trigger 1–30 internal calls)
- Resets daily at **07:00 UTC** (midnight Pacific)

The `recentCount: 4.3/5` seen in 429 errors does **not** mean "5 requests per model." It is a simplified UI message. The real bottleneck is either:
1. **Session limit** — 5 sessions/day (each ~1 hour)
2. **LLM call limit** — ~4,000 internal API calls/day
3. **Both** — whichever hits first

We need 2–3 days of granular telemetry starting from a fresh reset to determine which limit actually triggers 429s and at what rate.

---

## 2. What We Will Track

### 2.1 Per-Account Events (every single one)

| Event | When | Data Captured |
|-------|------|---------------|
| `session_created` | Every POST to `/freebuff/session` | model, session_id, remaining_ms, response_time_ms |
| `session_reused` | Cache hit in Redis | model, session_id, remaining_ms |
| `session_queued` | Queue polling starts | model, position, estimated_wait_ms |
| `llm_call` | Every POST to `/chat/completions` | model, session_id, input_tokens, output_tokens, duration_ms |
| `tool_call` | Tool use detected in stream | model, session_id, tool_name |
| `429_rate_limit` | HTTP 429 received | model, limit, recent_count, reset_at, retry_after_ms, response_body |
| `402_exhausted` | HTTP 402 received | model, response_body |
| `model_locked` | model_locked error | current_model, attempted_model |
| `waiting_room` | waiting_room_required | retry_after_ms |
| `ad_request` | Ad chain call | provider, ads_returned_count |
| `ad_impression` | Impression reported | provider, impression_id |

### 2.2 Per-Request Metadata (attached to every event)

```json
{
  "ts": "2026-06-21T08:15:32.123Z",
  "account_id": 4,
  "account_name": "codebuff-1",
  "request_id": "req_abc123",
  "event_type": "llm_call",
  "model": "moonshotai/kimi-k2.6",
  "session_id": "sess_def456",
  "run_id": "run_ghi789",
  "duration_ms": 3421,
  "input_tokens": 1651,
  "output_tokens": 35,
  "total_tokens": 1686,
  "user_id": "user_jkl012",
  "client_ip": "..."
}
```

### 2.3 Daily Aggregates (computed at end of day)

| Metric | Formula |
|--------|---------|
| `sessions_created` | count of `session_created` events |
| `sessions_reused` | count of `session_reused` events |
| `llm_calls` | count of `llm_call` events |
| `requests_served` | count of distinct proxy requests |
| `total_input_tokens` | sum of `input_tokens` |
| `total_output_tokens` | sum of `output_tokens` |
| `429_count` | count of `429_rate_limit` events |
| `first_429_at` | timestamp of first 429 |
| `last_429_at` | timestamp of last 429 |
| `peak_rpm` | max requests in any 1-minute window |
| `avg_calls_per_request` | `llm_calls / requests_served` |
| `max_calls_per_request` | max `llm_calls` for a single request |

---

## 3. Storage Strategy

### 3.1 Redis (primary, 3-day TTL)

```
codebuff:telemetry:events:acc:{account_id}:{yyyy-mm-dd}  → LIST of JSON events
codebuff:telemetry:daily:acc:{account_id}:{yyyy-mm-dd}   → HASH of aggregates
codebuff:telemetry:window:acc:{account_id}               → SORTED SET (timestamp → event_type) for RPM calculation
```

**TTL**: All keys expire after 5 days (gives us buffer after 3-day tracking period).

### 3.2 Local JSON Backup (optional)

At end of each day, dump aggregates to:
```
/data/telemetry/codebuff/{account_id}/{yyyy-mm-dd}.json
```

For offline analysis.

---

## 4. Implementation

### 4.1 New Files

| File | Purpose |
|------|---------|
| `internal/codebuff/telemetry.go` | Event logger, Redis writer, daily aggregator |
| `internal/codebuff/telemetry_test.go` | Unit tests for telemetry |

### 4.2 Modified Files

| File | Change |
|------|--------|
| `internal/codebuff/provider.go` | Hook `acquireSession` → log `session_created/reused/queued`. Hook `ChatCompletions` → log `llm_call` with tokens. Hook error paths → log `429`, `402`, `model_locked`. |
| `internal/codebuff/client.go` | Return `rateLimitsByModel` from `CreateSession` response. Pass usage data back to caller. |
| `internal/codebuff/session.go` | Log cache hits/misses with reasons. |
| `internal/codebuff/ads.go` | Log ad requests and impressions. |
| `internal/api/api.go` | New endpoint: `GET /api/accounts/{id}/codebuff-telemetry?date=YYYY-MM-DD`. Return events + aggregates. |
| `cmd/server/background.go` | Daily aggregation job at 07:05 UTC (5 min after reset). Compute and store aggregates. |

### 4.3 Telemetry API

**`GET /api/accounts/{id}/codebuff-telemetry?date=2026-06-21`**

```json
{
  "account_id": 4,
  "date": "2026-06-21",
  "aggregates": {
    "sessions_created": 3,
    "sessions_reused": 12,
    "llm_calls": 47,
    "requests_served": 8,
    "total_input_tokens": 12453,
    "total_output_tokens": 3892,
    "429_count": 1,
    "first_429_at": "2026-06-21T14:23:12Z",
    "last_429_at": "2026-06-21T14:23:12Z",
    "peak_rpm": 4.2,
    "avg_calls_per_request": 5.875,
    "max_calls_per_request": 12
  },
  "events": [
    {"ts": "...", "event_type": "session_created", "model": "...", ...},
    {"ts": "...", "event_type": "llm_call", "input_tokens": 1651, ...},
    ...
  ]
}
```

**`GET /api/accounts/codebuff-telemetry/summary?date=2026-06-21`**

```json
{
  "date": "2026-06-21",
  "accounts": [
    {"account_id": 4, "sessions_created": 3, "llm_calls": 47, "429_count": 1},
    {"account_id": 5, "sessions_created": 2, "llm_calls": 31, "429_count": 0}
  ],
  "pool_totals": {
    "total_sessions_created": 5,
    "total_llm_calls": 78,
    "total_429s": 1,
    "total_requests_served": 15
  }
}
```

---

## 5. Timeline

| Day | Date | Action |
|-----|------|--------|
| 0 | 2026-06-20 | Implement telemetry code, deploy to production |
| 1 | 2026-06-21 | **07:00 UTC — reset** → tracking starts fresh. Monitor via admin API. |
| 2 | 2026-06-22 | Continue tracking. Check mid-day summary. |
| 3 | 2026-06-23 | Continue tracking. End-of-day summary. |
| 4 | 2026-06-24 | **Analysis day**: Review 3 days of data. Determine real limits. |
| 5 | 2026-06-25 | **Decision day**: Disable telemetry tracker. Set production rate limits based on findings. |

---

## 6. Analysis Questions (answered after tracking)

1. **Which limit hits first?** Sessions (5/day) or LLM calls (~4,000/day)?
2. **What is the real LLM call budget?** Is it exactly 4,000? Less? More?
3. **How many internal LLM calls per user request?** Average, max, p95?
4. **Does the 5-session limit matter?** Or can we reuse sessions enough to stay under it?
5. **What is the peak RPM we can sustain?** Before 429s start?
6. **Are all accounts equal?** Or do some have higher/lower limits?
7. **When do 429s start relative to reset?** Immediately? After hours?
8. **Is `recentCount` in 429s accurate?** Does it match our tracked LLM calls?

---

## 7. Post-Tracking Actions

Based on findings:

### If sessions are the bottleneck:
- Set `UsageLimit = 5` per account per day
- Track session creation count
- Cooldown account after 5 sessions until next reset

### If LLM calls are the bottleneck:
- Set `UsageLimit = measured_limit` (likely ~4,000)
- Track LLM call count per account
- Pre-emptively block before hitting 429

### If both matter:
- Track both independently
- Cooldown whichever hits first

### Production rate limit config:
```json
{
  "codebuff": {
    "daily_session_limit": 5,
    "daily_llm_call_limit": 4000,
    "reset_utc": "07:00",
    "cooldown_before_limit": 0.9
  }
}
```

---

## 8. Risk & Mitigation

| Risk | Mitigation |
|------|------------|
| Telemetry adds latency | Events logged asynchronously (goroutine + Redis pipeline) |
| Redis memory bloat | 5-day TTL on all keys; events are small JSON (~200 bytes each) |
| Tracker left on forever | Flag `CodebuffTelemetryEnabled`; manual disable after analysis |
| Data loss on crash | Redis persistence (AOF) already enabled |
| Privacy | No user message content stored; only metadata (tokens, model, timestamps) |

---

## 9. Assumptions

- All 6 codebuff accounts have identical quota limits (to be verified).
- `remainingMs` from session response indicates session lifetime, not quota.
- `rateLimitsByModel` in session response is accurate and up-to-date.
- Reset happens exactly at 07:00 UTC (midnight Pacific, UTC-7).

---

*Plan version: 1.0*  
*Created: 2026-06-20*  
*Status: PLANNED — awaiting approval*
