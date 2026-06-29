# ops/inf-api/scripts/

Per-project deploy/orchestration scripts. Move a script here (not to the
top-level `scripts/`) when it's tightly coupled to this project — Go build,
binary swap, systemd unit swap, etc. — so a future operator finds them next
to the thing they actually want to operate on.

## deploy-inf-api.sh

Build orchids-server for ARM64 and ship it.

| Mode | Where to run | What it does |
|---|---|---|
| (no flag) | host with sudo+go | builds, replaces binary, restarts `orchids-api.service`, waits for `/health` |
| `--build-only` | host with go | builds, doesn't restart |
| `--check` | host or hermes | just GETs `/health` and exits 0/1 |
| `--request` | hermes container | `curl POST /api/internal/deploy` on orchids-api; server does the build in-process, swaps, exits; systemd picks up the new binary |

The Hermes path is the recommended one for automation — it gives auth, audit
logs (every deploy records `requested_by` IP), Redis-persisted state that
survives process restart, and JSON responses for easy scripting.

Full doc + endpoint reference: [`../docs/deployment.md`](../docs/deployment.md).
