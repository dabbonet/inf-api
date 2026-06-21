# Plan 04 — Root cause retrospective: 8-hour codebuff debugging ordeal

**Status:** COMPLETED ✅ (deployed as of 2026-06-21 08:43 UTC)
**Branch:** wip/plan03-openai-compat-2026-06-20
**Predecessor:** Plan 03 (OpenAI Compatibility Fixes)

## What worked (solutions that actually fixed things)

### 1. Raw SSE passthrough (`streamChatRaw`)
- **File:** `internal/codebuff/provider.go:streamChatRaw`
- **What:** Bypasses StreamParser → stream_handler pipeline entirely. Reads raw SSE lines from codebuff upstream and forwards them directly to the client.
- **Why it worked:** Removed 6 layers of indirection that were corrupting the response format.

### 2. `hasReturn = true` for codebuff
- **File:** `internal/handler/handler.go`
- **What:** Tells the streamHandler that codebuff already manages its own lifecycle. Suppresses `message_start`, `message_delta`, `message_stop`, keepAlive, `forceFinishIfMissing`, and `finishResponse`.
- **Why it worked:** The Anthropic lifecycle events were being INJECTED by streamHandler, not by codebuff upstream. Setting `hasReturn = true` prevents all injection.

### 3. `rawSSEWriter` — suppress trailing `finish_reason: "stop"` after `finish_reason: "tool_calls"`
- **File:** `internal/handler/handler.go:rawSSEWriter`
- **What:** After seeing `finish_reason: "tool_calls"`, drops any subsequent chunk containing `finish_reason: "stop"`.
- **Why it worked:** Codebuff sends tool_calls then stop in the same SSE stream. Without suppression, opencode ends the conversation prematurely (it sees "stop" and thinks the model is done).

### 4. Remove `ClaudeRequest` type conversion for codebuff (direct handler)
- **File:** `internal/handler/handler.go:handleCodebuffDirect`
- **What:** Codebuff path now reads raw body bytes, extracts only model name via simple JSON lookup, and forwards everything upstream. No `ClaudeRequest`, no `prompt.Message`, no `ContentBlock`, no `SystemItems`.
- **Why it worked:** Matches freebuff2api's approach exactly — 244 lines of Python passthrough vs our ~1500 lines of handler with 6 intermediate type conversions.

### 5. Remove `cache_control` from tools entirely
- **File:** `internal/handler/caching.go` (removed lines 48-60), `internal/codebuff/payload.go` (removed lines 88-95)
- **What:** `caching.go` was adding `cache_control: {type: "ephemeral"}` to the last tool, then `payload.go` was stripping it. Completely useless round-trip.
- **Why it worked:** `cache_control` is an Anthropic-format concept. Codebuff doesn't understand it. Never add, never strip — simpler and faster.

## Root causes of the 8-hour ordeal

### Root cause #1: StreamParser → stream_handler pipeline
The handler pipeline was designed for Anthropic-format providers (Warp, Puter). For codebuff (which speaks native OpenAI format), every layer of the pipeline added incorrect transformations:

```
codebuff upstream → SSE stream → StreamParser → Anthropic events → streamHandler → OpenAIReconversion → client
```

Instead of the simple:

```
codebuff upstream → SSE stream → client
```

**Why it took 8 hours to find:** Every fix at the streamHandler level (suppress `message_start`, suppress `message_stop`, suppress `forceFinishIfMissing`) created new edge cases. The `finish_reason: "stop"` was coming from the streamHandler's `message_delta` handler, NOT from codebuff upstream. This was invisible in logs because the handler wrote the same `finish_reason` field.

### Root cause #2: Intermediate type conversions
`ClaudeRequest` → `prompt.Message` → `ContentBlock` → `SystemItems` → `upstream.UpstreamRequest` → `BuildPayload`. Five type conversions between HTTP body and upstream request. Each one mutated the data:

- `prompt.Message.UnmarshalJSON` converts `tool_calls` → `ContentBlock{Type:"tool_use"}`
- Tool role `"tool"` → role `"user"` with `ContentBlock{Type:"tool_result"}`
- System items rebuilt from scratch with different structure

**Why it took 8 hours to find:** The corruption was silent. Messages looked "similar enough" that they worked in some cases but broke in subtle ways (missing system prompts, wrong tool formats).

### Root cause #3: Anthropic lifecycle injection
The streamHandler's Anthropic lifecycle was designed for Anthropic-format providers. Codebuff's native OpenAI format doesn't have:

- `message_start` events
- `message_delta` events (with `finish_reason`)
- `message_stop` events

The streamHandler was INVENTING these events based on OpenAI chunks, then the OpenAI reconversion was trying to turn them BACK into OpenAI chunks. The `message_delta` handler was setting `finish_reason: "stop"` which then appeared in the output stream when it shouldn't have.

**Why it took 8 hours to find:** The injected events were indistinguishable from real upstream events in the logs. Both showed `finish_reason: "stop"`. We had to insert a raw logger (before any handler processing) to discover that codebuff upstream was NOT sending `finish_reason: "stop"` — the streamHandler was.

### Root cause #4: `cache_control` dance
A classic "two wrongs make a right" pattern:
1. `caching.go` adds `cache_control` to tools (for Anthropic providers)
2. `payload.go` strips `cache_control` from tools (for codebuff)

Both were unnecessary. The addition was for Anthropic caching; the stripping was a workaround because codebuff doesn't support it. Instead of questioning whether the addition was needed at all, we added the stripping as a fix — classic workaround stacking.

### Root cause #5: Missing `codebuff.html` template
The `case "codebuff"` was added to `template.go` referencing `page-codebuff` — but the actual template file was never created. This caused a 500 on the admin page at `/admin/?tab=codebuff`. Discovered and fixed in this plan.

## Architecture decision: pure passthrough

After the ordeal, the codebuff path follows freebuff2api's architecture exactly:

```
Client → inf-api → [extract model, select account] → codebuff upstream
                                                              ↓
Client ← inf-api ← [metadata injection only]      ← codebuff upstream
```

No intermediate type conversions. No handler pipeline. No Anthropic lifecycle. Just:
1. Read raw body bytes
2. Extract model name (simple JSON lookup)
3. Select codebuff account
4. Inject metadata (Buffy, stop, provider, codebuff_metadata)
5. Forward to upstream
6. Stream raw SSE back

## Files changed (this plan)

- `internal/handler/caching.go` (removed `cache_control` tool injection)
- `internal/codebuff/payload.go` (removed `cache_control` stripping)
- `internal/handler/handler.go` (added `handleCodebuffDirect` + fast path + rawSSEWriter)
- `web/templates/pages/codebuff.html` (new — admin monitoring page)

## Key insight for future providers

If a new provider speaks native OpenAI format:
1. Add an early fast path in HandleMessages (before ClaudeRequest parsing)
2. Use `RawBody` for upstream payload
3. Use `RawSSEWriter` for response streaming
4. Do NOT parse into `ClaudeRequest` or `prompt.Message`
5. Do NOT route through streamHandler or StreamParser
