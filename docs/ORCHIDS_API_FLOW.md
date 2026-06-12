# Request Flow (Legacy Orchids Channel Removed)

> **Note:** The `orchids` channel has been removed from this codebase. This file previously documented the Orchids-2api request flow but has been updated to reflect the current supported channels: `warp`, `puter`, and `grok`.

## 1. Startup Process

```text
main.go
  -> Read configuration file
  -> Apply default values and hardcoded runtime configurations
  -> Initialize logging
  -> Initialize Redis Store
  -> If settings:config exists in Redis, override file configuration
  -> Initialize LoadBalancer
  -> Initialize Handler and Grok Handler
  -> Initialize token cache / prompt cache
  -> Initialize session / dedup / audit (when Redis is available)
  -> Register provider registry
  -> Register routes
  -> Start background token refresh / auth cleanup
  -> Listen on port
```

Key points:

- Configuration will eventually go through `ApplyHardcoded()`, not all fields can be overridden by the configuration file
- Models, accounts, configurations, and API Keys are persisted by Redis
- When `settings:config` exists, it takes precedence over the local `config.json`

## 2. External Entry Points

### 2.1 Claude Messages

- `/warp/v1/messages`
- `/puter/v1/messages`

### 2.2 OpenAI Chat Completions

- `/warp/v1/chat/completions`
- `/puter/v1/chat/completions`
- `/grok/v1/chat/completions`
- `/v1/chat/completions`, Grok compatible alias

### 2.3 Models, Images, Files and Public Capabilities

- `/v1/models`
- `/health`
- `/metrics`
- `/grok/v1/images/generations`
- `/grok/v1/images/edits`
- `/grok/v1/files/*`
- `/api/*`
- `/api/v1/admin/*`, `/v1/admin/*`
- `/api/v1/public/*`, `/v1/public/*`

## 3. `warp` / `puter` Processing Flow

These channels uniformly use [handler.go](/D:/Code/Orchids-2api/internal/handler/handler.go) and [stream_handler.go](/D:/Code/Orchids-2api/internal/handler/stream_handler.go).

```text
HTTP Request
  -> SecurityHeaders / Trace / Logging / ConcurrencyLimiter
  -> HandleMessages
  -> Parse Claude/OpenAI request
  -> Request deduplication
  -> Identify channel based on path
  -> Verify if the model is available in the local model table
  -> Read or create session state
  -> LoadBalancer selects account
  -> provider registry creates corresponding upstream client
  -> Assemble upstream payload
  -> Request upstream
  -> stream_handler converts events
  -> Output Claude/OpenAI compatible response
  -> Update account status, session, cache and statistics
```

### 3.1 Failure Retry

When the error belongs to a retryable type:

- Mark current account status
- Exclude failed account
- Reselect account
- Return error after reaching retry limit

### 3.2 Puter Special Points

Although Puter also uses the unified handler, its upstream characteristics are different:

- Tool calls returned by upstream need to be reassembled into Claude Messages `tool_use`
- Non-streaming responses must retain the `tool_use` block
- `tool_result` follow-up can continue to initiate the next round of tool calls, or converge into text
- Currently has `Read`, `Write`, `Edit`, `Delete`, long context, multi-round `tool_result` regression tests

## 4. `grok` Processing Flow

Grok uses an independent [handler.go](/D:/Code/Orchids-2api/internal/grok/handler.go).

### 4.1 Chat Completions

```text
/grok/v1/chat/completions
  -> Verify request and model
  -> Select grok account
  -> Extract text and attachments
  -> Request grok upstream
  -> Parse streaming/non-streaming events
  -> Convert to OpenAI Chat Completions
```

### 4.2 Image Generation and Editing

```text
/grok/v1/images/generations | /edits
  -> Verify model and parameters
  -> Request grok upstream
  -> Extract candidate image URLs
  -> Filter invalid candidates
  -> Download and cache to data/tmp/image
  -> Return local file URL or b64_json
```

### 4.3 Local File Service

`GET /grok/v1/files/{image|video}/{name}` and `/v1/files/*`:

- Only allow `image` or `video`
- Perform path security validation
- Read cached files from `data/tmp`

## 5. Model Refresh Flow

Refresh entry: `POST /api/models/refresh`

```text
Admin Request
  -> SessionAuth
  -> makeModelRefreshHandler
  -> Select source by channel
  -> Pull candidate model list
  -> Select default model
  -> Write new model to local table
  -> Update status/sorting/default mark for existing models
  -> Directly delete local models that have disappeared from the source
  -> Return added / updated / deleted / verified
```

Current sources:

- `warp`: Account GraphQL discovery, fallback to seed models on failure
- `puter`: Public model list
- `grok`: Built-in supported models + existing models + public documentation probing

## 6. Admin and Public Interface Flow

### 6.1 Admin

```text
/api/login
  -> Verify admin_user/admin_pass
  -> Write session_token cookie

/api/*
  -> SessionAuth
  -> Account / Model / Config / token cache management
```

### 6.2 Public / Admin Alias

- `SessionAuth` supports cookie, Bearer, `X-Admin-Token`, Basic Auth
- Public API uses `public_key` for authentication
- If `public_key` is empty, public API defaults to no authentication

## 7. Observability

- `/health`
- `/metrics`
- `/debug/pprof/`, only in debug mode and requires admin authentication

Common troubleshooting keywords:

- `model not found`
- `no available grok token`
- `stream parse error`
- `Bad Gateway`
