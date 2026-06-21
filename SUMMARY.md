---
**[2026-06-20 18:54 UTC — Plan 03 OpenAI Compatibility Fixes DEPLOYED]**

Codebuff provider now emits strict OpenAI-compatible streaming chunks. Mechanism: ChunkRewriter hook (`internal/codebuff/openai_compat.go`, ~67 lines) installed via `ChunkRewriterInstaller` interface in `handler.go`; `stream_handler.go`'s `writeOpenAISSEBytes` calls the rewriter on every chunk before flushing to the client. Shared adapter `internal/adapter/openai_sse.go` is NOT touched — other providers unaffected because they don't implement the interface.

Files this plan (exclusive to Plan 03): {internal/codebuff/openai_compat.go, internal/codebuff/openai_compat_test.go, internal/codebuff/provider.go, internal/codebuff/provider_chunk_rewriter_test.go, internal/handler/handler.go (added ChunkRewriterInstaller interface, BuildChunkRewriter call site, setModelHint line), internal/handler/stream_handler.go (chunkRewriter atomic.Pointer field, SetChunkRewriter method, invoke in writeOpenAISSEBytes)}.

Tests: 43/43 in `internal/codebuff` pass (9 new ChunkRewriter tests including idempotency/closing-quote-regression). 8/8 in `internal/adapter` unchanged. 2 PRE-EXISTING failures in `internal/handler/utils_test.go` (`shouldKeepToolsForWarpToolResultFollowup`, `explicitlyRequestsDeepAnalysis`) are from commit `8a55962` (chore: full English translation) — unrelated to Plan 03, will file separately.

Live verified:
  - GET /codebuff/v1/chat/completions (streaming): chunks now have `id=chatcmpl-<24hex>`, `model=minimax/minimax-m3`, `object=chat.completion.chunk`, finish_reason="stop" at end, [DONE] as last frame.
  - GET /api/codebuff/pool-status: 401 (admin auth needed, expected).

Non-stream id still reads `msg_<ms>` (separate `writeSSEFinal` path) — cosmetic, deferred to future plan.

Plan doc: `ops/inf-api/plans/03-openai-compat-fixes-2026-06-20.md`.
