# Plan 05: Codebuff Retry + Quota-Exhausted Rerouting

**Date:** 2026-06-21
**Status:** DEPLOYED (PR #2)

## Problem

The codebuff handler (`handleCodebuffDirect`) had zero retry logic — pick one account, call once, fail silently. When all codebuff accounts hit the daily limit (5 requests/day), every request failed with no account switching and no error returned to the client.

Additionally, the upstream error classifier treated `insufficient_funds` (402) and `rate_limit` (429) as the same category, preventing intelligent retry decisions.

## Root Cause

```
handleCodebuffDirect() at handler.go:1650
  ├── selectAccountWithOptions() — picks ONE account
  ├── SendRequestWithPayload()   — calls ONCE
  └── if err → slog.Error()      — logs and returns nothing to client
       ↑ No retry. No switch. No error response.
```

Codebuff daily limit response:
```
HTTP 429 {"status":"rate_limited","recentCount":5.1,"limit":5,"retryAfterMs":46192144}
```
This looks like a 429 but is actually a daily quota exhaustion (resets at 07:00 UTC).

## Changes

### 1. Codebuff daily limit → `quota_exhausted` classification

Pre-switch check in `classify.go` before the main switch-case. Detects the codebuff-specific JSON body pattern `"status":"rate_limited"` + `"recentCount"`:

```go
if strings.Contains(lower, `"status":"rate_limited"`) && strings.Contains(lower, `recentcount`) {
    return UpstreamErrorClass{Category: "quota_exhausted", Retryable: true, SwitchAccount: true}
}
```

This runs before the `429`/`402` switch cases, correctly classifying codebuff's daily limit as quota exhaustion.

### 2. Split `quota_exhausted` from `rate_limit` (all providers)

| Pattern | Before | After |
|---------|--------|-------|
| 429, too many requests, rate_limit | `rate_limit` | `rate_limit` |
| 402, insufficient_funds, quota_limit, out of credits, credits exhausted, run out of credits | `rate_limit` | **`quota_exhausted`** |

Both categories: `Retryable: true, SwitchAccount: true`. The difference is in retry behavior:
- `quota_exhausted`: 2s minimum delay, don't retry same account when no alternative exists
- `rate_limit`: Same delay, same no-retry-on-same-account behavior

### 3. Retry + account-switch loop for codebuff handler

Replaced single-shot call with retry loop (up to 3 attempts):

```
handleCodebuffDirect():
  ┌─ for attempt := 0; attempt < 3; attempt++:
  │    ├── SendRequestWithPayload()
  │    ├── if success → break
  │    ├── classifyUpstreamError(err) → errClass
  │    ├── if !errClass.Retryable → break
  │    ├── if errClass.SwitchAccount:
  │    │    ├── MarkAccountStatus(currentAccount, status)
  │    │    ├── MarkModelStatus(currentAccount, model, "402")  // if quota_exhausted
  │    │    └── selectAccountWithOptions() → next account
  │    └── computeRetryDelay(attempt, errClass.Category) → sleep
  └─ audit event (success or error)
```

### 4. Per-model block tracking

Added to `store.Account`:
```go
ModelStatuses map[string]string   `json:"model_statuses,omitempty"`
ModelStatusAt map[string]time.Time `json:"model_status_at,omitempty"`
```

New load balancer methods:
- `IsModelAvailable(acc, modelID)` — per-model cooldown check (402: 24h standard / 5min aihubmix/zenmux; 429: standard)
- `MarkModelStatus(acc, modelID, status)` — blocks one model on an account
- `clearAccountStatus` now also wipes per-model blocks on account recovery

### 5. Model-aware account selection for ALL channels

Non-Warp channels now use `GetNextAccountExcludingByChannelWithTrackerFilter` with a model filter that calls `IsModelAvailable`. Previously only Warp considered the model during account selection.

### 6. Quota-aware retry delay

`computeRetryDelay` gives `quota_exhausted` a 2s minimum delay (same as `rate_limit`). `shouldRetryCurrentAccountWhenNoAlternative("quota_exhausted")` returns `false` — fail fast instead of retrying the exhausted account.

## Files Changed

| File | Change |
|------|--------|
| `internal/errors/classify.go` | Codebuff pre-check + split 402/429 |
| `internal/errors/classify_test.go` | 6 new classification tests including codebuff |
| `internal/store/store.go` | Added `ModelStatuses`, `ModelStatusAt` |
| `internal/store/redis_store.go` | Persist new fields |
| `internal/loadbalancer/loadbalancer.go` | `IsModelAvailable`, `MarkModelStatus`, model block cleanup |
| `internal/loadbalancer/loadbalancer_test.go` | 13 tests for model blocking |
| `internal/handler/handler.go` | Codebuff retry loop + quota_exhausted handling in main retry loop |
| `internal/handler/handler_helpers.go` | Model filter for all channels + `computeRetryDelay` for quota_exhausted |
| `internal/handler/handler_helpers_classify_test.go` | 8 tests for classification, retry delay, no-retry behavior |

9 files, +466/-14 original + codebuff loop.

## Tests

25 new tests. All existing tests pass. Zero regressions.

## Verfication

- `go build ./...` passes
- `go test ./...` passes (all 24 packages)
- Deployed to `orchids-api.service` (in.c.dabbo.net:3002), health check: `{"status":"ok"}`
