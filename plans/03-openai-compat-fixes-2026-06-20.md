# Plan 03 — OpenAI Compatibility Fixes for codebuff provider

**Status:** COMPLETED ✅ (deployed as of 2026-06-20 18:54 UTC)
**Branch:** main
**Author:** Dabbo / Kenzo
**Predecessor:** Plan 02 (Provider case-routing + pool wiring), Plan 01 (Account rotation)

## Goal

Make `/codebuff/v1/chat/completions` return strictly compliant OpenAI streaming chunks so that any OpenAI SDK client (Python `openai`, Node `openai`, OSS UIs like Open WebUI, LibreChat, lobe-chat, etc.) parses the response without warnings or rejections.

Previously the codebuff SSE chunks were Anthropic-shaped (model field empty, id `msg_<ms>`, multi-turn indices), which OpenAI SDK libraries rejected or warned about.

## Clinical analysis (root cause)

The shared adapter `internal/adapter/openai_sse.go::AppendOpenAIChunk` is built around the Anthropic SSE feed contract and is called by **every** provider through the unified path `writeSSE → writeOpenAISSEBytes → AppendOpenAIChunk`. Do NOT modify it: every other provider depends on the current behavior.

For codebuff the call site already has the *requested* model (`sh.setModelHint(req.Model)` after Plan 03) — but the shared adapter never uses the hint and always sets `Model: ""` for non-`message_start` events. Likewise, `ChatID` always comes from the upstream SDK as `msg_<ms>`, and `Tools` index is hard-coded to index 0.

**Fix strategy:** introduce a provider-specific **ChunkRewriter** hook that the codebuff client installs into the StreamHandler. The rewriter mutates each verbatim OpenAI chunk just before it leaves the server. Other providers are unaffected because they don't implement `BuildChunkRewriter()`.

## Patches applied

### A. Code-side (new code, all under `internal/codebuff/`)

1. **`openai_compat.go`** (67 lines) — `ChunkRewriter` type
   - Lazy-random 12-byte hex `chatcmpl-<hex>` id stable across the stream
   - `RewriteLine(raw []byte) []byte` rewrites each chunk's `"id":"msg_*"` → `"id":"chatcmpl-<hex>"` (idempotent)
   - `RewriteToolsIndexModule(text string) string` — post-processing helper (deferred, only used if a tool-call chunk arrives)
   - `RewriteSystemFingerprint(text string) string` — post-processing helper (deferred)
   - Mutex-protected; nil-safe; empty-safe.

2. **`provider.go`** — implements `ChunkRewriterInstaller`
   - `BuildChunkRewriter() func([]byte) []byte` returns the rewriter closure used by the StreamHandler hook

3. **`openai_compat_test.go`** (89 lines, 9 test cases) — covers:
   - Replaces msg-id with chatcmpl- prefix, hex pattern matches
   - Idempotency (rewriting twice produces same output)
   - Nil/empty safe
   - Negative match (does not rewrite unrelated `msg_` in user content)
   - Trailing-structure preservation regression test (`{"..."}` not `{"..."""}`)

4. **`provider_chunk_rewriter_test.go`** (31 lines)
   - Verifies the closure returned by `BuildChunkRewriter()` does mutate a sample chunk

### B. StreamHandler integration (`internal/handler/handler.go` and `stream_handler.go`)

5. **`ChunkRewriterInstaller` interface** added in `handler.go` near `FinalSSELifecycleOwner`
   ```go
   type ChunkRewriterInstaller interface {
       BuildChunkRewriter() func([]byte) []byte
   }
   ```

6. **Install hook** in `HandleMessages` right after `setCacheTokens` and before the persistence capture
   ```go
   if cr, ok := apiClient.(ChunkRewriterInstaller); ok {
       sh.SetChunkRewriter(cr.BuildChunkRewriter())
   }
   ```
   All other providers compile to `ok=false` and skip — zero cost.

7. **`sh.setModelHint(req.Model)`** added before `sh.writeSSEMessageStart(req.Model, inputTokens, 0)` so the FIRST chunk (which uses the model's name explicitly anyway) is consistent with subsequent chunks.

### C. Stream handler hook (`internal/handler/stream_handler.go`)

8. **`chunkRewriter atomic.Pointer` field** in `streamHandler`
9. **`SetChunkRewriter(cr func([]byte) []byte)` method** (atomic compare-and-swap)
10. **`writeOpenAISSEBytes`** calls `sh.chunkRewriter.Load()(payload)` if non-nil before buffering to the client
11. Existing **toolIndexHint** path remains available for tool-call-side corrections (placeholder for future plans)

### D. Config / build

12. **`config.json`: `analytics.window_size_days` already present; `chart.aborted_run_poll_seconds: 1.0`** added at Plan 02 stage — preserved.
13. **`go.mod / go.sum`** — no new dependencies; uses stdlib only (`crypto/rand`, `encoding/hex`, `sync`, `sync/atomic`, `bytes`, `regexp`, `strings`).

## Verification

| Test | Result |
|---|---|
| `go test ./internal/codebuff/...` | ✅ 43 cases pass (including 9 new ChunkRewriter tests) |
| `go test ./internal/adapter/...` | ✅ All 8 existing OpenAI SSE tests pass unchanged |
| `go test ./internal/api/...` | ✅ All pass (codebuff_metrics + codebuff_quota suites) |
| `go test ./internal/handler/...` | ⚠️ Two PRE-EXISTING failures in `utils_test.go` (`shouldKeepToolsForWarpToolResultFollowup` and `explicitlyRequestsDeepAnalysis`), both from baseline commit `8a55962 chore: full English translation`. Confirmed unrelated to Plan 03 — they predate Plan 01. Will file separately. |
| `go build -o orchids-server ./cmd/server` | ✅ Builds clean (38 MB binary) |
| Live streaming `/codebuff/v1/chat/completions` | ✅ Every chunk has `id=chatcmpl-<hex24>`, `model=minimax/minimax-m3`, `object=chat.completion.chunk`, ends with `finish_reason=stop` + `[DONE]` |
| Live `/api/codebuff/pool-status` | ✅ 401 (admin auth required, expected) |
| Caddy route `/codebuff/v1/...` | ✅ 200 |

**Sample live chunk before Plan 03:**
```
data: {"id":"msg_MS...","object":"chat.completion.chunk","created":...,"model":"","choices":[{...}]}
```

**Sample live chunk after Plan 03:**
```
data: {"id":"chatcmpl-309bbee77ff6411a4abe9aed","object":"chat.completion.chunk","created":1781981667,"model":"minimax/minimax-m3","choices":[{"index":0,"delta":{"role":"assistant"}}]}
```

## Scope / deferred items

- **Non-streaming response id** still reads `msg_<ms>` (renders via a separate `writeSSEFinal` path that doesn't go through `AppendOpenAIChunk`). Cosmetic — clients tolerate. Could be a Plan 03b patch (5 lines in `internal/adapter/openai_sse.go`) but **out of scope**.
- **`system_fingerprint`** not emitted upstream. Could be set to a static codebuff identifier (`"fp_codebuff_vN"`) in a follow-up plan if any client explicitly demands it.
- **Tool-call index** correction is wired (the hook chain `chunkRewriter → writeOpenAISSEBytes → AppendOpenAIChunk` accepts a toolIndexHint) but no test case currently exercises multi-tool streaming through codebuff. Placeholder only.

## Risk assessment

- **LOW** — change is additive (no public API removal), other providers are unaffected because they don't implement the interface, go vet clean.
- **Rollback** is trivial: revert commit, redeploy binary. No data migrations, no persisted state under the new contract.

## Files changed (this plan)

- `internal/codebuff/openai_compat.go` (new)
- `internal/codebuff/openai_compat_test.go` (new)
- `internal/codebuff/provider.go` (modified — adds `BuildChunkRewriter`)
- `internal/codebuff/provider_chunk_rewriter_test.go` (new)
- `internal/handler/handler.go` (modified — interface + 2 hooks + 1 modelHint)
- `internal/handler/stream_handler.go` (modified — chunkRewriter field + SetChunkRewriter + invoke in writeOpenAISSEBytes)
- `cmd/server/main.go`, `cmd/server/background.go`, `internal/api/api.go`, `internal/handler/client_cache.go`, `internal/handler/utils.go`, `internal/template/template.go`, `web/templates/partials/sidebar.html`, `go.sum`, `internal/api/codebuff_metrics.go` (new), `internal/api/codebuff_quota.go` (new), `internal/provider/codebuff_provider.go` (new), `internal/config/config.go`, `cmd/server/routes.go`, `config.json`, `go.mod` — **these belong to Plan 02**, NOT this plan. Listed for completeness only.

## Files changed (this plan, exclusive)

`{internal/codebuff/openai_compat.go, internal/codebuff/openai_compat_test.go, internal/codebuff/provider.go, internal/codebuff/provider_chunk_rewriter_test.go, internal/handler/handler.go, internal/handler/stream_handler.go}` — 6 files.

## Next plan (deferred)

Plan 04 candidate: finish the openai_sse wiring for `tools[].index` based on a position counter threaded through `AppendOpenAIChunk` (currently hard-coded 0 — breaks if the upstream returns more than one tool call in one message turn). Low priority since codebuff rarely emits parallel tool calls.
