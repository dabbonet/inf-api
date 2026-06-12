# Deployment Guide

This document is based on the current code implementation and applies to the three channels: `warp`, `puter`, and `grok`.

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
