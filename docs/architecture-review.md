# Architecture Review

This document is the current implementation review as of 2026-03-21, no longer retaining historical judgments from the early three-channel era.

## 1. Current Architecture Conclusions

The API server currently has a three-channel structure:

- `internal/handler` uniformly processes `warp`, `puter`
- `internal/grok` processes `grok` separately
- [routes.go](/D:/Code/Orchids-2api/cmd/server/routes.go) is responsible for uniformly registering public, admin, compatible aliases, and public/admin routes
- Redis not only saves accounts and models, but also carries runtime states of sessions, deduplication, auditing, and cache
- Model refresh uses the source synchronization logic in [model_refresh.go](/D:/Code/Orchids-2api/cmd/server/model_refresh.go), no longer testing individual model liveness

Overall, it is no longer in a state where "we can only continue to hardcode to add channels". The `provider` registry has been integrated into the main flow, and the cost of expanding new channels is significantly lower than in older versions.

## 2. Key Improvements Completed

Compared to the older version review, the following points are no longer issues or have been significantly mitigated:

- There is no longer a fixed default password `admin123`. When `admin_pass` is empty, a random password will be automatically generated.
- Global security response headers are now mounted on the middleware chain.
- Sensitive value comparisons in admin authentication have been changed to constant-time comparisons.
- Handler's session storage, deduplication storage, and audit logs can all fall back to Redis when Redis is available.
- Account client creation is now dispatched through the registry via `internal/provider`, rather than being scattered across hardcoded branches in the handler.
- Puter non-streaming `tool_use` / `tool_result` regressions have been solidified into integration tests.

## 3. Main Risks Still Existing

### 3.1 Configuration Layer is Still Somewhat "Semi-Hardcoded"

`ApplyHardcoded()` in [config.go](/D:/Code/Orchids-2api/internal/config/config.go) still forcibly overrides a large number of runtime values. As a result:

- Documentation needs to clearly distinguish between "configurable fields" and "final fixed fields".
- Operations may mistakenly believe that some historical configuration items can still take effect.
- Subsequent thermal updates or differentiated deployments by environment will be heavily constrained.

This is currently the most obvious "maintainability risk". It is not a functional bug, but it will continuously affect deployment and troubleshooting.

### 3.2 Admin Session is Still in Process Memory

The `session_token` in [auth.go](/D:/Code/Orchids-2api/internal/auth/auth.go) is still stored in an in-process map. Current impact:

- Fine for single instance.
- Session becomes invalid after process restart.
- Sessions cannot be shared in multi-instance deployments.

The session state of the request processing link can already go through Redis, but the admin login state has not been synchronized over yet.

### 3.3 Overly Large Files Remain the Main Maintenance Cost

The most obvious maintenance hotspots are still:

- `internal/grok/handler.go`
- `internal/grok/client.go`
- `internal/api/api.go`
- `internal/handler/stream_handler.go`

These files all have too many responsibilities. They are not points that immediately cause errors, but they will continuously slow down modification speed and are more likely to hide regression risks.

### 3.4 `/metrics` is Still Public by Default

`/metrics` is directly exposed in the main routing. This is fine for internal network deployment, but when placed on the public network, it is recommended to hand it over to an outer gateway or access control; direct exposure is not recommended.

### 3.5 Public API's "Empty Key Means Open" Semantics Need Clear Understanding

The current convention in [session.go](/D:/Code/Orchids-2api/internal/middleware/session.go) is:

- When `public_key` is empty, public API does not authenticate.
- Whether the page is displayed is controlled by `public_enabled`.

This set of semantics is explicitly implemented, not accidental behavior; but if operations don't read the documentation, they can easily misjudge that "closing the page equals closing the API".

## 4. Advantages of the Current Architecture

- Routing is centralized in `routes.go`, much clearer than inline registration in the early `main.go`.
- handler and grok are split into two main links, with clear responsibility boundaries.
- The provider registry has already driven down the cost of accessing new channels.
- Model refresh logic has become "source-driven synchronization", behavior is more predictable and more suitable for automatic deletion by the admin end.
- Puter's real toolchain regressions have entered testing, no longer entirely reliant on manual clicking for verification.

## 5. Recommended Next Round of Cleanup Order

1. Migrate admin session to Redis to fix multi-instance consistency.
2. Continue compressing `ApplyHardcoded()`, put operations items that truly need to be adjustable back to the configuration layer.
3. Split `internal/grok/handler.go` and `internal/api/api.go` first.
4. Add outer layer protection strategy description for `/metrics`, at least clearly state it in the deployment documentation.
5. Keep expanding integration tests for Puter / Grok, these two error-prone paths.

## 6. Conclusion

The current architecture has moved from the "rapidly stacking features" stage to the "sustainably maintainable but still having localized technical debt" stage.

The most worthwhile continuous optimizations are not adding another layer of abstraction, but three specific things:

- Converge hardcoded configurations
- Migrate admin session
- Split large files
