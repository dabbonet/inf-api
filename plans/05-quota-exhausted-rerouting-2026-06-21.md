# Plan 05: Quota-Exhausted Rerouting

**Date:** 2026-06-21
**Status:** PLANNED

## Problem

`insufficient_funds` (402) and `rate_limit` (429) produce the `exact same` `UpstreamErrorClass`:

```go
// internal/errors/classify.go:125-137
case /* both 402 and 429 land here */:
    return UpstreamErrorClass{Category: "rate_limit", Retryable: true, SwitchAccount: true}
```

This causes three failures:

1. **Wasted retries:** System retries an account that has permanently exhausted its quota for this model — it will never succeed.
2. **Account poisoning:** When `markAccountStatus` sets `StatusCode = "402"`, the entire account is excluded from all model requests for 24h, even though other models on that account may still have quota.
3. **No per-model awareness:** Non-Warp account selection (`handler_helpers.go:159-160`) ignores the model entirely. When a Puter/Grok/AIhubmix/Zenmux account fails with `insufficient_funds` for model X, the system can't route model Y requests through that same account.

## Root Cause Map

```
Puter: "code=insufficient_funds, status=402, message=..."
         │
         ▼
  classifyUpstreamError()        ← internal/errors/classify.go
         │
         ├─ matches "insufficient_funds" or "status=402"
         │
         ▼
  UpstreamErrorClass{Category: "rate_limit", Retryable: true, SwitchAccount: true}
         │
         ├─ SAME category as genuine 429 rate limits
         │
         ▼
  Retry loop (handler.go:1341-1492)
    ├─ tries to switch accounts (SwitchAccount=true)
    ├─ marks account status via classifyAccountStatus() → StatusCode="402" → 24h cooldown
    ├─ if no alternative: shouldRetryCurrentAccountWhenNoAlternative("rate_limit") → false → fail
    └─ result: entire account poisoned for all models
```

## Architecture Changes

### 1. New Error Category: `quota_exhausted`

Split `ClassifyUpstreamError()` in `internal/errors/classify.go`:

| Pattern | Current | New |
|---------|---------|-----|
| 429, too many requests, rate_limit | `rate_limit` | `rate_limit` |
| 402, insufficient_funds, quota_limit, out of credits, credits exhausted, run out of credits | `rate_limit` | **`quota_exhausted`** |

New semantics:
```
quota_exhausted: Retryable=false (same account+model), SwitchAccount=true
```

### 2. Per-Account Per-Model Block Tracking

Add to store and load balancer:

```
File: internal/store/store.go (Account struct)
New field: ModelStatuses map[string]string  // modelID → status code ("402" / "429" / "")
New field: ModelStatusAt  map[string]time.Time  // when each model was last marked
```

- When model X gets 402: `account.ModelStatuses["modelX"] = "402"`, `account.ModelStatusAt["modelX"] = now`
- When model X gets 429: same with "429"
- Cooldown durations per-model same as per-account: 402=24h, 429=1min

### 3. Model-Aware Account Selection for ALL Channels

**Currently** (`handler_helpers.go:159-160`):
```go
if !strings.EqualFold(strings.TrimSpace(targetChannel), "warp") {
    return h.loadBalancer.GetNextAccountExcludingByChannelWithTracker(...)
    // ↑ Model ID is completely ignored for non-Warp
}
```

**New:**
```go
// Build a filter that excludes accounts where the requested model is blocked
modelFilter := func(acc *store.Account) bool {
    return lb.IsModelAvailable(acc, requestedModel)
}
return h.loadBalancer.GetNextAccountExcludingByChannelWithTrackerFilter(..., modelFilter)
```

### 4. Quota-Aware Retry Logic

In `handler.go` retry loop, when category is `quota_exhausted`:

```go
case "quota_exhausted":
    // Mark only this model on this account
    h.markModelBlocked(currentAccount, requestedModel, "402")
    // Switch to another account that has this model available
    // If none available: fail immediately (no point retrying)
```

Add `shouldRetryCurrentAccountWhenNoAlternative("quota_exhausted") → false`.

### 5. Account Cooldown Still Applies

When `markAccountStatus("402")` is called (existing behavior), the account-level cooldown still runs. The per-model block is additive — even if the account cooldown expires, the model-level block persists independently.

**Priority:** Per-model check runs FIRST before account-level cooldown check.

## Files to Change

| File | Change | Risk |
|------|--------|------|
| `internal/errors/classify.go` | Split 402 from 429 into `quota_exhausted` category | Low — isolated classification logic |
| `internal/errors/classify_test.go` | Add tests for new category | Low |
| `internal/store/store.go` | Add `ModelStatuses`, `ModelStatusAt` to Account struct | Medium — DB schema change |
| `internal/store/redis_store.go` | Persist new fields in Redis | Medium |
| `internal/loadbalancer/loadbalancer.go` | Add `IsModelAvailable(acc, model)`, per-model cooldown durations | Medium |
| `internal/loadbalancer/loadbalancer_test.go` | Tests for model-aware availability | Low |
| `internal/handler/handler.go` | Quota-aware retry: model-level marking, fast failure | Medium |
| `internal/handler/handler_helpers.go` | Model-aware filtering for ALL channels, not just Warp | Medium |
| `internal/handler/handler_helpers_classify_test.go` | Tests for shouldRetryCurrentAccountWhenNoAlternative | Low |
| `internal/handler/account_status.go` | Add `markModelBlocked()`, `markPuterQuotaExhausted()` | Low |

## Non-Goals (Deferred)

- Per-model quota pre-check before account selection (would require knowing quota levels per model, which we don't have)
- Real-time quota sync with upstream APIs
- Cross-channel model blocking (a 402 on Puter doesn't block the same model on Grok)

## Verification

- `go test ./...` must pass
- New tests: `TestClassifyQuotaExhausted`, `TestIsModelAvailable`, `TestSelectAccountSkipsBlockedModel`, `TestRetryQuotaExhaustedSwitchesOrFails`
- Existing tests: all must continue passing
