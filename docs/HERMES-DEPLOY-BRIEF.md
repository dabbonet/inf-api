# Inf-API Deploy Brief — for Hermes

## When to use this
The user (or another agent) asks you to deploy, restart, rebuild, or update
`orchids-api` (the dabbo inf-api service). Don't do this on your own — only
when explicitly asked.

## TL;DR (one command)

```bash
export DABBO_DEPLOY_TOKEN=admin123
export DEPLOY_URL=http://172.17.0.1:3002/api/internal/deploy
bash /home/ubuntu/dabbo-state/ops/inf-api/scripts/deploy-inf-api.sh --request
```

Wait ~5s for the script to print `Build succeeded.` If you see `Build
succeeded.` the deploy is done. If you see `Deploy failed: …` or
`FATAL: …`, read the error, then go to "If it breaks" below.

## What this does
- POSTs to `orchids-api`'s own `/api/internal/deploy` endpoint.
- orchids-api builds the new ARM64 binary in-process, atomically swaps it,
  writes a state record (sha256, size, duration, your IP) to Redis at
  `orchids:internal:deploy:state`, then `os.Exit(0)`.
- systemd (`orchids-api.service`, `Restart=always`) brings the new binary
  up in ~1s. Total downtime: ~1s.
- You do NOT need `sudo`, `go`, or `systemctl` — that's the whole point.

## Verify after the deploy

```bash
curl -s -H "X-Admin-Token: $DABBO_DEPLOY_TOKEN" \
     http://172.17.0.1:3002/api/internal/deploy | python3 -m json.tool
```

You should see `status: "built"` and a `sha256` field. If `status` is
`"error"`, the field `error` has the reason.

```bash
curl -s http://172.17.0.1:3002/health   # must print {"status":"ok"}
```

## If it breaks

| Symptom | Fix |
|---|---|
| `curl: connection refused` to `172.17.0.1:3002` | orchids-api is down. From the **host** terminal: `sudo systemctl restart orchids-api` and look at `journalctl -u orchids-api.service -n 30`. |
| `401 Unauthorized` from the endpoint | Wrong token. The right one is the `admin_pass` in `/home/ubuntu/dabbo-state/ops/inf-api/config.json` (currently `admin123`). |
| `409 {"status":"busy"}` | Another deploy is already running. Wait 30s and retry. |
| `FATAL: Build failed: go build: ...` (from the script) | Go build error inside orchids-api. The full error is in the response `error` field. Don't try to fix it yourself — report the error to the user. |
| After the script says "Build succeeded" but `/health` doesn't return 200 | Run `curl http://localhost:3002/health` from the host (not from hermes) to see if it's a network/iptables thing or the new process actually crashed. If crashed: `journalctl -u orchids-api.service -n 50` from the host. |

## Important rules

- **Don't edit Go source code in this mission unless the user told you to.**
  A deploy does not require a code change.
- **Don't try to use `sudo` or `systemctl` from inside hermes.** They won't
  work in this container; the HTTP endpoint is the supported channel.
- **Don't write to `/home/ubuntu/dabbo-state/ops/inf-api/internal/`** with
  anything that becomes root-owned — Go's build cache remembers file
  permissions and a root-owned source file will break the next build.
  `ops/hermes/config.yaml` already has `docker_run_as_host_user: true`
  so your session runs as `uid 1000` (ubuntu); keep it that way.
- **Don't try to bypass auth.** The endpoint requires `X-Admin-Token` (or
  a session cookie). Don't POST without it.

## Full doc
- `ops/inf-api/docs/deployment.md` — full deployment + endpoint reference
- `ops/inf-api/scripts/deploy-inf-api.sh` — the script (read it, it's
  ~200 lines of bash and has --help)
- `ops/inf-api/internal/api/deploy.go` — the HTTP handler
