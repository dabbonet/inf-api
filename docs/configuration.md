# Configuration Guide

## 1. Loading Rules

Configuration loading order:

1. File specified by startup parameter `-config`
2. If not specified, sequentially search for `config.json` -> `config.yaml` -> `config.yml`
3. Read file and apply default values
4. If `settings:config` exists in Redis, overwrite the file configuration with the configuration saved in Redis
5. Finally, always execute `config.ApplyHardcoded()` to rewrite a batch of fixed runtime values

Notes:

- YAML only supports flat `key: value`
- Not all historical fields can still take effect through the configuration file

## 2. Fields Settable in Configuration File

These fields below will be persisted from the configuration file or admin interface to Redis.

### 2.1 Service and Admin Panel

| Field | Default Value | Description |
|---|---|---|
| `port` | `3002` | Service listening port |
| `debug_enabled` | `false` | Enable debug logging |
| `verbose_diagnostics` | `false` | Detailed diagnostic logs |
| `admin_user` | `admin` | Admin panel username |
| `admin_pass` | Auto-generated | Admin panel password, explicitly setting is recommended |
| `admin_path` | `/admin` | Admin panel path |
| `admin_token` | Empty | Admin panel static token |

### 2.2 Redis

| Field | Default Value | Description |
|---|---|---|
| `store_mode` | `redis` | Currently only supports `redis` |
| `redis_addr` | Empty | Redis address, e.g., `127.0.0.1:6379` |
| `redis_password` | Empty | Redis password |
| `redis_db` | `0` | Redis DB |
| `redis_prefix` | `orchids:` | Redis key prefix |

### 2.3 Cache

| Field | Default Value | Description |
|---|---|---|
| `cache_token_count` | `false` | Whether to cache token counts |
| `cache_ttl` | `5` | General cache TTL (minutes) |
| `cache_strategy` | `mix` | Cache strategy |
| `enable_token_cache` | `false` | Whether to enable token cache |
| `token_cache_ttl` | `300` | Token cache TTL (seconds) |
| `token_cache_strategy` | `1` | Token cache strategy |

### 2.4 Proxy

| Field | Default Value | Description |
|---|---|---|
| `proxy_http` | Empty | HTTP proxy |
| `proxy_https` | Empty | HTTPS proxy |
| `proxy_user` | Empty | Proxy username |
| `proxy_pass` | Empty | Proxy password |
| `proxy_bypass` | Empty array | Direct connection domains or subnets |

## 3. Runtime Hardcoded Defaults

These values are forcibly overwritten by `ApplyHardcoded()` in [config.go](/D:/Code/Orchids-2api/internal/config/config.go), do not expect to change them solely through the configuration file.

| Field | Current Value | Description |
|---|---|---|
| `output_token_mode` | `final` | Output token statistics mode |
| `context_max_tokens` | `100000` | Context limit |
| `context_summary_max_tokens` | `800` | Summary limit |
| `context_keep_turns` | `6` | Session keep turns |
| `grok_api_base_url` | `https://grok.com` | Grok base URL |
| `warp_disable_tools` | `false` | Warp tools enabled by default |
| `warp_max_tool_results` | `10` | Warp max tool results per turn |
| `warp_max_history_messages` | `20` | Warp history message limit |
| `stream` | `true` | Chat streams by default |
| `image_nsfw` | `true` | Public imagine NSFW enabled by default |
| `public_enabled` | `true` | Public pages enabled by default |
| `image_final_min_bytes` | `100000` | Imagine final image threshold |
| `image_medium_min_bytes` | `30000` | Imagine intermediate image threshold |
| `max_retries` | `3` | Maximum request retries |
| `retry_delay` | `1000` | Base retry delay (milliseconds) |
| `request_timeout` | `600` | Request timeout (seconds) |
| `retry_429_interval` | `60` | 429 retry interval (seconds) |
| `token_refresh_interval` | `1` | Token automatic refresh interval (minutes) |
| `auto_refresh_token` | `true` | Automatically refresh account token |
| `load_balancer_cache_ttl` | `5` | Load balancer cache TTL (seconds) |
| `concurrency_limit` | `100` | Concurrency limit |
| `concurrency_timeout` | `300` | Concurrency wait timeout (seconds) |
| `adaptive_timeout` | `true` | Adaptive timeout |

## 4. Minimum Usable Configuration

```json
{
  "port": "3002",
  "store_mode": "redis",
  "redis_addr": "127.0.0.1:6379",
  "admin_user": "admin",
  "admin_pass": "change-me",
  "admin_path": "/admin",
  "debug_enabled": true
}
```

## 5. Configuration Save Entry Points

The admin panel has two sets of commonly used configuration interfaces:

| Path | Method | Description |
|---|---|---|
| `/api/config` | GET/POST | Read directly / overwrite the entire configuration object |
| `/api/config/list` | GET | Read admin form configuration |
| `/api/config/save` | POST | Save admin form configuration via patch |

## 6. Important Notes

- If `admin_pass` is left empty, a random password will be automatically generated at startup and written to the log
- After the configuration is saved in Redis, subsequent restarts will prioritize the Redis version
- Directories like `data/tmp` and `debug-logs` are runtime products, not configuration items
- Many historical fields, even if they still appear in old configurations, will not change current runtime behavior

## 7. Recommended Historical Fields to Cleanup

It is not recommended to keep the following old fields in the configuration file:

- `summary_cache_*`
- `tool_call_mode`
- `warp_tool_call_mode`
- `disable_tool_filter`
