# Plan 03: OpenAI-compat fixes — universal adapter bugs + codebuff-local

> Date: 2026-06-20
> Trigger: codebuff egress SSE chunks not truly OpenAI-compatible
> Reference: freebuff2api/openai_compat.py:117-147

---

## Goal

Fix the OpenAI SSE egress shape emitted by infra-api for the codebuff provider, while preserving (or improving) correctness for warp/puter/grok.

Two categories of fixes:
1. **Universal adapter bugs** — real OpenAI spec violations that affect ALL providers. Fix once, fix everywhere.
2. **Codebuff-local improvements** — id format and tool-index collisions that are codebuff-specific. Fix with a codebuff-scoped layer, no shared code changes.

---

## Cross-check summary

| Check | Result |
|---|---|
| freebuff2api reference (openai_compat.py:117-147) | Direct passthrough; always emits model, id, per-tool index |
| Our adapter (openai_sse.go:260-277) | model passed as nil -> "" on non-first chunks. Hard-coded "index":0 |
| Our tests (openai_sse_test.go) | 7/8 cases assert Model: "" — tests match buggy behaviour |
| Our tests (stream_handler_test.go) | Only ONE test uses FormatOpenAI (checks [DONE] only) |
| Shared path | writeSSE -> writeOpenAISSEBytes -> AppendOpenAIChunk — called by ALL providers |
| Codebuff-specific path | decodeChunk -> onMessage -> streamHandler.handleMessage — same shared adapter |

---

## Verified gaps vs OpenAI spec + freebuff2api

| Gap | Severity | Scope | Fix location |
|---|---|---|---|
| model empty on non-message_start chunks | HIGH | ALL providers | adapter (universal) |
| id shape msg_MS not chatcmpl-HEX | LOW | cosmetic | codebuff-local |
| Tool index hard-coded 0 | MEDIUM | ALL providers | adapter (universal) |
| system_fingerprint not emitted | LOW | optional | skip for now |
| stop ["cb_easp"] not injected | LOW | codebuff only | BuildPayload (already local) |

---

## Phase A — Universal: always emit model on every chunk

**Why universal**: The OpenAI spec requires model on every chunk. Our adapter emits "" because appendOpenAIChunkText/Thinking/ToolArgs/ToolStart/MessageDelta pass nil as quotedModel. This is a bug for all providers, not just codebuff.

**Files touched**:
- internal/adapter/openai_sse.go
- internal/adapter/openai_sse_test.go
- internal/handler/stream_handler.go (1 line: add modelHint field)

**What changes**:

1. Add `modelHint string` field to `streamHandler` struct. Set once at construction from `req.Model`.

2. Add `AppendOpenAIChunkWithModel(dst, msgID, created, event, data, modelHint)` to adapter. Wraps existing fast-path but passes quotedModel from modelHint instead of nil.

3. In `streamHandler.writeOpenAISSEBytes` (line 516), call `AppendOpenAIChunkWithModel` with h.modelHint.

4. In `appendOpenAIChunkPrefix` (line 260): if len(quotedModel) == 0, use empty `""` only when modelHint is also empty (should never happen). Remove the nil -> "" fallback for callers that should have a model.

5. All internal callers (appendOpenAIChunkText, appendOpenAIChunkThinking, etc.) gain a quotedModel []byte param, passed through from caller.

6. Update openai_sse_test.go:
   - All want.Model: "" -> want.Model: "claude-3-7-sonnet" (test model hint)
   - Benchmarks update to pass modelHint

**Test impact**: All openai_sse_test.go cases with Model: "" change to Model: "claude-3-7-sonnet". This is a correctness fix. Existing warp/puter/grok consumers will now see model: "their-model" instead of model: "" — strictly better.

---

## Phase B — Universal: fix tool-call index collisions

**Why universal**: Hard-coded "index":0 in openAIToolArgsDeltaPrefix/openAIToolStartDeltaPrefix means any provider emitting two parallel tool calls merges their arguments. Latent bug.

**Files touched**:
- internal/adapter/openai_sse.go
- internal/handler/stream_handler.go
- internal/adapter/openai_sse_test.go

**What changes**:

1. appendOpenAIChunkToolStart and appendOpenAIChunkToolArgs gain an `index int` parameter.

2. Replace hard-coded constants with dynamic formatting:
   - openAIToolStartDeltaPrefix split: prefix part + strconv.AppendInt(index) + suffix part
   - Same for openAIToolArgsDeltaPrefix

3. streamHandler writes index from its toolBlocks[toolID] map (already maintained at stream_handler.go:1010).

4. Update tests to pass explicit indices. Add multi-tool test case.

**Test impact**: Existing single-tool tests still emit index:0 (unchanged). New multi-tool test verifies distinct indices.

---

## Phase C — Codebuff-local: chatcmpl-* id format

**Why codebuff-local**: The msg_MS format is cosmetic. Warp/puter/grok clients may rely on msg_* in fixtures or logs. Don't change their behavior.

**Files touched**:
- internal/codebuff/openai_compat.go (NEW file)
- internal/handler/stream_handler.go (add setter)
- internal/codebuff/provider.go (wire rewriter)

**What changes**:

1. New `internal/codebuff/openai_compat.go`:

   ChunkRewriter struct:
   - mu          sync.Mutex
   - id          string           // "chatcmpl-<hex>"
   - toolIndices map[string]int   // toolID -> index
   - nextIndex   int

   Methods:
   - NewChunkRewriter(chatID string) *ChunkRewriter
   - CaptureToolIndices(toolCalls []any) — records upstream tool indices, assigns monotonic fallback
   - RewriteLine(raw []byte) []byte — fast byte-level substitution: replace msg_MS with chatcmpl-HEX, replace hard-coded index:0 with per-tool index

2. stream_handler.go:
   - Add field `chunkRewriter func([]byte) []byte` (default nil — no behavior change for warp/puter/grok)
   - Add method `SetChunkRewriter(fn func([]byte) []byte)`
   - In writeOpenAISSEBytes, after AppendOpenAIChunk but before writeOpenAIFrame:
     if h.chunkRewriter != nil { raw = h.chunkRewriter(raw) }

3. codebuff/provider.go:
   - In streamChat, create cr := NewChunkRewriter("chatcmpl-" + hex[:24])
   - After each parser.Next(), call cr.CaptureToolIndices(deltaToolCalls)
   - Pass cr.RewriteLine to sh via SetChunkRewriter

**Test impact**: ZERO impact on warp/puter/grok (rewriter is nil by default). New codebuff/openai_compat_test.go tests rewriter in isolation.

---

## Phase D — Codebuff-local: stop sequence injection

Already codebuff-local in internal/codebuff/payload.go:BuildPayload.

**What changes**:
1. Add "stop" to upstreamChatKeys (line 16) so client-provided stop sequences pass through.
2. After copy pass, if stop is unset: set default stop sequence per freebuff2api L104.

**Test impact**: Zero. Only affects codebuff payload construction.

---

## Phase E — Tests

**New test files**:
- internal/codebuff/openai_compat_test.go — tests ChunkRewriter.RewriteLine in isolation
- internal/codebuff/payload_test.go — tests BuildPayload stop sequence default

**Updated test files**:
- internal/adapter/openai_sse_test.go — Model field assertions, index parameter, multi-tool test
- internal/handler/stream_handler_tool_validation_test.go — verify tool index propagation through shared handler

**Test commands**:
```
cd /home/ubuntu/dabbo-state/ops/inf-api
/usr/bin/go test ./internal/adapter/... -v -count=1
/usr/bin/go test ./internal/codebuff/... -v -count=1
/usr/bin/go test ./internal/handler/... -v -count=1
```

---

## Phase F — Build, deploy, smoke

1. Build: `/usr/bin/go build -o /home/ubuntu/dabbo-state/ops/inf-api/orchids-server ./cmd/server`
2. Deploy: `sudo systemctl restart orchids-api.service`
3. Smoke: `curl -s http://127.0.0.1:3002/codebuff/v1/chat/completions -H "Authorization: Bearer ..." -d '{"model":"minimax/minimax-m3","stream":true,"messages":[{"role":"user","content":"hi"}]}' --no-buffer`
4. Verify:
   - Every chunk has non-empty "model" field
   - First chunk has "delta":{"role":"assistant"}
   - Last chunk has "finish_reason":"stop"
   - Stream ends with "data: [DONE]"
   - id format: "chatcmpl-<hex>" (not "msg_<ms>")

---

## Files touched (complete list)

**Shared (universal fixes)**:
- internal/adapter/openai_sse.go — model on every chunk + tool index param
- internal/adapter/openai_sse_test.go — updated assertions
- internal/handler/stream_handler.go — modelHint field + chunkRewriter setter (3 lines)

**Codebuff-local**:
- internal/codebuff/openai_compat.go (NEW) — ChunkRewriter
- internal/codebuff/openai_compat_test.go (NEW) — rewriter tests
- internal/codebuff/payload.go — stop key in upstreamChatKeys
- internal/codebuff/provider.go — wire ChunkRewriter into streamChat

**Not touched**:
- internal/codebuff/client.go
- internal/codebuff/quotastore.go
- internal/codebuff/telemetry.go
- internal/codebuff/runs.go
- internal/codebuff/errors.go
- internal/api/codebuff_*.go
- All warp/puter/grok provider code
- All handler code (only 3 lines added to streamHandler struct)

---

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| model change breaks warp/puter/grok clients | Strictly better: "" -> real model name. No client rejects a populated model field |
| chunkRewriter nil check in hot path | Branch predictor handles nil check in ~1ns. No measurable perf impact |
| RewriteLine byte manipulation errors | New test file covers: empty chunks, multi-tool, id replacement, edge cases |
| Build failure from adapter signature change | Existing AppendOpenAIChunk preserved; new WithModel variant added alongside |
| Restart during active requests | Existing connections drain; new requests get updated code |
