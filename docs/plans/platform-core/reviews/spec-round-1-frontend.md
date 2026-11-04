<!-- file: docs/plans/platform-core/reviews/spec-round-1-frontend.md -->
<!-- version: 1.0.0 -->
<!-- guid: bc3e2c00-c972-469a-b8ed-31addee34328 -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 1: FRONTEND SHELL lens (ID prefix FE)

Scope: the `@mediacore/shell` extension API, G3 intersection, the npm workspace layout before and
after the split, per-app embedding (`core/ui`), the generated TS client, auth/session/CSRF in the
shell, the realtime client, theming, the react-router 8 migration, bundle splitting, tests, and how
ABO's pages split. Primary files: 07, 05-http-api §7–§8, 05-realtime, 06 (UI parts), 09 (S2/S4/S5/S6/S9),
12 (npm), 13 (frontend), checked against 03 v1.2.0. Code anchors are at `748fd787a`
(`git show 748fd787a:<path>`).

Experiment (run for this review, 2026-11-04, Node 22, empty scratch project): `openapi-typescript@7.13.0`
plus `"typescript": "npm:@typescript/typescript6@6.0.2"` **fails `npm install` with ERESOLVE**
(peer `typescript@^5.x`). Adding `"overrides": {"openapi-typescript": {"typescript": "$typescript"}}`
installs, and generating from a small OpenAPI 3.1 document (`type: ["string","null"]`, dotted component
names like `core.Me`) **works**. The same override pointing at `typescript@7.0.2` crashes at
`openapi-typescript/dist/lib/ts.mjs:11` (TS 7 has no JS API). Used in FE-20.

Counts: **2 BLOCKER, 17 MAJOR, 9 MINOR** (28 findings).

## Findings

### FE-1: BLOCKER. Nothing in the spec serves ABO's SPA between S2 and S9; as written ABO either fails boot or hits a `/` pattern conflict

- **Where:** 09 §S2 "Moves" (l.309); 07 §7.6 (l.345–355), §7.12 (l.480–481); 05-http-api §4.1 (l.407), §8.2 (l.641–648); 06 P8 (l.594).
- **Evidence:** 09 S2: "The gin engine … is mounted by `books` at `/` as an `http.Handler`". 07 §7.6: the kernel mounts `ui.Handler` as "the lowest-priority route (`/` catch-all)". 05-http-api §4.1 gives `/` to `core/ui`, and §4.9 fails `Build` with `ErrPrefixCollision` when two owners claim overlapping prefixes. `LegacyMount.Patterns` (05-http-api l.641) lists `/api/v1/`, `/api/v3/ops/`, `GET /auth/temp-login` and ABS paths, but not `/`. So either gin owns `/` (collision with `core/ui`), or gin does not own `/` and the SPA must come from `ui.Handler`. In the second case, `ui.MustFromFS` "fails if index.html or the meta tag is missing" (07 l.316–318, 480–481). ABO's `web/index.html` has no `<meta name="mediacore-build">` (`web/index.html` @748fd787a), and only the `@mediacore/shell/vite` preset adds one (07 l.357), at S9. Today SPA serving is gin `NoRoute` (`internal/server/server_lifecycle.go:2141`, `static_embed.go:59-124`). 07 §7.6 changes `main.go:34` to `ui.MustFromFS` but does not say in which stage.
- **Fix:** Add to 09 S2, and mirror in 07 §7.6 and 05-http-api §8.1 H0: "S2–S8: `app.Options.UI` is the zero `Bundle`; `ui.Handler` is not mounted when a module's `LegacyMount` declares `ServesSPA: true`; gin keeps `NoRoute` SPA serving. S9: ABO's build gains the preset (meta tag), `main.go` switches to `ui.FromFS`, and gin's `NoRoute`/`setupStaticFiles` are deleted in the same PR." Make `MountLegacy` reject `/` unless `ServesSPA` is set, and make the kernel skip `ui.Handler` in that case. In S2–S8 make `FromFS` treat a missing meta tag as a WARN with `buildID=""` rather than a boot failure (the `uiBuildId` check is then skipped, see FE-15).

### FE-2: BLOCKER. Step 2 of 07 §7.19 (switch to `createShell`) cannot ship before step 3 (react-router 8)

- **Where:** 07 §7.19 (l.553–558), §7.3.3 (l.91), §7.4.2 (l.252–256); 13 §13.4.1 E4 (l.177).
- **Evidence:** The shell has `react-router ^8.5.1` as a **peer** and renders a react-router 8 `RouterProvider` data router (07 l.91, 254). 13 E4 says "ABO stays on `react-router-dom` 7.18.5 until the shell-adoption wave". 07 §7.19 orders "(2) switch ABO `main.tsx` to `createShell`; (3) react-router 7 → 8". React Router v8 removed `react-router-dom` (13 l.177). If ABO's 52 non-test files still import `react-router-dom@7`, they read v7's router context, but the shell provides v8's. Every `useNavigate`/`useLocation` in ABO pages would then throw "may be used only in the context of a <Router>". npm also reports an unmet peer (ERESOLVE) for `react-router@8`. The claim "each step shippable" is false.
- **Fix:** Reorder 07 §7.19 to: (1) react-router-dom 7 → react-router 8 inside ABO with the declarative `<BrowserRouter>` unchanged (pure import swap plus the `vite.config.ts:81-82` chunk regex); (2) create `core/web`; (3) `createShell`, which moves to the data router and deletes `useUnsavedChangesBlocker`; then the rest. Update 13 E4's exit condition to "before `createShell` adoption", and change 09 S9's prerequisites to match.

### FE-3: MAJOR. `ui.Handler` drops today's SPA-fallback protections and has no hook for app SPA middleware, which regresses ABS clients and CF recovery

- **Where:** 07 §7.6 `Handler` doc (l.335–342), §7.13 (l.497–498).
- **Evidence:** The spec only says "unmatched `/api/...` → 404 JSON; every unmatched non-API GET → index". Today `spaWillNotServe` also 404s (a) whole-segment server paths `auth`, `socket.io`, `ping`, `status`, `logout`, `health`, `metrics`, `public`, (b) any non-GET/HEAD method, and (c) case, trailing-slash and doubled-slash variants via `normalizeNonSPAPath` (`internal/server/spa_fallback.go:58-60,74-83,90,119`). These rules came from real AudioBooth (OIDC probe) and Absorb (`/socket.io` handshake) failures documented there. ABO also runs CF service-token recovery **only on SPA page navigations** (`setupStaticFiles(cfRecoveryMW)`, `server_lifecycle.go:2141`; `cf_recovery.go:36-60`), and that machinery stays in ABO (09 S4). `ui.Options` has no field for either. A missing hashed file under `/assets/` also falls to index.html (200 text/html), so a stale tab's chunk 404 shows up as a MIME error, not a 404.
- **Fix:** Extend `ui.Options` with `NonSPA func(*http.Request) bool` (or `NonSPAPrefixes []string` plus `NonSPASegments []string`, normalised exactly as `normalizeNonSPAPath`), populated by the kernel from every module's `LegacyPrefixes`/compat claims, and `PageMiddleware func(http.Handler) http.Handler` applied only to index responses. Add rules: non-GET/HEAD → 404 problem; missing `/assets/*` or any path with a file extension → 404, never index. Add `TestHandler_NonSPASegments`, `TestHandler_MissingAssetIs404` and a port of `static_embed_recovery_test.go` to 07 §7.18.

### FE-4: MAJOR. CSP scope narrows from "every response" to "SPA only", and the directive set is left unspecified

- **Where:** 07 §7.6 (l.328–342); 05-http-api §4.2 stage 5 (l.425: "CSP owned by `core/ui`"); 07 §7.14–7.15.
- **Evidence:** Today `securityHeadersMiddleware` sets CSP on **every** response (`internal/server/server_middleware.go:108-109`), including server-rendered HTML such as the ABS OIDC consent form (`handlers/abs/openid.go:1055`) and the placeholder page. Under the spec, legacy/module HTML gets no CSP. The policy itself has more than `script-src`: `style-src 'self' 'unsafe-inline'` (emotion/MUI inject styles), `img-src … https:` (remote covers), `media-src 'self' blob:` (playback), `base-uri 'none'`, `frame-ancestors`, and `report-uri /api/v1/csp-report` (`csp.go:57-67`, `csp_report.go:36`). 07 specifies only `script-src 'self' + hashes`. 07 §7.15 also marks `core.ui.csp.connect_src`/`report_only` as hot-reloadable, while §7.14 says the handler is "immutable after construction".
- **Fix:** In 07 §7.6, list the default policy verbatim (today's `cspBase`) and add `Options.ExtraDirectives` for apps. Split responsibilities: `core/ui` computes the policy (hashes plus config), and `httpapi` stage 5 applies a **default** CSP (`default-src 'none'; frame-ancestors 'self'`) to every non-SPA response, which handlers may override (today's "handler that sets its own CSP keeps it"). Add a core CSP report route `POST /api/core/v1/ui/csp-report` (Public, rate-limited) with a legacy alias. Resolve the hot-reload contradiction: either the handler reads an `atomic.Pointer[policy]` refreshed on config change (as `csp.go:129-137` does today), or mark both keys hot-reload "no".

### FE-5: MAJOR. No file owns CSRF, and 13 claims a mechanism that 05-http-api does not include

- **Where:** 07 §7.20 O5 (l.568) → 05-authn; 05-authn §4.3 (l.403–405) and §6 (CSRF appears only for OIDC state, l.605); 05-http-api §4.2 (no CSRF stage, l.419–437); 13 §13.3 (l.132: "CSRF via `CrossOriginProtection` … `httpapi`").
- **Evidence:** Today's only defence for cookie sessions is `SameSite=Strict` (`handlers/auth.go:233,246`), plus one custom-header check on repairs (`handlers/repairs/handler.go:27-28`). SameSite is **site**-scoped and ignores ports and sibling subdomains. The target topology (ABO, TV and Movies side by side on one host, *arr-style ports, D2/10) is therefore same-site: one app's XSS or a malicious sibling service can POST to another app with cookies attached. 13 says stdlib `http.CrossOriginProtection` is the answer, but the normative middleware table has no stage for it, and 07 leaves it open.
- **Fix:** Add a stage to 05-http-api §4.2 between 10 and 11: "CrossOriginProtection (Go 1.25 `http.CrossOriginProtection`; `Sec-Fetch-Site`/`Origin` check on non-safe methods; trusted origins from `core.http.cors.allowed_origins`; bypass only for routes authenticated by Bearer/API-key header, never for cookie-authenticated requests; compat mounts that declare own auth opt out explicitly)". Add the error row "403 `cross_origin_rejected`". Close 07 O5 by pointing to it, and add `TestCSRF_CrossOriginCookiePostRejected` to 05-http-api §12. Note that the Vite dev proxy is unaffected (same-origin from the browser's point of view).

### FE-6: MAJOR. Two apps on one host overwrite each other's session cookie and log each other out

- **Where:** 05-authn §4.3 (l.403–405: default name `mc_session`, `Path=/`); 07 §7.9; 10 (TV/Movies as separate binaries).
- **Evidence:** Cookies ignore ports. TV and Movies both default to `mc_session` with `Path=/`, so signing in to one replaces the other's cookie, and each browser request carries the *other* app's token. 05-authn §6 (l.591) says "Presented credential invalid … 401 … no fall-through", so the second app answers 401, and the shell's global 401 handler (07 §7.9) sends the user to login. That is a logout ping-pong, and it also discloses one app's session token to the other app's server and logs. ABO escapes only because it keeps `session_id`.
- **Fix:** Change the 05-authn default to `core.auth.session.cookie_name = "<AppID>_session"`, with the `__Host-` prefix when Secure (which forces `Path=/` and no `Domain`). When `core.ui.base_path` is set (FE-19), set the cookie Path to the base path. Add to 05-authn §6: "a cookie whose token is unknown to this app is treated as *absent* (not presented-invalid) when a Bearer/API key is absent", so a foreign cookie cannot cause a 401. Add `TestSession_TwoAppsSameHostIndependent` to the core e2e.

### FE-7: MAJOR. The global "any 401 → reset and go to /login" rule breaks the Public auth flows that return 401 by design

- **Where:** 07 §7.9 (l.418–420), §7.13 (l.494); 05-authn §6 (l.598 `bootstrap_invalid` = 401; login failure).
- **Evidence:** A wrong bootstrap token on `/setup` returns 401, and the shell would navigate away from `/setup` to `/login`, which cannot work with zero users. A failed `POST /auth/login` returns 401, and the reset/redirect wipes the form error. Today there is no global interceptor: `AuthContext` treats 401 as "unauthenticated" only for its own status/me probe (`web/src/contexts/AuthContext.tsx:90-91`). 503 `auth_unavailable` (05-http-api §6) has no defined UI behaviour.
- **Fix:** In 07 §7.9, apply the global handler only to (a) 401 responses whose problem `code ∈ {authentication_required, expired, revoked, inactive_user, invalid_credential}` and (b) requests not marked `skipAuthRedirect`. Generated operations whose OpenAPI security is `public` set `skipAuthRedirect` automatically from `x-mediacore-permission: public`. Add rows to 07 §7.13: "503 `auth_unavailable` → keep session, show retry banner, no logout"; "401 on a Public auth route → error shown in place". Add `auth.flow.test.tsx` cases for both.

### FE-8: MAJOR. As specified, the realtime client cannot combine backoff with resume, cannot see 401/429/503/SSO, and loses named events

- **Where:** 07 §7.9 (l.423–428); 05-realtime §4.1–4.3 (l.161–196), §11 (l.358).
- **Evidence:**
  1. Native `EventSource` sends `Last-Event-ID` **only on its own automatic reconnect**, which uses the server's fixed `retry: 3000` with no backoff. The spec asks for "today's backoff (3 s doubling to 60 s)", and today's manager achieves that by `close()` plus `new EventSource(url)` (`eventSourceManager.ts:119,136-146`), which never sends `Last-Event-ID`. 05-realtime §11 claims "native `EventSource` auto-sends `Last-Event-ID`". That holds only without the backoff.
  2. On any non-200 response (401, 429 and 503 with `Retry-After` from the hub caps, §4.5) or a cross-origin SSO 302, `EventSource` fails permanently and exposes **no status**. So the shell can apply neither "401 → login" nor `Retry-After`, nor detect an `ApiAuthRedirectError`.
  3. The native framing sends `event: <topic>` on every frame (05-realtime §4.2), and such frames are **not** delivered to `onmessage`. Today's manager uses only `onmessage` (`eventSourceManager.ts:135`). A client therefore needs `addEventListener` per topic name and gets no wildcard, although the default subscription is `*`.
- **Fix:** In 07 §7.9, specify the shell's realtime transport as a `fetch()` + `ReadableStream` SSE reader (no new dependency; ~150 LOC). It sends `Last-Event-ID` as a header on every reconnect it schedules (or `?last_event_id=`, already allowed in 05-realtime §4.3), sees status/`Retry-After`/`response.redirected`, routes 401 through FE-7's rule, and keeps the 3 s→60 s backoff. In 05-realtime §4.2, drop the `event:` line for the native stream (the topic is already in `Envelope.Topic`) so a plain `EventSource` fallback still works. Fix the 05-realtime §11 sentence. Add a test: "resume after scheduled reconnect replays from last id".

### FE-9: MAJOR. Stream caps are smaller than the number of streams the UI opens, both during the projection phase and with several tabs

- **Where:** 05-realtime §3 `Options` (l.109–110), §4.5 (l.220–222), §4.8, §11 (l.360); 07 §7.9 and §7.14 ("one realtime connection per tab").
- **Evidence:** `MaxPerBucket` defaults to 4 per user per credential class. Today each tab opens **two** streams, `/api/events` (`App.tsx:137` via `eventSourceManager`) and `/api/v1/operations/events` (`App.tsx:120` → `useOperationsStore.ts:411`). 05-realtime §11 ships "projections first (no client change)", and projections are views of the same hub, so they share its caps. The third ABO tab therefore gets 429 and permanently dead realtime (FE-8.2). The same user on a phone and a desktop shares the bucket too. Separately, plain-HTTP deployments use HTTP/1.1, where browsers allow 6 connections per host *across tabs*: one stream per tab freezes the UI at tab 6. Today's code already worries about this ("avoid blocking browser connections", `eventSourceManager.ts:41-42`).
- **Fix:** In 05-realtime §4.5, count legacy projections in a separate bucket (or exempt them, bounded by `MaxClients`) for the compatibility window, and raise the default `max_per_bucket` to 16. In 07 §7.9/§7.14, require **one stream per browser per app**: tab leader election via the Web Locks API plus `BroadcastChannel` fan-out (both native, no dependency), with followers subscribing through the leader. Add an e2e test that opens 6 tabs over HTTP/1.1 and checks that all still load.

### FE-10: MAJOR. The three files disagree on when and how the UI moves to the native stream, and the shutdown banner is wired to a topic that doesn't exist

- **Where:** 09 §S6 "UI" (l.368); 05-realtime §11 (l.358–361), §4.7 (l.244); 07 §7.9 (l.427–428), §7.20 O2.
- **Evidence:** 09 S6: "`eventSourceManager` default URL → core stream in the **same build**". 05-realtime: "projections first (no client change), then the shell moves". 07: the move happens with the shell (S9). Changing only the URL breaks every consumer, because native frames are `Envelope{topic,ts,key,data}` with new topic names. Consumers include the `system.shutdown` check (`App.tsx:137-141`), TopBar (`TopBar.tsx:52`) and Library's `books.changed` handlers (`Library.tsx:527,1074`). 07 drives the shutdown backdrop from `core.system.shutdown.v1`, which 05-realtime does not declare. Its closest topic, `core.system.status.v1`, requires `core:system.view`, so non-admin users would never see the backdrop (today any authenticated stream-A subscriber does). The hub already sends `core.stream.closed.v1 {"reason":"shutdown"}` to everyone (05-realtime §5.4). The hook is also named `useTopic`/`useResync` (07) in one place and `useEventStream({topics,key})` (05-realtime §11) in another.
- **Fix:** Pick one sequence and write it in all three files. Recommended: S6 ships hub plus projections with no client change; the S9 shell moves to native. Delete 09 S6's UI row or change it to "no change". In 07 §7.9, drive the backdrop from `core.stream.closed.v1{reason:"shutdown"}` and close O2. Use `useTopic`/`useResync` in 05-realtime §11.

### FE-11: MAJOR. In S4 the server switches permissions to canonical names, but the UI's hard-coded legacy atoms are not updated, so buttons disappear

- **Where:** 09 §S4 "Permissions" ("output always canonical") and "UI" (l.340); 07 §7.9.
- **Evidence:** Client constants: `LIBRARY_DELETE_PERMISSION = 'library.delete'` / `LIBRARY_ORGANIZE_PERMISSION` (`web/src/hooks/useLibraryPermission.ts:9,11`), `useCanOrganizeLibrary.ts:9`, `COLLECTIONS_MANAGE_PERMISSION = 'collections.manage'` (`hooks/useCollections.ts:24`), `THROTTLE_RESET_PERMISSION = 'scan.trigger'` (`components/system/ThrottlePanel.tsx:24`). The API-key scope picker sends legacy atoms (`components/settings/APIKeysTab.tsx:49-61`). Once `/me` returns `books:delete` etc., the fail-closed advisory checks hide delete/organize/collection/throttle controls for everyone. Key creation keeps sending legacy atoms, so the legacy-input counter never reaches zero, which is the very reason 09 gives for switching UI calls "in the same build".
- **Fix:** Add to 09 S4 "UI": "replace the 5 permission constants and the `APIKeysTab` scope list with canonical atoms read from `GET /api/core/v1/rbac/permissions` (titles from 05-rbac `PermissionDef.Title`); add a vitest that fails on any `'<word>.<word>'` permission literal outside generated files." Add the same note to 07 §7.11's row for the Users/API-keys pages.

### FE-12: MAJOR. The generated client arrives at S9, but S4–S6 move UI calls to native routes "in the same build", and the native JSON naming is undefined

- **Where:** 09 §S4/S5/S6 "UI" (l.340, 355, 368) and §S9 "Goal" (l.402: "generated core API client"); 07 §7.19 step 6; 07 §7.5.1 (l.262 "camelCase"); 05-http-api §3–§4, §7 (no naming rule).
- **Evidence:** S4–S6 require the hand-written `api.ts` to call `/api/core/v1/{auth,users,tasks,events}`, which have new shapes: problem+json errors (05-http-api §4.3), cursor pagination (§4.4), `PublicID` sessions, and canonical permissions. Those calls are then rewritten a second time for the generated client at S9. The naming of native fields is not normative anywhere. 07 asserts camelCase for `/modules` and 08 F2 says camelCase for the facade, while ABO's wire is snake_case (`AuthUser.created_at`, `root_dir`, `op_id`; raw/13 §9, `App.tsx:89`, `useOperationsStore.ts:416`). The tygo/opsgen goldens (`internal/wirecontract`) assume snake_case Go tags.
- **Fix:** (a) Make the JSON naming rule normative in 05-http-api §4.3, with one convention for all native routes. snake_case keeps every moved Go type and tygo golden valid; camelCase matches 08. Record the choice in 02. (b) Add a UI stage before S4 to 09 ("UI-0: `@mediacore/api-client` runtime + generator + `make generate-api-client` in `core/web`, consumed by ABO via `file:`"), so S4/S5/S6 switch straight to the generated client. Change 09 S9's goal to "shell and books contribution" only.

### FE-13: MAJOR. Eleven-plus build entry points run `cd web && npm ci` and break once `web/` depends on built `core/web/packages/*/dist`; `.dockerignore` lets host `node_modules` into the image

- **Where:** 07 §7.3.1–7.3.2 (l.61–79); 13 §13.4; raw/14.
- **Evidence:** With `exports` pointing at `dist/` (07 l.61–63), any `npm ci` plus typecheck/vitest/build in `web/` without a prior `core-web-build` fails module resolution. The spec updates only `make web-build` and `Dockerfile`. Unlisted sites: `.github/workflows/ci.yml:379-383`, `e2e.yml:161-167`, `binary-smoke.yml:40,55`, `prerelease.yml:51` and `release-prod.yml:58` (goreleaser `pre-build-script`), `security.yml:81-82`, `vulnerability-scan.yml:59-62` (with `cache-dependency-path: web/package-lock.json`), `Dockerfile.build-cgo:17`, `scripts/ci_remote.py:516`, Makefile `web-install`/`web-test`/`web-lint`/`web-typecheck`/`web-dev` (`Makefile:192-218`, none depend on a core build), and CLAUDE.md's worktree recipe `npm ci --prefix web`. `.dockerignore` excludes only root `node_modules`/`dist` and `web/node_modules`/`web/dist` (l.18–23). `COPY core/web/ ./` would therefore copy the host's `core/web/node_modules` (with macOS native rolldown/esbuild/oxc binaries) and the host-built `dist/` over the image's own install. The dev loop is also slower: shell source edits are invisible to ABO's Vite dev server and vitest until `core/web` is rebuilt.
- **Fix:** In 07 §7.3.2, (1) add a single entry point `make web-deps` (`core-web-build` then `npm ci` in `web/`) and require every site above to call it; list them in 07 and the plan; add `core/web/package-lock.json` to every `cache-dependency-path`; add a Dependabot npm entry for `/core/web`. (2) Add `**/node_modules` and `core/web/packages/*/dist` to `.dockerignore`. (3) Add `exports` condition `"development": "./src/index.ts"` for in-repo dev, plus a Vite `resolve.conditions` entry in the preset, so `make web-dev` and vitest use shell sources while production builds use `dist`. Alternatively, record why a single repo-root workspace (`["web","core/web/packages/*"]`, one lockfile, no duplicate React, no dedupe script) was rejected for the in-repo phase.

### FE-14: MAJOR. 07 and 12 contradict each other on npm publishing and consumption after the split

- **Where:** 07 §7.17 (l.527–533), §7.3.2 (l.69–79); 12 §12.4 S8 (l.160), §12.5 step 6 (l.238–239), §12.6 (l.250–268), cutover l.340.
- **Evidence:** 07 chooses GitHub Release tarball URLs, with Renovate bumping them and GitHub Packages as fallback. 12 publishes to the npm registry or GitHub Packages and pins `"@mediacore/shell": "X.Y.Z"` (a registry specifier), with Dependabot. 12 l.340 speaks of "`web/package.json` workspace links" and l.160 of "the root workspace", while 07 uses `file:` deps and no root workspace. 12 also claims a Dependabot config where "the Go module and the three npm packages arrive in one PR", but its YAML puts a `groups` entry under each `package-ecosystem`, which yields **two** PRs. That briefly leaves Go core at X+1 with the shell at X (see FE-15).
- **Fix:** Choose one mechanism and state it in both files. Since 14 Q-B recommends a public repo, the npm registry (free for public packages) with exact versions is simpler than tarball URLs and works with Dependabot. Change 07 §7.17 to that, keeping tarballs only as the private-repo fallback. Replace "workspace links" and "root workspace" in 12 with "`file:` deps". Either use Dependabot's multi-ecosystem grouping, if available on the account, or state "two PRs" and add the FE-15 lockstep check so a half-bumped state fails CI.

### FE-15: MAJOR. G3 checks module versions but never compares the shell's core version with the server's; the build-ID check misfires in dev

- **Where:** 07 §7.5.1–7.5.2 (l.264–301); 12 §12.6.
- **Evidence:** `/modules` returns `core.version`, but the algorithm ignores it. Within one in-repo build the two always match. After the split, though, the Go pin (`go.mod`) and npm pins (`@mediacore/*`) move in separate PRs (FE-14), so a binary can embed shell X+1, which calls `/api/core/v1` routes that core X does not have, and nothing catches it. 07 l.299–301's argument "server and SPA from one commit" no longer holds. Also, with `make build-api` plus the Vite dev server (zero bundle, `buildID=""`) or the S2–S8 legacy bundle (FE-1), `app.uiBuildId` never equals the bundle's ID, so the non-dismissable "new version, reload" banner shows permanently.
- **Fix:** (a) Add a CI check in every app (`scripts/ci/check_mediacore_lockstep.py`) that the `@mediacore/*` versions in `web/package-lock.json` equal the `github.com/falkcorp/mediacore` version in `go.mod` (in-repo: both are the workspace version). (b) Add a §7.5.2 row: "`major(shell core) ≠ major(S.core)` or `minor(shell core) > minor(S.core)` → shell renders a blocking 'UI/server mismatch' page (diagnostic `core-version-incompatible`)". (c) Skip the build-ID banner when either side is empty or `"dev"`; the preset defines `MEDIACORE_BUILD_ID="dev"` under `vite serve`.

### FE-16: MAJOR. The extension API lacks slots that ABO's own page split needs, so the "move unchanged" step would have `core/web` import ABO code, and nothing forbids that for npm

- **Where:** 07 §7.4.2 `ModuleUIInput` (l.205–216), §7.11 table (l.452–471), §7.19 step 1 ("moving the shell-classified files … unchanged"); 05-logging §5 (l.249–250); 09 §S9 (l.403–404); 03 §3.5 R1 (Go only).
- **Evidence:** Shell-classified files import audiobook code: `MainLayout.tsx:9-10,25` (`ReviewBanner`, `usePendingFileOps`); `Users.tsx:8,12-13` (`GuestPhoneKeys`, which is CF guest tokens and stays in ABO per 09 S4; `UserMergeDialog`); `ActivityLog.tsx:7,60` (`versionApi` undo preflight, `PendingFileOpsBanner`); `useOperationsStore.ts:7` and `AuthContext.tsx:16` (`* as api from '../services/api'`, the 8.7k-line ABO module). The wizard gate uses a books concept (`App.tsx:87-89`: `config.root_dir || config.setup_complete`). Not expressible today: layout banners, TopBar items, Users-page row actions and tabs, Activity panes (05-logging §5 says "module-contributed panes (the books feed)"), a setup-required predicate, and module theme tokens (only the *app* passes `extend`). The shell routes also disagree: 07 puts tasks at `/system/tasks`, while 09 S9 lists `/activity` as a shell page.
- **Fix:** Add to `ModuleUIInput`: `banners?: {id, lazy, permission?}[]` (rendered by `MainLayout`), `topBarItems?`, `userPageExtensions?: {tabs?, rowActions?}`, `activityPanes?: {id,label,lazy,permission?}[]`, `setup?: {isRequired: () => Promise<boolean>, steps}` (replacing bare `setupSteps`), and `themeExtend?: ThemeOptions`, which the shell merges per module. List the new IDs in `UIDescriptor` (06 l.212–217). Settle the route as either `/system/tasks` or `/activity` and use it in 07, 09 and 05-logging. Add an R1 equivalent for npm in 07 §7.18: an ESLint `no-restricted-imports` (or `import/no-relative-packages`) rule in `core/web` that rejects any specifier resolving outside `core/web`, running in `make ci` from wave 0 rather than only at split criterion S8.

### FE-17: MAJOR. The react-router migration is undercounted (52 files vs 122), 07 and 13 disagree, and the test harness rework is missing

- **Where:** 07 §7.2 (l.35), §7.3.3 (l.91), §7.19 step 3; 13 E4 (l.177), §13.7; 14 K4.
- **Evidence:** `git grep -lE "from ['\"]react-router(-dom)?['\"]" 748fd787a -- web` returns 122 files: 52 non-test plus 70 test/e2e files. 07 says "ABO migrates its 52 importing files", 13 and 14 say 121. Moving to the **data router** (needed for `useBlocker`, 07 l.465) is more than an import swap. `web/src/test/renderWithProviders.tsx:7` wraps in `MemoryRouter` and is called from 306 sites, and 65 files use `MemoryRouter`/`BrowserRouter`. Any component that calls `useBlocker` (Settings, today via `useUnsavedChangesBlocker`) throws under a non-data router in tests. `vite.config.ts:81-82` names `react-router-dom` in the chunk regex. 07's layout mentions `@mediacore/shell` `/testing` but gives it no API.
- **Fix:** Use "122 files (52 source, 70 test)" in 07, 13 and 14. Add to 07 §7.4.2 a `@mediacore/shell/testing` API: `renderInShell(ui, {route, modules?, auth?, permissions?, realtime?: FakeRealtime})` built on `createMemoryRouter` plus fake `ClientProvider`/`AuthProvider`. Add "rewrite `renderWithProviders` onto it" as an explicit step with its own PR in §7.19.

### FE-18: MAJOR. The `mediacore` chunk group pulls lazy shell pages into one eagerly loaded chunk, and it matches nothing in-repo

- **Where:** 07 §7.10 (l.438–440), §7.3.1 (l.61–63, "byte-for-byte").
- **Evidence:** In rolldown, `codeSplitting.groups` place every matching module in the group chunk regardless of static vs dynamic import. The group loads with the entry (`createShell`), so the shell's lazy pages go with it: the Tasks page (from `ActivityLog.tsx`, 3,320 LOC), Users, System, and the Settings frame. ABO's own config comment warns that groups are hints and that the exact-file check must be re-run (`vite.config.ts:66-75`). In-repo, `file:` deps are symlinks that Vite resolves to real paths (`core/web/packages/shell/dist/...`), so a `node_modules/@mediacore` test matches nothing. After the split it matches everything. Bundle composition then differs between in-repo and split builds, contradicting "byte-for-byte".
- **Fix:** Drop the `mediacore` group from 07 §7.10 and rely on dynamic imports, which library-mode builds preserve as separate chunks. If a group is kept, restrict it to the shell's eager entry files by package-internal path (`/@mediacore\/shell\/dist\/(index|runtime)-/` and the real-path equivalent). Add a CI bundle check that loads `/login` with an empty cache and asserts that no shell-page chunk is fetched, plus a size budget for the entry and `mui` chunks.

### FE-19: MAJOR. `core.ui.base_path` exists only on the UI side; the API, cookies and SSE stay absolute, so a sub-path deployment cannot work

- **Where:** 07 §7.4.2 `ShellConfig.basePath` (l.230), §7.6 `Options.BasePath` (l.329), §7.15 (l.513); 05-http-api §10 (no base path); 05-authn §4.3 (`Path=/`).
- **Evidence:** The shell's API client and realtime use absolute `/api/core/v1/...` paths (07 §7.8–7.9). `httpapi` has no prefix option, the cookie path is `/`, and the dev proxy is `/api`. With `base_path=/abo` behind a reverse proxy, the SPA loads at `/abo/` but calls `/api/...` at the proxy root. That reaches another app, or nothing. The *arr ecosystem treats URL base as a standard feature (05-http-api §4.9 relies on it for facades).
- **Fix:** Either (a) remove `core.ui.base_path`/`basePath` and state "served at `/` only; use a host-based proxy", or (b) add `core.http.base_path`, applied by `Router.Build` to every pattern (including the facade and legacy mounts). The cookie Path comes from it (FE-6). The shell reads it from `<base href>` injected by `ui.Handler` into index.html (so one build serves any base), the api-client and SSE reader use relative URLs, and the Vite preset sets `base: './'`. Add an e2e test under `/sub/`.

### FE-20: MINOR. O4 is answerable today: the override works on TS 6, fails on TS 7, and the Go-emitter fallback is not needed; also undecided is where `core.json` lives

- **Where:** 07 §7.3.3 matrix row (l.105), §7.8 (l.404–409), §7.20 O4; 05-http-api §7 (l.606–622).
- **Evidence:** See the experiment at the top. Without an override, `npm install` fails ERESOLVE. With `"overrides": {"openapi-typescript": {"typescript": "$typescript"}}`, TS 6.0.2 installs and generates correctly. TS 7.0.2 crashes. Separately, 05-http-api has *each app's* `openapi` subcommand emit `api/openapi/<owner>.json` including `core`, but `@mediacore/api-client` must be generated from **one** core document inside `core/` (post-split repo). Nothing says which binary emits it or forbids ABO's copy from diverging.
- **Fix:** In 07 §7.8 and 05-http-api §7, put the exact override text in every package that runs the generator, and delete "Go emitter fallback" (or keep it only as "if a future openapi-typescript drops TS 6"). Close O4. State: "`core.json` is emitted by `core/examples/hello`'s `openapi` subcommand into `core/web/packages/api-client/openapi/core.json` and freshness-gated in core CI; apps do not emit `core`; `app.Describe` returns core's document only for the core CI target".

### FE-21: MINOR. 07's frontend version targets violate 13's release-age rule, and 13 is wrong about knip

- **Where:** 07 §7.3.3 (l.87–94); 13 §13.4 (l.153–154), §13.4.1 E1 (l.174), §13.6.
- **Evidence:** 07 targets `vitest ^5.1.0` and `@vitejs/plugin-react ^6.2.1`. 13 holds both at `^5.0.5`/`^6.1.0` because they were published 2026-11-02 (14-day rule). 13 E1's exit condition says knip "also loads the TS API", but `npm view knip@6.44.1 dependencies` shows `oxc-parser`/`oxc-resolver` and no `typescript`, which matches 07 l.103.
- **Fix:** Make 07 §7.3.3 refer to 13 §13.4 for versions instead of restating them. Drop knip from 13 E1's exit condition.

### FE-22: MINOR. `badge: () => useReviewStore(...)` calls a hook from a function not named `use*`, so ABO's lint/compiler gate rejects it

- **Where:** 07 §7.4.2 `NavContribution.badge` (l.174), §7.11 (l.463).
- **Evidence:** `eslint-plugin-react-hooks` 7 (`web/package.json` devDeps), together with the React Compiler (`vite.config.ts:21`), treats only `use*` functions and components as hook callers. An arrow function stored in a nav object that calls `useReviewStore` triggers `rules-of-hooks` and is not compiled as a hook.
- **Fix:** Rename the field to `useBadge?: () => number | undefined`. Have the shell call it from a dedicated `<NavBadge>` component per entry, and document "must be a hook; stable identity". Update the §7.11 example.

### FE-23: MINOR. 03's reserved module IDs don't cover the shell's own top-level routes

- **Where:** 03 §3.6 Module ID row (l.206); 07 §7.4.2 reserved shell routes (l.255–256); 05-http-api §4.1 (l.409–411); 09 §S9 (`/activity`).
- **Evidence:** Module pages live at `/<moduleID>/…`, and the shell owns `/invite/:token` and (per 09) `/activity`. `invite` and `activity` are not reserved, so a module `invite` would shadow the public invite page. 05-http-api proposes its own, different reserved list as an objection.
- **Fix:** Have 03 §3.6 reserve the union of every shell route's first segment (`invite`, `activity` or `tasks` per FE-16, `system`, `settings`, `users`, `login`, `setup`) and 05-http-api's list. Have the kernel derive the reserved set from one exported `ui.ReservedSegments` so the two cannot drift.

### FE-24: MINOR. 07's "Objections to 03" is stale

- **Where:** 07 "Objections to 03" item 1 (l.573–575); 03 §3.1 SDK row (l.18).
- **Evidence:** 03 v1.2.0 already lists `@mediacore/ui-kit` in the SDK row.
- **Fix:** Delete the objection, or mark it "applied in 03 v1.2.0".

### FE-25: MINOR. The SPA handler's place in the route chain (permission, rate class) is unspecified, and R6 or the rate limiter would trip on it

- **Where:** 07 §7.6 (l.345); 05-http-api §4.2 stages 9–12, §8.2; 03 R6.
- **Evidence:** `ui.Handler` is a `/` catch-all "route". R6 and `archtest.RoutesHavePermissions` require a permission, and Public routes must be on the reviewed allowlist. Stage 9 rate-limits "routes" at 100/min per client address by default (05-http-api §10), and a cold load fetches index plus entry, `mui`, `vendor` and per-page chunks plus API calls. Today the SPA fallback is gin `NoRoute`, outside `apiRateLimiter` (`server_lifecycle.go:2128-2141`).
- **Fix:** In 07 §7.6, mount `ui.Handler` outside the route chain (stages 0–8 only: peer allowlist, request ID, tracing, recovery, access log, headers, compression), list it explicitly as Public in the allowlist with `RateExempt`, and add it to `archtest.RoutesHavePermissions`' known infra set.

### FE-26: MINOR. The peer-dependency list is incomplete, and how packages are compiled is unstated

- **Where:** 07 §7.3.1 (l.64–65), §7.4.2 (l.140), §7.10.
- **Evidence:** Shell types import `SvgIconComponent` from `@mui/icons-material` (07 l.140), but that package is not in the peer list, so a second copy and duplicate type identity are possible. It is also unspecified whether library-mode builds run the React Compiler. If they do, the dist imports `react/compiler-runtime`; if not, shell components are unmemoized, because app Babel plugins normally skip `node_modules`.
- **Fix:** Add `@mui/icons-material ^9.5.0` as a peer (or type the icon as `ComponentType<SvgIconProps>` from `@mui/material`). State "packages are built with `reactCompilerPreset()`; `react` peer ≥ 19 covers `react/compiler-runtime`", and add `react/compiler-runtime` to the singleton check.

### FE-27: MINOR. The schema-driven settings forms that 07 relies on are not designed anywhere

- **Where:** 07 §7.11 Settings row (l.456: "Deluge → generic adapter-instance form rendered from the config schema"); 05-config open item 3 (l.579–580, `x-mediacore-ui` hints undecided).
- **Evidence:** No renderer, library or hint vocabulary is specified. Secret fields (05-config `secrets` references), locked keys (`KeyState`/`settingLocks.ts`) and restart-required badges all need UI semantics.
- **Fix:** Add an item to 07 §7.20 owned by 07, and either specify a minimal in-house renderer (string/number/bool/enum/array/secret-ref/locked, driven by JSON Schema plus `x-mediacore-ui: {group, order, widget}`) or name a free library. Close 05-config open item 3 by reference.

### FE-28: MINOR. The `api.ts` migration ignores the seven sibling hand-written service files, and the module client is called a "package" in an app with no packages

- **Where:** 07 §7.11 `services/api.ts` row (l.468), §7.8 (l.399); 05-http-api §8.3 (l.681).
- **Evidence:** raw/13 §12 counts 8 hand-written service files totalling 9,811 LOC. Besides `api.ts`, they are `activityApi.ts`, `collectionApi.ts`, `fileOpsApi.ts`, `playlistApi.ts`, `readingApi.ts`, `versionApi.ts` and `eventSourceManager.ts`, and only `api.ts` and the event source are mapped. `@abo/books-api-client` is called a package, but 07 §7.11 keeps books as a directory `src/modules/books/` "promoted to its own package only when ABO gains a second module".
- **Fix:** Add rows for the six `*Api.ts` files (→ books generated client, or `modules/books/legacyApi.ts` during the window). Rename the books client to a directory `web/src/modules/books/api/` (generated) until promotion, and say so in 05-http-api §7.

## Not attacked (fixed decisions)

D6 (core shell plus app contributions, one shell per binary) and D8/D9 are taken as given. The findings
challenge only how they are realised.
