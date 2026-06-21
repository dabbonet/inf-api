# Unified Release: Codebuff Native Provider for inf-api

**Status:** DEPLOYED (pure passthrough active | decommission pending)
**Branch:** `wip/plan03-openai-compat-2026-06-20`
**PR:** https://github.com/dabbonet/inf-api/pull/1
**Dates:** 2026-06-19 → 2026-06-21
**Plans consolidated:** 01 (integration), 02 (telemetry), 03 (OpenAI compat), 04 (retrospective)

## Overview

Replaced the Python `freebuff2api` service with a native Go module in `inf-api`. The codebuff provider now handles sessions, ad chains, run bookkeeping, multi-token pooling, OpenAI-compatible streaming, and real-time telemetry — all under one binary.

Traffic is routed through `infra.c.dabbo.net` with Caddy, and the provider exposes:
- `/codebuff/v1/chat/completions` — pure SSE passthrough (Plan 04)
- `/codebuff/v1/models` — model list
- `/api/codebuff/pool-status` — account pool dashboard
- `/api/codebuff/metrics` — telemetry endpoint

---

## What was delivered

### Infrastructure (Plan 01)

| File | Lines | Purpose |
|------|-------|---------|
| `internal/codebuff/client.go` | 458 | HTTP client for codebuff.com — sessions, runs, ads, chat, impressions |
| `internal/codebuff/session.go` | 198 | Redis-backed session cache — NX locks, queued polling, model_locked retry |
| `internal/codebuff/runs.go` | 186 | Agent run bookkeeping — parent/child chain, start/step/finish |
| `internal/codebuff/ads.go` | implemented | Ad chain — gravity → zeroclick with impression reporting |
| `internal/codebuff/payload.go` | 492 | OpenAI request → codebuff payload builder with Buffy injection + metadata |
| `internal/codebuff/models.go` | 173 | Model registry — 10 models, agent IDs, session model mapping |
| `internal/codebuff/errors.go` | 96 | Error classification — waiting_room, model_locked, retryAfterMs parsing |
| `internal/codebuff/provider.go` | 440 | Provider adapter — SendRequestWithPayload, BuildChunkRewriter, streamChatRaw |
| `internal/codebuff/codebuff_test.go` | 166 | Integration tests — all endpoints verified |
| `internal/provider/codebuff_provider.go` | 17 | Provider registry entry |
| `internal/config/config.go` | +11 | `CodebuffEnabled` + `CodebuffAdProviders` feature flags |
| `cmd/server/main.go` | +39 | Register codebuff provider, initialize chart/config |
| `cmd/server/routes.go` | +12 | `/codebuff/v1/*` routes |
| `internal/handler/utils.go` | +3 | `channelFromPath` → "codebuff" |
| `internal/handler/client_cache.go` | +4 | Client cache fallback for codebuff type |

### Quota & Telemetry (Plan 02)

| File | Lines | Purpose |
|------|-------|---------|
| `internal/codebuff/quotastore.go` | 449 | Redis quota store — consumption tracking, cool-down logic, 429 window |
| `internal/codebuff/telemetry.go` | 181 | Event logger — session events, LLM calls, 429s, daily aggregation |
| `internal/api/codebuff_metrics.go` | 47 | `GET /api/codebuff/metrics` endpoint |
| `internal/api/codebuff_quota.go` | 108 | `GET /api/accounts/{id}/codebuff-telemetry` endpoint |
| `cmd/server/background.go` | +31 | Daily aggregation at 07:05 UTC |
| `internal/api/api.go` | +30 | Route registration for codebuff admin endpoints |

> **Note:** Plan 02 was written as "PLANNED" but all the code was committed. The 2-3 day empirical tracking period (measuring real quota limits) never started. The telemetry infrastructure is passive — it collects data but no analysis was run.

### OpenAI Compatibility (Plan 03)

| File | Lines | Purpose |
|------|-------|---------|
| `internal/codebuff/openai_compat.go` | 67 | ChunkRewriter — `chatcmpl-<hex24>` IDs, idempotent |
| `internal/codebuff/openai_compat_test.go` | 89 | 9 tests — rewriting, idempotency, nil safety, closing-quote regression |
| `internal/codebuff/provider_chunk_rewriter_test.go` | 31 | Smoke test for BuildChunkRewriter closure |
| `internal/handler/handler.go` | +12 | `ChunkRewriterInstaller` interface + install hook + `setModelHint` |
| `internal/handler/stream_handler.go` | +41 | `chunkRewriter atomic.Pointer` + `SetChunkRewriter` + invoke in `writeOpenAISSEBytes` |

### Pure Passthrough + Root Cause Fixes (Plan 04)

| File | Change | Purpose |
|------|--------|---------|
| `internal/handler/handler.go` | +193 | `handleCodebuffDirect` — raw body passthrough, `hasReturn=true`, `rawSSEWriter` |
| `internal/handler/caching.go` | -14 | Removed `cache_control` injection for tools (Anthropic-only concept) |
| `internal/codebuff/payload.go` | -13 | Removed `cache_control` stripping (workaround for removed injection) |
| `internal/upstream/types.go` | +20 | `RawBody` field for direct passthrough |
| `web/templates/pages/codebuff.html` | 376 | Admin telemetry dashboard — cards layout with countdown timer |
| `web/static/js/codebuff.js` | 374 | Dashboard JS — `metricsData`, `renderCards()`, auto-refresh |
| `internal/template/template.go` | +2 | `case "codebuff"` → `page-codebuff` template |
| `web/templates/partials/sidebar.html` | +8 | Codebuff sidebar link |

### Adapter Fixes

| File | Change | Purpose |
|------|--------|---------|
| `internal/adapter/openai_sse.go` | +141 | Extended OpenAI SSE adapter for multi-provider support |
| `internal/adapter/openai_sse_test.go` | +103 | SSE adapter tests (8 cases) |
| `internal/codebuff/tool_input_end_test.go` | +132 | Tool input end regression tests |

---

## Architecture: Pure passthrough

After Plan 04's root cause analysis, codebuff bypasses the entire handler pipeline:

```
Client → inf-api → [extract model + select account] → codebuff upstream
                                                               ↓
Client ← inf-api ← [metadata injection + raw SSE forward] ← codebuff upstream
```

No intermediate type conversions (ClaudeRequest, prompt.Message, ContentBlock, SystemItems). The codebuff path reads raw body bytes, extracts the model name via simple JSON lookup, selects a codebuff account, injects metadata, and forwards raw SSE directly to the client.

This is the exact architecture of `freebuff2api` (244 lines of Python), replacing ~1500 lines of handler pipeline with ~200 lines of direct passthrough.

---

## Verification results

| Check | Result |
|-------|--------|
| `go build -o orchids-server ./cmd/server` | Pass (38 MB binary) |
| `go test ./internal/codebuff/...` | Pass (43 cases) |
| `go test ./internal/adapter/...` | Pass (8 SSE cases) |
| `go test ./internal/api/...` | Pass (quota + metrics suites) |
| `go vet ./...` | Clean |
| Live `/codebuff/v1/chat/completions` streaming | `id=chatcmpl-<hex24>`, `model=minimax/minimax-m3` |
| Live `/api/codebuff/pool-status` | 401 (admin auth required, expected) |
| Live telemetry dashboard (`/admin/?tab=codebuff`) | Cards load with metricsData |
| Caddy route `/codebuff/v1/...` | 200 |

Two pre-existing handler test failures (`shouldKeepToolsForWarpToolResultFollowup` and `explicitlyRequestsDeepAnalysis` in `utils_test.go`) — both predate Plan 01 and are unrelated.

---

## Plan inaccuracies corrected

### Plan 01

- **Claimed:** "~90% complete" — accurate. 10% remaining is traffic cutover + decommission + documentation.
- **Claimed:** "Admin API credential normalization — Partial" — minor. POST/PUT work; quota display was added in Plan 02.

### Plan 02

- **Claimed:** "Status: PLANNED — awaiting approval" — **INCORRECT**. All telemetry/quota code was implemented and committed. The quota store, telemetry store, pool-status endpoint, and metrics endpoint are all deployed.
- **Claimed:** "Tracking Period: 2–3 days (starting after next 07:00 UTC reset)" — never started. The infrastructure collects data passively but the empirical measurement campaign was not run.
- **Claimed:** Timeline with 5 daily phases — never executed. The 8 analysis questions (which limit hits first, real LLM budget, etc.) remain unanswered.

Plan 02's correct status: **CODE COMPLETE, ANALYSIS DEFERRED**.

### Plan 03

All claims verified accurate against `faa162e0`.

### Plan 04

All claims verified accurate against the 5 commits between `9a5f677` and `a8176a80`.

---

## Deferred items (not in scope for this release)

| Item | Plan | Priority |
|------|------|----------|
| Caddy weighted traffic cutover | 01 | HIGH — needed to decommission freebuff2api |
| `freebuff2api` decommission | 01 | MEDIUM — after 7 days stability |
| Archive `ops/freebuff2api/` | 01 | LOW — after decommission |
| Update `docs/CONTAINERS.md` and `AGENTS.md` | 01 | LOW — after decommission |
| Load testing (wrk/oha) | 01 | LOW |
| Empirical quota tracking (2-3 day campaign) | 02 | DEFERRED — code is ready |
| Non-streaming `id` fix (still `msg_*`) | 03 | LOW — cosmetic |
| `system_fingerprint` not emitted | 03 | LOW |
| Tool-call index correction for parallel tools | 03 | LOW — codebuff rarely emits parallel tools |
| Production rate limits based on findings | 02 | DEFERRED — needs tracking data first |

---

## Commits on this release

```
9b8c6b9b fix: restore Plan 01 and Plan 02 from stash untracked section
a8176a80 feat: pure passthrough codebuff + telemetry dashboard + Plan 04 retrospective
cf606675 fix: skip Anthropic lifecycle events for codebuff raw passthrough
80391b67 fix: codebuff raw SSE passthrough - no more format conversion
5790762e fix: make codebuff a pure passthrough like freebuff2api
2bb70a35 fix: pass raw OpenAI messages directly to codebuff upstream
9a5f677f fix: forward tools to codebuff upstream + fix stream parser index-to-ID mapping
faa162e0 Plan 03: OpenAI compatibility fixes for codebuff
```

---

## Post-release checklist

- [ ] Merge PR #1 into `main`
- [ ] Run Caddy weighted traffic cutover (start 5% → 100%)
- [ ] Wait 7 days for stability
- [ ] Stop `freebuff2api` systemd service
- [ ] Archive `ops/freebuff2api/`
- [ ] Update `docs/CONTAINERS.md` and `AGENTS.md`
- [ ] (Optional) Run 2-3 day empirical quota tracking campaign
- [ ] (Optional) Set production rate limits based on findings
