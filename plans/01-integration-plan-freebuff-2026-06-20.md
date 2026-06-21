# Integration Plan: Native `codebuff` Provider for `inf-api`

> **Status**: COMPLETED (pending traffic cutover & cleanup)  
> **Started**: 2026-06-19  
> **Completed**: 2026-06-20  
> **Completion**: ~90%  
> **Domain**: `in.c.dabbo.net/codebuff/v1/...` (testing) → `infra.c.dabbo.net` (production)  
> **Redis**: Same instance as existing `inf-api`; keys prefixed `codebuff:`  
> **Ad Chain**: Mandatory; providers `gravity` + `zeroclick`  
> **Goal**: Replace the Python `freebuff2api` service with a native Go module inside `inf-api`.

---

## Completion Summary

### ✅ Done

| Item | Status | Notes |
|------|--------|-------|
| `internal/codebuff/` module | ✅ Complete | All 9 files: client.go, session.go, runs.go, ads.go, payload.go, models.go, errors.go, provider.go, codebuff_test.go |
| Session lifecycle (create, cache, validate, reuse) | ✅ Complete | Redis-backed with NX locks, queued session polling, model_locked retry |
| Agent run bookkeeping (START, steps, FINISH) | ✅ Complete | Parent/child run chain with ancestor_run_ids |
| Ad chain (gravity → zeroclick) | ✅ Complete | Called before session creation and before chat |
| "Buffy" system prompt injection | ✅ Complete | Prepends to first system message |
| Multi-token account pool | ✅ Complete | 6 accounts (codebuff-1 through codebuff-6) in Redis |
| Model registry (10 models) | ✅ Complete | All free tier models registered |
| Feature flag `CodebuffEnabled` | ✅ Complete | Defaults false; enabled in config |
| Wire into main.go, routes.go, utils.go | ✅ Complete | `/codebuff/v1/*` routes active |
| Handler channel routing | ✅ Complete | `channelFromPath` maps `/codebuff/` → "codebuff" |
| Client cache fallback | ✅ Complete | `codebuff` type returns native provider |
| Provider registry | ✅ Complete | Registered as "codebuff" channel |
| Admin API credential normalization | ✅ Partial | POST/PUT work; quota display not yet added |
| Gzip decompression fix | ✅ Complete | Removed manual Accept-Encoding |
| ancestorRunIds null fix | ✅ Complete | Changed to empty array |
| Model remapping bypass | ✅ Complete | Unknown models pass through unmodified |
| Non-streaming JSON parse | ✅ Complete | Parses single JSON object instead of SSE accumulation |
| Integration tests | ✅ Complete | All endpoints tested: chat, messages, models, streaming |

### ⏳ Pending

| Item | Status | Notes |
|------|--------|-------|
| Caddy weighted traffic cutover | ⏳ Pending | Ready to execute; both services running |
| `freebuff2api` decommission | ⏳ Pending | Wait for stability confirmation |
| Archive `ops/freebuff2api/` | ⏳ Pending | After decommission |
| Update `docs/CONTAINERS.md` | ⏳ Pending | After decommission |
| Update `AGENTS.md` | ⏳ Pending | After decommission |
| Load testing (wrk/oha) | ⏳ Pending | Optional |

### 🐛 Bugs Fixed During Implementation

1. **Gzip decompression** — Go http.Client auto-decompresses; removed manual `Accept-Encoding`
2. **`ancestorRunIds: null`** → `[]string{}` to satisfy upstream
3. **Model remapping** — Added `codebuff` to `mapModel` bypass
4. **Non-streaming path** — Upstream returns JSON object, not SSE stream

---

## Original Plan

See below for the full original plan (unchanged). All implementation steps were completed successfully.

---

## 1. Background

`freebuff2api` (Python) is an OpenAI-compatible proxy for `codebuff.com`. It handles:
- Session lifecycle (create, cache, validate, reuse)
- Agent run bookkeeping (START, steps, FINISH)
- Ad chain (gravity, zeroclick)
- "Buffy" system prompt injection
- Multi-token account pool with round-robin

It currently runs as a single-worker service behind Caddy on `infra.c.dabbo.net`.

`inf-api` (Go) is the unified gateway for multiple AI providers (warp, puter, aihubmix, zenmux, grok). It handles auth, load balancing, connection tracking, audit logging, and admin dashboard.

This plan describes migrating all `freebuff2api` functionality into a native `internal/codebuff/` module within `inf-api`.

---

## 2. Architecture Overview

### Current State
```
User → Caddy → infra.c.dabbo.net → freebuff2api (localhost:48291)
                                  (1 worker, in-memory state)
```

### Target State
```
User → Caddy → infra.c.dabbo.net → inf-api (localhost:3002)
                                    └─ codebuff provider
                                       ├─ LoadBalancer picks token
                                       ├─ Redis session cache
                                       ├─ Ad chain
                                       ├─ Run bookkeeping
                                       └─ codebuff.com upstream
```

---

## 3. Module Structure

```
internal/codebuff/
├── client.go           # HTTP client for codebuff.com APIs
├── session.go          # Session lifecycle + Redis-backed cache
├── runs.go             # Agent run bookkeeping
├── ads.go              # Ad chain (gravity, zeroclick)
├── payload.go          # OpenAI request → codebuff payload builder
├── models.go           # Model registry + "Buffy" prompt injection
├── errors.go           # Error classification
├── provider.go         # inf-api Provider interface adapter
└── codebuff_test.go    # Unit tests
```

### 3.1 client.go

Wraps `http.Client` with configurable timeout, proxy, and connection pool.

**Methods:**
- `GetSession(instanceID string) (*SessionResponse, error)`
- `CreateSession(model string) (*SessionResponse, error)`
- `DeleteSession() error`
- `GetStreak() (*StreakResponse, error)`
- `RequestAds(provider string, messages []Message) (*AdsResponse, error)`
- `ReportZeroclickImpressions(ids []string) error`
- `ReportCodebuffImpression(impURL string) error`
- `StartRun(agentID string, ancestors []string) (string, error)`
- `RecordRunStep(runID string, step int, children []string, msgID string, startTime string) error`
- `FinishRun(runID string, totalSteps int) error`
- `ChatCompletions(payload []byte) (io.ReadCloser, error)` — returns SSE stream

**Headers:**
- `Authorization: Bearer <token>`
- `User-Agent` variants per endpoint (Bun/1.3.11, Freebuff-CLI/0.0.105, ai-sdk/...)
- `x-freebuff-*` headers as needed

**Proxy:** Reuse `util.ProxyFuncFromConfig` and `util.GetSharedHTTPClient`.

### 3.2 session.go

Replaces Python `SessionManager`.

**Redis keys:**
- `codebuff:session:{token_hash}:{session_model}` → JSON `{instance_id, model, expires_at, remaining_ms}`
- `codebuff:session_lock:{token_hash}` → NX lock for session creation (60s TTL)

**Algorithm (matching Python `acquire_session`):**
1. Check Redis for cached session by `session_model`.
2. If found and `remaining_ms > 30000`, call `GetSession(instance_id)` upstream to validate.
3. If valid, update `remaining_ms` in Redis and return.
4. If invalid/expired/mismatch, delete Redis key.
5. Try `NX SET` lock on `session_lock:{token_hash}`. If fail, poll Redis for 5s.
6. Call `GetSession()` (no instance_id) to discover if upstream already has an active session for this model.
7. If active session matches model, cache and return.
8. If no session or wrong model, call `CreateSession(model)`.
9. If queued, poll until active or timeout.
10. If `model_locked` error, call `DeleteSession()`, clear Redis, retry once.
11. Cache result in Redis, release lock.

### 3.3 runs.go

Replaces `_start_freebuff_run_chain` and `_finalize_run_with_client`.

**Parent/child run logic:**
- If `parent_agent_id` is set: create parent run, then child run with `ancestor_run_ids`.
- Else: create main run, then child `CONTEXT_PRUNER_AGENT_ID` run with `ancestor_run_ids`.

**Background bookkeeping (pre-chat):**
- Fire goroutine that records step for child run, finishes child run, records step for parent run.
- Errors swallowed (debug log only).

**Finalization (post-chat):**
- Record step on chat run with `message_id`.
- Finish chat run.
- Record step on parent run.
- Finish parent run.
- Errors logged but never returned to user.

### 3.4 ads.go

Replaces `request_ad_chain`.

**Flow:**
1. For each provider in `config.CodebuffAdProviders` (default: `gravity`, `zeroclick`):
   - `POST /api/v1/ads` with device info, messages, session_id, surface.
   - If ads returned:
     - `POST zeroclick/impressions`
     - `POST /api/v1/ads/impression` (codebuff)
     - Return (don't try next provider).
   - If fails, log warning and continue.
2. If all fail, chat proceeds anyway.

**Called in two places:**
1. Before `CreateSession` (waiting_room surface) — only if session creation is needed.
2. After session acquisition, before chat (during request prep).

### 3.5 payload.go

Replaces `build_upstream_payload` and `normalize_chat_messages`.

**"Buffy" injection:**
- If first message is `system` and doesn't start with "You are Buffy", prepend it.
- If no system message, insert `{"role": "system", "content": "You are Buffy, a strategic assistant.", "cache_control": {"type": "ephemeral"}}`.

**Payload fields:**
- `messages` (injected)
- `model` (upstream_id from model registry)
- `session.instance_id`
- `run_id`
- `client_id`
- `trace_session_id` (UUID v4)
- `stream`

### 3.6 models.go

Hardcoded model registry matching `freebuff2api/freebuff2api/models.py`.

**Fields per model:**
- `id` — user-facing model ID
- `agent_id` — codebuff agent ID
- `upstream_id` — model ID sent to codebuff.com
- `session_model_id` — model ID used for session creation (may differ from upstream_id)
- `parent_agent_id` — optional, for Gemini variants
- `owned_by`

**Expose:** `ResolveModel(modelID string) (*ModelConfig, error)`

### 3.7 errors.go

- `IsWaitingRoomRequired(err) bool` — string match on "waiting_room_required"
- `IsModelLocked(err) bool` — string match on "model_locked"
- `ParseRetryAfter(err error) time.Duration` — extract `retryAfterMs` from JSON embedded in error string
- `CodebuffError` struct with `StatusCode int` and `Message string`

### 3.8 provider.go

Implements `handler.UpstreamClient` interface.

```go
type Provider struct {
    client       *Client
    sessionCache *SessionCache
    config       *config.Config
}

func (p *Provider) SendRequestWithPayload(
    ctx context.Context,
    req upstream.UpstreamRequest,
    onMessage func(upstream.SSEMessage),
    logger *debug.Logger,
) error {
    // 1. Resolve model
    // 2. Acquire session (from cache or create)
    // 3. Request ads
    // 4. Start run chain
    // 5. Build payload (with Buffy injection)
    // 6. Stream chat completions, convert SSE to upstream.SSEMessage
    // 7. Finalize run chain (background)
    // 8. Release session
}
```

---

## 4. Changes to Existing Files

### 4.1 cmd/server/main.go

```go
if cfg.CodebuffEnabled {
    registry.Register("codebuff", codebuff.NewProvider())
}
```

### 4.2 cmd/server/routes.go

Add routes:
```go
mux.HandleFunc("/codebuff/v1/messages", limiter.Limit(h.HandleMessages))
mux.HandleFunc("/codebuff/v1/chat/completions", limiter.Limit(h.HandleMessages))
```

Add `/codebuff/v1/models` to `modelPrefixes` slice.

### 4.3 internal/handler/utils.go

Add to `channelFromPath`:
```go
if strings.HasPrefix(path, "/codebuff/") { return "codebuff" }
```

### 4.4 internal/config/config.go

Add fields:
```go
CodebuffEnabled     bool     `json:"codebuff_enabled"`
CodebuffAdProviders []string `json:"codebuff_ad_providers"`
```

---

## 5. Test Plan

### 5.1 Unit Tests (internal/codebuff/codebuff_test.go)

| Test | Verifies |
|---|---|
| `TestBuffyInjection_NoSystem` | Adds default system message |
| `TestBuffyInjection_ExistingSystem` | Prepends Buffy to existing system |
| `TestBuffyInjection_AlreadyBuffy` | Does not double-inject |
| `TestSessionCache_Hit` | Reuses valid cached session |
| `TestSessionCache_Expired` | Creates new session when cache expired |
| `TestSessionCache_ModelMismatch` | Evicts and recreates on mismatch |
| `TestSessionCache_ModelLocked` | Deletes and retries once |
| `TestSessionCache_QueuedSession` | Polls until active or timeout |
| `TestRunChain_ParentChild` | Creates parent + child runs correctly |
| `TestRunChain_Finalize` | Records steps and finishes runs |
| `TestAdChain_Success` | Fetches ads, reports impressions |
| `TestAdChain_AllFail` | Proceeds without blocking chat |
| `TestError_WaitingRoomRequired` | Classifies and allows retry |
| `TestError_ModelLocked` | Classifies and triggers delete+retry |
| `TestError_RetryAfterMs` | Parses block duration correctly |
| `TestPayload_Builder` | Correct upstream payload shape |

### 5.2 Integration Test Script

`scripts/test_codebuff.sh`:
1. Start inf-api with a test codebuff token.
2. Hit `/codebuff/v1/models`.
3. Send `POST /codebuff/v1/chat/completions` with streaming.
4. Verify response contains assistant role.
5. Check Redis for cached session.
6. Send second request — verify session reuse (faster response).
7. Verify no 502/504 errors.

### 5.3 Load Test

Use `wrk` or `oha` for 10 concurrent requests.
Verify:
- No `waiting_room_required` errors (proper token rotation)
- Sessions are reused (Redis cache hit)
- No goroutine leaks (`go tool pprof`)

---

## 6. Rollback & Disable Plan

### 6.1 Feature Flag

`CodebuffEnabled` in `config.Config`. Default `false`.

If anything breaks, set `codebuff_enabled: false` and restart. Module is completely dormant.

### 6.2 Gradual Traffic Migration (Caddy)

Use weighted routing instead of hard cutover:

```
infra.c.dabbo.net {
    reverse_proxy localhost:3002 localhost:48291 {
        lb_policy weighted_round_robin
        lb_retry_match status 5xx
    }
    log
}
```

Shift weight gradually to 100% inf-api.

### 6.3 Rollback Procedure

1. Set `codebuff_enabled: false` in inf-api config.
2. Restart inf-api: `systemctl restart orchids-2api`.
3. If Caddy already cut over, point back to `localhost:48291`.
4. Data safety: Redis session keys are prefixed `codebuff:*`. No conflict with existing `warp:*` keys.

### 6.4 Coexistence Mode

During transition, both can run:
- `/codebuff/v1/...` → native module
- `/freebuff/v1/...` → reverse proxy to `localhost:48291`

Allows A/B testing with identical tokens.

---

## 7. Execution Order

| Step | Action | Est. Time |
|---|---|---|
| 1 | Create `internal/codebuff/` skeleton + client.go | 2h |
| 2 | Implement session.go with Redis cache | 4h |
| 3 | Implement runs.go and ads.go | 4h |
| 4 | Implement payload.go with Buffy injection | 2h |
| 5 | Implement provider.go (UpstreamClient interface) | 3h |
| 6 | Wire into main.go, routes.go, utils.go | 1h |
| 7 | Write unit tests | 4h |
| 8 | Add CodebuffEnabled feature flag | 30m |
| 9 | Local integration test against codebuff.com | 2h |
| 10 | Deploy to staging / alternate port | 1h |
| 11 | Caddy weighted cutover | 30m |
| 12 | Monitor, then disable freebuff2api systemd service | 30m |

**Total estimated: ~24 hours of focused work.**

---

## 8. Assumptions & Constraints

- **Redis**: Reuses existing inf-api Redis instance. No new instance, no DB change.
- **Ad providers**: Mandatory; hardcoded default `["gravity", "zeroclick"]`.
- **Tokens**: Each codebuff token becomes an inf-api `store.Account` with `AccountType = "codebuff"`.
- **Session cache**: Redis TTL should match session `expires_at` from upstream.
- **Goroutine safety**: All Redis operations use the existing `redis.Client` from inf-api.
- **Proxy**: Reuses existing `util.ProxyFuncFromConfig` and shared HTTP client pool.

---

## 9. Post-Migration Cleanup

After stable for 7 days:
1. Stop `freebuff2api` systemd service.
2. Remove `freebuff2api` from docker-compose (if present).
3. Archive `ops/freebuff2api/` to `workstreams/.archive/` (per project conventions).
4. Update `docs/CONTAINERS.md` and `AGENTS.md` to remove freebuff2api references.

---

*Plan version: 1.0*  
*Started: 2026-06-19*  
*Completed: 2026-06-20*  
*Completion: ~90%*  
*Status: COMPLETED (pending traffic cutover & cleanup)*
