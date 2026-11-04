<!-- file: docs/plans/platform-core/raw/09-http-api.md -->
<!-- version: 1.0.0 -->
<!-- guid: c332e835-2e8d-4306-87d0-421f187aa663 -->
<!-- last-edited: 2026-11-03 -->

# 09 - HTTP API surface (gin routes, wire contract, realtime, listeners)

Evidence commit: `748fd787a` (HEAD checked out is 2 docs-only commits ahead; no code differs). Anchors are repo-relative `path:line`.

## 0. Method and caveats

- Routes are registered through helper wrappers (`s.perm`, `s.credRoute`, `s.credRouteWhen`, `s.adminRoute`, `RouterGroup.Group`) so grep cannot see final paths (`internal/server/wire_abs_routes.go:297-338` says so itself). The table in section 3 is therefore NOT hand-read: it was produced by building the real `*Server` in a throwaway copy of the repo (test fixture `setupCredGuardServer`, `internal/server/apikey_credential_guard_test.go:44`), walking gin's internal route trees by reflection, and labelling `perm()/adminRole()/credGuard` closures. Handler `file:line` are resolved to the function definition. The copy was discarded; no repo file was modified.
- Two builds were dumped: default config (EnableAuth=true, ABS off) = **497 routes**; plus ABS surface on (`ABSAPIEnabled=true` with a synthetic signing secret) = **+75 routes** (total 572). Routes that exist only when other runtime flags/tags are set are NOT in the table: plugin-registered routes (none in-tree register any), the `bench` group (`internal/server/bench.go:35`, build tag `bench`), and cfMW (Cloudflare Access middleware, only inserted when configured, see 2.2).
- "generic/audiobook" is MY classification by path group (column `class`): `generic` = would belong in a shared platform core (auth, users, ops/tasks, system, config, backup, plugins, health); `mixed` = generic mechanism but audiobook-specific content or unclear (AI, repairs, review, maintenance, diagnostics, import paths, filesystem, iTunes...); `audiobook` = domain routes; `compat` = Audiobookshelf-compatible surface. It is a judgment call, not a code fact.
- Auth column: `session/key` = `RequireAuth` (`internal/server/middleware/auth.go:152`) in chain; `ABS auth` = `ABSRequireAuth` (`internal/server/middleware/absauth.go:621`); `none` = public. Permission column = `s.perm(...)` atoms in the chain. Guards: `RequireAdmin` = role-name check (`internal/server/middleware/auth.go:522`); `credGuard` = refuses API-key/ABS-token methods (`internal/server/credential_routes.go`); `RequireOwner` = owner proof (`internal/server/middleware/owner_guard.go:51`); `loginCeiling`, `inviteRL`, `IPRateLimiter` = per-route throttles.

## 1. Totals

| Metric | Count |
|---|---|
| Routes, default build (ABS off) | 497 |
| Additional routes with ABS surface on | 75 |
| Total distinct (method,path) | 572 |
| By method (default build) | DELETE 35, GET 222, PATCH 9, POST 215, PUT 16 |
| Public (no RequireAuth) routes, default build | 15 (listed in 2.4) |
| Authenticated routes that declare NO route-level permission and no admin gate | 36 (listed in section 4) |
| Routes answering 410 Gone via `gone()` stubs (retired, `internal/server/gone.go:28`) | 45 |
| Routes behind `credGuard` (API keys refused) | 26 |
| Routes behind `RequireAdmin` (role name "admin") | 20 |
| Routes behind `RequireOwner` | 9 |
| class (default build): generic / mixed / audiobook | 150 / 132 / 215 |

### 1.1 By top-level group (default build)

| group | routes | class |
|---|---|---|
| `/api/v1/audiobooks` | 106 | audiobook |
| `/api/v1/operations` | 38 | generic |
| `/api/v1/dedup` | 35 | audiobook |
| `/api/v1/auth` | 22 | generic |
| `/api/v1/metadata` | 21 | mixed |
| `/api/v1/authors` | 19 | audiobook |
| `/api/v1/series` | 16 | audiobook |
| `/api/v1/users` | 15 | generic |
| `/api/v1/ai` | 14 | mixed |
| `/api/v3/ops` | 12 | generic |
| `/api/v1/admin` | 11 | generic |
| `/api/v1/itunes` | 11 | mixed |
| `/api/v1/diagnostics` | 10 | mixed |
| `/api/v1/maintenance` | 10 | mixed |
| `/api/v1/playlists` | 10 | audiobook |
| `/api/v1/books` | 9 | audiobook |
| `/api/v1/collections` | 8 | audiobook |
| `/api/v1/review` | 7 | mixed |
| `/api/v1/system` | 7 | generic |
| `/api/v1/deluge` | 6 | mixed |
| `/api/v1/plugins` | 6 | generic |
| `/api/v1/repairs` | 6 | mixed |
| `/api/v1/works` | 6 | audiobook |
| `/api/v1/fingerprint` | 5 | mixed |
| `/api/v1/openlibrary` | 5 | mixed |
| `/api/v1/activity` | 4 | mixed |
| `/api/v1/backup` | 4 | generic |
| `/api/v1/cache` | 4 | generic |
| `/api/v1/filesystem` | 4 | mixed |
| `/api/v1/master-lists` | 4 | mixed |
| `/api/v1/merge` | 4 | mixed |
| `/api/v1/blocked-hashes` | 3 | generic |
| `/api/v1/import-paths` | 3 | mixed |
| `/api/v1/maintenance-window` | 3 | generic |
| `/api/v1/preferences` | 3 | generic |
| `/api/v1/tasks` | 3 | generic |
| `/api/v1/tools` | 3 | mixed |
| `/api/v1/update` | 3 | generic |
| `/api/v1/catalog` | 2 | mixed |
| `/api/v1/config` | 2 | generic |
| `/api/v1/covers` | 2 | mixed |
| `/api/v1/import` | 2 | mixed |
| `/api/v1/library` | 2 | mixed |
| `/api/v1/narrators` | 2 | audiobook |
| `/api/v1/op-defs` | 2 | generic |
| `/api/v1/work` | 2 | audiobook |
| `/` | 1 | audiobook |
| `/api/events` | 1 | generic |
| `/api/health` | 1 | generic |
| `/api/v1/ai-jobs` | 1 | mixed |
| `/api/v1/csp-report` | 1 | generic |
| `/api/v1/dashboard` | 1 | generic |
| `/api/v1/discovery` | 1 | mixed |
| `/api/v1/file-ops` | 1 | generic |
| `/api/v1/health` | 1 | generic |
| `/api/v1/me` | 1 | generic |
| `/api/v1/metadata-sources` | 1 | mixed |
| `/api/v1/policy` | 1 | mixed |
| `/api/v1/purged-versions` | 1 | mixed |
| `/api/v1/search` | 1 | mixed |
| `/api/v1/setup` | 1 | generic |
| `/api/v1/signals` | 1 | generic |
| `/api/v1/tags` | 1 | mixed |
| `/api/v1/version-groups` | 1 | audiobook |
| `/auth` | 1 | generic |
| `/health` | 1 | generic |
| `/metrics` | 1 | generic |

### 1.2 ABS-surface groups (extra routes, only when `ABSAPIEnabled`)

| group | routes |
|---|---|
| `/api/me` | 20 |
| `/api/libraries` | 12 |
| `/api/collections` | 8 |
| `/api/items` | 8 |
| `/api/playlists` | 7 |
| `/auth` | 5 |
| `/api/session` | 4 |
| `/api/narrators` | 2 |
| `/api/authorize` | 1 |
| `/api/authors` | 1 |
| `/api/emails` | 1 |
| `/api/series` | 1 |
| `/login` | 1 |
| `/logout` | 1 |
| `/ping` | 1 |
| `/public` | 1 |
| `/status` | 1 |

Registered by `internal/server/wire_abs_routes.go` (group at :628) via `handler.Register(absGroup)` (:653); handler package `internal/server/handlers/abs/`.

### 1.3 Permission atoms used at route level (count of routes)

| permission | routes |
|---|---|
| `library.view` | 147 |
| `library.edit_metadata` | 111 |
| `settings.manage` | 83 |
| `library.delete` | 22 |
| `users.manage` | 20 |
| `scan.trigger` | 15 |
| `integrations.manage` | 12 |
| `library.organize` | 9 |
| `collections.manage` | 6 |
| `fingerprint.worker` | 5 |

Not used at route level but defined: `playlists.create`, `requests.create`, `requests.approve` (defined `internal/auth/permissions.go:60,66,70`; no route in the dump carries them). See file 10.

## 2. Registration sites and middleware chain

### 2.1 Where routes are registered

| Site | What | Anchor |
|---|---|---|
| `gin.New()` | engine, trusted proxies disabled | `internal/server/server.go:638`, `:689` |
| `setupRoutes()` | all top-level + `/api/v1` + `/api/v3` registration; called once from `NewServer` | `internal/server/server_lifecycle.go:1710`, call `internal/server/server.go:1258` |
| `wireHandlers()` | instantiates handler structs, then calls the per-domain `wire*Routes` | `internal/server/wire_handlers.go:42`; per-domain calls `:64`, `:754-765` |
| per-domain wiring | `wire_auth_routes.go`, `wire_library_routes.go`, `wire_media_routes.go`, `wire_entities_routes.go`, `wire_operations_routes.go`, `wire_system_routes.go`, `wire_dedup_routes.go`, `wire_review_routes.go`, `wire_repairs_routes.go`, `wire_master_list_routes.go`, `wire_catalog_routes.go`, `wire_audiobooks_routes.go`, `wire_metadata_routes.go`, `wire_fpworker_routes.go`, `wire_ops_v3_routes.go`, `wire_abs_routes.go` | `internal/server/` |
| leftovers still in `setupRoutes` | ~100 `protected.GET/POST` lines (operations survivors, reconcile, openlibrary, update, cover proxy, maintenance jobs) | `internal/server/server_lifecycle.go:1879-2123` |
| route helpers | `s.perm` (:1693), `credRoute/credRouteWhen` (`credential_routes.go`), `adminRoute/adminGroup` (`wire_admin_role.go`), `gone()` (`gone.go:28`), `buildTopLevelAuthChain` (`toplevel_auth_chain.go:30`) | |
| handler sub-packages that register themselves | `handlers/admindebug` (group `/admin/debug`, `handler.go:127`), `handlers/fpworker` (`handler.go:58`), `handlers/opsv3` (`/api/v3/ops`), `handlers/abs` (ABS surface), `handlers/audiobooks`, `handlers/operations`, `handlers/system`, `handlers/repairs`, ... | `internal/server/handlers/` |
| plugin extension point | `plugin.PluginRouter` (GET/POST/PUT/DELETE/Group) scoped to `/api/v1/plugins/<id>/` | `internal/plugin/router.go:11-26`, `internal/server/plugins_init.go:42` |

Handler locations (default build, 497 routes): 150 in the flat `internal/server/handlers/*.go` package, 215 in handler sub-packages (audiobooks 39, entities 33, dedup 30, system 24, metadata 19, operations 14, duplicates 13, opsv3 12, admindebug 7, repairs 6, review 6, fpworker 5, aibackends 4, tools 3), 131 in package `server` itself (methods on `*Server` and closures, of which 45 are `gone` stubs), 1 `gin.WrapH` (`/metrics`). ABS adds 75 in `handlers/abs`.

### 2.2 Middleware chain order (outermost first)

Verified by dumping the real chain of a route (labels came from function names; see 0).

| # | Layer | Scope | Anchor |
|---|---|---|---|
| 0 | Peer allow-list (`ALLOWED_PEER_CIDRS`), wraps the gin engine as the `http.Handler` of EVERY listener, outside gin | all listeners (HTTPS/H2, HTTP/3, plain HTTP, port-80 redirect) | `internal/server/server_lifecycle.go:1078`, `internal/server/peer_allowlist.go:237` |
| 1 | `gin.LoggerWithConfig` (query-redacting formatter; skips `/api/v1/operations/events`, `/api/v3/ops/events`) | global | `internal/server/server.go:695` |
| 2 | `RedactingRecovery` | global | `internal/server/server.go:704` |
| 3 | `securityHeadersMiddleware` | global | `internal/server/server.go:705` (def `server_middleware.go:102`) |
| 4 | `corsMiddleware` (allow-list) | global | `internal/server/server.go:706` (def `server_middleware.go:56`) |
| 5 | `BasicAuth` (optional, config `basic_auth_enabled`; exempts health, static, ABS-authed routes) | global | `internal/server/server.go:707`, `internal/server/middleware/basicauth.go:24` |
| 6 | `captureRawResponseController` | global | `internal/server/server.go:724` |
| 7 | `compressionMiddleware` (gzip; excludes `/metrics`, `/api/events`) | global | `internal/server/server.go:725` (def `:1602`) |
| 8 | `tracingMiddleware()` = 3 handlers: redacted-invite-path swap, `otelgin`, redaction | global | `internal/server/server.go:731`, def `:1672`, `internal/server/middleware/trace_redact.go` |
| -- | routes registered NOW (before the redirect layer): `/metrics`, `/health`, `/api/health`, `/api/v1/health`, `POST /api/v1/csp-report`, `/api/events`, `/auth/temp-login` (7 routes, no redirect/rate-limit/body-limit) | | `internal/server/server_lifecycle.go:1746-1788` |
| 9 | `/api/*` -> `/api/v1/*` 301 redirect (excludes `/api/v1/`, `/api/v3/`, health, events, metrics, ABS-reserved paths) | all routes registered after it | `internal/server/server_lifecycle.go:1791-1822` |
| 10 | ABS group only: `MaxRequestBodySize`, optional `ABSAuthProbe`, `loginCeiling` (login only), then `ABSRequireAuth` per route | `ABS surface` | `internal/server/wire_abs_routes.go:628-650` |
| 11 | `/api/v1` and `/api/v3` group: `apiRateLimiter` (IP limiter, opt-in; passthrough when off) | group | `internal/server/server_lifecycle.go:1829-1838`, group use `:1869-1871`, v3 `:2129-2131` |
| 12 | `bodyLimitMiddleware` (`MaxRequestBodySize`: JSON/upload MB limits) | group | `internal/server/server_lifecycle.go:1840` |
| 13 | `cfMW` Cloudflare Access (fail-open; binds user from verified JWT) - ONLY if configured | group | `internal/server/server_lifecycle.go:1869` (built `wire_oauth.go:35`) |
| 14 | `RequireAuth` (`protected` group; no-op when `EnableAuth=false`; no-op when 0 users = first-run) | `protected` / `protectedV3` | `internal/server/server_lifecycle.go:1841-1848`, `:1874-1875`, `:2133-2134`; impl `internal/server/middleware/auth.go:152` |
| 15 | per-route: `s.perm(p)` -> `RequirePermission`; `adminRole/RequireAdmin`; `credGuard`; `RequireOwner` | route | `server_lifecycle.go:1693`, `wire_admin_role.go`, `credential_routes.go` |
| 16 | Worker confinement: a credential whose permissions are exactly `fingerprint.worker` is refused (403) on any path outside `/api/v1/fingerprint/worker/` | inside RequireAuth | `internal/server/middleware/auth.go:271-306` |
| 17 | SPA/static: `NoRoute` handler serves `index.html` (embedded or on-disk) with `cfRecoveryMW`; ABS-reserved/unmatched `/api` paths answer 404 not HTML | NoRoute | `internal/server/static_embed.go:121`, `static_nonembed.go:109`, `spa_fallback.go:100` |

Notes: (a) the `/api/v1/fingerprint/worker/*` group inserts its own `flagGuard`, `bodyCap`, an `IPRateLimiter`, then auth+perm (`handlers/fpworker/handler.go:56-58`). (b) When `EnableAuth=false`, `authMiddleware` and every `s.perm`/`s.adminRole` become no-ops (`server_lifecycle.go:1693-1698`, `wire_admin_role.go`); `RequireAdmin` guarded "always-authenticated" wipe routes still enforce. Default for `enable_auth` is true (`internal/config/config.go:2801`). (c) An `Alt-Svc` header middleware is added with `router.Use` inside `Start` (`internal/server/server_lifecycle.go:959`) AFTER `setupRoutes` ran (`server.go:1258`); gin binds a route's chain at registration (the repo documents this at `server_lifecycle.go:1712-1717`), so it likely does not reach already-registered routes. INFERENCE, not tested.

### 2.3 Versioning scheme

| Prefix | Meaning | Anchor |
|---|---|---|
| `/api/v1/*` | the application API (all domain routes) | `internal/server/server_lifecycle.go:1867` |
| `/api/v3/ops/*` | operations "v3" API (12 routes: defs, runs, rows, timeline, counts, SSE events); own prefix, same middleware chain | `server_lifecycle.go:2127-2135`, `internal/server/wire_ops_v3_routes.go`, `internal/server/handlers/opsv3/` |
| `/api/v1/operations/v2/*`, `/api/v1/op-defs` | "v2" operations registry, still under v1 prefix (no `/api/v2` exists) | `internal/server/handlers/operations_v2.go` |
| `/api/*` (unversioned) | 301 to `/api/v1/*` unless reserved | `server_lifecycle.go:1791` |
| unversioned ABS paths (`/api/me`, `/api/items`, `/login`, `/ping`, ...) | Audiobookshelf protocol, unversioned by design, feature-flagged | `internal/server/wire_abs_routes.go:628`, reservation lists `:283-345` |
| `/health`, `/api/health`, `/api/v1/health`, `/metrics`, `/api/events` | infra routes at three different prefixes | `server_lifecycle.go:1746-1782` |

There is no breaking-change policy in code: retired routes are kept as `gone()` 410 stubs for "one release" then deleted (`internal/server/gone.go:20-32`, e.g. `wire_catalog_routes.go:25-26`). The web client hard-codes `API_BASE = '/api/v1'` (`web/src/services/api.ts:24`); no `/api/v3` reference exists in non-test `web/src` TS (UNVERIFIED beyond a grep).

### 2.4 Public (unauthenticated) routes, default build

| M | path | handler | note |
|---|---|---|---|
| GET | `/` | `server.Server.setupPlaceholder.func1` `s/static_embed.go:125` |  |
| GET | `/api/health` | `server.Server.setupRoutes.func3` `s/server_lifecycle.go:1710` |  |
| POST | `/api/v1/auth/accept-invite` | `server.Server.handleAcceptInvite` `s/auth_accept_invite.go:40` | inviteRL |
| POST | `/api/v1/auth/bootstrap` | `server.Server.handleBootstrap` `s/bootstrap.go:305` |  |
| GET | `/api/v1/auth/invite/:token` | `h.UserHandler.PublicInvite` `s/handlers/invite_delivery.go:413` | inviteRL |
| POST | `/api/v1/auth/login` | `h.AuthHandler.Login` `s/handlers/auth.go:314` | loginCeiling |
| GET | `/api/v1/auth/oauth-providers` | `h.OAuthHandler.Providers` `s/handlers/oauth_login.go:44` |  |
| GET | `/api/v1/auth/oauth/:provider/callback` | `h.OAuthHandler.Callback` `s/handlers/oauth_login.go:98` |  |
| GET | `/api/v1/auth/oauth/:provider/start` | `h.OAuthHandler.Start` `s/handlers/oauth_login.go:54` |  |
| POST | `/api/v1/auth/setup` | `h.AuthHandler.SetupInitialAdmin` `s/handlers/auth.go:267` |  |
| GET | `/api/v1/auth/status` | `h.AuthHandler.GetStatus` `s/handlers/auth.go:251` |  |
| POST | `/api/v1/csp-report` | `server.cspReporter.handle` `s/csp_report.go:163` |  |
| GET | `/api/v1/health` | `server.Server.setupRoutes.func4` `s/server_lifecycle.go:1710` |  |
| GET | `/auth/temp-login` | `server.Server.consumeTempLoginToken` `s/auth_temp_login.go:161` |  |
| GET | `/health` | `server.Server.setupRoutes.func2` `s/server_lifecycle.go:1710` |  |

`/metrics` and `/api/events` are authenticated via the inline `RequireAuth` in `buildTopLevelAuthChain` (not `protected`), so they appear in the section 4 list, not here. `GET /` is the SPA placeholder when the frontend is not embedded (`static_embed.go:125`).

## 3. Full route table

Closure handlers (`server.Server.setupRoutes.funcN`) are anchored at their enclosing function. Columns: M(ethod) | path (final) | handler `pkg.Type.Func` + definition location (`s/`=`internal/server/`, `i/`=`internal/`, `h.`=`handlers.`) | auth | permission atoms | extra guards | class. Sorted by path. `server.gone.func1` = 410 stub.

### 3.1 Default build (497 routes)

| M | path | handler | auth | permission | guards | class |
|---|---|---|---|---|---|---|
| GET | `/` | `server.Server.setupPlaceholder.func1` `s/static_embed.go:125` | none | - |  | audiobook |
| GET | `/api/events` | `server.Server.setupRoutes.func6` `s/server_lifecycle.go:1710` | session/key | - |  | generic |
| GET | `/api/health` | `server.Server.setupRoutes.func3` `s/server_lifecycle.go:1710` | none | - |  | generic |
| GET | `/api/v1/activity` | `h.ActivityHandler.ListActivity` `s/handlers/activity.go:636` | session/key | library.view |  | mixed |
| POST | `/api/v1/activity/clamp-summaries` | `server.gone.func1` `s/gone.go:28` | session/key | settings.manage |  | mixed |
| POST | `/api/v1/activity/compact` | `h.ActivityCompactHandler.CompactActivity` `s/handlers/activity_compact.go:86` | session/key | settings.manage |  | mixed |
| GET | `/api/v1/activity/sources` | `h.ActivityHandler.ListActivitySources` `s/handlers/activity.go:788` | session/key | library.view |  | mixed |
| GET | `/api/v1/admin/debug/book-files/:id` | `h/admindebug.Handler.GetBookFile` `s/handlers/admindebug/handler.go:303` | session/key | - | RequireAdmin | generic |
| PATCH | `/api/v1/admin/debug/book-files/:id` | `h/admindebug.Handler.PatchBookFile` `s/handlers/admindebug/handler.go:430` | session/key | - | RequireAdmin | generic |
| GET | `/api/v1/admin/debug/books/:id` | `h/admindebug.Handler.GetBook` `s/handlers/admindebug/handler.go:237` | session/key | - | RequireAdmin | generic |
| PATCH | `/api/v1/admin/debug/books/:id` | `h/admindebug.Handler.PatchBook` `s/handlers/admindebug/handler.go:423` | session/key | - | RequireAdmin | generic |
| GET | `/api/v1/admin/debug/edits` | `h/admindebug.Handler.ListEdits` `s/handlers/admindebug/handler.go:748` | session/key | - | RequireAdmin | generic |
| POST | `/api/v1/admin/debug/edits/:edit_id/undo` | `h/admindebug.Handler.UndoEdit` `s/handlers/admindebug/handler.go:779` | session/key | - | RequireAdmin | generic |
| GET | `/api/v1/admin/debug/lookup` | `h/admindebug.Handler.Lookup` `s/handlers/admindebug/handler.go:328` | session/key | - | RequireAdmin | generic |
| GET | `/api/v1/admin/guest-service-tokens` | `server.Server.handleListGuestServiceTokens` `s/guest_service_tokens.go:707` | session/key | - | RequireAdmin, credGuard | generic |
| POST | `/api/v1/admin/guest-service-tokens/:userId/revoke` | `server.Server.handleRevokeGuestServiceToken` `s/guest_service_tokens.go:729` | session/key | - | RequireAdmin, credGuard, RequireOwner | generic |
| GET | `/api/v1/admin/guest-service-tokens/plan` | `server.Server.handlePlanGuestServiceToken` `s/guest_service_tokens.go:768` | session/key | - | RequireAdmin, credGuard, RequireOwner | generic |
| POST | `/api/v1/admin/recompact-digests` | `server.gone.func1` `s/gone.go:28` | session/key | - | RequireAdmin | generic |
| GET | `/api/v1/ai-jobs` | `h.AIHandler.ListAIJobs` `s/handlers/ai.go:945` | session/key | settings.manage |  | mixed |
| POST | `/api/v1/ai/backends/pull-model` | `h/aibackends.Handler.PullModel` `s/handlers/aibackends/aibackends.go:160` | session/key | settings.manage |  | mixed |
| GET | `/api/v1/ai/backends/status` | `h/aibackends.Handler.Status` `s/handlers/aibackends/aibackends.go:102` | session/key | settings.manage |  | mixed |
| GET | `/api/v1/ai/capabilities` | `h/aibackends.Handler.Capabilities` `s/handlers/aibackends/aibackends.go:227` | session/key | settings.manage |  | mixed |
| GET | `/api/v1/ai/endpoints/status` | `h/aibackends.Handler.EndpointsStatus` `s/handlers/aibackends/aibackends.go:311` | session/key | settings.manage |  | mixed |
| POST | `/api/v1/ai/parse-filename` | `h.AIHandler.ParseFilename` `s/handlers/ai.go:251` | session/key | library.edit_metadata |  | mixed |
| GET | `/api/v1/ai/scans` | `h.AIHandler.ListScans` `s/handlers/ai.go:585` | session/key | library.view |  | mixed |
| POST | `/api/v1/ai/scans` | `h.AIHandler.StartScan` `s/handlers/ai.go:525` | session/key | library.edit_metadata |  | mixed |
| DELETE | `/api/v1/ai/scans/:id` | `h.AIHandler.DeleteScan` `s/handlers/ai.go:855` | session/key | library.delete |  | mixed |
| GET | `/api/v1/ai/scans/:id` | `h.AIHandler.GetScan` `s/handlers/ai.go:602` | session/key | library.view |  | mixed |
| POST | `/api/v1/ai/scans/:id/apply` | `h.AIHandler.ApplyScanResults` `s/handlers/ai.go:741` | session/key | library.edit_metadata |  | mixed |
| POST | `/api/v1/ai/scans/:id/cancel` | `h.AIHandler.CancelScan` `s/handlers/ai.go:873` | session/key | library.edit_metadata |  | mixed |
| GET | `/api/v1/ai/scans/:id/results` | `h.AIHandler.GetScanResults` `s/handlers/ai.go:659` | session/key | library.view |  | mixed |
| GET | `/api/v1/ai/scans/compare` | `h.AIHandler.CompareScans` `s/handlers/ai.go:891` | session/key | library.view |  | mixed |
| POST | `/api/v1/ai/test-connection` | `h.AIHandler.TestConnection` `s/handlers/ai.go:279` | session/key | library.edit_metadata |  | mixed |
| GET | `/api/v1/audiobooks` | `h/audiobooks.Handler.ListAudiobooks` `s/handlers/audiobooks/handler.go:743` | session/key | library.view |  | audiobook |
| DELETE | `/api/v1/audiobooks/:id` | `h/audiobooks.Handler.DeleteAudiobook` `s/handlers/audiobooks/handler_crud.go:263` | session/key | library.delete |  | audiobook |
| GET | `/api/v1/audiobooks/:id` | `h/audiobooks.Handler.GetAudiobook` `s/handlers/audiobooks/handler.go:1511` | session/key | library.view |  | audiobook |
| PUT | `/api/v1/audiobooks/:id` | `h/audiobooks.Handler.UpdateAudiobook` `s/handlers/audiobooks/handler_crud.go:41` | session/key | library.edit_metadata |  | audiobook |
| DELETE | `/api/v1/audiobooks/:id/alternative-titles` | `server.gone.func1` `s/gone.go:28` | session/key | library.delete |  | audiobook |
| GET | `/api/v1/audiobooks/:id/alternative-titles` | `server.gone.func1` `s/gone.go:28` | session/key | library.view |  | audiobook |
| POST | `/api/v1/audiobooks/:id/alternative-titles` | `server.gone.func1` `s/gone.go:28` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/:id/apply-metadata` | `h/metadata.Handler.ApplyAudiobookMetadata` `s/handlers/metadata/exported.go:38` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/audiobooks/:id/changelog` | `h/audiobooks.Handler.GetBookChangelog` `s/handlers/audiobooks/handler_metadata.go:287` | session/key | library.view |  | audiobook |
| GET | `/api/v1/audiobooks/:id/changes` | `h/audiobooks.Handler.GetBookChanges` `s/handlers/audiobooks/handler_metadata.go:308` | session/key | library.view |  | audiobook |
| POST | `/api/v1/audiobooks/:id/clear-no-match` | `h.MetadataCacheHandler.ClearMetadataNoMatch` `s/handlers/metadata_cache.go:1531` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/:id/compare-acoustid` | `h/dedup.Handler.HandleCompareAcoustID` `s/handlers/dedup/handler.go:2668` | session/key | library.view |  | audiobook |
| GET | `/api/v1/audiobooks/:id/cover` | `h/audiobooks.Handler.ServeAudiobookCover` `s/handlers/audiobooks/handler.go:1492` | session/key | library.view |  | audiobook |
| GET | `/api/v1/audiobooks/:id/cover-history` | `server.gone.func1` `s/gone.go:28` | session/key | library.view |  | audiobook |
| POST | `/api/v1/audiobooks/:id/cover-history/restore` | `server.gone.func1` `s/gone.go:28` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/audiobooks/:id/cover-text` | `server.Server.handleGetCoverText` `s/cover_text_handler.go:38` | session/key | library.view |  | audiobook |
| GET | `/api/v1/audiobooks/:id/cow-versions` | `h/metadata.Handler.ListBookCOWVersions` `s/handlers/metadata/exported.go:50` | session/key | library.view |  | audiobook |
| POST | `/api/v1/audiobooks/:id/cow-versions/prune` | `h/metadata.Handler.PruneBookCOWVersions` `s/handlers/metadata/exported.go:53` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/:id/discard-progress-and-purge` | `h/audiobooks.Handler.DiscardProgressAndPurge` `s/handlers/audiobooks/handler.go:1023` | session/key | library.delete |  | audiobook |
| GET | `/api/v1/audiobooks/:id/external-ids` | `h/audiobooks.Handler.GetAudiobookExternalIDs` `s/handlers/audiobooks/handler_metadata.go:257` | session/key | library.view |  | audiobook |
| POST | `/api/v1/audiobooks/:id/extract-track-info` | `h/audiobooks.Handler.ExtractTrackInfo` `s/handlers/audiobooks/handler_files.go:628` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/:id/fetch-metadata` | `h/metadata.Handler.FetchAudiobookMetadata` `s/handlers/metadata/exported.go:32` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/audiobooks/:id/field-states` | `h/audiobooks.Handler.GetAudiobookFieldStates` `s/handlers/audiobooks/handler_metadata.go:62` | session/key | library.view |  | audiobook |
| GET | `/api/v1/audiobooks/:id/files` | `h/audiobooks.Handler.ListBookFiles` `s/handlers/audiobooks/handler_files.go:358` | session/key | library.view |  | audiobook |
| PATCH | `/api/v1/audiobooks/:id/files/:file_id` | `h/audiobooks.Handler.PatchBookFile` `s/handlers/audiobooks/handler_files.go:458` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/:id/force-rescan` | `h/audiobooks.Handler.ForceRescanAudiobook` `s/handlers/audiobooks/handler.go:1354` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/:id/mark-no-match` | `h/metadata.Handler.MarkAudiobookNoMatch` `s/handlers/metadata/exported.go:41` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/audiobooks/:id/metadata-history` | `h/audiobooks.Handler.GetBookMetadataHistory` `s/handlers/audiobooks/handler_metadata.go:35` | session/key | library.view |  | audiobook |
| GET | `/api/v1/audiobooks/:id/metadata-history/:field` | `h/audiobooks.Handler.GetFieldMetadataHistory` `s/handlers/audiobooks/handler_metadata.go:73` | session/key | library.view |  | audiobook |
| POST | `/api/v1/audiobooks/:id/metadata-history/:field/undo` | `h/audiobooks.Handler.UndoMetadataChange` `s/handlers/audiobooks/handler_metadata.go:99` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/audiobooks/:id/metadata-rejections` | `h/metadata.Handler.HandleGetMetadataRejections` `s/handlers/metadata/exported.go:44` | session/key | library.view |  | audiobook |
| POST | `/api/v1/audiobooks/:id/move-segments` | `h.VersionsHandler.MoveSegments` `s/handlers/versions.go:1488` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/audiobooks/:id/narrators` | `server.gone.func1` `s/gone.go:28` | session/key | library.view |  | audiobook |
| PUT | `/api/v1/audiobooks/:id/narrators` | `server.gone.func1` `s/gone.go:28` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/:id/organize` | `h.OrganizeHandler.OrganizeBook` `s/handlers/organize.go:171` | session/key | library.organize |  | audiobook |
| POST | `/api/v1/audiobooks/:id/parse-with-ai` | `h.AIHandler.ParseAudiobook` `s/handlers/ai.go:443` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/audiobooks/:id/path-history` | `server.gone.func1` `s/gone.go:28` | session/key | library.view |  | audiobook |
| GET | `/api/v1/audiobooks/:id/position` | `h.ReadingHandler.GetPosition` `s/handlers/reading.go:130` | session/key | - |  | audiobook |
| POST | `/api/v1/audiobooks/:id/position` | `h.ReadingHandler.SetPosition` `s/handlers/reading.go:90` | session/key | - |  | audiobook |
| GET | `/api/v1/audiobooks/:id/preview-organize` | `h.OrganizeHandler.PreviewOrganize` `s/handlers/organize.go:147` | session/key | library.organize |  | audiobook |
| DELETE | `/api/v1/audiobooks/:id/quarantine` | `server.Server.unquarantineBook` `s/quarantine_handlers.go:44` | session/key | settings.manage |  | audiobook |
| POST | `/api/v1/audiobooks/:id/quarantine` | `server.Server.quarantineBook` `s/quarantine_handlers.go:15` | session/key | settings.manage |  | audiobook |
| PATCH | `/api/v1/audiobooks/:id/rating` | `h/metadata.Handler.HandleUpdateBookRating` `s/handlers/metadata/exported.go:68` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/:id/reconcile-files` | `h/audiobooks.Handler.ReconcileAudiobookFiles` `s/handlers/audiobooks/handler.go:1233` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/:id/relocate` | `h/audiobooks.Handler.RelocateBookFiles` `s/handlers/audiobooks/handler_files.go:741` | session/key | library.organize |  | audiobook |
| POST | `/api/v1/audiobooks/:id/rescan` | `server.gone.func1` `s/gone.go:28` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/:id/restore` | `h/audiobooks.Handler.RestoreAudiobook` `s/handlers/audiobooks/handler.go:1380` | session/key | library.organize |  | audiobook |
| GET | `/api/v1/audiobooks/:id/retired-versions` | `h/audiobooks.Handler.ListRetiredVersions` `s/handlers/audiobooks/handler_retired_versions.go:111` | session/key | library.view |  | audiobook |
| POST | `/api/v1/audiobooks/:id/retired-versions/:vid/restore` | `h/audiobooks.Handler.RestoreRetiredVersion` `s/handlers/audiobooks/handler_retired_versions.go:227` | session/key | library.organize |  | audiobook |
| POST | `/api/v1/audiobooks/:id/revert-metadata` | `h/metadata.Handler.RevertAudiobookMetadata` `s/handlers/metadata/exported.go:47` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/audiobooks/:id/sample` | `server.Server.handleAudioSample` `s/audio_sample.go:27` | session/key | library.view |  | audiobook |
| POST | `/api/v1/audiobooks/:id/search-metadata` | `h/metadata.Handler.SearchAudiobookMetadata` `s/handlers/metadata/exported.go:35` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/audiobooks/:id/segments` | `h/audiobooks.Handler.ListAudiobookSegments` `s/handlers/audiobooks/handler_files.go:277` | session/key | library.view |  | audiobook |
| GET | `/api/v1/audiobooks/:id/segments/:segmentId/tags` | `h/audiobooks.Handler.GetSegmentTags` `s/handlers/audiobooks/handler_files.go:862` | session/key | library.view |  | audiobook |
| PUT | `/api/v1/audiobooks/:id/set-primary` | `h.VersionsHandler.SetAudiobookPrimary` `s/handlers/versions.go:536` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/audiobooks/:id/similar` | `server.gone.func1` `s/gone.go:28` | session/key | library.view |  | audiobook |
| POST | `/api/v1/audiobooks/:id/split-to-books` | `h.VersionsHandler.SplitSegmentsToBooks` `s/handlers/versions.go:1048` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/:id/split-version` | `h.VersionsHandler.SplitVersion` `s/handlers/versions.go:742` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/audiobooks/:id/state` | `h.ReadingHandler.GetBookState` `s/handlers/reading.go:146` | session/key | - |  | audiobook |
| DELETE | `/api/v1/audiobooks/:id/status` | `h.ReadingHandler.ClearBookStatus` `s/handlers/reading.go:193` | session/key | - |  | audiobook |
| PATCH | `/api/v1/audiobooks/:id/status` | `h.ReadingHandler.SetBookStatus` `s/handlers/reading.go:162` | session/key | - |  | audiobook |
| POST | `/api/v1/audiobooks/:id/status/repair` | `h.ReadingHandler.RepairBookStatus` `s/handlers/reading.go:247` | session/key | - |  | audiobook |
| GET | `/api/v1/audiobooks/:id/tags` | `h/audiobooks.Handler.GetAudiobookTags` `s/handlers/audiobooks/handler_tags.go:22` | session/key | library.view |  | audiobook |
| GET | `/api/v1/audiobooks/:id/tags-detailed` | `h/audiobooks.Handler.GetBookTagsDetailed` `s/handlers/audiobooks/handler_tags.go:74` | session/key | library.view |  | audiobook |
| POST | `/api/v1/audiobooks/:id/undo-last-apply` | `h/audiobooks.Handler.UndoLastApply` `s/handlers/audiobooks/handler_metadata.go:213` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/audiobooks/:id/user-tags` | `h/audiobooks.Handler.GetBookUserTags` `s/handlers/audiobooks/handler_tags.go:59` | session/key | library.view |  | audiobook |
| POST | `/api/v1/audiobooks/:id/user-tags` | `server.Server.addBookUserTag` `s/user_tags.go:78` | session/key | library.edit_metadata |  | audiobook |
| PUT | `/api/v1/audiobooks/:id/user-tags` | `server.Server.setBookUserTags` `s/user_tags.go:36` | session/key | library.edit_metadata |  | audiobook |
| DELETE | `/api/v1/audiobooks/:id/user-tags/:tag` | `server.Server.removeBookUserTag` `s/user_tags.go:113` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/audiobooks/:id/versions` | `h.VersionsHandler.ListAudiobookVersions` `s/handlers/versions.go:143` | session/key | library.view |  | audiobook |
| POST | `/api/v1/audiobooks/:id/versions` | `h.VersionsHandler.LinkAudiobookVersion` `s/handlers/versions.go:195` | session/key | library.edit_metadata |  | audiobook |
| DELETE | `/api/v1/audiobooks/:id/versions/:vid` | `server.Server.handleTrashVersion` `s/version_lifecycle.go:128` | session/key | library.delete |  | audiobook |
| POST | `/api/v1/audiobooks/:id/versions/:vid/purge-now` | `server.Server.handlePurgeVersion` `s/version_lifecycle.go:409` | session/key | library.delete |  | audiobook |
| POST | `/api/v1/audiobooks/:id/versions/:vid/restore` | `server.Server.handleRestoreVersion` `s/version_lifecycle.go:340` | session/key | library.organize |  | audiobook |
| POST | `/api/v1/audiobooks/:id/versions/resume-swap` | `server.Server.handleResumeVersionSwap` `s/version_lifecycle.go:638` | session/key | library.organize |  | audiobook |
| POST | `/api/v1/audiobooks/:id/write-back` | `h/metadata.Handler.WriteBackAudiobookMetadata` `s/handlers/metadata/exported.go:56` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/batch` | `h/audiobooks.Handler.BatchUpdateAudiobooks` `s/handlers/audiobooks/handler_crud.go:370` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/batch-operations` | `h/audiobooks.Handler.BatchOperations` `s/handlers/audiobooks/handler_crud.go:401` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/batch-tags` | `h/audiobooks.Handler.BatchUpdateTags` `s/handlers/audiobooks/handler_tags.go:92` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/batch-write-back` | `h/metadata.Handler.BatchWriteBackAudiobooks` `s/handlers/metadata/exported.go:62` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/bulk-write-back` | `h/metadata.Handler.HandleBulkWriteBack` `s/handlers/metadata/exported.go:59` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/combine` | `h/duplicates.Handler.CombineBooks` `s/handlers/duplicates/handler.go:422` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/audiobooks/count` | `h/audiobooks.Handler.CountAudiobooks` `s/handlers/audiobooks/handler.go:1416` | session/key | library.view |  | audiobook |
| GET | `/api/v1/audiobooks/duplicates` | `server.gone.func1` `s/gone.go:28` | session/key | library.view |  | audiobook |
| POST | `/api/v1/audiobooks/duplicates/dismiss` | `server.gone.func1` `s/gone.go:28` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/duplicates/link` | `server.gone.func1` `s/gone.go:28` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/duplicates/merge` | `server.gone.func1` `s/gone.go:28` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/duplicates/reject` | `server.gone.func1` `s/gone.go:28` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/duplicates/scan` | `server.gone.func1` `s/gone.go:28` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/audiobooks/duplicates/scan-results` | `server.gone.func1` `s/gone.go:28` | session/key | library.view |  | audiobook |
| GET | `/api/v1/audiobooks/facets` | `h/audiobooks.Handler.AudiobookFacets` `s/handlers/audiobooks/handler.go:1442` | session/key | library.view |  | audiobook |
| POST | `/api/v1/audiobooks/link` | `h/duplicates.Handler.LinkBooks` `s/handlers/duplicates/handler.go:362` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/audiobooks/metadata/batch-apply-cached` | `h.MetadataCacheHandler.BatchApplyFromCache` `s/handlers/metadata_cache.go:1280` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/audiobooks/metadata/cache/review` | `h.MetadataCacheHandler.GetCacheReviewResults` `s/handlers/metadata_cache.go:587` | session/key | library.view |  | audiobook |
| POST | `/api/v1/audiobooks/metadata/cache/review/selection` | `h.MetadataCacheHandler.ReviewSelectionCheck` `s/handlers/metadata_cache_query.go:1582` | session/key | library.view |  | audiobook |
| GET | `/api/v1/audiobooks/metadata/cached` | `h.MetadataCacheHandler.ListCachedCandidates` `s/handlers/metadata_cache.go:366` | session/key | library.view |  | audiobook |
| DELETE | `/api/v1/audiobooks/purge-soft-deleted` | `h/audiobooks.Handler.PurgeSoftDeletedAudiobooks` `s/handlers/audiobooks/handler.go:1160` | session/key | library.delete |  | audiobook |
| POST | `/api/v1/audiobooks/purge-soft-deleted/forget-waiting-file-records` | `h/audiobooks.Handler.ForgetWaitingPurgeFileRecords` `s/handlers/audiobooks/handler.go:1098` | session/key | library.delete |  | audiobook |
| POST | `/api/v1/audiobooks/purge-soft-deleted/preview` | `h/audiobooks.Handler.PreviewPurgeSoftDeleted` `s/handlers/audiobooks/handler.go:1168` | session/key | library.delete |  | audiobook |
| DELETE | `/api/v1/audiobooks/purge-soft-deleted/preview/:id` | `server.Server.cancelPurgePreview` `s/purge_preview_cancel.go:36` | session/key | library.delete |  | audiobook |
| GET | `/api/v1/audiobooks/purge-soft-deleted/waiting-file-records` | `h/audiobooks.Handler.ListWaitingPurgeFileRecords` `s/handlers/audiobooks/handler.go:1072` | session/key | library.delete |  | audiobook |
| GET | `/api/v1/audiobooks/purged-versions` | `server.Server.handleListPurgedVersions` `s/version_lifecycle.go:546` | session/key | library.view |  | audiobook |
| GET | `/api/v1/audiobooks/quarantined` | `server.Server.listQuarantinedBooks` `s/quarantine_handlers.go:62` | session/key | library.view |  | audiobook |
| GET | `/api/v1/audiobooks/soft-deleted` | `h/audiobooks.Handler.ListSoftDeletedAudiobooks` `s/handlers/audiobooks/handler.go:917` | session/key | library.view |  | audiobook |
| GET | `/api/v1/audiobooks/swap-incomplete` | `server.Server.handleListSwapIncompleteBooks` `s/version_lifecycle.go:611` | session/key | library.view |  | audiobook |
| GET | `/api/v1/audiobooks/trashed-versions` | `server.Server.handleListTrashedVersions` `s/version_lifecycle.go:533` | session/key | library.view |  | audiobook |
| POST | `/api/v1/auth/accept-invite` | `server.Server.handleAcceptInvite` `s/auth_accept_invite.go:40` | none | - | inviteRL | generic |
| GET | `/api/v1/auth/api-keys` | `h.APIKeyHandler.List` `s/handlers/apikeys.go:259` | session/key | - |  | generic |
| POST | `/api/v1/auth/api-keys` | `h.APIKeyHandler.Create` `s/handlers/apikeys.go:174` | session/key | - | credGuard-cond | generic |
| DELETE | `/api/v1/auth/api-keys/:id` | `h.APIKeyHandler.Revoke` `s/handlers/apikeys.go:386` | session/key | - |  | generic |
| GET | `/api/v1/auth/api-keys/:id` | `h.APIKeyHandler.Get` `s/handlers/apikeys.go:307` | session/key | - |  | generic |
| PATCH | `/api/v1/auth/api-keys/:id` | `h.APIKeyHandler.UpdateStatus` `s/handlers/apikeys.go:337` | session/key | - | credGuard | generic |
| POST | `/api/v1/auth/api-keys/:id/rotate` | `server.gone.func1` `s/gone.go:28` | session/key | - | credGuard-cond | generic |
| POST | `/api/v1/auth/bootstrap` | `server.Server.handleBootstrap` `s/bootstrap.go:305` | none | - |  | generic |
| GET | `/api/v1/auth/invite/:token` | `h.UserHandler.PublicInvite` `s/handlers/invite_delivery.go:413` | none | - | inviteRL | generic |
| POST | `/api/v1/auth/login` | `h.AuthHandler.Login` `s/handlers/auth.go:314` | none | - | loginCeiling | generic |
| POST | `/api/v1/auth/logout` | `h.AuthHandler.Logout` `s/handlers/auth.go:503` | session/key | - |  | generic |
| GET | `/api/v1/auth/me` | `h.AuthHandler.Me` `s/handlers/auth.go:433` | session/key | - |  | generic |
| PATCH | `/api/v1/auth/me` | `h.AuthHandler.UpdateMe` `s/handlers/auth.go:459` | session/key | - | credGuard | generic |
| PUT | `/api/v1/auth/me/password` | `h.AuthHandler.ChangePassword` `s/handlers/auth.go:572` | session/key | - | credGuard | generic |
| GET | `/api/v1/auth/oauth-providers` | `h.OAuthHandler.Providers` `s/handlers/oauth_login.go:44` | none | - |  | generic |
| GET | `/api/v1/auth/oauth/:provider/callback` | `h.OAuthHandler.Callback` `s/handlers/oauth_login.go:98` | none | - |  | generic |
| GET | `/api/v1/auth/oauth/:provider/start` | `h.OAuthHandler.Start` `s/handlers/oauth_login.go:54` | none | - |  | generic |
| GET | `/api/v1/auth/sessions` | `h.AuthHandler.ListMySessions` `s/handlers/auth.go:513` | session/key | - |  | generic |
| DELETE | `/api/v1/auth/sessions/:id` | `h.AuthHandler.RevokeMySession` `s/handlers/auth.go:540` | session/key | - |  | generic |
| POST | `/api/v1/auth/setup` | `h.AuthHandler.SetupInitialAdmin` `s/handlers/auth.go:267` | none | - |  | generic |
| GET | `/api/v1/auth/status` | `h.AuthHandler.GetStatus` `s/handlers/auth.go:251` | none | - |  | generic |
| POST | `/api/v1/auth/temp-tokens` | `server.Server.createTempLoginToken` `s/auth_temp_login.go:79` | session/key | users.manage | credGuard | generic |
| GET | `/api/v1/authors` | `h/entities.Handler.ListAuthors` `s/handlers/entities/handler.go:345` | session/key | library.view |  | audiobook |
| DELETE | `/api/v1/authors/:id` | `h/entities.Handler.DeleteAuthor` `s/handlers/entities/handler.go:718` | session/key | library.delete |  | audiobook |
| GET | `/api/v1/authors/:id` | `h/entities.Handler.GetAuthor` `s/handlers/entities/handler.go:381` | session/key | library.view |  | audiobook |
| GET | `/api/v1/authors/:id/aliases` | `h/entities.Handler.GetAuthorAliases` `s/handlers/entities/handler.go:820` | session/key | library.view |  | audiobook |
| POST | `/api/v1/authors/:id/aliases` | `h/entities.Handler.CreateAuthorAlias` `s/handlers/entities/handler.go:835` | session/key | library.edit_metadata |  | audiobook |
| DELETE | `/api/v1/authors/:id/aliases/:aliasId` | `h/entities.Handler.DeleteAuthorAlias` `s/handlers/entities/handler.go:866` | session/key | library.delete |  | audiobook |
| GET | `/api/v1/authors/:id/books` | `h/entities.Handler.GetAuthorBooks` `s/handlers/entities/handler.go:793` | session/key | library.view |  | audiobook |
| PUT | `/api/v1/authors/:id/name` | `h/entities.Handler.RenameAuthor` `s/handlers/entities/handler.go:478` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/authors/:id/reclassify-as-narrator` | `h/entities.Handler.ReclassifyAuthorAsNarrator` `s/handlers/entities/handler.go:888` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/authors/:id/resolve-production` | `h/entities.Handler.ResolveProductionAuthor` `s/handlers/entities/handler.go:919` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/authors/:id/split` | `h/entities.Handler.SplitCompositeAuthor` `s/handlers/entities/handler.go:518` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/authors/:id/tags` | `server.gone.func1` `s/gone.go:28` | session/key | library.view |  | audiobook |
| POST | `/api/v1/authors/:id/tags` | `server.gone.func1` `s/gone.go:28` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/authors/bulk-delete` | `h/entities.Handler.BulkDeleteAuthors` `s/handlers/entities/handler.go:749` | session/key | library.delete |  | audiobook |
| GET | `/api/v1/authors/count` | `h/entities.Handler.CountAuthors` `s/handlers/entities/handler.go:468` | session/key | library.view |  | audiobook |
| GET | `/api/v1/authors/duplicates` | `h/duplicates.Handler.ListDuplicateAuthors` `s/handlers/duplicates/handler.go:470` | session/key | library.view |  | audiobook |
| POST | `/api/v1/authors/duplicates/refresh` | `h/duplicates.Handler.RefreshDuplicateAuthors` `s/handlers/duplicates/handler.go:484` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/authors/merge` | `h/entities.Handler.MergeAuthors` `s/handlers/entities/handler.go:661` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/authors/ref-audit` | `h/entities.Handler.AuditAuthorRefs` `s/handlers/entities/author_ref_audit.go:67` | session/key | library.view |  | audiobook |
| DELETE | `/api/v1/backup/:filename` | `h/system.Handler.DeleteBackup` `s/handlers/system/handler.go:906` | session/key | settings.manage |  | generic |
| POST | `/api/v1/backup/create` | `h/system.Handler.CreateBackup` `s/handlers/system/handler.go:726` | session/key | settings.manage |  | generic |
| GET | `/api/v1/backup/list` | `h/system.Handler.ListBackups` `s/handlers/system/handler.go:787` | session/key | settings.manage |  | generic |
| POST | `/api/v1/backup/restore` | `h/system.Handler.RestoreBackup` `s/handlers/system/handler.go:824` | session/key | settings.manage | credGuard, RequireOwner | generic |
| GET | `/api/v1/blocked-hashes` | `h/system.Handler.ListBlockedHashes` `s/handlers/system/handler.go:984` | session/key | library.view |  | generic |
| POST | `/api/v1/blocked-hashes` | `h/system.Handler.AddBlockedHash` `s/handlers/system/handler.go:1004` | session/key | library.edit_metadata |  | generic |
| DELETE | `/api/v1/blocked-hashes/:hash` | `h/system.Handler.RemoveBlockedHash` `s/handlers/system/handler.go:1042` | session/key | library.delete |  | generic |
| GET | `/api/v1/books/:id/position` | `h.ReadingHandler.GetPosition` `s/handlers/reading.go:130` | session/key | - |  | audiobook |
| POST | `/api/v1/books/:id/position` | `h.ReadingHandler.SetPosition` `s/handlers/reading.go:90` | session/key | - |  | audiobook |
| GET | `/api/v1/books/:id/state` | `h.ReadingHandler.GetBookState` `s/handlers/reading.go:146` | session/key | - |  | audiobook |
| DELETE | `/api/v1/books/:id/status` | `h.ReadingHandler.ClearBookStatus` `s/handlers/reading.go:193` | session/key | - |  | audiobook |
| PATCH | `/api/v1/books/:id/status` | `h.ReadingHandler.SetBookStatus` `s/handlers/reading.go:162` | session/key | - |  | audiobook |
| POST | `/api/v1/books/:id/status/repair` | `h.ReadingHandler.RepairBookStatus` `s/handlers/reading.go:247` | session/key | - |  | audiobook |
| DELETE | `/api/v1/books/:id/versions/:vid` | `server.gone.func1` `s/gone.go:28` | session/key | library.delete |  | audiobook |
| POST | `/api/v1/books/:id/versions/:vid/purge-now` | `server.gone.func1` `s/gone.go:28` | session/key | library.delete |  | audiobook |
| POST | `/api/v1/books/:id/versions/:vid/restore` | `server.gone.func1` `s/gone.go:28` | session/key | library.organize |  | audiobook |
| POST | `/api/v1/cache/invalidate` | `server.gone.func1` `s/gone.go:28` | session/key | - | RequireAdmin | generic |
| GET | `/api/v1/cache/stats` | `h.CacheHandler.HandleCacheStats` `s/handlers/cache.go:98` | session/key | library.view |  | generic |
| GET | `/api/v1/cache/stats/history` | `h.CacheHandler.HandleCacheStatsHistory` `s/handlers/cache.go:228` | session/key | library.view |  | generic |
| GET | `/api/v1/cache/stats/keys` | `h.CacheHandler.HandleCacheKeysIntrospection` `s/handlers/cache.go:196` | session/key | - | RequireAdmin | generic |
| GET | `/api/v1/catalog/entries` | `server.gone.func1` `s/gone.go:28` | session/key | library.view |  | mixed |
| GET | `/api/v1/catalog/entries/:id` | `server.gone.func1` `s/gone.go:28` | session/key | library.view |  | mixed |
| GET | `/api/v1/collections` | `h.CollectionHandler.ListCollections` `s/handlers/collections.go:339` | session/key | library.view |  | audiobook |
| POST | `/api/v1/collections` | `h.CollectionHandler.CreateCollection` `s/handlers/collections.go:246` | session/key | collections.manage |  | audiobook |
| DELETE | `/api/v1/collections/:id` | `h.CollectionHandler.DeleteCollection` `s/handlers/collections.go:600` | session/key | collections.manage |  | audiobook |
| GET | `/api/v1/collections/:id` | `h.CollectionHandler.GetCollection` `s/handlers/collections.go:390` | session/key | library.view |  | audiobook |
| PUT | `/api/v1/collections/:id` | `h.CollectionHandler.UpdateCollection` `s/handlers/collections.go:463` | session/key | collections.manage |  | audiobook |
| POST | `/api/v1/collections/:id/books` | `h.CollectionHandler.AddBooksToCollection` `s/handlers/collections.go:1040` | session/key | collections.manage |  | audiobook |
| DELETE | `/api/v1/collections/:id/books/:bookId` | `h.CollectionHandler.RemoveBookFromCollection` `s/handlers/collections.go:1089` | session/key | collections.manage |  | audiobook |
| POST | `/api/v1/collections/:id/materialize` | `h.CollectionHandler.MaterializeCollection` `s/handlers/collections.go:621` | session/key | collections.manage |  | audiobook |
| GET | `/api/v1/config` | `h/system.Handler.GetConfig` `s/handlers/system/handler.go:600` | session/key | settings.manage |  | generic |
| PUT | `/api/v1/config` | `h/system.Handler.UpdateConfig` `s/handlers/system/handler.go:645` | session/key | settings.manage |  | generic |
| GET | `/api/v1/covers/local/:filename` | `server.Server.handleLocalCover` `s/covers.go:66` | session/key | library.view |  | mixed |
| GET | `/api/v1/covers/proxy` | `server.Server.handleCoverProxy` `s/covers.go:26` | session/key | library.view |  | mixed |
| POST | `/api/v1/csp-report` | `server.cspReporter.handle` `s/csp_report.go:163` | none | - |  | generic |
| GET | `/api/v1/dashboard` | `h/system.Handler.GetDashboard` `s/handlers/system/handler.go:934` | session/key | library.view |  | generic |
| GET | `/api/v1/dedup/candidates` | `h/dedup.Handler.ListDedupCandidates` `s/handlers/dedup/handler.go:294` | session/key | library.view |  | audiobook |
| POST | `/api/v1/dedup/candidates` | `h/dedup.Handler.EnqueueManualDedupCandidates` `s/handlers/dedup/manual_candidates.go:102` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/dedup/candidates/:id/breakdown` | `h/dedup.Handler.GetDedupCandidateBreakdown` `s/handlers/dedup/handler.go:573` | session/key | library.view |  | audiobook |
| POST | `/api/v1/dedup/candidates/:id/link` | `h/dedup.Handler.LinkDedupCandidate` `s/handlers/dedup/handler.go:2361` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/dedup/candidates/:id/reject` | `h/dedup.Handler.RejectDedupCandidate` `s/handlers/dedup/handler.go:2510` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/dedup/candidates/book-pair-counts` | `h/dedup.Handler.BookPairCounts` `s/handlers/dedup/book_pair_counts.go:63` | session/key | library.view |  | audiobook |
| POST | `/api/v1/dedup/candidates/bulk-count` | `h/dedup.Handler.BulkCountDedupCandidates` `s/handlers/dedup/handler.go:1685` | session/key | library.view |  | audiobook |
| POST | `/api/v1/dedup/candidates/bulk-link` | `h/dedup.Handler.BulkLinkDedupCandidates` `s/handlers/dedup/handler.go:1301` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/dedup/candidates/bulk-reject` | `h/dedup.Handler.BulkRejectDedupCandidates` `s/handlers/dedup/handler.go:1760` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/dedup/candidates/bulk-reject/revert` | `h/dedup.Handler.RevertBulkRejectDedupCandidates` `s/handlers/dedup/handler.go:1879` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/dedup/candidates/export` | `h/dedup.Handler.ExportDedupCandidates` `s/handlers/dedup/handler.go:723` | session/key | library.view |  | audiobook |
| POST | `/api/v1/dedup/candidates/link-cluster` | `h/dedup.Handler.LinkDedupCluster` `s/handlers/dedup/handler.go:1994` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/dedup/candidates/link-series` | `h/dedup.Handler.LinkDedupCandidateSeries` `s/handlers/dedup/handler.go:1089` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/dedup/candidates/reject-cluster` | `h/dedup.Handler.RejectDedupCluster` `s/handlers/dedup/handler.go:2108` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/dedup/candidates/remove-from-cluster` | `h/dedup.Handler.RemoveFromDedupCluster` `s/handlers/dedup/handler.go:2246` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/dedup/candidates/series-summary` | `h/dedup.Handler.ListDedupCandidateSeries` `s/handlers/dedup/handler.go:965` | session/key | library.view |  | audiobook |
| POST | `/api/v1/dedup/embed` | `h/dedup.Handler.TriggerEmbedScan` `s/handlers/dedup/handler.go:2637` | session/key | scan.trigger |  | audiobook |
| POST | `/api/v1/dedup/fingerprint-rescan` | `server.Server.triggerFingerprintRescan` `s/fingerprint_rescan.go:40` | session/key | scan.trigger |  | audiobook |
| GET | `/api/v1/dedup/labels` | `h/dedup.Handler.ListDedupLabels` `s/handlers/dedup/label_review.go:25` | session/key | library.view |  | audiobook |
| POST | `/api/v1/dedup/labels/:id/override` | `h/dedup.Handler.OverrideDedupLabel` `s/handlers/dedup/label_review.go:199` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/dedup/labels/export` | `h/dedup.Handler.ExportLabeledExamples` `s/handlers/dedup/label_review.go:265` | session/key | library.view |  | audiobook |
| GET | `/api/v1/dedup/labels/stats` | `h/dedup.Handler.GetDedupLabelStats` `s/handlers/dedup/label_review.go:161` | session/key | library.view |  | audiobook |
| GET | `/api/v1/dedup/labels/suspicious` | `h/dedup.Handler.ListSuspiciousDedupLabels` `s/handlers/dedup/label_review.go:123` | session/key | library.view |  | audiobook |
| POST | `/api/v1/dedup/purge-stale` | `h/dedup.Handler.PurgeStaleCandidates` `s/handlers/dedup/handler.go:2622` | session/key | scan.trigger |  | audiobook |
| POST | `/api/v1/dedup/rescore` | `h/dedup.Handler.RescoreDedupCandidates` `s/handlers/dedup/handler.go:666` | session/key | scan.trigger |  | audiobook |
| POST | `/api/v1/dedup/reset-acoustid` | `h/dedup.Handler.ResetAcoustIDFingerprints` `s/handlers/dedup/handler.go:2587` | session/key | scan.trigger |  | audiobook |
| POST | `/api/v1/dedup/scan` | `h/dedup.Handler.TriggerDedupScan` `s/handlers/dedup/handler.go:2538` | session/key | scan.trigger |  | audiobook |
| POST | `/api/v1/dedup/scan-acoustid` | `h/dedup.Handler.TriggerDedupAcoustID` `s/handlers/dedup/handler.go:2568` | session/key | scan.trigger |  | audiobook |
| POST | `/api/v1/dedup/scan-llm` | `h/dedup.Handler.TriggerDedupLLM` `s/handlers/dedup/handler.go:2553` | session/key | scan.trigger |  | audiobook |
| GET | `/api/v1/dedup/split-book-candidates` | `h.SplitBookHandler.ListSplitBookCandidates` `s/handlers/split_book.go:70` | session/key | library.view |  | audiobook |
| POST | `/api/v1/dedup/split-book-candidates/:id/merge` | `h.SplitBookHandler.MergeSplitBookCandidate` `s/handlers/split_book.go:98` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/dedup/split-book-candidates/bulk-merge` | `h.SplitBookHandler.BulkMergeSplitBookCandidates` `s/handlers/split_book.go:181` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/dedup/split-book-scan` | `h.SplitBookHandler.TriggerSplitBookScan` `s/handlers/split_book.go:55` | session/key | scan.trigger |  | audiobook |
| GET | `/api/v1/dedup/stats` | `h/dedup.Handler.GetDedupStats` `s/handlers/dedup/handler.go:1253` | session/key | library.view |  | audiobook |
| POST | `/api/v1/dedup/validate` | `h/duplicates.Handler.ValidateDedupEntry` `s/handlers/duplicates/handler.go:512` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/deluge/discover` | `server.Server.handleDelugeDiscover` `s/deluge_discovery.go:32` | session/key | integrations.manage |  | mixed |
| POST | `/api/v1/deluge/discover/import` | `server.Server.handleDelugeDiscoverImport` `s/deluge_discovery.go:84` | session/key | integrations.manage |  | mixed |
| GET | `/api/v1/deluge/labels` | `server.Server.handleDelugeListLabels` `s/deluge_discovery.go:63` | session/key | integrations.manage |  | mixed |
| GET | `/api/v1/deluge/status` | `server.Server.handleDelugeStatus` `s/deluge_integration.go:110` | session/key | integrations.manage |  | mixed |
| POST | `/api/v1/deluge/test-connection` | `server.Server.handleDelugeTestConnection` `s/deluge_integration.go:59` | session/key | integrations.manage |  | mixed |
| GET | `/api/v1/deluge/torrents` | `server.Server.handleDelugeListTorrents` `s/deluge_integration.go:91` | session/key | integrations.manage |  | mixed |
| GET | `/api/v1/diagnostics/ai-results/:operationId` | `h.DiagnosticsHandler.GetAIResults` `s/handlers/diagnostics.go:516` | session/key | settings.manage |  | mixed |
| POST | `/api/v1/diagnostics/apply-suggestions` | `h.DiagnosticsHandler.ApplySuggestions` `s/handlers/diagnostics.go:584` | session/key | settings.manage |  | mixed |
| GET | `/api/v1/diagnostics/db-census` | `h.DiagnosticsHandler.GetDBCensus` `s/handlers/diagnostics.go:1207` | session/key | settings.manage |  | mixed |
| GET | `/api/v1/diagnostics/db-health` | `h.DiagnosticsHandler.GetDBHealth` `s/handlers/diagnostics.go:850` | session/key | settings.manage |  | mixed |
| POST | `/api/v1/diagnostics/export` | `h.DiagnosticsHandler.StartExport` `s/handlers/diagnostics.go:315` | session/key | settings.manage |  | mixed |
| GET | `/api/v1/diagnostics/export/:operationId/download` | `h.DiagnosticsHandler.DownloadExport` `s/handlers/diagnostics.go:372` | session/key | settings.manage |  | mixed |
| GET | `/api/v1/diagnostics/fingerprint-failures` | `server.Server.getFingerprintFailures` `s/fingerprint_diagnosis_handler.go:38` | session/key | settings.manage |  | mixed |
| POST | `/api/v1/diagnostics/submit-ai` | `h.DiagnosticsHandler.SubmitAI` `s/handlers/diagnostics.go:471` | session/key | settings.manage |  | mixed |
| GET | `/api/v1/diagnostics/traces` | `h.DiagnosticsTracesHandler.List` `s/handlers/diagnostics_traces.go:43` | session/key | settings.manage |  | mixed |
| GET | `/api/v1/diagnostics/traces/:name` | `h.DiagnosticsTracesHandler.Download` `s/handlers/diagnostics_traces.go:59` | session/key | settings.manage |  | mixed |
| POST | `/api/v1/discovery/import` | `server.Server.handleDiscoveryImport` `s/deluge_discovery.go:260` | session/key | settings.manage |  | mixed |
| GET | `/api/v1/file-ops/pending` | `server.Server.handleListPendingFileOps` `s/file_ops_handlers.go:28` | session/key | library.view |  | generic |
| GET | `/api/v1/filesystem/browse` | `h.FilesystemHandler.BrowseFilesystem` `s/handlers/filesystem.go:130` | session/key | settings.manage |  | mixed |
| DELETE | `/api/v1/filesystem/exclude` | `h.FilesystemHandler.RemoveExclusion` `s/handlers/filesystem.go:161` | session/key | settings.manage |  | mixed |
| POST | `/api/v1/filesystem/exclude` | `h.FilesystemHandler.CreateExclusion` `s/handlers/filesystem.go:145` | session/key | settings.manage |  | mixed |
| GET | `/api/v1/filesystem/home` | `h.FilesystemHandler.GetHomeDirectory` `s/handlers/filesystem.go:120` | session/key | settings.manage |  | mixed |
| GET | `/api/v1/fingerprint/worker/hello` | `h/fpworker.Handler.hello` `s/handlers/fpworker/handler.go:145` | session/key | fingerprint.worker | fpFlagGuard, bodyCap, IPRateLimiter | mixed |
| POST | `/api/v1/fingerprint/worker/lease` | `h/fpworker.Handler.lease` `s/handlers/fpworker/handler.go:162` | session/key | fingerprint.worker | fpFlagGuard, bodyCap, IPRateLimiter | mixed |
| POST | `/api/v1/fingerprint/worker/lease/:id/release` | `h/fpworker.Handler.release` `s/handlers/fpworker/handler.go:198` | session/key | fingerprint.worker | fpFlagGuard, bodyCap, IPRateLimiter | mixed |
| POST | `/api/v1/fingerprint/worker/lease/:id/renew` | `h/fpworker.Handler.renew` `s/handlers/fpworker/handler.go:182` | session/key | fingerprint.worker | fpFlagGuard, bodyCap, IPRateLimiter | mixed |
| POST | `/api/v1/fingerprint/worker/results` | `h/fpworker.Handler.results` `s/handlers/fpworker/handler.go:214` | session/key | fingerprint.worker | fpFlagGuard, bodyCap, IPRateLimiter | mixed |
| GET | `/api/v1/health` | `server.Server.setupRoutes.func4` `s/server_lifecycle.go:1710` | none | - |  | generic |
| GET | `/api/v1/import-paths` | `h.FilesystemHandler.ListImportPaths` `s/handlers/filesystem.go:177` | session/key | settings.manage |  | mixed |
| POST | `/api/v1/import-paths` | `h.FilesystemHandler.AddImportPath` `s/handlers/filesystem.go:215` | session/key | settings.manage | credGuard | mixed |
| DELETE | `/api/v1/import-paths/:id` | `h.FilesystemHandler.RemoveImportPath` `s/handlers/filesystem.go:328` | session/key | settings.manage |  | mixed |
| POST | `/api/v1/import/collision-preview` | `server.gone.func1` `s/gone.go:28` | session/key | library.view |  | mixed |
| POST | `/api/v1/import/file` | `h.FilesystemHandler.ImportFile` `s/handlers/filesystem.go:347` | session/key | scan.trigger |  | mixed |
| GET | `/api/v1/itunes/books` | `h.ITunesHandler.ListBooks` `s/handlers/itunes.go:359` | session/key | library.view |  | mixed |
| POST | `/api/v1/itunes/import` | `h.ITunesHandler.Import` `s/handlers/itunes.go:280` | session/key | library.edit_metadata |  | mixed |
| GET | `/api/v1/itunes/import-status/:id` | `h.ITunesHandler.ImportStatus` `s/handlers/itunes.go:447` | session/key | library.view |  | mixed |
| POST | `/api/v1/itunes/import-status/bulk` | `h.ITunesHandler.ImportStatusBulk` `s/handlers/itunes.go:485` | session/key | library.edit_metadata |  | mixed |
| GET | `/api/v1/itunes/library-stats` | `h.ITunesHandler.LibraryStats` `s/handlers/itunes.go:592` | session/key | library.view |  | mixed |
| GET | `/api/v1/itunes/library-status` | `h.ITunesHandler.LibraryStatus` `s/handlers/itunes.go:535` | session/key | library.view |  | mixed |
| GET | `/api/v1/itunes/library/download` | `server.Server.itunesSvcGuard.func1` `s/server_lifecycle.go:1700` | session/key | integrations.manage |  | mixed |
| GET | `/api/v1/itunes/pid-integrity` | `server.Server.pidIntegrityHandler` `s/itl_pid.go:27` | session/key | library.edit_metadata |  | mixed |
| POST | `/api/v1/itunes/pid-repair` | `server.gone.func1` `s/gone.go:28` | session/key | library.edit_metadata |  | mixed |
| POST | `/api/v1/itunes/test-mapping` | `h.ITunesHandler.TestMapping` `s/handlers/itunes.go:251` | session/key | library.edit_metadata |  | mixed |
| POST | `/api/v1/itunes/validate` | `h.ITunesHandler.Validate` `s/handlers/itunes.go:212` | session/key | library.edit_metadata |  | mixed |
| GET | `/api/v1/library/metadata-results` | `server.Server.handleListMetadataResults` `s/metadata_batch_candidates.go:1183` | session/key | library.view |  | mixed |
| GET | `/api/v1/library/quick-queries` | `h/system.Handler.GetQuickQueries` `s/handlers/system/handler.go:1165` | session/key | library.view |  | mixed |
| PUT | `/api/v1/maintenance-window/config` | `h.SchedulerHandler.UpdateMaintenanceWindowConfig` `s/handlers/scheduler_admin.go:523` | session/key | settings.manage |  | generic |
| POST | `/api/v1/maintenance-window/run` | `h.SchedulerHandler.RunMaintenanceWindowNow` `s/handlers/scheduler_admin.go:477` | session/key | settings.manage |  | generic |
| GET | `/api/v1/maintenance-window/status` | `h.SchedulerHandler.GetMaintenanceWindowStatus` `s/handlers/scheduler_admin.go:493` | session/key | settings.manage |  | generic |
| GET | `/api/v1/maintenance/acoustid-stats` | `server.Server.handleGetAcoustIDStats` `s/maintenance_fixups.go:269` | session/key | settings.manage |  | mixed |
| GET | `/api/v1/maintenance/book-file-hash-stats` | `server.Server.handleGetBookFileHashStats` `s/maintenance_fixups.go:234` | session/key | settings.manage |  | mixed |
| GET | `/api/v1/maintenance/book-metadata-hash-stats` | `server.Server.handleGetBookMetadataHashStats` `s/maintenance_fixups.go:253` | session/key | settings.manage |  | mixed |
| GET | `/api/v1/maintenance/file-intro-stats` | `h/system.Handler.GetFileIntroStats` `s/handlers/system/handler.go:1226` | session/key | library.view |  | mixed |
| GET | `/api/v1/maintenance/jobs` | `server.Server.listMaintenanceJobs` `s/maintenance_dispatcher.go:23` | session/key | settings.manage |  | mixed |
| POST | `/api/v1/maintenance/jobs/:job_id` | `server.Server.runMaintenanceJob` `s/maintenance_dispatcher.go:81` | session/key | settings.manage |  | mixed |
| GET | `/api/v1/maintenance/repair-missing-files/:id` | `server.gone.func1` `s/gone.go:28` | session/key | settings.manage |  | mixed |
| GET | `/api/v1/maintenance/scan-composer-tags/:id` | `server.Server.handleGetComposerScanResults` `s/maintenance_fixups.go:158` | session/key | settings.manage |  | mixed |
| GET | `/api/v1/maintenance/transcribe-stats` | `h/system.Handler.GetTranscribeStats` `s/handlers/system/handler.go:1198` | session/key | library.view |  | mixed |
| POST | `/api/v1/maintenance/wipe` | `server.gone.func1` `s/gone.go:28` | session/key | - | RequireAdmin | mixed |
| GET | `/api/v1/master-lists` | `server.Server.listMasterLists` `s/master_list_handler.go:243` | session/key | users.manage | RequireAdmin | mixed |
| POST | `/api/v1/master-lists` | `server.Server.uploadMasterList` `s/master_list_handler.go:108` | session/key | users.manage | RequireAdmin | mixed |
| DELETE | `/api/v1/master-lists/:id` | `server.Server.deleteMasterList` `s/master_list_handler.go:261` | session/key | users.manage | RequireAdmin | mixed |
| GET | `/api/v1/master-lists/elsewhere` | `server.Server.listMasterListElsewhere` `s/master_list_handler.go:292` | session/key | users.manage | RequireAdmin | mixed |
| GET | `/api/v1/me/:status` | `h.ReadingHandler.ListByStatus` `s/handlers/reading.go:275` | session/key | - |  | generic |
| GET | `/api/v1/merge/combine-journal` | `h/duplicates.Handler.ListCombineJournals` `s/handlers/duplicates/handler.go:227` | session/key | library.view |  | mixed |
| GET | `/api/v1/merge/sibling-journal` | `h/duplicates.Handler.ListSiblingMoveJournals` `s/handlers/duplicates/handler.go:264` | session/key | library.view |  | mixed |
| POST | `/api/v1/merge/sibling-undo/:journal_id` | `server.gone.func1` `s/gone.go:28` | session/key | library.edit_metadata |  | mixed |
| POST | `/api/v1/merge/undo/:journal_id` | `server.gone.func1` `s/gone.go:28` | session/key | library.edit_metadata |  | mixed |
| POST | `/api/v1/metadata-sources/test` | `h.AIHandler.TestMetadataSource` `s/handlers/ai.go:308` | session/key | settings.manage |  | mixed |
| POST | `/api/v1/metadata/batch-apply-candidates` | `server.Server.handleBatchApplyCandidates` `s/metadata_batch_candidates.go:805` | session/key | library.edit_metadata |  | mixed |
| POST | `/api/v1/metadata/batch-fetch-candidates` | `server.Server.handleBatchFetchCandidates` `s/metadata_batch_candidates.go:57` | session/key | library.edit_metadata |  | mixed |
| POST | `/api/v1/metadata/batch-reject-candidates` | `server.Server.handleRejectCandidates` `s/metadata_batch_candidates.go:911` | session/key | library.edit_metadata |  | mixed |
| POST | `/api/v1/metadata/batch-unreject-candidates` | `server.Server.handleUnrejectCandidates` `s/metadata_batch_candidates.go:986` | session/key | library.edit_metadata |  | mixed |
| POST | `/api/v1/metadata/batch-update` | `h/metadata.Handler.BatchUpdateMetadata` `s/handlers/metadata/exported.go:17` | session/key | library.edit_metadata |  | mixed |
| POST | `/api/v1/metadata/bulk-apply-preview` | `server.gone.func1` `s/gone.go:28` | session/key | library.view |  | mixed |
| GET | `/api/v1/metadata/bulk-apply-preview/:id` | `server.gone.func1` `s/gone.go:28` | session/key | library.view |  | mixed |
| POST | `/api/v1/metadata/bulk-fetch` | `server.gone.func1` `s/gone.go:28` | session/key | library.edit_metadata |  | mixed |
| GET | `/api/v1/metadata/candidate-feedback` | `server.Server.handleExportCandidateFeedback` `s/candidate_feedback.go:136` | session/key | library.edit_metadata |  | mixed |
| POST | `/api/v1/metadata/candidate-feedback` | `server.Server.handlePutCandidateFeedback` `s/candidate_feedback.go:54` | session/key | library.edit_metadata |  | mixed |
| DELETE | `/api/v1/metadata/candidate-feedback/:id` | `server.Server.handleDeleteCandidateFeedback` `s/candidate_feedback.go:118` | session/key | library.edit_metadata |  | mixed |
| GET | `/api/v1/metadata/export` | `h/metadata.Handler.ExportMetadata` `s/handlers/metadata/exported.go:23` | session/key | library.view |  | mixed |
| GET | `/api/v1/metadata/fields` | `h/metadata.Handler.GetMetadataFields` `s/handlers/metadata/exported.go:65` | session/key | library.view |  | mixed |
| POST | `/api/v1/metadata/import` | `h/metadata.Handler.ImportMetadata` `s/handlers/metadata/exported.go:26` | session/key | library.edit_metadata |  | mixed |
| DELETE | `/api/v1/metadata/providers/throttles` | `server.gone.func1` `s/gone.go:28` | session/key | library.edit_metadata |  | mixed |
| GET | `/api/v1/metadata/providers/throttles` | `h/metadata.Handler.ListProviderThrottles` `s/handlers/metadata/exported.go:71` | session/key | library.view |  | mixed |
| DELETE | `/api/v1/metadata/providers/throttles/:id` | `server.gone.func1` `s/gone.go:28` | session/key | library.edit_metadata |  | mixed |
| GET | `/api/v1/metadata/recent-fetches` | `server.Server.handleGetLatestMetadataFetch` `s/metadata_batch_candidates.go:710` | session/key | library.view |  | mixed |
| GET | `/api/v1/metadata/search` | `h/metadata.Handler.SearchMetadata` `s/handlers/metadata/exported.go:29` | session/key | library.view |  | mixed |
| POST | `/api/v1/metadata/unreject-candidate` | `server.Server.handleUnrejectCandidate` `s/metadata_batch_candidates.go:1077` | session/key | library.edit_metadata |  | mixed |
| POST | `/api/v1/metadata/validate` | `h/metadata.Handler.ValidateMetadata` `s/handlers/metadata/exported.go:20` | session/key | library.edit_metadata |  | mixed |
| GET | `/api/v1/narrators` | `h/entities.Handler.ListNarrators` `s/handlers/entities/handler.go:1256` | session/key | library.view |  | audiobook |
| GET | `/api/v1/narrators/count` | `h/entities.Handler.CountNarrators` `s/handlers/entities/handler.go:1270` | session/key | library.view |  | audiobook |
| GET | `/api/v1/op-defs` | `h.OperationsV2Handler.ListOpDefs` `s/handlers/operations_v2.go:1032` | session/key | library.view |  | generic |
| GET | `/api/v1/op-defs/:id` | `h.OperationsV2Handler.GetOpDef` `s/handlers/operations_v2.go:1046` | session/key | library.view |  | generic |
| DELETE | `/api/v1/openlibrary/data` | `server.Server.deleteOLData` `s/openlibrary_service.go:197` | session/key | integrations.manage |  | mixed |
| POST | `/api/v1/openlibrary/download` | `server.Server.startOLDownload` `s/openlibrary_service.go:74` | session/key | integrations.manage |  | mixed |
| POST | `/api/v1/openlibrary/import` | `server.Server.startOLImport` `s/openlibrary_service.go:108` | session/key | integrations.manage |  | mixed |
| GET | `/api/v1/openlibrary/status` | `server.Server.getOLStatus` `s/openlibrary_service.go:27` | session/key | integrations.manage |  | mixed |
| POST | `/api/v1/openlibrary/upload` | `server.Server.uploadOLDump` `s/openlibrary_service.go:138` | session/key | integrations.manage |  | mixed |
| GET | `/api/v1/operations/:id/activity` | `h.ActivityHandler.ListOperationActivity` `s/handlers/activity.go:900` | session/key | library.view |  | generic |
| GET | `/api/v1/operations/:id/changes` | `h/operations.Handler.GetOperationChanges` `s/handlers/operations/handler.go:614` | session/key | library.view |  | generic |
| GET | `/api/v1/operations/:id/class-lists/:class` | `h/operations.Handler.GetOperationClassList` `s/handlers/operations/handler.go:523` | session/key | library.view |  | generic |
| GET | `/api/v1/operations/:id/result` | `h/operations.Handler.GetOperationResult` `s/handlers/operations/handler.go:464` | session/key | library.view |  | generic |
| GET | `/api/v1/operations/:id/results` | `server.Server.handleGetOperationResults` `s/metadata_batch_candidates.go:581` | session/key | library.view |  | generic |
| POST | `/api/v1/operations/:id/revert` | `h/operations.Handler.RevertOperation` `s/handlers/operations/handler.go:696` | session/key | library.organize |  | generic |
| GET | `/api/v1/operations/:id/undo/preflight` | `h/operations.Handler.UndoPreflightHandler` `s/handlers/operations/handler.go:660` | session/key | library.view |  | generic |
| GET | `/api/v1/operations/active` | `server.gone.func1` `s/gone.go:28` | session/key | library.view |  | generic |
| POST | `/api/v1/operations/activity/merged` | `h.ActivityHandler.ListMergedOperationActivity` `s/handlers/activity.go:953` | session/key | library.view |  | generic |
| POST | `/api/v1/operations/assign-orphan-vgs` | `server.Server.assignOrphanVGsHandler` `s/reconcile.go:274` | session/key | settings.manage |  | generic |
| GET | `/api/v1/operations/audit-files` | `h/operations.Handler.AuditFileConsistency` `s/handlers/operations/handler.go:401` | session/key | settings.manage |  | generic |
| POST | `/api/v1/operations/backfill-scan-cache` | `server.Server.backfillScanCacheHandler` `s/scan_cache_backfill.go:56` | session/key | settings.manage |  | generic |
| POST | `/api/v1/operations/cleanup-version-groups` | `server.Server.cleanupDuplicateVersionGroupsHandler` `s/reconcile.go:235` | session/key | settings.manage |  | generic |
| POST | `/api/v1/operations/clear-stale` | `h/operations.Handler.ClearStaleOperations` `s/handlers/operations/handler.go:224` | session/key | settings.manage |  | generic |
| POST | `/api/v1/operations/elect-missing-primaries` | `server.Server.electMissingPrimariesHandler` `s/reconcile.go:303` | session/key | settings.manage |  | generic |
| GET | `/api/v1/operations/events` | `h.OperationsV2Handler.OperationsSSE` `s/handlers/operations_v2.go:1063` | session/key | library.view |  | generic |
| POST | `/api/v1/operations/mark-broken-segments` | `server.Server.markBrokenSegmentBooksHandler` `s/reconcile.go:248` | session/key | settings.manage |  | generic |
| POST | `/api/v1/operations/merge-novg-duplicates` | `server.Server.mergeNoVGDuplicatesHandler` `s/reconcile.go:261` | session/key | settings.manage |  | generic |
| POST | `/api/v1/operations/optimize-database` | `h/operations.Handler.OptimizeDatabase` `s/handlers/operations/handler.go:254` | session/key | settings.manage |  | generic |
| GET | `/api/v1/operations/pause` | `h/operations.Handler.GetPauseState` `s/handlers/operations/pause.go:114` | session/key | library.view |  | generic |
| POST | `/api/v1/operations/pause` | `h/operations.Handler.PauseOperations` `s/handlers/operations/pause.go:81` | session/key | settings.manage |  | generic |
| GET | `/api/v1/operations/recent` | `server.gone.func1` `s/gone.go:28` | session/key | library.view |  | generic |
| POST | `/api/v1/operations/reconcile` | `server.Server.startReconcile` `s/reconcile.go:195` | session/key | scan.trigger |  | generic |
| GET | `/api/v1/operations/reconcile/preview` | `server.Server.reconcilePreview` `s/reconcile.go:27` | session/key | library.view |  | generic |
| POST | `/api/v1/operations/reconcile/scan` | `server.Server.startReconcileScan` `s/reconcile.go:50` | session/key | scan.trigger |  | generic |
| GET | `/api/v1/operations/reconcile/scan/latest` | `server.Server.latestReconcileScan` `s/reconcile.go:137` | session/key | library.view |  | generic |
| POST | `/api/v1/operations/resume` | `h/operations.Handler.ResumeOperations` `s/handlers/operations/pause.go:106` | session/key | settings.manage |  | generic |
| POST | `/api/v1/operations/set-internal-flag` | `h/operations.Handler.SetInternalFlag` `s/handlers/operations/handler.go:379` | session/key | settings.manage |  | generic |
| GET | `/api/v1/operations/stale` | `h/operations.Handler.ListStaleOperations` `s/handlers/operations/handler.go:417` | session/key | library.view |  | generic |
| POST | `/api/v1/operations/sweep-tombstones` | `h/operations.Handler.SweepTombstones` `s/handlers/operations/handler.go:359` | session/key | settings.manage |  | generic |
| GET | `/api/v1/operations/timeline` | `h.OperationsV2Handler.GetOperationTimeline` `s/handlers/operations_v2.go:237` | session/key | library.view |  | generic |
| POST | `/api/v1/operations/v2` | `h.OperationsV2Handler.TriggerOperationV2` `s/handlers/operations_v2.go:704` | session/key | scan.trigger |  | generic |
| DELETE | `/api/v1/operations/v2/:id` | `h.OperationsV2Handler.CancelOperationV2` `s/handlers/operations_v2.go:620` | session/key | settings.manage |  | generic |
| GET | `/api/v1/operations/v2/:id` | `h.OperationsV2Handler.GetOperationV2` `s/handlers/operations_v2.go:435` | session/key | library.view |  | generic |
| POST | `/api/v1/operations/v2/:id/finish` | `h.OperationsV2Handler.FinishOperationV2` `s/handlers/operations_v2.go:974` | session/key | settings.manage |  | generic |
| GET | `/api/v1/operations/v2/:id/logs/download` | `h.OperationsV2Handler.DownloadOperationLogs` `s/handlers/operations_v2.go:494` | session/key | library.view |  | generic |
| DELETE | `/api/v1/operations/v2/:id/record` | `h.OperationsV2Handler.DiscardOperationV2` `s/handlers/operations_v2.go:680` | session/key | settings.manage |  | generic |
| POST | `/api/v1/operations/v2/:id/retry` | `h.OperationsV2Handler.RetryOperationV2` `s/handlers/operations_v2.go:849` | session/key | settings.manage |  | generic |
| GET | `/api/v1/playlists` | `h.PlaylistHandler.ListPlaylists` `s/handlers/playlists.go:158` | session/key | library.view |  | audiobook |
| POST | `/api/v1/playlists` | `h.PlaylistHandler.CreatePlaylist` `s/handlers/playlists.go:121` | session/key | - |  | audiobook |
| DELETE | `/api/v1/playlists/:id` | `h.PlaylistHandler.DeletePlaylist` `s/handlers/playlists.go:329` | session/key | - |  | audiobook |
| GET | `/api/v1/playlists/:id` | `h.PlaylistHandler.GetPlaylist` `s/handlers/playlists.go:181` | session/key | - |  | audiobook |
| PUT | `/api/v1/playlists/:id` | `h.PlaylistHandler.UpdatePlaylist` `s/handlers/playlists.go:271` | session/key | - |  | audiobook |
| POST | `/api/v1/playlists/:id/books` | `h.PlaylistHandler.AddBooksToPlaylist` `s/handlers/playlists.go:352` | session/key | - |  | audiobook |
| DELETE | `/api/v1/playlists/:id/books/:bookID` | `h.PlaylistHandler.RemoveBookFromPlaylist` `s/handlers/playlists.go:378` | session/key | - |  | audiobook |
| GET | `/api/v1/playlists/:id/export.m3u` | `server.gone.func1` `s/gone.go:28` | session/key | - |  | audiobook |
| POST | `/api/v1/playlists/:id/materialize` | `h.PlaylistHandler.MaterializePlaylist` `s/handlers/playlists.go:476` | session/key | - |  | audiobook |
| POST | `/api/v1/playlists/:id/reorder` | `h.PlaylistHandler.ReorderPlaylist` `s/handlers/playlists.go:398` | session/key | - |  | audiobook |
| GET | `/api/v1/plugins` | `h.PluginsHandler.ListPlugins` `s/handlers/plugins.go:68` | session/key | settings.manage |  | generic |
| GET | `/api/v1/plugins/:id` | `h.PluginsHandler.GetPlugin` `s/handlers/plugins.go:82` | session/key | settings.manage |  | generic |
| POST | `/api/v1/plugins/:id/disable` | `h.PluginsHandler.DisablePlugin` `s/handlers/plugins.go:121` | session/key | settings.manage |  | generic |
| POST | `/api/v1/plugins/:id/enable` | `h.PluginsHandler.EnablePlugin` `s/handlers/plugins.go:97` | session/key | settings.manage |  | generic |
| GET | `/api/v1/plugins/:id/health` | `h.PluginsHandler.PluginHealth` `s/handlers/plugins.go:144` | session/key | settings.manage |  | generic |
| PUT | `/api/v1/plugins/:id/settings` | `h.PluginsHandler.UpdatePluginSettings` `s/handlers/plugins.go:163` | session/key | settings.manage | credGuard | generic |
| GET | `/api/v1/policy/tags` | `h/system.Handler.HandlePolicyTags` `s/handlers/system/handler.go:1156` | session/key | library.view |  | mixed |
| DELETE | `/api/v1/preferences/:key` | `h/system.Handler.DeleteUserPreference` `s/handlers/system/handler.go:1133` | session/key | library.delete |  | generic |
| GET | `/api/v1/preferences/:key` | `h/system.Handler.GetUserPreference` `s/handlers/system/handler.go:1074` | session/key | library.view |  | generic |
| PUT | `/api/v1/preferences/:key` | `h/system.Handler.SetUserPreference` `s/handlers/system/handler.go:1101` | session/key | library.edit_metadata |  | generic |
| DELETE | `/api/v1/purged-versions/:vid` | `server.Server.handleHardDeleteVersion` `s/version_lifecycle.go:568` | session/key | library.delete |  | mixed |
| GET | `/api/v1/repairs` | `h/repairs.Handler.ListFixers` `s/handlers/repairs/handler.go:268` | session/key | library.view |  | mixed |
| POST | `/api/v1/repairs/:fixer/apply` | `h/repairs.Handler.StartApply` `s/handlers/repairs/handler.go:446` | session/key | library.edit_metadata |  | mixed |
| POST | `/api/v1/repairs/:fixer/owner-apply` | `h/repairs.Handler.OwnerApply` `s/handlers/repairs/handler.go:502` | session/key | library.edit_metadata |  | mixed |
| POST | `/api/v1/repairs/:fixer/plan` | `h/repairs.Handler.StartPlan` `s/handlers/repairs/handler.go:352` | session/key | library.edit_metadata |  | mixed |
| GET | `/api/v1/repairs/:fixer/plan/:op_id/rows` | `h/repairs.Handler.ListPlanRows` `s/handlers/repairs/handler.go:384` | session/key | library.view |  | mixed |
| GET | `/api/v1/repairs/owner-status` | `h/repairs.Handler.GetOwnerStatus` `s/handlers/repairs/handler.go:195` | session/key | library.view |  | mixed |
| POST | `/api/v1/review/bulk` | `h/review.Handler.BulkReviewAction` `s/handlers/review/handler.go:1199` | session/key | library.edit_metadata |  | mixed |
| GET | `/api/v1/review/count` | `h/review.Handler.GetReviewCount` `s/handlers/review/handler.go:369` | session/key | library.view |  | mixed |
| GET | `/api/v1/review/items` | `h/review.Handler.ListReviewItems` `s/handlers/review/handler.go:413` | session/key | library.view |  | mixed |
| POST | `/api/v1/review/items/:id/abandon-apply` | `h/review.Handler.AbandonApply` `s/handlers/review/handler.go:1072` | session/key | library.edit_metadata |  | mixed |
| POST | `/api/v1/review/items/:id/approve` | `h/review.Handler.ApproveReviewItem` `s/handlers/review/handler.go:502` | session/key | library.edit_metadata |  | mixed |
| POST | `/api/v1/review/items/:id/reject` | `h/review.Handler.RejectReviewItem` `s/handlers/review/handler.go:1031` | session/key | library.edit_metadata |  | mixed |
| POST | `/api/v1/review/replay-approved` | `server.gone.func1` `s/gone.go:28` | session/key | library.edit_metadata |  | mixed |
| GET | `/api/v1/search/:search_id` | `server.Server.getSearchJob` `s/search_result_cache.go:56` | session/key | library.view |  | mixed |
| GET | `/api/v1/series` | `h/entities.Handler.ListSeries` `s/handlers/entities/handler.go:975` | session/key | library.view |  | audiobook |
| DELETE | `/api/v1/series/:id` | `h/entities.Handler.DeleteEmptySeries` `s/handlers/entities/handler.go:1154` | session/key | library.delete |  | audiobook |
| PATCH | `/api/v1/series/:id` | `h/entities.Handler.UpdateSeriesName` `s/handlers/entities/handler.go:1231` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/series/:id/books` | `h/entities.Handler.GetSeriesBooks` `s/handlers/entities/handler.go:1003` | session/key | library.view |  | audiobook |
| PUT | `/api/v1/series/:id/name` | `h/entities.Handler.RenameSeries` `s/handlers/entities/handler.go:1031` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/series/:id/split` | `h/entities.Handler.SplitSeries` `s/handlers/entities/handler.go:1100` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/series/:id/tags` | `server.gone.func1` `s/gone.go:28` | session/key | library.view |  | audiobook |
| POST | `/api/v1/series/:id/tags` | `server.gone.func1` `s/gone.go:28` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/series/bulk-delete` | `h/entities.Handler.BulkDeleteSeries` `s/handlers/entities/handler.go:1184` | session/key | library.delete |  | audiobook |
| GET | `/api/v1/series/count` | `h/entities.Handler.CountSeries` `s/handlers/entities/handler.go:955` | session/key | library.view |  | audiobook |
| POST | `/api/v1/series/deduplicate` | `h/duplicates.Handler.DeduplicateSeriesHandler` `s/handlers/duplicates/handler.go:590` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/series/duplicates` | `h/duplicates.Handler.ListSeriesDuplicates` `s/handlers/duplicates/handler.go:490` | session/key | library.view |  | audiobook |
| POST | `/api/v1/series/duplicates/refresh` | `h/duplicates.Handler.RefreshSeriesDuplicates` `s/handlers/duplicates/handler.go:504` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/series/merge` | `h/duplicates.Handler.MergeSeriesGroup` `s/handlers/duplicates/handler.go:625` | session/key | library.edit_metadata |  | audiobook |
| POST | `/api/v1/series/prune` | `h/duplicates.Handler.SeriesPrune` `s/handlers/duplicates/handler.go:618` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/series/prune/preview` | `h/duplicates.Handler.SeriesPrunePreview` `s/handlers/duplicates/handler.go:601` | session/key | library.view |  | audiobook |
| POST | `/api/v1/setup/validate-openai-key` | `h.ValidateOpenAIKey` `s/handlers/openai_validate.go:54` | session/key | settings.manage |  | generic |
| GET | `/api/v1/signals/coverage` | `server.Server.handleGetSignalCoverage` `s/signals_coverage_handler.go:64` | session/key | settings.manage |  | generic |
| GET | `/api/v1/system/activity-log` | `h/system.Handler.GetSystemActivityLog` `s/handlers/system/handler.go:433` | session/key | settings.manage |  | generic |
| GET | `/api/v1/system/announcements` | `h/system.Handler.GetSystemAnnouncements` `s/handlers/system/handler.go:307` | session/key | settings.manage |  | generic |
| POST | `/api/v1/system/factory-reset` | `h/system.Handler.FactoryReset` `s/handlers/system/handler.go:538` | session/key | - | RequireAdmin, credGuard, RequireOwner | generic |
| GET | `/api/v1/system/logs` | `h/system.Handler.GetSystemLogs` `s/handlers/system/handler.go:396` | session/key | settings.manage |  | generic |
| POST | `/api/v1/system/reset` | `h/system.Handler.ResetSystem` `s/handlers/system/handler.go:463` | session/key | - | RequireAdmin, credGuard, RequireOwner | generic |
| GET | `/api/v1/system/status` | `h/system.Handler.GetSystemStatus` `s/handlers/system/handler.go:278` | session/key | settings.manage |  | generic |
| GET | `/api/v1/system/storage` | `h/system.Handler.GetSystemStorage` `s/handlers/system/handler.go:364` | session/key | settings.manage |  | generic |
| GET | `/api/v1/tags` | `h/audiobooks.Handler.ListAllUserTags` `s/handlers/audiobooks/handler_tags.go:46` | session/key | library.view |  | mixed |
| GET | `/api/v1/tasks` | `h.SchedulerHandler.ListTasks` `s/handlers/scheduler_admin.go:96` | session/key | settings.manage |  | generic |
| PUT | `/api/v1/tasks/:name` | `h.SchedulerHandler.UpdateTaskConfig` `s/handlers/scheduler_admin.go:388` | session/key | settings.manage |  | generic |
| POST | `/api/v1/tasks/:name/run` | `h.SchedulerHandler.RunTask` `s/handlers/scheduler_admin.go:106` | session/key | settings.manage |  | generic |
| GET | `/api/v1/tools` | `h/tools.Handler.List` `s/handlers/tools/tools.go:39` | session/key | settings.manage |  | mixed |
| POST | `/api/v1/tools/:name/install` | `h/tools.Handler.Install` `s/handlers/tools/tools.go:56` | session/key | settings.manage | credGuard | mixed |
| GET | `/api/v1/tools/:name/status` | `h/tools.Handler.Status` `s/handlers/tools/tools.go:46` | session/key | settings.manage |  | mixed |
| POST | `/api/v1/update/apply` | `server.Server.applyUpdate` `s/update_handlers.go:48` | session/key | settings.manage | credGuard | generic |
| POST | `/api/v1/update/check` | `server.Server.checkForUpdate` `s/update_handlers.go:37` | session/key | settings.manage |  | generic |
| GET | `/api/v1/update/status` | `server.Server.getUpdateStatus` `s/update_handlers.go:15` | session/key | settings.manage |  | generic |
| GET | `/api/v1/users` | `h.UserHandler.ListUsers` `s/handlers/user.go:128` | session/key | users.manage |  | generic |
| POST | `/api/v1/users/:id/deactivate` | `h.UserHandler.DeactivateUser` `s/handlers/user.go:394` | session/key | users.manage | credGuard | generic |
| PUT | `/api/v1/users/:id/email` | `h.UserHandler.SetUserEmail` `s/handlers/user.go:451` | session/key | users.manage | credGuard | generic |
| GET | `/api/v1/users/:id/footprint` | `h.UserHandler.UserFootprint` `s/handlers/user_merge.go:97` | session/key | users.manage |  | generic |
| POST | `/api/v1/users/:id/merge-into/:target/apply` | `h.UserHandler.ApplyUserMerge` `s/handlers/user_merge.go:150` | session/key | users.manage | credGuard, RequireOwner | generic |
| POST | `/api/v1/users/:id/merge-into/:target/preview` | `h.UserHandler.PreviewUserMerge` `s/handlers/user_merge.go:127` | session/key | users.manage | credGuard, RequireOwner | generic |
| POST | `/api/v1/users/:id/reactivate` | `h.UserHandler.ReactivateUser` `s/handlers/user.go:499` | session/key | users.manage | credGuard | generic |
| POST | `/api/v1/users/:id/reset-password` | `server.Server.handleResetPassword` `s/auth_reset_password.go:52` | session/key | users.manage | credGuard | generic |
| POST | `/api/v1/users/invite` | `h.UserHandler.CreateInvite` `s/handlers/user.go:155` | session/key | users.manage | credGuard | generic |
| GET | `/api/v1/users/invites` | `h.UserHandler.ListInvites` `s/handlers/user.go:307` | session/key | users.manage |  | generic |
| DELETE | `/api/v1/users/invites/:id` | `h.UserHandler.DeleteInvite` `s/handlers/user.go:332` | session/key | users.manage |  | generic |
| POST | `/api/v1/users/invites/:id/resend` | `h.UserHandler.ResendInvite` `s/handlers/invite_delivery.go:367` | session/key | users.manage | credGuard | generic |
| GET | `/api/v1/users/merges` | `h.UserHandler.ListUserMerges` `s/handlers/user_merge.go:190` | session/key | users.manage |  | generic |
| POST | `/api/v1/users/merges/:jid/resume` | `h.UserHandler.ResumeUserMerge` `s/handlers/user_merge.go:204` | session/key | users.manage | credGuard, RequireOwner | generic |
| POST | `/api/v1/users/merges/:jid/undo` | `h.UserHandler.UndoUserMerge` `s/handlers/user_merge.go:210` | session/key | users.manage | credGuard, RequireOwner | generic |
| GET | `/api/v1/version-groups/:id` | `h.VersionsHandler.GetVersionGroup` `s/handlers/versions.go:700` | session/key | library.view |  | audiobook |
| GET | `/api/v1/work` | `h/entities.Handler.ListWork` `s/handlers/entities/handler.go:224` | session/key | library.view |  | audiobook |
| GET | `/api/v1/work/stats` | `h/entities.Handler.GetWorkStats` `s/handlers/entities/handler.go:279` | session/key | library.view |  | audiobook |
| GET | `/api/v1/works` | `h/entities.Handler.ListWorks` `s/handlers/entities/handler.go:127` | session/key | library.view |  | audiobook |
| POST | `/api/v1/works` | `h/entities.Handler.CreateWork` `s/handlers/entities/handler.go:137` | session/key | library.edit_metadata |  | audiobook |
| DELETE | `/api/v1/works/:id` | `h/entities.Handler.DeleteWork` `s/handlers/entities/handler.go:187` | session/key | library.delete |  | audiobook |
| GET | `/api/v1/works/:id` | `h/entities.Handler.GetWork` `s/handlers/entities/handler.go:152` | session/key | library.view |  | audiobook |
| PUT | `/api/v1/works/:id` | `h/entities.Handler.UpdateWork` `s/handlers/entities/handler.go:163` | session/key | library.edit_metadata |  | audiobook |
| GET | `/api/v1/works/:id/books` | `h/entities.Handler.ListWorkBooks` `s/handlers/entities/handler.go:201` | session/key | library.view |  | audiobook |
| GET | `/api/v3/ops/counts` | `h/opsv3.Handler.Counts` `s/handlers/opsv3/counts.go:31` | session/key | library.view |  | generic |
| GET | `/api/v3/ops/defs` | `h/opsv3.Handler.ListDefs` `s/handlers/opsv3/defs.go:214` | session/key | library.view |  | generic |
| GET | `/api/v3/ops/defs/:id` | `h/opsv3.Handler.GetDef` `s/handlers/opsv3/defs.go:234` | session/key | library.view |  | generic |
| POST | `/api/v3/ops/defs/:id/runs` | `h/opsv3.Handler.StartRun` `s/handlers/opsv3/runs.go:144` | session/key | scan.trigger |  | generic |
| GET | `/api/v3/ops/events` | `h/opsv3.Handler.Events` `s/handlers/opsv3/events.go:526` | session/key | library.view |  | generic |
| GET | `/api/v3/ops/runs` | `h/opsv3.Handler.ListRuns` `s/handlers/opsv3/runs.go:31` | session/key | library.view |  | generic |
| POST | `/api/v3/ops/runs` | `h/opsv3.Handler.StartRun` `s/handlers/opsv3/runs.go:144` | session/key | scan.trigger |  | generic |
| GET | `/api/v3/ops/runs/:id` | `h/opsv3.Handler.GetRun` `s/handlers/opsv3/runs.go:80` | session/key | library.view |  | generic |
| POST | `/api/v3/ops/runs/:id/approve` | `h/opsv3.Handler.ApproveRun` `s/handlers/opsv3/runs.go:401` | session/key | library.view |  | generic |
| POST | `/api/v3/ops/runs/:id/cancel` | `h/opsv3.Handler.CancelRun` `s/handlers/opsv3/runs.go:301` | session/key | library.view |  | generic |
| GET | `/api/v3/ops/runs/:id/rows` | `h/opsv3.Handler.ListRows` `s/handlers/opsv3/rows.go:30` | session/key | library.view |  | generic |
| GET | `/api/v3/ops/timeline` | `h/opsv3.Handler.Timeline` `s/handlers/opsv3/timeline.go:30` | session/key | library.view |  | generic |
| GET | `/auth/temp-login` | `server.Server.consumeTempLoginToken` `s/auth_temp_login.go:161` | none | - |  | generic |
| GET | `/health` | `server.Server.setupRoutes.func2` `s/server_lifecycle.go:1710` | none | - |  | generic |
| GET | `/metrics` | `gin.WrapH.func1` `<gomodcache>/github.com/gin-gonic/gin@v1.12.0/utils.go:55` | session/key | - |  | generic |

### 3.2 Added when ABS surface is enabled (75 routes)

Auth is `ABSRequireAuth` (fail-closed ABS identity: ABS JWT, ABS-minted refresh, `abk_` API key, or Cloudflare service token / Access); permissions are mostly NOT route-level here (a few in-handler, e.g. `auth.Can(PermCollectionsManage)` at `internal/server/handlers/abs/collections.go:575`).

| M | path | handler | auth | permission | guards | class |
|---|---|---|---|---|---|---|
| POST | `/api/authorize` | `h/abs.Handler.Authorize` `s/handlers/abs/authorize.go:55` | ABS auth | - | loginCeiling | compat |
| GET | `/api/authors/:id` | `h/abs.Handler.AuthorDetail` `s/handlers/abs/browse.go:1925` | ABS auth | - | loginCeiling | compat |
| POST | `/api/collections` | `h/abs.Handler.CreateCollection` `s/handlers/abs/collections.go:219` | ABS auth | - | loginCeiling | compat |
| DELETE | `/api/collections/:id` | `h/abs.Handler.DeleteCollection` `s/handlers/abs/collections.go:361` | ABS auth | - | loginCeiling | compat |
| GET | `/api/collections/:id` | `h/abs.Handler.CollectionDetail` `s/handlers/abs/collections.go:182` | ABS auth | - | loginCeiling | compat |
| PATCH | `/api/collections/:id` | `h/abs.Handler.UpdateCollection` `s/handlers/abs/collections.go:287` | ABS auth | - | loginCeiling | compat |
| POST | `/api/collections/:id/batch/add` | `h/abs.Handler.BatchAddToCollection` `s/handlers/abs/collections.go:481` | ABS auth | - | loginCeiling | compat |
| POST | `/api/collections/:id/batch/remove` | `h/abs.Handler.BatchRemoveFromCollection` `s/handlers/abs/collections.go:493` | ABS auth | - | loginCeiling | compat |
| POST | `/api/collections/:id/book` | `h/abs.Handler.AddBookToCollection` `s/handlers/abs/collections.go:384` | ABS auth | - | loginCeiling | compat |
| DELETE | `/api/collections/:id/book/:bookId` | `h/abs.Handler.RemoveBookFromCollection` `s/handlers/abs/collections.go:425` | ABS auth | - | loginCeiling | compat |
| POST | `/api/emails/send-ebook-to-device` | `h/abs.Handler.SendEbookToDevice` `s/handlers/abs/unsupported.go:41` | ABS auth | - | loginCeiling | compat |
| GET | `/api/items/:id` | `h/abs.Handler.Item` `s/handlers/abs/item.go:46` | ABS auth | - | loginCeiling | compat |
| GET | `/api/items/:id/cover` | `h/abs.Handler.ItemCover` `s/handlers/abs/item.go:233` | none | - | loginCeiling | compat |
| HEAD | `/api/items/:id/cover` | `h/abs.Handler.ItemCover` `s/handlers/abs/item.go:233` | none | - | loginCeiling | compat |
| GET | `/api/items/:id/file/:ino` | `h/abs.Handler.ItemFile` `s/handlers/abs/stream.go:66` | ABS auth | - | loginCeiling | compat |
| HEAD | `/api/items/:id/file/:ino` | `h/abs.Handler.ItemFile` `s/handlers/abs/stream.go:66` | ABS auth | - | loginCeiling | compat |
| GET | `/api/items/:id/file/:ino/download` | `h/abs.Handler.ItemFileDownload` `s/handlers/abs/stream.go:74` | ABS auth | - | loginCeiling | compat |
| HEAD | `/api/items/:id/file/:ino/download` | `h/abs.Handler.ItemFileDownload` `s/handlers/abs/stream.go:74` | ABS auth | - | loginCeiling | compat |
| POST | `/api/items/:id/play` | `h/abs.Handler.Play` `s/handlers/abs/play.go:160` | ABS auth | - | loginCeiling | compat |
| GET | `/api/libraries` | `h/abs.Handler.Libraries` `s/handlers/abs/browse.go:110` | ABS auth | - | loginCeiling | compat |
| GET | `/api/libraries/:libraryId` | `h/abs.Handler.Library` `s/handlers/abs/browse.go:127` | ABS auth | - | loginCeiling | compat |
| GET | `/api/libraries/:libraryId/authors` | `h/abs.Handler.LibraryAuthors` `s/handlers/abs/browse.go:1842` | ABS auth | - | loginCeiling | compat |
| GET | `/api/libraries/:libraryId/collections` | `h/abs.Handler.LibraryCollections` `s/handlers/abs/collections.go:140` | ABS auth | - | loginCeiling | compat |
| GET | `/api/libraries/:libraryId/filterdata` | `h/abs.Handler.LibraryFilterData` `s/handlers/abs/browse.go:2247` | ABS auth | - | loginCeiling | compat |
| GET | `/api/libraries/:libraryId/items` | `h/abs.Handler.LibraryItems` `s/handlers/abs/browse.go:498` | ABS auth | - | loginCeiling | compat |
| GET | `/api/libraries/:libraryId/narrators` | `h/abs.Handler.LibraryNarrators` `s/handlers/abs/browse.go:2212` | ABS auth | - | loginCeiling | compat |
| GET | `/api/libraries/:libraryId/personalized` | `h/abs.Handler.Personalized` `s/handlers/abs/browse.go:648` | ABS auth | - | loginCeiling | compat |
| GET | `/api/libraries/:libraryId/playlists` | `h/abs.Handler.LibraryPlaylists` `s/handlers/abs/playlists.go:92` | ABS auth | - | loginCeiling | compat |
| GET | `/api/libraries/:libraryId/recent-episodes` | `h/abs.Handler.RecentEpisodes` `s/handlers/abs/browse.go:616` | ABS auth | - | loginCeiling | compat |
| GET | `/api/libraries/:libraryId/search` | `h/abs.Handler.LibrarySearch` `s/handlers/abs/browse.go:2817` | ABS auth | - | loginCeiling | compat |
| GET | `/api/libraries/:libraryId/series` | `h/abs.Handler.LibrarySeries` `s/handlers/abs/browse.go:948` | ABS auth | - | loginCeiling | compat |
| GET | `/api/me` | `h/abs.Handler.Me` `s/handlers/abs/me.go:35` | ABS auth | - | loginCeiling | compat |
| GET | `/api/me/bookmarks/:id` | `h/abs.Handler.ListItemBookmarks` `s/handlers/abs/bookmarks.go:58` | ABS auth | - | loginCeiling | compat |
| PATCH | `/api/me/item/:id/bookmark` | `h/abs.Handler.UpdateBookmark` `s/handlers/abs/bookmarks.go:139` | ABS auth | - | loginCeiling | compat |
| POST | `/api/me/item/:id/bookmark` | `h/abs.Handler.CreateBookmark` `s/handlers/abs/bookmarks.go:84` | ABS auth | - | loginCeiling | compat |
| DELETE | `/api/me/item/:id/bookmark/:time` | `h/abs.Handler.DeleteBookmark` `s/handlers/abs/bookmarks.go:192` | ABS auth | - | loginCeiling | compat |
| GET | `/api/me/item/:id/remove-from-continue-listening` | `h/abs.Handler.RemoveFromContinueListening` `s/handlers/abs/progress.go:326` | ABS auth | - | loginCeiling | compat |
| POST | `/api/me/item/:id/remove-from-continue-listening` | `h/abs.Handler.RemoveFromContinueListening` `s/handlers/abs/progress.go:326` | ABS auth | - | loginCeiling | compat |
| GET | `/api/me/item/listening-sessions/:id` | `h/abs.Handler.ItemListeningSessions` `s/handlers/abs/stats.go:133` | ABS auth | - | loginCeiling | compat |
| GET | `/api/me/listening-sessions` | `h/abs.Handler.ListeningSessions` `s/handlers/abs/stats.go:108` | ABS auth | - | loginCeiling | compat |
| GET | `/api/me/listening-stats` | `h/abs.Handler.ListeningStats` `s/handlers/abs/stats.go:64` | ABS auth | - | loginCeiling | compat |
| GET | `/api/me/progress` | `h/abs.Handler.MediaProgressList` `s/handlers/abs/progress.go:78` | ABS auth | - | loginCeiling | compat |
| DELETE | `/api/me/progress/:id` | `h/abs.Handler.MediaProgressDelete` `s/handlers/abs/progress.go:235` | ABS auth | - | loginCeiling | compat |
| GET | `/api/me/progress/:id` | `h/abs.Handler.MediaProgressGet` `s/handlers/abs/progress.go:99` | ABS auth | - | loginCeiling | compat |
| PATCH | `/api/me/progress/:id` | `h/abs.Handler.MediaProgressPatch` `s/handlers/abs/progress.go:135` | ABS auth | - | loginCeiling | compat |
| GET | `/api/me/progress/:id/remove-from-continue-listening` | `h/abs.Handler.RemoveFromContinueListening` `s/handlers/abs/progress.go:326` | ABS auth | - | loginCeiling | compat |
| POST | `/api/me/progress/:id/remove-from-continue-listening` | `h/abs.Handler.RemoveFromContinueListening` `s/handlers/abs/progress.go:326` | ABS auth | - | loginCeiling | compat |
| PATCH | `/api/me/progress/batch/update` | `h/abs.Handler.MediaProgressBatchUpdate` `s/handlers/abs/progress.go:188` | ABS auth | - | loginCeiling | compat |
| GET | `/api/me/sessions` | `h/abs.Handler.Sessions` `s/handlers/abs/me.go:93` | ABS auth | - | loginCeiling | compat |
| DELETE | `/api/me/sessions/:id` | `h/abs.Handler.DeleteSession` `s/handlers/abs/me.go:199` | ABS auth | - | loginCeiling | compat |
| GET | `/api/me/stats/year/:year` | `h/abs.Handler.YearStats` `s/handlers/abs/stats.go:167` | ABS auth | - | loginCeiling | compat |
| GET | `/api/narrators/:id/image` | `h/abs.Handler.NarratorImage` `s/handlers/abs/unsupported.go:30` | ABS auth | - | loginCeiling | compat |
| HEAD | `/api/narrators/:id/image` | `h/abs.Handler.NarratorImage` `s/handlers/abs/unsupported.go:30` | ABS auth | - | loginCeiling | compat |
| POST | `/api/playlists` | `h/abs.Handler.CreatePlaylist` `s/handlers/abs/playlists_write.go:96` | ABS auth | - | loginCeiling | compat |
| DELETE | `/api/playlists/:id` | `h/abs.Handler.DeletePlaylist` `s/handlers/abs/playlists_write.go:205` | ABS auth | - | loginCeiling | compat |
| GET | `/api/playlists/:id` | `h/abs.Handler.PlaylistDetail` `s/handlers/abs/playlists.go:250` | ABS auth | - | loginCeiling | compat |
| PATCH | `/api/playlists/:id` | `h/abs.Handler.UpdatePlaylist` `s/handlers/abs/playlists_write.go:147` | ABS auth | - | loginCeiling | compat |
| POST | `/api/playlists/:id/batch/add` | `h/abs.Handler.BatchAddToPlaylist` `s/handlers/abs/playlists_write.go:225` | ABS auth | - | loginCeiling | compat |
| POST | `/api/playlists/:id/batch/remove` | `h/abs.Handler.BatchRemoveFromPlaylist` `s/handlers/abs/playlists_write.go:254` | ABS auth | - | loginCeiling | compat |
| DELETE | `/api/playlists/:id/item/:libraryItemId` | `h/abs.Handler.RemovePlaylistItem` `s/handlers/abs/playlists_write.go:278` | ABS auth | - | loginCeiling | compat |
| GET | `/api/series/:id` | `h/abs.Handler.SeriesDetail` `s/handlers/abs/browse.go:1116` | ABS auth | - | loginCeiling | compat |
| POST | `/api/session/:id/close` | `h/abs.Handler.SessionClose` `s/handlers/abs/play.go:354` | ABS auth | - | loginCeiling | compat |
| POST | `/api/session/:id/sync` | `h/abs.Handler.SessionSync` `s/handlers/abs/play.go:348` | ABS auth | - | loginCeiling | compat |
| POST | `/api/session/local` | `h/abs.Handler.SessionLocal` `s/handlers/abs/play.go:377` | ABS auth | - | loginCeiling | compat |
| POST | `/api/session/local-all` | `h/abs.Handler.SessionLocalAll` `s/handlers/abs/session_local_all.go:140` | ABS auth | - | loginCeiling | compat |
| GET | `/auth/openid` | `h/abs.Handler.OpenIDAuthorize` `s/handlers/abs/openid.go:595` | none | - | loginCeiling | compat |
| GET | `/auth/openid/browser` | `h/abs.Handler.OpenIDBrowser` `s/handlers/abs/openid.go:738` | none | - | loginCeiling | compat |
| POST | `/auth/openid/browser` | `h/abs.Handler.OpenIDBrowserConsent` `s/handlers/abs/openid.go:865` | none | - | loginCeiling | compat |
| GET | `/auth/openid/callback` | `h/abs.Handler.OpenIDCallback` `s/handlers/abs/openid.go:1120` | none | - | loginCeiling | compat |
| POST | `/auth/refresh` | `h/abs.Handler.Refresh` `s/handlers/abs/refresh.go:44` | none | - | loginCeiling | compat |
| POST | `/login` | `h/abs.Handler.Login` `s/handlers/abs/login.go:54` | none | - | loginCeiling | compat |
| POST | `/logout` | `h/abs.Handler.Logout` `s/handlers/abs/me.go:242` | ABS auth | - | loginCeiling | compat |
| GET | `/ping` | `h/abs.Handler.Ping` `s/handlers/abs/status.go:24` | none | - | loginCeiling | compat |
| GET | `/public/session/:id/track/:index` | `h/abs.Handler.PublicSessionTrack` `s/handlers/abs/stream.go:153` | none | - | loginCeiling | compat |
| GET | `/status` | `h/abs.Handler.Status` `s/handlers/abs/status.go:37` | none | - | loginCeiling | compat |


## 4. Authenticated routes with no route-level permission (default build)

These pass `RequireAuth` only: any valid session or API key (whatever its scopes) reaches them unless the handler checks in-body. A scoped key is only narrowed by `perm()` checks, so a key with a single unrelated scope can still call these (exception: worker-only credentials, `middleware/auth.go:289`).

| M | path | handler | guards |
|---|---|---|---|
| GET | `/api/events` | `server.Server.setupRoutes.func6` |  |
| GET | `/api/v1/audiobooks/:id/position` | `h.ReadingHandler.GetPosition` |  |
| POST | `/api/v1/audiobooks/:id/position` | `h.ReadingHandler.SetPosition` |  |
| GET | `/api/v1/audiobooks/:id/state` | `h.ReadingHandler.GetBookState` |  |
| DELETE | `/api/v1/audiobooks/:id/status` | `h.ReadingHandler.ClearBookStatus` |  |
| PATCH | `/api/v1/audiobooks/:id/status` | `h.ReadingHandler.SetBookStatus` |  |
| POST | `/api/v1/audiobooks/:id/status/repair` | `h.ReadingHandler.RepairBookStatus` |  |
| GET | `/api/v1/auth/api-keys` | `h.APIKeyHandler.List` |  |
| POST | `/api/v1/auth/api-keys` | `h.APIKeyHandler.Create` | credGuard-cond |
| DELETE | `/api/v1/auth/api-keys/:id` | `h.APIKeyHandler.Revoke` |  |
| GET | `/api/v1/auth/api-keys/:id` | `h.APIKeyHandler.Get` |  |
| PATCH | `/api/v1/auth/api-keys/:id` | `h.APIKeyHandler.UpdateStatus` | credGuard |
| POST | `/api/v1/auth/api-keys/:id/rotate` | `server.gone.func1` | credGuard-cond |
| POST | `/api/v1/auth/logout` | `h.AuthHandler.Logout` |  |
| GET | `/api/v1/auth/me` | `h.AuthHandler.Me` |  |
| PATCH | `/api/v1/auth/me` | `h.AuthHandler.UpdateMe` | credGuard |
| PUT | `/api/v1/auth/me/password` | `h.AuthHandler.ChangePassword` | credGuard |
| GET | `/api/v1/auth/sessions` | `h.AuthHandler.ListMySessions` |  |
| DELETE | `/api/v1/auth/sessions/:id` | `h.AuthHandler.RevokeMySession` |  |
| GET | `/api/v1/books/:id/position` | `h.ReadingHandler.GetPosition` |  |
| POST | `/api/v1/books/:id/position` | `h.ReadingHandler.SetPosition` |  |
| GET | `/api/v1/books/:id/state` | `h.ReadingHandler.GetBookState` |  |
| DELETE | `/api/v1/books/:id/status` | `h.ReadingHandler.ClearBookStatus` |  |
| PATCH | `/api/v1/books/:id/status` | `h.ReadingHandler.SetBookStatus` |  |
| POST | `/api/v1/books/:id/status/repair` | `h.ReadingHandler.RepairBookStatus` |  |
| GET | `/api/v1/me/:status` | `h.ReadingHandler.ListByStatus` |  |
| POST | `/api/v1/playlists` | `h.PlaylistHandler.CreatePlaylist` |  |
| DELETE | `/api/v1/playlists/:id` | `h.PlaylistHandler.DeletePlaylist` |  |
| GET | `/api/v1/playlists/:id` | `h.PlaylistHandler.GetPlaylist` |  |
| PUT | `/api/v1/playlists/:id` | `h.PlaylistHandler.UpdatePlaylist` |  |
| POST | `/api/v1/playlists/:id/books` | `h.PlaylistHandler.AddBooksToPlaylist` |  |
| DELETE | `/api/v1/playlists/:id/books/:bookID` | `h.PlaylistHandler.RemoveBookFromPlaylist` |  |
| GET | `/api/v1/playlists/:id/export.m3u` | `server.gone.func1` |  |
| POST | `/api/v1/playlists/:id/materialize` | `h.PlaylistHandler.MaterializePlaylist` |  |
| POST | `/api/v1/playlists/:id/reorder` | `h.PlaylistHandler.ReorderPlaylist` |  |
| GET | `/metrics` | `gin.WrapH.func1` |  |

In-handler checks that exist for some of these: `PermUsersManage` for acting on another user's sessions/keys (`internal/server/handlers/auth.go:599`, `internal/server/handlers/apikeys.go:170`), and per-user ownership checks in playlists/reading state (handlers `handlers/playlist*.go`, `handlers/reading*.go`; not individually verified).

### 4.1 In-handler / dynamic permission enforcement (not visible in the route chain)

| Where | Rule | Anchor |
|---|---|---|
| `POST /api/v1/operations/v2` (start op by `def_id`) | every permission in the op definition's `Permissions` must be held; empty list defaults to `settings.manage` | `internal/server/handlers/operations_v2.go:781-800`, def field `internal/operations/registry/types.go:166` |
| `/api/v3/ops` start/approve/cancel | `opsv3.Authz{Require,Holds}` closures; per-def permissions | `internal/server/handlers/opsv3/handler.go:171-172`, `internal/server/wire_ops_v3_routes.go:90-105` |
| Maintenance jobs `POST /maintenance/jobs/:job_id` | `PermissionAware` job interface, plus route `settings.manage` | `internal/server/maintenance_dispatcher.go:37,93-96` |
| Repairs routes | route perm + every `repairs.RequiredPermissions(fixer)` | `internal/server/wire_repairs_routes.go:32,111,294`, `internal/server/handlers/repairs/handler.go:148-168` |
| Audiobook handlers | helper wrappers around `auth.Can` / `CheckPermission` | `internal/server/handlers/audiobooks/handler_permission.go:35,63` |
| ABS collections | `auth.Can(PermCollectionsManage)` | `internal/server/handlers/abs/collections.go:575` |

## 5. API wire contract and TypeScript generation

| Mechanism | What it covers | Anchor |
|---|---|---|
| `tygo` (Go struct -> TS), config `tygo.yaml` | only 2 packages: `internal/repairs` (`wire.go` only) -> `web/src/types/generated/repairs.ts` (318 lines); `internal/database` (`bookcore.go`, `bookfilecore.go`, `scan_state.go`, `wire_types.go`) -> `web/src/types/generated/database.ts` (542 lines). Type maps: `time.Time`->`string`, `json.RawMessage`->`unknown` | `tygo.yaml:17-40`, `Makefile:560-571` (`make generate-types`, `make types-check`) |
| Freshness gate | `scripts/ci/check_generated_types.sh` regenerates to a temp dir and diffs; run by `make types-check` in CI | `scripts/ci/check_generated_types.sh:1-22`, `.woodpecker/checks-build.yaml:72`, `.github/workflows/ci.yml:483` |
| Golden contract test (`internal/wirecontract`) | for each tygo-generated type marshals a fully-populated synthetic sample and compares with checked-in JSON goldens under `web/src/types/contract/testdata/` (10 goldens); `contract.test.ts` assigns the same JSON to the generated TS types so a renamed `json` tag breaks the TS compile; regenerate with `UPDATE_WIRE_GOLDEN=1 go test ./internal/wirecontract/` | `internal/wirecontract/contract_test.go:1-20`, `web/src/types/contract/contract.test.ts` |
| `opsgen` (status enums -> TS) | `tools/cmd/opsgen` writes `web/src/generated/ops.ts` (373 lines) from `internal/operations/state/{state,v3}.go`; `make generate-ops` / `make ops-check` | `tools/cmd/opsgen/main.go:6,28`, `Makefile:547-554` |
| Hand-written TS types | the bulk: `web/src/types/index.ts` (289 lines, `Audiobook` etc.) and the typed fetch layer `web/src/services/api.ts` (`API_BASE = '/api/v1'`, :24); `axios` is a dependency (`web/package.json:56`) | `web/src/types/index.ts:1-40`, `web/src/services/api.ts:24` |
| OpenAPI / swagger / protobuf / GraphQL | NONE. No `openapi*`/`swagger*` file, no spec generator, no `.proto`; `go.mod` has `grpc`, `protobuf`, `grpc-gateway` only as OTLP-exporter / indirect deps | find over repo; `go.mod:50,109,158` |
| Plugin SDK boundary | `pkg/plugin/sdk` guarded by `tools/cmd/sdkguard` (no unexpected `internal/` deps) | `Makefile:540-545` |

Consumption in web/: `web/src/services/api.ts` imports `types/generated/repairs`; `types/generated/database.ts` and `generated/ops.ts` are imported by `api.ts`, `utils/operationPolling.ts` (grep hits: `web/src/services/api.ts`, `web/src/utils/operationPolling.ts`). Response envelope/error shape is NOT in any generated type: errors go through `internal/httputil` (`RespondWithError(c, status, msg, CODE)`), e.g. `{ "error": "gone", "message": ... }` for 410 stubs (`internal/server/wire_entities_gone_test.go:19-20`).

## 6. Realtime, sync and streaming APIs

| Endpoint | Protocol | Messages | Auth | Anchor |
|---|---|---|---|---|
| `GET /api/events` | SSE, `data: <json>` frames (no `event:` names, no ids), 25 s `: ping` comment heartbeat; optional `?operation=<id>` filter (filter by client subscription; no per-user filtering) | `Event{type,id,timestamp,data}`; types `operation.progress`, `operation.status`, `operation.log`, `system.status`, `books.changed` (`{kind: created|updated|deleted, ids[]}`, coalesced 250 ms), initial `connection.established` | `RequireAuth` inline (+ cfMW), no permission | `internal/realtime/events.go:18-33,216-296`, `internal/realtime/book_changes.go:18`, `internal/server/server.go:1082-1087`, `internal/server/server_lifecycle.go:1782` |
| `GET /api/v1/operations/events` | SSE, named events `event: <name>\ndata: <json>`, immediate `: heartbeat` | names published on `opsregistry.EventHub`: `op.created`, `op.updated`, `op.terminal`, `op.log`, `op.current_item` | `library.view` | `internal/server/handlers/operations_v2.go:1063-1110`, `internal/operations/registry/bus.go:14-50`, publishers `internal/operations/registry/registry.go:1287,1332`, `dispatcher.go:549`, `reporter_db.go:464,603,776` |
| `GET /api/v3/ops/events` | SSE with resume: `id: <boot-nonce>-<seq>`, `event: run` (RunView with progress chunks/workers), `: heartbeat` every 15 s, `event: resync` when events missed; honours `Last-Event-ID`; per-user stream cap (429 + Retry-After) and global cap (503) | `run`, `resync` | `library.view` | `internal/server/handlers/opsv3/events.go:490-640` |
| `GET /metrics` | Prometheus text, authenticated (any valid credential) | n/a | `RequireAuth` inline | `internal/server/server_lifecycle.go:1746` |
| Web clients | `EventSource` manager with exponential reconnect 3 s..60 s default URL `/api/events`; separate `EventSource` for `/api/v1/operations/events` | | | `web/src/services/eventSourceManager.ts:22-40,119`, `web/src/services/api.ts:733-734` |
| `internal/syncapi` | NOT a network API: pure packages for the Audiobookshelf-compatible sync: `progress/` (I/O-free conflict-resolution policy: merge incoming vs explicit progress, finished/duration invariants, bookmarks) and `conformance/` (diffs our JSON responses against fixtures captured from real ABS: missing/extra field, type mismatch). Consumed by `handlers/abs` | `internal/syncapi/progress/policy.go:1-40`, `internal/syncapi/conformance/diff.go:1-30`, importers `internal/server/handlers/abs/handler.go`, `item.go`, `play.go`, `session_local_all.go` |
| WebSocket | NONE in the server (`coder/websocket` appears only as an indirect dep, `go.mod:95`); no gorilla/nhooyr | `go.mod:95` |

Observation: three separate SSE implementations (hub A `internal/realtime`, registry bus + v2 handler, v3 broker with resume) with different framing; only v3 supports resume/backpressure signalling.

## 7. Listeners, HTTP/2, HTTP/3 (QUIC), gRPC

| Component | Why it exists | Anchor |
|---|---|---|
| Main listener | `http.Server{Handler: peerGatedHandler}`; `WriteTimeout` 0 so SSE stays open; `ReadHeaderTimeout` 15 s, `IdleTimeout` 120 s, `MaxHeaderBytes` 1 MiB; defaults host `localhost`, port `8484` | `internal/server/server_lifecycle.go:903-920`, `GetDefaultServerConfig` `:2187` |
| TLS + HTTP/2 | enabled only when `--tls-cert` and `--tls-key` are set and readable (else falls back to plain HTTP); TLS 1.2 minimum | `server_lifecycle.go:936-975` |
| HTTP/3 (QUIC) via `quic-go/http3` | OPTIONAL: when TLS is on and `--http3-port` is set (flag default "8484" on UDP, `cmd/root.go:533`); serves the same gated handler; 0-RTT disabled because early data precedes address validation (peer allow-list); `Alt-Svc` header advertised via middleware (see 2.2 note c) | `server_lifecycle.go:1000-1020,1061-1067`, `cmd/root.go:446,533`, `internal/server/server.go:315` |
| Port-80 redirect | HTTP->HTTPS redirect server, non-fatal if port 80 is unbindable | `server_lifecycle.go:1019-1044` |
| gRPC | NO gRPC server anywhere. `google.golang.org/grpc` is a direct dep only for the OpenTelemetry OTLP gRPC exporters (traces + metrics) and its credentials packages | `internal/telemetry/telemetry.go:19-25,139-180,262-278`, `go.mod:33-34,50` |
| mTLS | `internal/mtls` (CA/PSK dir, server TLS requiring client certs, stdio<->conn bridge) is NOT used by the HTTP server; its only importer is the separate `cmd/mtls-bridge` binary (bridges an MCP protocol between two hosts) | `internal/mtls/transport.go:14`, `cmd/mtls-bridge/main.go:1-25` |
| Peer allow-list | refuses peers outside loopback / configured tunnel connectors / `ALLOWED_PEER_CIDRS` before gin; warns at startup when unset (accepts any peer) | `internal/server/peer_allowlist.go:277-290` |

## 8. Findings relevant to a shared platform core

1. Route registration is imperative and Server-method based: handlers hang off `*Server` (131 routes) or `handlers.*` structs fed by a very wide `wireHandlers` (`wire_handlers.go:42`; file is 864 lines). There is no declarative route table, no route metadata (permission, class, op) attached to routes; the permission is only inside the middleware chain. A second app cannot contribute routes without being wired inside `setupRoutes`/`wireHandlers`.
2. A reusable core would need a `RouteGroup` abstraction that owns the chain `[rate-limit, body-limit, cfMW, RequireAuth]` (`server_lifecycle.go:1867-1875`) and hands each app a `protected` group; today that chain is built inline in `setupRoutes` and the permission helper `s.perm` closes over `Server` state (`server_lifecycle.go:1693`).
3. The plugin extension point (`PluginRouter`) is mounted on a fresh `s.router.Group("/api/v1/plugins")` (`plugins_init.go:42`), NOT on `api`/`protected`: plugin-registered routes would get none of rate limiting, body limit, cfMW or `RequireAuth` (only global middleware 1-9). No in-tree plugin uses `deps.Router`, so the gap is latent. Any app-as-plugin design must fix this first.
4. `/api/v3/ops` is a separate, cleaner API (own handler package, `Authz` injected, SSE with resume) and is the best candidate to promote into the core task/job API; route-level permissions are only `library.view` (reads, approve, cancel) and `scan.trigger` (start); the real checks are in-handler: cancel = starter or `scan.trigger`+definition permission (`opsv3/runs.go:295-301`), approve = verified auth method + definition approver permission, default `settings.manage` (`opsv3/runs.go:396-401`).
5. Wire contract coverage is small (2 tygo packages, ~860 generated TS lines vs ~290 hand-written index types plus a large `api.ts`); no OpenAPI. For multi-app, either extend tygo+golden pattern per app package (it is cheap and already CI-gated) or introduce an API spec; the golden-file technique is reusable as is.
6. Versioning is by path prefix with unversioned legacy redirects; retired routes become 410 stubs that preserve auth ordering (`gone.go`). A multi-app core needs a per-app prefix convention (e.g. `/api/v1/<app>/...`); today everything audiobook-specific sits directly under `/api/v1/` (audiobooks, authors, series, dedup, itunes, ...), so apps would collide on generic names like `/search`, `/tags`, `/import`.
7. HTTP/3 is an opt-in transport nicety, gRPC is telemetry-only: neither constrains the core design. No WebSocket/gRPC streaming exists; SSE is the only push channel.

