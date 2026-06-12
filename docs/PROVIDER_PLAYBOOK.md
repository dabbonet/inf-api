# Provider Playbook — Add / Remove a Channel in inf-api

> **Audience:** Anyone adding a 4th channel to `in.c.dabbo.net` (or removing one).
> **Goal:** Go from "I want a new channel" to a merged, deployed, tested branch in **under 30 minutes**, with no leftover references in code, tests, frontend, or docs.
> **Last updated:** 2026-06-12 (after the orchids removal that took 1h 21m).

---

## 0. Why this exists

Adding a channel touches **~25 files** spread across backend, frontend, docs, and tests. The first time we did it the hard way (removing orchids), it took 1h 21m because:

1. We didn't enumerate touch points up front — discovered them iteratively.
2. We ran cleanup tasks **sequentially** instead of in parallel.
3. We made subagents loop on translation work that needed direct file inspection.
4. We built and tested once at the very end instead of after each phase.

This playbook fixes all four.

---

## 1. The 30-second mental model

A "channel" in this codebase is **a string** (`"warp"`, `"puter"`, `"grok"`) that ties together:

```
URL prefix          → /<channel>/v1/...
Account.AccountType → "<channel>"
Model.Channel       → "<Channel>" (TitleCase)
Frontend badges     → badge-<channel>
CSS color           → .badge-<channel> in main.css
Admin dropdown      → <option value="<channel>">
```

If a string leaks into the wrong place (e.g. `"orchids"` left in a Lua script, or `accountType` defaulted to a removed channel in HTML), users see orphan accounts, broken filters, or admin UI dead-ends.

**The cardinal rule:** `grep -rn "<channel>"` must return **zero hits in production code** after a remove, and a **consistent set of expected hits** after an add.

---

## 2. Architecture overview (read this once)

```
                         ┌─────────────────────────────────────┐
HTTPS request           │  Caddy (in.c.dabbo.net)             │
  in.c.dabbo.net/* ────▶│  reverse_proxy localhost:3002       │
                         └──────────────┬──────────────────────┘
                                        │
                         ┌──────────────▼──────────────────────┐
                         │  middleware.Chain                    │
                         │  (Security, Trace, Logging)          │
                         └──────────────┬──────────────────────┘
                                        │
                         ┌──────────────▼──────────────────────┐
                         │  http.ServeMux (mux)                 │
                         │  (cmd/server/routes.go)              │
                         └─┬─────────┬────────────┬─────────────┘
                           │         │            │
                  /warp/v1/*   /puter/v1/*    /grok/v1/*
                           │         │            │
                  limiter   limiter    limiter (grok has its own handler)
                           │         │
                  h.HandleMessages  h.HandleMessages
                           │         │
                           └────┬────┘
                                │
                  channelFromPath() → "<channel>"
                                │
                  loadbalancer.GetNextAccountExcludingByChannel
                                │
                  h.clientFactory(acc, cfg)
                  = provider.registry.Get("<channel>").NewClient(acc, cfg)
                                │
                  internal/<channel>/Client (implements handler.UpstreamClient)
                                │
                  → upstream API
```

**Grok is the odd one out** — it does NOT use the registry. It has its own `grok.Handler` registered directly in `routes.go` (L53-66, L97-162). **A new channel should follow the warp/puter pattern, not the grok pattern.**

---

## 3. The two file maps (memorize these)

### 3.1 ADD a channel `"x"` — 27 files

| # | File | Action | Critical? |
|---|---|---|---|
| **Backend (12 files)** |||
| 1 | `internal/x/` (new package) | Implement `Client` matching `handler.UpstreamClient` | yes |
| 2 | `internal/provider/x_provider.go` (new file, ~17 lines) | Wire to `internal/x` via `NewXProvider()` | yes |
| 3 | `cmd/server/main.go` L127 | Add `registry.Register("x", provider.NewXProvider())` | yes |
| 4 | `cmd/server/routes.go` L39-51 | Add 4 lines: `/x/v1/messages`, `/x/v1/messages/count_tokens`, `"/x/v1"` in `modelPrefixes`, `/x/v1/chat/completions` | yes |
| 5 | `internal/handler/utils.go` L112-123 | Add `if strings.HasPrefix(path, "/x/") { return "x" }` in `channelFromPath` | yes |
| 6 | `internal/store/x_seed.go` (new file, optional) | Fallback models if discovery fails | no |
| 7 | `cmd/server/model_refresh.go` (switch/case) | Add `case "x":` for periodic discovery | yes |
| 8 | `internal/config/config.go` `ApplyHardcoded()` | Add `x_*` hardcoded defaults if needed | no |
| 9 | `internal/loadbalancer/loadbalancer.go` | **No change** — already channel-agnostic | — |
| 10 | `internal/api/api.go` (15+ branches + 3 credential selector switches) | Add `case "x":` per channel branch AND wire `ResolveXxxCredential` into 3 switch points (L89-93 create, L221-228 display, L1029-1037 update) | yes |
| 11 | `cmd/server/x_public_models.go` (new file, optional) | Static public model list | no |
| 12 | `internal/toolname/toolname.go` | Add channel-specific tool aliases if needed | no |
| 12a | `internal/<channel>/credential.go` (new file) | `ResolveXxxCredential(*store.Account) string` — picks right field, strips legacy formats, returns `""` if none | yes |
| 12b | `cmd/server/background.go` (`startTokenRefreshLoop`) | Add `case "x":` calling `<channel>.RefreshToken(ctx, acc)` if upstream supports auto-refresh | yes (if applicable) |
| 12c | `internal/loadbalancer/loadbalancer.go` | Per-channel cooldown config (warp=short, grok=long) — only if your channel's auth has different recovery characteristics | no (only if different from existing) |
| **Frontend (7 files)** |||
| 13 | `web/static/js/accounts.js` L501 | Add `"x"` to `defaultTypes` | yes |
| 14 | `web/static/js/common.js` L15 | Optionally change `normalizeSidebarAccountType` default | no |
| 15 | `web/static/js/models.js` | Add X to channel list builder | yes |
| 16 | `web/templates/components/modals/account-modal.html` L12 | Add `<option value="x">X</option>` | yes |
| 17 | `web/templates/components/modals/model-modal.html` L12-16 | Add `<option value="x">X</option>` | yes |
| 18 | `web/static/css/main.css` L2196-2212 | Add `.badge-x` class | yes |
| 19 | `web/templates/pages/tutorial.html` | Document X routing | yes |
| **Docs (6 files)** |||
| 20 | `docs/api-reference.md` | Add X rows to API table | yes |
| 21 | `docs/architecture.md` L7-8, 17-39, 95-123 | Update chain description | yes |
| 22 | `docs/architecture-review.md` L7-15 | Update review text | yes |
| 23 | `docs/configuration.md` §3 | Add `x_*` defaults if applicable | no |
| 24 | `docs/deployment.md` L3 | Update intro "N channels" | yes |
| 25 | `docs/ORCHIDS_API_FLOW.md` | Add X request flow section | no |
| **Tests (2 files)** |||
| 26 | `internal/api/api_x_test.go` (new file) | Pattern after `api_warp_test.go` | yes |
| 27 | `internal/handler/handler_x_integration_test.go` (new file) | End-to-end smoke | no |

### 3.2 REMOVE a channel `"x"` — same 25 files in reverse

| Action | Files |
|---|---|
| Delete package | `internal/x/`, `internal/provider/x_provider.go`, `internal/store/x_seed.go`, `cmd/server/x_public_models.go`, `internal/api/api_x_test.go`, `internal/handler/handler_x_*.go` |
| Edit backend | `cmd/server/main.go` (delete register line), `cmd/server/routes.go` (delete 4 lines), `cmd/server/model_refresh.go` (delete case), `internal/handler/utils.go` (delete channelFromPath branch), `internal/api/api.go` (delete all x branches), `internal/config/config.go` (delete x_* fields) |
| Edit frontend | `web/static/js/accounts.js`, `web/static/js/common.js`, `web/static/js/models.js`, `web/templates/components/modals/account-modal.html`, `web/templates/components/modals/model-modal.html`, `web/static/css/main.css`, `web/templates/pages/tutorial.html` |
| Edit docs | All 6 docs files (remove X references) |
| **Data cleanup** | **Run the Redis purge script (see §6.3) — REQUIRED** |

---

## 4. The standard workflow (do these in order)

### Phase 1: Discovery (5 min, single-agent)

**Goal:** Confirm file map is current. Use the explore agent.

```bash
# Confirm architecture hasn't drifted
grep -rn "registry.Register" /home/ubuntu/inf-api/cmd/server/
grep -n "channelFromPath" /home/ubuntu/inf-api/internal/handler/utils.go
grep -n "modelPrefixes" /home/ubuntu/inf-api/cmd/server/routes.go
ls /home/ubuntu/inf-api/internal/provider/
```

**Output:** Updated file map with current line numbers.

**Anti-pattern:** Skip this step because "I did it last week." File contents drift; line numbers move; new files appear.

### Phase 2: Branch and stub (2 min, main agent)

```bash
cd /home/ubuntu/inf-api
git checkout main
git pull origin main
git checkout -b feature/add-channel-x    # or feature/remove-channel-x
```

For ADD only, scaffold the package skeleton with one-line stubs:
- `internal/x/client.go` — type `Client struct{}` + 3 methods returning empty
- `internal/provider/x_provider.go` — full 17-line skeleton
- `internal/store/x_seed.go` — empty `BuildXSeedModels()` returning nil

For REMOVE, do nothing in this phase.

### Phase 3: Parallel implementation (15 min, 3-4 subagents)

Launch **3 subagents in parallel**, each with a tightly-scoped file list:

| Subagent | Scope | Files |
|---|---|---|
| **A: backend core** | Registry, routes, channelFromPath, config | Files #1-#5, #8, #9 |
| **B: store + refresh + admin** | Seed models, model refresh, admin API branches | Files #6, #7, #10-#12 |
| **C: frontend + docs** | JS, HTML, CSS, tutorial, docs | Files #13-#25 |

**For each subagent, give:**
- The exact file paths and line numbers from Phase 1.
- A 3-line code snippet of an existing channel (warp or puter) as the pattern to follow.
- A reminder: **don't run the build** — that's the verifier's job (Phase 4).

**Critical subagent prompt template:**
```
You are implementing the "<X>" channel in /home/ubuntu/inf-api.

Touch ONLY these files (others are owned by other agents):
- path/to/file1.go  ← what to change
- path/to/file2.go  ← what to change

Follow the EXACT pattern of the "puter" channel:
- internal/provider/puter_provider.go
- internal/puter/client.go
- internal/store/puter_seed.go

DO NOT run `go build` — verification is a separate phase.
DO NOT touch files outside your scope.
Report back the diff for each file (or "no change" if 0 lines).
```

**Anti-patterns:**
- ❌ One subagent doing everything (no parallelism).
- ❌ Subagent running the build (contention, blocks other agents).
- ❌ Subagent making judgment calls on UI text without consulting the main agent.
- ❌ Skipping the explore phase because "I know where the files are."

### Phase 4: Verify and build (3 min, main agent)

```bash
cd /home/ubuntu/inf-api

# 1. Build (catches type errors, missing imports, dead code)
CGO_ENABLED=0 /usr/local/go/bin/go build -o /tmp/x-server ./cmd/server

# 2. Vet (catches shadowing, unreachable code, printf format issues)
CGO_ENABLED=0 /usr/local/go/bin/go vet ./...

# 3. The cardinal grep — should match ONLY expected references
grep -rn --include="*.go" -i "x" . | grep -v "_test.go" | grep -v vendor/

# 4. Frontend consistency
grep -rn "badge-x" web/static/css/main.css
grep -n "value=\"x\"" web/templates/components/modals/*.html
```

If any of these fail, **fix in the main agent, not a subagent** (subagents can't see each other's work).

### Phase 5: Test (5 min, main agent)

```bash
# Run only the new/modified channel tests
CGO_ENABLED=0 /usr/local/go/bin/go test -run "TestX" -count=1 ./...

# Smoke test against running instance
sudo systemctl restart orchids-2api
sleep 2
curl -s http://localhost:3002/v1/models | python3 -c "import sys,json; print(sorted(set(m['owned_by'] for m in json.load(sys.stdin)['data'])))"
curl -X POST http://localhost:3002/x/v1/chat/completions \
  -H "Content-Type: application/json" \
  -d '{"model":"<a-real-x-model>","messages":[{"role":"user","content":"hi"}],"max_tokens":10}'
```

### Phase 6: Commit, push, deploy (2 min, main agent)

```bash
cd /home/ubuntu/inf-api
git add -A
git status --short | wc -l   # sanity: should be ~25-30 files

git commit -m "feat: add <X> channel

- Add internal/x/ client package
- Register in provider registry
- Add routes /x/v1/*, model prefix, chat completions
- Update channelFromPath, model refresh, admin API
- Add frontend dropdowns, badges, tutorial
- Add seed models, integration tests"

git push myfork feature/add-channel-x
gh pr create --base main --title "Add <X> channel" --body "..."

# After merge:
git checkout main && git pull
CGO_ENABLED=0 /usr/local/go/bin/go build -o orchids-server ./cmd/server
sudo systemctl restart orchids-2api
curl -s https://in.c.dabbo.net/health
```

---

## 5. Parallelization matrix (the speed-up)

| Step | Sequential (1h 21m, what we did) | Parallel (target: 25 min) |
|---|---|---|
| Architecture discovery | 1 explore agent | 1 explore agent |
| Backend code | 1 subagent | **subagent A** in parallel |
| Frontend + docs | 1 subagent (after backend) | **subagent C** in parallel |
| Store + refresh + admin | 1 subagent (after backend) | **subagent B** in parallel |
| Test files | 1 subagent (after backend done) | Part of subagent A's scope |
| Build + verify | 1 main agent | 1 main agent |
| Test + deploy | 1 main agent | 1 main agent |

**Wall-clock savings:** ~40 min on a typical change (the 3 file-group workstreams are independent).

---

## 6. Special cases

### 6.1 When adding a channel that has its own admin UI

**Pattern to follow: warp/puter** (uses the shared `internal/api/api.go` with a `case "x":` per branch).

**Anti-pattern to avoid: grok** (separate handler, separate admin routes, separate cache dirs). Grok was merged in 2026-03 as a quick hack and is flagged in `docs/architecture-review.md` §3.3 for refactor.

**Decision rule:** If the upstream has <3 admin operations (token refresh, account creation, model list), put it in `internal/api/api.go`. If it has ≥5 admin operations OR needs its own cache directories, do NOT clone the grok pattern — refactor the shared admin API first.

### 6.2 When adding a channel that needs streaming + tools

`internal/handler/stream_handler.go` has channel-agnostic plumbing. The only channel-specific bits are in:
- `internal/warp/request.go`, `response.go` — tool name normalization (now in `internal/toolname/`)
- `internal/handler/utils.go:1669` — switch on tool name alias

**Rule:** If your channel uses Anthropic-compatible tools (`Edit`, `Read`, `Bash`), reuse `internal/toolname/`. If it uses a different vocabulary, add a normalization function in `internal/<channel>/request.go` and call it from `stream_handler.go`.

### 6.3 Removing a channel with existing user data (CRITICAL)

Stale data does NOT break the system (loadbalancer silently skips orphan accounts), but the admin UI will show them and `/v1/models` will list them forever.

**Required cleanup script (run once after removing the channel):**

```bash
# Set the channel name
CHANNEL="<the-removed-channel>"

# Dry-run first
redis-cli -h 127.0.0.1 -p 6379 --scan --pattern "orchids:account:*" | \
  xargs -I {} sh -c 'redis-cli -h 127.0.0.1 -p 6379 HGET {} account_type | grep -q "^'"$CHANNEL"'$" && echo "would-delete {}"'

# Real run (after dry-run looks right)
redis-cli -h 127.0.0.1 -p 6379 --scan --pattern "orchids:account:*" | \
  xargs -I {} sh -c 'redis-cli -h 127.0.0.1 -p 6379 HGET {} account_type | grep -q "^'"$CHANNEL"'$" && redis-cli -h 127.0.0.1 -p 6379 DEL {}'

# Same for models (note: Channel is TitleCase, AccountType is lowercase)
redis-cli -h 127.0.0.1 -p 6379 --scan --pattern "orchids:model:*" | \
  xargs -I {} sh -c 'TYPE=$(redis-cli -h 127.0.0.1 -p 6379 HGET {} channel); [ "$TYPE" = "<TitleCase>" ] && redis-cli -h 127.0.0.1 -p 6379 DEL {}'
```

**Note:** Adjust the prefix from `orchids:` to whatever `cfg.RedisPrefix` is set to. As of 2026-06-12 the in-code default is `"warp:"`; check `/home/ubuntu/inf-api/config.json`.

### 6.5 Multi-credential handling (the 5-token pattern) — CRITICAL FOR ADD

Each `Account` row in Redis stores **up to 5 credential-like fields**, not just one. The channel-specific code picks the right one at runtime.

| Field (in `store.Account`) | Used by | Format |
|---|---|---|
| `RefreshToken` | warp, puter | OAuth refresh token (long string) |
| `Token` | warp, puter | Bearer / API token |
| `SessionCookie` | warp | `__client=...; __session=...` cookie jar |
| `ClientCookie` | grok, warp | Clerk session cookie (e.g. `__client=...`) |
| `ClientUat` | warp | Unix timestamp (e.g. `1700000000`) |
| `SessionID` | warp | Anthropic session UUID |
| `ProjectID` | warp | Anthropic project UUID |
| `DeviceID` | warp | Per-device fingerprint |
| `UserID` | warp | Anthropic user UUID |
| `Email` | warp | Account email (display only) |

**Selection logic lives in `internal/api/api.go` ~L186-228:**

```go
func firstNonEmptyString(values ...string) string { ... }

// Per-channel pick (simplified):
switch strings.ToLower(acc.AccountType) {
case "warp":
    token = strings.TrimSpace(warp.ResolveRefreshToken(acc))        // -> RefreshToken
case "grok":
    token = grok.NormalizeSSOToken(firstNonEmptyString(
        acc.ClientCookie, acc.RefreshToken, acc.Token))             // -> ClientCookie > RefreshToken > Token
case "puter":
    token = strings.TrimSpace(firstNonEmptyString(
        acc.RefreshToken, acc.SessionCookie, acc.ClientCookie,
        acc.Token))                                                  // -> RefreshToken > SessionCookie > ClientCookie > Token
}
```

**When adding a new channel, you must:**

1. **Decide which credential field(s) the upstream accepts.** Map them to the existing `Account` fields — do NOT add new fields. The 5 fields above cover 99% of cases. If you genuinely need a new field, that's a schema change requiring:
   - Add field to `Account` struct (`internal/store/store.go:14`)
   - Add field to `AccountRecord` (Redis hash marshaling, `internal/store/redis_store.go`)
   - Add field to admin POST/PUT payload parsing (`internal/api/api.go` HandleAccounts)
   - Add field to admin UI form (`web/templates/components/forms/account-*.html`)
   - **Migration:** existing rows in Redis will not have the new field; handle as empty string in `ResolveRefreshToken`-style functions

2. **Add a `ResolveXxxCredential(acc *store.Account) string` function in `internal/<channel>/`** following the `warp.ResolveRefreshToken` pattern. This function:
   - Returns the strongest credential available (e.g. RefreshToken > Bearer)
   - Strips surrounding quotes / whitespace
   - Handles legacy formats (e.g. `warp.ResolveRefreshToken` strips `grant_type=refresh_token&refresh_token=...` prefix)
   - Returns `""` if no usable credential

3. **Wire it into the 3 switch points in `internal/api/api.go`:**
   - L89-93: account creation / verification (before storing)
   - L221-228: per-channel display in admin UI (mask + show)
   - L1029-1037: account update logic (preserve existing credential if not provided)

4. **Wire it into `internal/loadbalancer/loadbalancer.go`** if your channel needs different cooldowns per credential type. Warp uses 401-cooldown because the refresh token auto-rotates; grok uses longer cooldown because cookies don't auto-refresh.

5. **Wire it into the token refresh loop (`cmd/server/background.go:startTokenRefreshLoop`)** if your channel supports automatic token refresh. Pattern:
   - Add a `case "x":` to the switch on `acc.AccountType`
   - Call `<channel>.RefreshToken(ctx, acc)` which returns a new `*Account` with refreshed fields
   - Save back to store

6. **Test cases for the credential selector** (pattern in `internal/api/api_account_refresh_test.go:TestHandleAccounts_PostRejectsDuplicateWarpRefreshToken`):
   - Empty `RefreshToken` → falls through to `SessionCookie`
   - Quoted `RefreshToken` `"foo"` → returns `foo`
   - Legacy format `grant_type=refresh_token&refresh_token=foo` → returns `foo`
   - All empty → returns `""` (caller should 401)

**Anti-patterns:**
- ❌ Adding a new `Account.APIKey` field for every channel (use the existing 5)
- ❌ Hardcoding `acc.RefreshToken` in your channel client (use a `ResolveXxxCredential` helper so legacy formats are handled)
- ❌ Forgetting the token refresh loop (your channel will silently die after the first token expires)
- ❌ Returning the credential unmasked in admin UI responses (use `template.maskToken()` from `internal/template/functions.go`)

**Why this matters:** The orchids removal kept `Token` / `RefreshToken` field names on `Account` (didn't rename to warp-only), precisely so multi-credential channels (grok with its Clerk cookies, puter with its bearer tokens) keep working. If you add a new channel and decide to "clean up" the credential field naming, you'll break grok and puter.

### 6.4 Caddy changes (only if URL prefix changes)

If your channel needs a special subdomain (e.g. `x.c.dabbo.net` instead of `in.c.dabbo.net/x/v1/`), edit `/etc/caddy/Caddyfile` (NOT `/home/ubuntu/dabbo-state/docker-compose/Caddyfile` — that one is for `*.fin.dabbo.net` and is currently inactive).

```caddyfile
x.c.dabbo.net {
    reverse_proxy localhost:3002
}
```

Then `sudo systemctl reload caddy`.

### 6.5 Redis prefix gotcha

`internal/config/config.go ApplyHardcoded()` and `internal/store/redis_store.go` have **two different defaults**:

- `config.go`: `"orchids:"` (in comments + docs)
- `redis_store.go`: `"warp:"` (in code)

This drift is a known bug (flagged in `docs/architecture-review.md`). The runtime value comes from `cfg.RedisPrefix` which is set in `config.json`. **Do not change one without the other** — pick a canonical name and update both + the config.json + the cleanup scripts.

---

## 7. Pre-flight checklist (print and tick)

### For ADD

```
[ ]  Phase 1: ran explore agent, got updated line numbers
[ ]  Phase 2: created feature branch + stubbed internal/x/ skeleton
[ ]  Phase 3A: subagent A done — backend core
[ ]  Phase 3B: subagent B done — store + refresh + admin
[ ]  Phase 3C: subagent C done — frontend + docs
[ ]  Phase 3:  `ResolveXxxCredential` implemented + tested (empty, quoted, legacy formats)
[ ]  Phase 3:  credential selector wired into 3 api.go switch points (create, display, update)
[ ]  Phase 3:  token refresh loop updated (if channel supports auto-refresh)
[ ]  Phase 4:  `go build` clean
[ ]  Phase 4:  `go vet` clean
[ ]  Phase 4:  `grep "<x>"` matches expected references only
[ ]  Phase 5:  new tests pass
[ ]  Phase 5:  smoke test against running instance
[ ]  Phase 6:  committed + pushed + PR created
[ ]  Phase 6:  deployed via `sudo systemctl restart orchids-2api`
[ ]  Phase 6:  `curl https://in.c.dabbo.net/health` → ok
```

### For REMOVE

```
[ ]  Phase 1: ran explore agent, got updated line numbers
[ ]  Phase 2: created feature branch from current main
[ ]  Phase 3A: subagent A done — backend deletions + edits
[ ]  Phase 3B: subagent B done — store/refresh/admin edits
[ ]  Phase 3C: subagent C done — frontend + docs deletions
[ ]  Phase 4:  `go build` clean
[ ]  Phase 4:  `go vet` clean
[ ]  Phase 4:  `grep -i "channel-name"` returns ZERO hits in production code
[ ]  Phase 4:  `grep -i "channel-name"` in tests returns ZERO hits (or only intentional)
[ ]  Phase 5:  existing test suite does not regress on removed-channel paths
[ ]  Phase 6:  ran Redis purge script (DRY RUN first)
[ ]  Phase 6:  ran Redis purge script (real run, with prefix from config.json)
[ ]  Phase 6:  verified `/v1/models` no longer lists removed channel
[ ]  Phase 6:  verified admin UI no longer shows removed channel
[ ]  Phase 6:  committed + pushed + PR created
[ ]  Phase 6:  deployed via `sudo systemctl restart orchids-2api`
[ ]  Phase 6:  `curl https://in.c.dabbo.net/health` → ok
```

---

## 8. Common gotchas (the things that bit us)

| Gotcha | Symptom | Fix |
|---|---|---|
| Forgot `channelFromPath` | New channel gets routed to wrong handler | Check `internal/handler/utils.go:112-123` |
| Forgot `modelPrefixes` | `/v1/models` doesn't list X models | Check `cmd/server/routes.go:45` |
| `EqualFold` vs `==` | Tests pass, prod doesn't | `api.go` uses both — match the surrounding pattern |
| `warp:` vs `orchids:` prefix | Cleaned wrong Redis keyspace | Check `cfg.RedisPrefix` in `config.json` |
| Subagent ran `go build` | Contention, blocks other subagents | Tell subagents "DO NOT run `go build`" in prompt |
| Skipped Redis purge after remove | Orphan accounts in admin UI forever | Run §6.3 script |
| Edited `docker-compose/Caddyfile` | Reload had no effect | Edit `/etc/caddy/Caddyfile` instead |
| Forgot to `git pull` before branch | Conflicts at merge | Always start from fresh main |

---

## 9. Time budget (realistic)

| Phase | Time | Parallelizable? |
|---|---|---|
| 1. Discovery | 5 min | No |
| 2. Branch + stub | 2 min | No |
| 3. Implementation | 15 min | **Yes (3 subagents)** |
| 4. Verify + build | 3 min | No |
| 5. Test | 5 min | No |
| 6. Commit + deploy | 2 min | No |
| **Total** | **~32 min** | |

A 25-minute add (matching the original target) is achievable if:
- Discovery takes 3 min (familiar with code)
- Subagents don't block each other
- No test regressions need fixing

A 60-minute add means: someone skipped Phase 1, or subagents collided on the same files.

---

## 10. The 7-question sanity check (run before committing)

1. **`grep -rn "<channel>" internal/ cmd/`** — does the count match the expected map (registry, routes, model_refresh, channelFromPath, config, api, provider, store seed)?
2. **`grep -rn "<channel>" web/`** — does the count match the expected map (JS defaults, modal options, CSS badge, tutorial)?
3. **`grep -rn "<channel>" docs/`** — are all 6 doc files updated?
4. **`go build && go vet`** — both pass with zero warnings?
5. **Manual smoke test** — does `POST /<channel>/v1/chat/completions` with a real model return a real response?
6. **Credential coverage** — is `ResolveXxxCredential` implemented and tested for empty / quoted / legacy formats? Are all 3 api.go switch points updated?
7. **Token refresh** — if your channel supports auto-refresh, is `startTokenRefreshLoop` updated? After 1 hour, do tokens still work?

If any answer is "no" or "I don't know," don't commit. Fix and re-check.

---

## Appendix A: The current channel map (baseline)

As of 2026-06-12, the codebase has 3 channels with this consistent shape:

| Channel | URL prefix | AccountType | Model.Channel | CSS class | Color |
|---|---|---|---|---|---|
| warp | `/warp/v1/*` | `warp` | `Warp` | `.badge-warp` | green-ish |
| puter | `/puter/v1/*` | `puter` | `Puter` | `.badge-puter` | blue-ish |
| grok | `/grok/v1/*` (own handler) | `grok` | `Grok` | `.badge-grok` | purple-ish |
| aihubmix | `/aihubmix/v1/*` (registry) | `aihubmix` | `Aihubmix` | `.badge-aihubmix` | purple-ish |
| zenmux | `/zenmux/v1/*` (registry) | `zenmux` | `Zenmux` | `.badge-zenmux` | green-ish |

When you add a 6th, **match this exact casing convention** (lowercase for `AccountType`, TitleCase for `Model.Channel`).

## Appendix B: Where the registry is partially broken

The `internal/provider/` registry is only used by warp+puter+aihubmix+zenmux. Grok bypasses it. This is a known design smell. When adding a new channel, **use the registry** (it's the simple path). Refactoring grok onto the registry is a separate workstream — see `docs/architecture-review.md` §3.3.

## Appendix C: Worked Example — Parallel 2-Channel Add (aihubmix + zenmux)

This is the recipe used in commit `feat: add aihubmix and zenmux channels`. Use it as a template for any future parallel add.

### C.1 Upstream reconnaissance

Before writing code, gather these facts for each provider (one engineer-hour each):

| Fact | Where to look |
|---|---|
| Base URL + auth header scheme | Provider docs (e.g. `https://aihubmix.com/v1` + `Authorization: Bearer …`) |
| Endpoint shape | `/v1/chat/completions` (OpenAI-compatible) or proprietary |
| Default model ID | Listed on provider's catalog page |
| Model catalog endpoint | Public? Auth? No-auth = better (no chicken-and-egg) |
| Free tier rate limits | Try a request, observe 429 patterns |
| Image endpoint? | Many OpenAI-compatible APIs ship `/v1/images/generations` too |

### C.2 Package layout decision

Both aihubmix and zenmux are **OpenAI-compatible**. Don't duplicate the OpenAI logic. Create a shared `internal/openai/` package:

```
internal/openai/
  types.go    # ChatRequest/Response/Usage/ErrorEnvelope/ImageRequest/Response
  convert.go  # promptToOpenAIMessages() + content-block-to-parts helpers
  sse.go      # StreamParser with comment-skip + [DONE] handling
  client.go   # SendRequestWithPayload with finish_reason translation
```

Then each provider package is a **thin wrapper** (one file, ~60 lines):

```go
type Client struct{ *openai.Client }
func NewFromAccount(acc *store.Account, cfg *config.Config) *Client { ... }
func ResolveAPIKey(acc *store.Account) string { ... }
```

### C.3 Provider registry stub

`internal/provider/aihubmix_provider.go`:

```go
type aihubmixProvider struct{}
func NewAihubmixProvider() *aihubmixProvider { return &aihubmixProvider{} }
func (p *aihubmixProvider) Name() string { return "aihubmix" }
func (p *aihubmixProvider) NewClient(acc *store.Account, cfg *config.Config) interface{} {
    return aihubmix.NewFromAccount(acc, cfg)
}
```

Same shape for zenmux. Total per provider: **~17 lines**.

### C.4 Seed models

Both seeds go in `internal/store/{provider}_seed.go` with stable IDs in the 200-299 range (after warp 1-99, puter 100-199, then 200+):

```go
func buildAihubmixSeedModels() []Model {
    return []Model{
        { ID: 220, Channel: "Aihubmix", ModelID: "gpt-5.5-free", Name: "GPT-5.5 Free", IsDefault: true, Enabled: true, Status: "available" },
        ...
    }
}
```

Register both in `internal/store/store.go` `seedModels()`:

```go
models = append(models, buildAihubmixSeedModels()...)
models = append(models, buildZenmuxSeedModels()...)
```

### C.5 Model discovery

Two patterns, choose by public catalog auth:

| Public catalog auth | Function to add |
|---|---|
| None (aihubmix) | `cmd/server/aihubmix_public_models.go` hits `https://{host}/api/v1/models` |
| Required (zenmux) | `cmd/server/zenmux_public_models.go` reads accounts via `enabledAccountsByType(ctx, s, "zenmux")` |

Both return `[]discoveredModel` (the type lives in `cmd/server/model_refresh.go`). Filter out embeddings/moderations/whisper/tts. Wire into:

```go
// cmd/server/model_refresh.go
case "aihubmix":
    return discoverAihubmixModelsConcurrent(...)
case "zenmux":
    return discoverZenmuxModelsConcurrent(...)
```

### C.6 Channel routing (5 files, ~30 lines total)

| File | Add |
|---|---|
| `cmd/server/main.go` | `registry.Register("aihubmix", provider.NewAihubmixProvider())` |
| `cmd/server/routes.go` | 2 `mux.HandleFunc("/aihubmix/v1/messages", ...)` lines + 2 prefixes in `modelPrefixes` |
| `internal/handler/utils.go` | 2 branches in `channelFromPath` |
| `cmd/server/model_refresh.go` | 2 cases in `normalizeAdminModelChannel` and `discoverModelsForChannelConcurrent` |
| `internal/api/api.go` | 6 spots: `normalizedAccountCredentialKey` switch, `refreshAccountState` switch, 3x `normalizeWarpTokenInput`-style if/else (create/update/import) |

### C.7 Image generation (optional, aihubmix only)

If the provider ships an image endpoint, add `cmd/server/aihubmix_image_handler.go` (~120 lines). Pattern:

1. Decode `openai.ImageRequest` from body
2. Loop: `lb.GetNextAccountExcludingByChannel(ctx, excludeIDs, "aihubmix")` for up to 8 accounts
3. Forward raw JSON to `{BaseURL}/images/generations` with `Authorization: Bearer {key}`
4. Return decoded `openai.ImageResponse` JSON
5. On 4xx (non-408/429), surface the error; on 5xx/timeout, blacklist and try the next account
6. Wire via `mux.HandleFunc("/aihubmix/v1/images/generations", limiter.Limit(makeAihubmixImageHandler(cfg, s, lb)))`

### C.8 Frontend (4 files, ~20 lines)

- `web/static/js/accounts.js` `accountTypeLabel()` + `defaultTypes` array
- `web/static/js/models.js` `defaultChannels` array
- `web/templates/components/modals/model-modal.html` 2 `<option>` lines
- `web/static/css/main.css` 2 `.badge-{channel}` classes (purple for aihubmix, green for zenmux) + 2 entries in the tag-free group

### C.9 Docs (5 files)

- `docs/api-reference.md` — 4 route tables
- `docs/architecture.md` — 2 lines in the "Sources by channel" section
- `docs/deployment.md` — first paragraph channel count
- `docs/ORCHIDS_API_FLOW.md` — first blockquote
- `docs/PROVIDER_PLAYBOOK.md` — Appendix A table + Appendix B header

### C.10 Verification (no surprises)

```bash
CGO_ENABLED=0 /usr/local/go/bin/go build -o /tmp/orchids-server ./cmd/server
CGO_ENABLED=0 /usr/local/go/bin/go vet ./internal/...  # pre-existing test failures OK
sudo systemctl restart orchids-2api
curl -s https://in.c.dabbo.net/health
# Then 4 smoke tests:
curl -X POST https://in.c.dabbo.net/aihubmix/v1/chat/completions -H 'Content-Type: application/json' -d '{"model":"gpt-5.5-free","messages":[{"role":"user","content":"hi"}]}'
curl -X POST https://in.c.dabbo.net/zenmux/v1/chat/completions   -H 'Content-Type: application/json' -d '{"model":"moonshotai/kimi-k2.7-code-free","messages":[{"role":"user","content":"hi"}]}'
curl -X POST https://in.c.dabbo.net/aihubmix/v1/images/generations -H 'Content-Type: application/json' -d '{"model":"gpt-image-2-free","prompt":"a cat","n":1,"size":"1024x1024"}'
curl -s 'https://in.c.dabbo.net/admin/api/models?channel=aihubmix' --cookie 'admin_session=…'   # admin auth via login
```

### C.11 Why this is the right pattern

- **Shared `internal/openai/`** prevents 4-way drift if a 3rd OpenAI-compat provider joins later
- **Thin wrapper packages** keep blast radius small (one provider broken ≠ others broken)
- **Seed models with stable IDs** mean the migration is idempotent on every restart
- **Public discovery when possible** means no chicken-and-egg: add a key, models appear
- **Image endpoint as a separate route file** means we never entangle chat and image client logic

