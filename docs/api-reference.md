# API Reference

This document is based on the current implementations of [routes.go](/D:/Code/Orchids-2api/cmd/server/routes.go) and [model_refresh.go](/D:/Code/Orchids-2api/cmd/server/model_refresh.go).

## 1. Public Interfaces

### 1.1 Claude Messages Style

| Path | Method | Description |
|---|---|---|
| `/orchids/v1/messages` | POST | Orchids channel Claude Messages proxy |
| `/warp/v1/messages` | POST | Warp channel Claude Messages proxy |
| `/puter/v1/messages` | POST | Puter channel Claude Messages proxy |
| `/*/v1/messages/count_tokens` | POST | Input token estimation |

### 1.2 OpenAI Chat Completions Style

| Path | Method | Description |
|---|---|---|
| `/orchids/v1/chat/completions` | POST | Orchids OpenAI compatible entry |
| `/warp/v1/chat/completions` | POST | Warp OpenAI compatible entry |
| `/puter/v1/chat/completions` | POST | Puter OpenAI compatible entry |
| `/grok/v1/chat/completions` | POST | Grok OpenAI compatible entry |
| `/v1/chat/completions` | POST | Grok compatible alias |

### 1.3 Grok Images and Files

| Path | Method | Description |
|---|---|---|
| `/grok/v1/images/generations` | POST | Image generation |
| `/grok/v1/images/edits` | POST | Image editing |
| `/v1/images/generations` | POST | Grok image generation alias |
| `/v1/images/edits` | POST | Grok image editing alias |
| `/grok/v1/files/{image\|video}/{name}` | GET | Local cached media file |
| `/v1/files/{image\|video}/{name}` | GET | Grok file alias |

### 1.4 Models, Health and Metrics

| Path | Method | Description |
|---|---|---|
| `/v1/models` | GET | All-channel model list |
| `/v1/models/{id}` | GET | Query single model |
| `/orchids/v1/models` | GET | Orchids model list |
| `/warp/v1/models` | GET | Warp model list |
| `/puter/v1/models` | GET | Puter model list |
| `/grok/v1/models` | GET | Grok model list |
| `/health` | GET | Health check |
| `/metrics` | GET | Prometheus metrics |

## 2. Admin Interfaces

### 2.1 `/api/*`

| Path | Method | Description |
|---|---|---|
| `/api/login` | POST | Admin login |
| `/api/logout` | POST | Admin logout |
| `/api/accounts` | GET/POST | Account list / Create account |
| `/api/accounts/{id}` | GET/PUT/DELETE | Query / Update / Delete account |
| `/api/accounts/{id}/check` | GET | Account check |
| `/api/accounts/{id}/usage` | GET | Account usage |
| `/api/keys` | GET/POST | API Key list / Create |
| `/api/keys/{id}` | GET/PUT/DELETE | API Key details / Update / Delete |
| `/api/models` | GET/POST | Model list / Create model |
| `/api/models/{id}` | GET/PUT/DELETE | Model details / Update / Delete |
| `/api/models/refresh` | POST | Refresh model list by channel |
| `/api/export` | GET | Export accounts and models |
| `/api/import` | POST | Import accounts and models |
| `/api/config` | GET/POST | View / Update configuration |
| `/api/config/list` | GET | Read admin form configuration |
| `/api/config/save` | POST | Save admin form configuration |
| `/api/config/cache/clear` | POST | Clear prompt/token cache |
| `/api/token-cache/stats` | GET | Token cache stats |
| `/api/token-cache/clear` | POST | Clear token cache |

### 2.2 `/api/v1/admin/*` and `/v1/admin/*`

These paths are Grok admin capabilities and grok2api alignment aliases, both prefixes are usable.

| Path | Method | Description |
|---|---|---|
| `/config` | GET/POST | Manage configuration |
| `/verify` | GET | Grok admin verification |
| `/storage` | GET | Grok storage information |
| `/tokens` | GET/POST | Grok token pool |
| `/tokens/refresh` | POST | Sync refresh tokens |
| `/tokens/refresh/async` | POST | Async refresh tokens |
| `/tokens/nsfw/enable` | POST | Sync enable NSFW |
| `/tokens/nsfw/enable/async` | POST | Async enable NSFW |
| `/batch/{task}` | GET/POST | Batch task flow and cancellation |
| `/cache` | GET | Cache summary |
| `/cache/list` | GET | Cache list |
| `/cache/clear` | POST | Clear cache |
| `/cache/item/delete` | POST | Delete single cache item |
| `/cache/online/clear` | POST | Remote cache cleanup |
| `/cache/online/clear/async` | POST | Remote cache async cleanup |
| `/cache/online/load/async` | POST | Remote cache async load |
| `/voice/token` | GET | Voice token |
| `/imagine/start` | POST | Start imagine |
| `/imagine/stop` | POST | Stop imagine |
| `/imagine/sse` | GET | Imagine SSE |
| `/imagine/ws` | GET | Imagine WebSocket |
| `/video/start` | POST | Start video task |
| `/video/stop` | POST | Stop video task |
| `/video/sse` | GET | Video SSE |

### 2.3 `/api/v1/public/*` and `/v1/public/*`

| Path | Method | Description |
|---|---|---|
| `/verify` | GET | Public verification interface |
| `/voice/token` | GET | Public voice token |
| `/imagine/config` | GET | Imagine configuration |
| `/imagine/start` | POST | Start imagine |
| `/imagine/stop` | POST | Stop imagine |
| `/imagine/sse` | GET | Imagine SSE |
| `/imagine/ws` | GET | Imagine WebSocket |
| `/video/start` | POST | Start video task |
| `/video/stop` | POST | Stop video task |
| `/video/sse` | GET | Video SSE |

## 3. Authentication Methods

### 3.1 Admin Interfaces

Any of the following conditions must be met:

1. `session_token` cookie
2. `Authorization: Bearer <admin_token>`
3. `X-Admin-Token: <admin_token>`
4. Basic Auth, password equals `admin_pass`

### 3.2 Public Interfaces

- Normal proxy interfaces do not force authentication by default, usually controlled by upper-layer gateways
- `/api/v1/public/*` and `/v1/public/*` will authenticate based on current `public_key` / `public_enabled` logic

## 4. Request Semantics Description

### 4.1 Claude Messages Non-streaming Tool Calls

When the model wants to call a tool, the non-streaming response directly returns the `tool_use` block in the `content` array, rather than empty content.

Currently regression-covered Puter scenarios:

- `Read`
- `Write`
- `Edit`
- `Delete`
- Long context
- Multi-round `tool_result`

### 4.2 `tool_result` follow-up

A follow-up request with `tool_result` has two normal outcomes:

1. Continues to return new `tool_use`
2. Converges to final `text`

The current implementation will not mistakenly judge "empty content" as valid output just because upstream usage tokens have been generated.

### 4.3 Model Refresh

`POST /api/models/refresh` Example:

```bash
curl -s http://127.0.0.1:3002/api/models/refresh \
  -H 'Content-Type: application/json' \
  -d '{"channel":"puter"}'
```

Response fields:

| Field | Description |
|---|---|
| `channel` | Current refresh channel |
| `source` | Model discovery source |
| `discovered` | Number of models discovered in source |
| `verified` | Number of models included in sync set |
| `added` | Number of additions in this round |
| `updated` | Number of updates in this round |
| `deleted` | Number of deletions in this round |
| `default_model_id` | Current default model |
| `added_model_ids` | List of added model IDs |
| `deleted_model_ids` | List of deleted model IDs |

Notes:

- The current refresh is "source synchronization", not individual model liveness testing
- Models unobtainable from the source will be deleted

## 5. Common Request Examples

### 5.1 Orchids Claude Messages

```bash
curl -s http://127.0.0.1:3002/orchids/v1/messages \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "claude-sonnet-4-6",
    "messages": [{"role":"user","content":"hello"}],
    "stream": false
  }'
```

### 5.2 Puter Claude Messages First Round Tool Call

```bash
curl -s http://127.0.0.1:3002/puter/v1/messages \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "claude-opus-4-5",
    "messages": [{"role":"user","content":"Read README.md"}],
    "tools": [{
      "name": "Read",
      "input_schema": {
        "type": "object",
        "properties": {
          "file_path": {"type": "string"}
        },
        "required": ["file_path"]
      }
    }],
    "stream": false
  }'
```

### 5.3 Grok Chat Completions

```bash
curl -s http://127.0.0.1:3002/grok/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{
    "model": "grok-4",
    "messages": [{"role":"user","content":"introduce yourself"}],
    "stream": false
  }'
```

## 6. Error Conventions

- `400`: Request parameter error, model error, method error
- `401` / `403` / `429`: Account status or authentication status error
- `502`: Upstream request failed or stream parsing failed
- `503`: No available accounts for the current channel

Common errors:

- `model not found`
- `puter API error: ...`
- `Bad Gateway`
- `stream parse error`
