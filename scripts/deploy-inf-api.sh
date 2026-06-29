#!/usr/bin/env bash
# deploy-inf-api.sh — build orchids-server for ARM64 and deploy via systemd
#
# Usage:
#   ./scripts/deploy-inf-api.sh               # host: build + restart orchids-api via sudo+systemctl
#   ./scripts/deploy-inf-api.sh --build-only  # host: build only, skip restart
#   ./scripts/deploy-inf-api.sh --check       # just verify current binary health
#   ./scripts/deploy-inf-api.sh --request     # hermes: curl POST /api/internal/deploy
#
# Hermes path (no sudo, no go needed):
#   ./scripts/deploy-inf-api.sh --request
#   -> curls POST /api/internal/deploy with admin auth (from env or .env)
#      orchids-api internally builds ARM64, swaps, and exits; systemd restart picks up
#      the new binary. GET /api/internal/deploy returns the deploy state.
#
# Two deploy modes are intentionally supported:
#   - Default (host):  external sudo+systemctl restart after build — leaves the
#     process alive; useful for testing without killing the API connection.
#   - --request (hermes): in-process self-build+exit via HTTP endpoint — clean
#     swap, server picks up new binary on systemd restart. The HTTP path is the
#     recommended one for hermes/automation because it gives JSON status and
#     audit logs for free.
set -euo pipefail

INF_API_DIR="${DABBO_STATE_DIR:-/home/ubuntu/dabbo-state}/ops/inf-api"
BINARY="${INF_API_DIR}/orchids-server"
BACKUP="${INF_API_DIR}/orchids-server.old"
SERVICE="orchids-api.service"
HEALTH_URL="${HEALTH_URL:-http://localhost:3002/health}"
DEPLOY_URL="${DEPLOY_URL:-http://localhost:3002/api/internal/deploy}"
TIMEOUT=60

log()  { echo "[$(date -Iseconds)] $*"; }
die()  { log "FATAL: $*" >&2; exit 1; }

# --- arg parse ---
BUILD_ONLY=false
CHECK_ONLY=false
REQUEST_MODE=false
for arg in "$@"; do
  case "$arg" in
    --build-only) BUILD_ONLY=true ;;
    --check)      CHECK_ONLY=true ;;
    --request)    REQUEST_MODE=true ;;
    --help|-h)
      sed -n '2,30p' "$0"
      exit 0 ;;
    *) die "Unknown argument: $arg" ;;
  esac
done

# --- health check ---
check_health() {
  local code
  code=$(curl -s -o /dev/null -w "%{http_code}" --max-time 5 "$HEALTH_URL" 2>/dev/null || echo "000")
  [ "$code" = "200" ]
}

if $CHECK_ONLY; then
  if check_health; then log "Health OK"; exit 0; fi
  log "Health FAIL"; exit 1
fi

# --- pre-flight ---
if ! $REQUEST_MODE; then
  command -v go >/dev/null 2>&1 || die "go not found in PATH"
  command -v sudo >/dev/null 2>&1 || die "sudo not found (use --request from hermes)"
fi

# --- fix permissions (defense vs hermes-as-root past) ---
CURRENT_USER="$(id -un)"
CURRENT_GROUP="$(id -gn)"
if [ "$CURRENT_USER" != "root" ] && [ -d "$INF_API_DIR/internal" ] && ! $REQUEST_MODE; then
  find "$INF_API_DIR" \
       -not -user "$CURRENT_USER" \
       -not -path "$INF_API_DIR/.git/*" \
       -not -path "$INF_API_DIR/orchids-server*" \
       -print0 2>/dev/null \
    | xargs -0 -r chown "$CURRENT_USER:$CURRENT_GROUP" 2>/dev/null || true
  log "Ownership aligned to $CURRENT_USER:$CURRENT_GROUP"
fi

# --- --request: hermes path through HTTP endpoint ---
if $REQUEST_MODE; then
  log "Request mode: POST $DEPLOY_URL"

  # Auth strategies, in order of preference:
  #   1. DABBO_DEPLOY_TOKEN env var (X-Admin-Token header)
  #   2. DABBO_DEPLOY_COOKIE env var (Cookie: session_token=...)
  if [ -n "${DABBO_DEPLOY_TOKEN:-}" ]; then
    AUTH_ARGS=( -H "X-Admin-Token: ${DABBO_DEPLOY_TOKEN}" )
    log "Using X-Admin-Token auth"
  elif [ -n "${DABBO_DEPLOY_COOKIE:-}" ]; then
    AUTH_ARGS=( -H "Cookie: session_token=${DABBO_DEPLOY_COOKIE}" )
    log "Using session cookie auth"
  else
    die "No auth: set DABBO_DEPLOY_TOKEN or DABBO_DEPLOY_COOKIE"
  fi

  resp=$(curl -sS -X POST \
              -H "Content-Type: application/json" \
              "${AUTH_ARGS[@]}" \
              -d '{"service":"orchids-api"}' \
              --max-time 10 \
              "$DEPLOY_URL" 2>&1) \
    || die "curl POST /api/internal/deploy failed (HTTP endpoint unreachable?)"

  log "Response: $resp"

  # Extract status from response.
  status=$(echo "$resp" | python3 -c "import sys,json; print(json.load(sys.stdin).get('status','?'))" 2>/dev/null || echo "?")
  case "$status" in
    started)  log "Deploy queued. Polling GET /api/internal/deploy..." ;;
    busy)     die  "Deploy already in flight" ;;
    error)    die  "Endpoint returned error: $resp" ;;
    *)        die  "Unexpected response: $resp" ;;
  esac

  # Poll until status moves from "building" to a final state ("built" or "error").
  for i in $(seq 1 "$TIMEOUT"); do
    state=$(curl -sS --max-time 3 "${AUTH_ARGS[@]}" "${DEPLOY_URL}" 2>/dev/null || echo '{}')
    s=$(echo "$state" | python3 -c \
      "import sys,json; d=json.load(sys.stdin); print(d.get('status','?'),'in_flight=',d.get('in_flight',False))" 2>/dev/null || echo "?")
    log "poll ${i}s: $s"
    case "$s" in
      *built*)  log "Build succeeded."; break ;;
      *error*)  err=$(echo "$state" | python3 -c "import sys,json; print(json.load(sys.stdin).get('error','?'))" 2>/dev/null)
                 die "Deploy failed: $err" ;;
    esac
    sleep 1
  done

  log "Deploy sequence complete. The new binary should be running under systemd."
  exit 0
fi

# --- build (host default path) ---
log "Clearing Go build cache to avoid stale permission errors..."
go clean -cache 2>/dev/null || true

log "Building orchids-server for linux/arm64..."
cd "$INF_API_DIR"

GOOS=linux GOARCH=arm64 go build -trimpath -o "$BINARY" ./cmd/server/ \
  || die "Build failed"

file "$BINARY" | grep -q "ARM aarch64" \
  || die "Built binary is not ARM aarch64: $(file "$BINARY")"

BUILD_SHA="$(sha256sum "$BINARY" | awk '{print $1}')"
BUILD_SIZE="$(stat -c %s "$BINARY")"
log "Build OK: ${BUILD_SIZE} bytes ARM aarch64  sha256=${BUILD_SHA:0:12}…"

if $BUILD_ONLY; then
  log "Build-only mode. Skipping restart."
  exit 0
fi

# --- deploy (host default path: external restart) ---
log "Restarting via systemctl..."

[ -f "$BINARY" ] && cp "$BINARY" "$BACKUP" 2>/dev/null || true

MANUAL_PIDS=$(ss -tlnp 2>/dev/null | grep ":3002 " | grep -oP 'pid=\K[0-9]+' || true)
for pid in $MANUAL_PIDS; do
  if [ -n "$pid" ] && [ "$pid" != "1" ]; then
    log "Killing manual process on port 3002: PID $pid"
    kill "$pid" 2>/dev/null || true
    sleep 1
  fi
done

if ! sudo systemctl restart "$SERVICE" 2>/dev/null; then
  log "systemctl failed; starting directly..."
  nohup "$BINARY" > /tmp/orchids-server.log 2>&1 &
  sleep 2
fi

for i in $(seq 1 "$TIMEOUT"); do
  if check_health >/dev/null 2>&1; then
    log "Deploy complete. Server healthy after ${i}s.  sha256=${BUILD_SHA:0:12}…"
    exit 0
  fi
  sleep 1
done
die "Server did not become healthy within ${TIMEOUT}s. Check: journalctl -u $SERVICE -n 20"
