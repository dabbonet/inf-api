# Architecture Design

## 1. Overview

The API server currently consists of two main processing chains:

- `internal/handler`: Processes `warp`, `puter`
- `internal/grok`: Processes `grok`

Overall goals:

- Expose unified Claude Messages and OpenAI compatible interfaces externally
- Internally maintain account pools, model tables, failure switching, and session states by channel
- Use Redis to save accounts, models, configurations, API Keys, and cache-related states

## 2. Current Directory Structure

```text
├── cmd/server/                  # Program entry, routing, model refresh
├── internal/
│   ├── api/                     # Admin REST API
│   ├── auth/                    # Manage sessions
│   ├── config/                  # Configuration loading and default values
│   ├── debug/                   # Debug logs
│   ├── errors/                  # Error classification
│   ├── grok/                    # Grok chat/images/files/admin
│   ├── handler/                 # Warp/Puter main processor
│   ├── loadbalancer/            # Account selection and status management
│   ├── middleware/              # trace/log/session/concurrency
│   ├── provider/                # Registry mapping channels to clients
│   ├── prompt/                  # Shared message structure
│   ├── puter/                   # Puter upstream client
│   ├── store/                   # Redis storage
│   ├── template/                # Admin page templates
│   ├── tokencache/              # token / prompt cache
│   ├── upstream/                # Unified upstream event structure
│   ├── util/                    # General utilities
│   └── warp/                    # Warp upstream client
├── web/                         # Embedded frontend resources
└── docs/
```

## 3. Routing and Responsibility Layering

### 3.1 `cmd/server/routes.go`

Responsible for unified registration:

- `/*/v1/messages`
- `/*/v1/chat/completions`
- `/*/v1/models`
- `/api/*`
- `/api/v1/admin/*` / `/v1/admin/*`
- `/api/v1/public/*` / `/v1/public/*`

### 3.2 `internal/handler`

Responsible for `warp` / `puter`:

- Parse Claude/OpenAI requests
- Identify channel and target model
- Maintain session state, workdir, deduplication, and token statistics
- Select account, switch failed accounts, and retry
- Convert upstream SSE / direct events to Claude or OpenAI compatible responses

### 3.3 `internal/grok`

Responsible for `grok`:

- Chat Completions
- Image generation and editing
- Local media cache files
- Grok management interfaces and public imagine/video/voice capabilities

### 3.4 `internal/loadbalancer`

Core responsibilities:

- Filter available accounts by channel
- Record connections and failure status
- Trigger cooldown and recovery
- Select the most suitable account currently for the request

### 3.5 `internal/store`

Unified storage:

- Accounts
- Models
- API Keys
- Configuration snapshots

## 4. Main Request Flow

### 4.1 `warp` / `puter`

```text
HTTP Request
  -> middleware chain
  -> Handler.HandleMessages
  -> parse request + dedup
  -> resolve channel + model
  -> load session/workdir state
  -> select account from LoadBalancer
  -> build UpstreamRequest
  -> send to channel client
  -> stream_handler converts upstream events
  -> write Claude/OpenAI compatible response
  -> sync account/session/cache stats
```

### 4.2 `grok`

```text
HTTP Request
  -> middleware chain
  -> grok.Handler
  -> validate request/model
  -> select grok account
  -> call upstream
  -> normalize stream / image / file result
  -> write OpenAI-compatible response
```

## 5. Model Management Flow

Model refresh entry: `POST /api/models/refresh`

Sources by channel:

- `warp`: Account GraphQL discovery results, fallback to built-in seeds on failure
- `puter`: Puter public model list
- `grok`: Built-in support table + existing models + public documentation probing
- `aihubmix`: Public `/api/v1/models` (no-auth) plus 3 built-in seeds, filtered to remove embeddings/moderation
- `zenmux`: Account-authenticated `/v1/models`, fallback to 1 built-in seed, filtered to remove embeddings/moderation/whisper/tts

Current strategy:

- Newly discovered models are written to the local table
- Models missing from the source are deleted from the local table
- No individual model online liveness testing is performed

## 6. Puter Current Implementation Key Points

Puter goes through `internal/puter`, characteristics are:

- Upstream is a text stream, the client extracts `<tool_call>...</tool_call>` from the text
- The handler layer reassembles the result into Claude Messages style `tool_use` blocks
- Non-streaming `tool_use` and `tool_result` follow-up are covered by regression tests

## 7. Runtime State

### 7.1 Saved in Redis

- Accounts, models, API Keys, configurations
- Optional token cache / prompt cache
- When available, handler prioritizes Redis for session and deduplication storage

### 7.2 Local Directories

- `debug-logs/`: Debug logs
- `data/tmp/image`: Grok image cache
- `data/tmp/video`: Grok video cache

## 8. Current Known Design Boundaries

- Many runtime defaults are forcibly written by `config.ApplyHardcoded`, not all fields can be overridden by configuration files
- `/metrics` is public by default, in production it's recommended to place it on an internal network or behind an additional gateway
- Cache files are generated in `data/tmp` during runtime and should not be committed to Git
