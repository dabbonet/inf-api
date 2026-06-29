# Deployment Guide

This document is based on the current code implementation and applies to the five channels: `warp`, `puter`, `grok`, `aihubmix`, and `zenmux`.

## 1. Prerequisites

- Go `1.24+`
- Redis `7+`
- Prepared `config.json`

For minimum configuration examples, see [README.md](/D:/Code/Orchids-2api/README.md) and [configuration.md](/D:/Code/Orchids-2api/docs/configuration.md).

Note:

- After startup, if `settings:config` already exists in Redis, it will override the file configuration.
- When `admin_pass` is not set, the program will automatically generate a random password and write it to the startup log.

## 2. Local Development Startup

```bash
go mod download
go run ./cmd/server -config ./config.json
```

## 3. Production Compilation and Startup

### 3.1 Linux / macOS

```bash
go build -o orchids-server ./cmd/server
./orchids-server -config ./config.json
```

Run in background:

```bash
nohup ./orchids-server -config ./config.json > server.log 2>&1 &
```

### 3.2 Windows

```powershell
go build -o server.exe ./cmd/server
.\server.exe -config .\config.json
```

Run in background:

```powershell
Start-Process -FilePath .\server.exe -ArgumentList '-config','.\config.json'
```

## 4. Restart Process

### 4.1 Linux / macOS

```bash
pkill -f "./orchids-server -config ./config.json" || true
go build -o orchids-server ./cmd/server
nohup ./orchids-server -config ./config.json > server.log 2>&1 &
```

### 4.2 Windows

```powershell
Get-Process server -ErrorAction SilentlyContinue | Stop-Process -Force
go build -o server.exe ./cmd/server
Start-Process -FilePath .\server.exe -ArgumentList '-config','.\config.json'
```

## 5. Post-startup Verification

Basic checks:

```bash
curl -s http://127.0.0.1:3002/health
curl -s http://127.0.0.1:3002/v1/models
curl -s http://127.0.0.1:3002/metrics
```

Port check:

```bash
lsof -iTCP:3002 -sTCP:LISTEN -n -P
```

Windows:

```powershell
Get-NetTCPConnection -LocalPort 3002 -ErrorAction SilentlyContinue
```

Model sync verification:

- After logging into the admin panel, call `POST /api/models/refresh`
- The current refresh is "sync by source": newly added are written, vanished from source are deleted, no more per-model liveness testing.

Recommended regression:

```bash
go test ./...
go test ./internal/handler -run "Puter_"
```

## 6. Observability and Troubleshooting

Debugging endpoints:

- `GET /health`
- `GET /metrics`
- `GET /debug/pprof/`, accessible only when `debug_enabled=true` and admin authentication is passed

Linux / macOS logs:

```bash
tail -n 200 server.log
```

Windows logs usually depend on how you start it; if started in the foreground, just check the console output.

Pay special attention to:

- `model not found`
- `no available grok token`
- `Bad Gateway`
- `stream parse error`

## 7. Upgrade Recommendations

After each upgrade, execute at least:

```bash
go test ./...
go build -o orchids-server ./cmd/server
```

If the current version mainly involves Puter or model refresh logic, it is recommended to additionally execute:

```bash
go test ./internal/handler -run "Puter_"
```

## 8. Orchestrated Deploy (recommended)

Two entry points, both lead to a healthy `orchids-api` on `:3002` running the
newly-built ARM64 binary.

### 8.1 Operator at the host terminal (host has go + sudo)

```bash
# From anywhere — script uses ${DABBO_STATE_DIR}/ops/inf-api internally.
bash /home/ubuntu/dabbo-state/ops/inf-api/scripts/deploy-inf-api.sh

# Variations:
bash /home/ubuntu/dabbo-state/ops/inf-api/scripts/deploy-inf-api.sh --build-only  # build, no restart
bash /home/ubuntu/dabbo-state/ops/inf-api/scripts/deploy-inf-api.sh --check       # 0/1 exit on /health
```

This builds ARM64 natively (`GOOS=linux GOARCH=arm64 go build`), replaces
`orchids-server`, then `sudo systemctl restart orchids-api.service`. Service
is managed by `/etc/systemd/system/orchids-api.service` with
`Restart=always`, so a crash loop is bounded by `RestartSec=5`.

### 8.2 Hermes / contained environments (no sudo, no go needed)

Orchids-server exposes an in-process build+swap endpoint at
`/api/internal/deploy`. The endpoint is sessionAuth-protected (same middleware
as `/api/*` admin endpoints), so any admin token from `config.json`
(`admin_pass` or `admin_token`) authenticates it.

```
POST /api/internal/deploy
  Headers: X-Admin-Token: <admin_pass|admin_token>, Content-Type: application/json
  Body:    {"service":"orchids-api"}
  → 202 Accepted, {"status":"started","started_at":"...","requested_by":"..."}

GET /api/internal/deploy
  Headers: X-Admin-Token: <...>
  → {"status":"idle|building|built|error","sha256":"...","size":...,
     "duration_ms":...,"requested_by":"...","last_started_at":"..."}
```

**Hermes invocation** (from inside `dabbo-hermes`):

```bash
export DABBO_DEPLOY_TOKEN=admin123                    # or use session cookie
export DEPLOY_URL=http://172.17.0.1:3002/api/internal/deploy
bash /home/ubuntu/dabbo-state/ops/inf-api/scripts/deploy-inf-api.sh --request
```

The script POSTs to orchids-api, polls `GET /api/internal/deploy` until
status moves from `building` to `built`/`error`, then exits 0/non-zero.

**What happens server-side**:

1. `POST` handler goroutine runs `go build -trimpath -o orchids-server.new ./cmd/server/`
   with `GOOS=linux GOARCH=arm64`.
2. On success, atomic `rename(orchids-server.new, orchids-server)`.
3. State (`building` and final `built`/`error` with sha256, size, duration,
   `requested_by` from `X-Forwarded-For`/`RemoteAddr`) is written to Redis at
   `orchids:internal:deploy:state` (90-day TTL) — survives process restart.
4. `os.Exit(0)` after a 750ms drain pause. `systemd`'s `Restart=always`
   brings up the new binary. Total downtime: ~1s.

**Why the HTTP path is preferred for automation**:

- One `curl` = one deploy, idempotent: a second concurrent `POST` returns
  `409 Conflict {"status":"busy"}` while a build is in flight.
- Auth gives audit: every deploy records the source IP.
- Persisted state: after a swap, a fresh process can still answer "when was
  the last deploy and what sha" from Redis.
- No `/tmp` markers, no systemd path-units, no race conditions between
  inotify and Docker volume propagation.

### 8.3 Verifying after a deploy

```bash
# Systemd says the unit is running:
systemctl status orchids-api.service --no-pager -n 5

# Orchids-api itself says /health is ok:
curl -s http://localhost:3002/health                  # {"status":"ok"}

# Last deploy record:
curl -s -H "X-Admin-Token: $DABBO_DEPLOY_TOKEN" \
     http://localhost:3002/api/internal/deploy | python3 -m json.tool

# 7d-journal of the unit for any post-deploy errors:
journalctl -u orchids-api.service --since "5 min ago" --no-pager | tail -50
```

### 8.4 Troubleshooting

| Symptom | What to check |
|---|---|
| `go build` fails inside the endpoint | `journalctl -u orchids-api.service -n 50` — slog logs the exact error |
| Endpoint returns `401 Unauthorized` | `X-Admin-Token` env value vs `admin_pass`/`admin_token` in `/home/ubuntu/dabbo-state/ops/inf-api/config.json` |
| Endpoint unreachable from container | Is port 3002 reachable from the docker bridge? `curl http://172.17.0.1:3002/health` from inside the container |
| New process won't bind :3002 | Another manual instance still up: `ss -tlnp | grep 3002` and kill it; systemd-managed bind comes back automatically |
| Deploy state gone | Redis down — `systemctl status redis-server`. Without Redis the endpoint still works but state is in-memory only |

## 9. Permissions-Cleanliness Note

Hermes used to spawn sibling containers as **root**, which left
root-owned files in `internal/` and broke Go builds (`EACCES` on cached
file metadata, even when files are world-readable). Two mitigations:

- `ops/hermes/config.yaml`: `docker_run_as_host_user: true` makes
  hermes-spawned child containers run as `uid 1000` (ubuntu) so file edits
  land on the host filesystem owned by ubuntu.
- `deploy-inf-api.sh` (host path) auto-chowns `ops/inf-api/` (excluding
  `.git` and the binary) to the invoking user before `go build`. Cheap
  defense vs old root-owned artifacts that may be lingering.

