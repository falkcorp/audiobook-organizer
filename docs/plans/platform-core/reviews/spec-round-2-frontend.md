<!-- file: docs/plans/platform-core/reviews/spec-round-2-frontend.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2c619306-c4a4-450e-88d9-3a7917cad01b -->
<!-- last-edited: 2026-11-04 -->

# Spec review round 2: FRONTEND SHELL lens (ID prefix FE2)

Scope: the round-1 FE findings (FE-1..FE-28) as revised into 07 v1.2.0, 05-http-api, 05-realtime,
05-authn, 06, 09, 12, 13 and 03 v1.4.0; then new inconsistencies between those files. Code anchors are
at `748fd787a` (`git show 748fd787a:<path>`). Spec anchors are `file:line` in the current checkout.

Experiment (run for this review, 2026-11-04, Node 22.23.4, npm 10.9.9, empty scratch project laid
out like 07 §7.3.1: workspaces `["packages/*", "../examples/hello/web"]`, package `a` depending on
package `b`): (1) a workspace path outside the root (`../examples/hello/web`) installs and links
correctly; (2) `npm run build --workspaces` runs `a` **before** `b`, i.e. alphabetical, not dependency
order; (3) `npm pack --workspaces` also packs the hello web package; `npm publish --workspaces` skips
it only because it is `private`. Used in FE2-14.

## Part A: regression check of round-1 BLOCKERs and MAJORs

| Round-1 | Status | Note |
|---|---|---|
| FE-1 `/` ownership between S2 and S9 | landed (07 §7.6.3, 05-http-api §4.10, 06 `ProblemSPAOwner`, 09 S2/S9) | but the legacy phase's CSP is now broken: FE2-1 |
| FE-2 react-router 8 before `createShell` | landed in 07 §7.19 / 09 UI-0 | 13 E4 still describes a different scope: FE2-19 |
| FE-3 `ui.Handler` protections | **half**: `health`, `metrics` segments and the core probe paths are not in `NonSPA` | FE2-6 |
| FE-4 CSP everywhere | landed for the shell; **broken for the legacy mount** (`DefaultCSP` is a static string without hashes / connect-src; `Reporting-Endpoints` lost) | FE2-1 |
| FE-5 CSRF stage | landed (05-http-api stage 7) | wording residue: FE2-23 |
| FE-6 per-app cookie | landed in 05-authn §4.3 | 07 restates a different rule: FE2-23 |
| FE-7 401 rule | landed (07 §7.9.1, 05-http-api §4.3, 05-authn §6) | — |
| FE-8 realtime transport | landed | 07 and 05-realtime disagree on redirect detection: FE2-20 |
| FE-9 caps / one stream per browser | **half**: Web Locks is unavailable exactly where the six-connection limit bites | FE2-2 |
| FE-10 one realtime migration order | landed for S6 | S5 now creates a REST/stream vocabulary split: FE2-3 |
| FE-11 permission atoms in S4 | landed in substance | the literal-ban test is unimplementable as worded; route path wrong: FE2-17, FE2-18 |
| FE-12 generated client before S4, snake_case | landed in 07/09/05-http-api | the only `core.json` producer is never scheduled: FE2-9 |
| FE-13 `make web-deps`, `.dockerignore` | landed | the build step it defines fails on a clean tree: FE2-14 |
| FE-14 one npm mechanism | landed in 07/12 §12.6 | 12 §12.5 step 6 still offers GitHub Packages: FE2-22 |
| FE-15 core-version row, lockstep, dev banner | landed | build-ID derivation is circular; S0 adds a second, conflicting build check: FE2-5, FE2-15 |
| FE-16 slots, `/activity`, import boundary | landed | `/logout` shell route collides with the ABS segment: FE2-7 |
| FE-17 122 files, `renderInShell`, harness step | **half**: 13/14 still say 121; the harness step is unshippable before shell adoption | FE2-4, FE2-19 |
| FE-18 no `mediacore` chunk group | landed | — |
| FE-19 `base_path` removed everywhere | landed (07 §7.15 + O7, 05-http-api §1, 05-authn §4.3; no other occurrence) | — |

## Part B: findings

### FE2-1: MAJOR. From S2 to S9 the SPA is served with `cspBase` alone: the inline theme script is blocked, the Access-host `connect-src` is lost, and `Reporting-Endpoints` disappears

- **Where:** 05-http-api §8.2 `LegacyMount.DefaultCSP string` (`05-http-api.md:794`), stage 5 (`:495`), §8.1 S2 row (`:774`); 09 S2 HTTP row (`09-abo-on-core.md:410`); 07 §7.6.3 phase table row 1 (`07-ui-shell.md:604`: "ABO's CSP machinery unchanged"), §7.6.2 (`:582-589`).
- **Evidence:** `cspBase` (`internal/server/csp.go:57-67`) has **no** `script-src` and **no** `connect-src`; the served policy is `buildCSP(hashes, team)` (`csp.go:95-106`): `script-src 'self' <sha256 of index.html's inline script>` and `connect-src 'self' https://<Access team>`, re-read per response (`csp.go:129-138`). The header `Reporting-Endpoints: csp="/api/v1/csp-report"` is set by `securityHeadersMiddleware` (`server_middleware.go:109`). 09 S2 and 05-http-api §8.1 remove the security-header middleware from gin in the S2 PR and let stage 5 write `DefaultCSP` ("today's cspBase"). With `default-src 'self'` and no hash, the pre-paint colour-scheme script in `index.html` is blocked, and a Cloudflare Access expiry redirect is blocked by `connect-src 'self'`, so `apiFetch` can no longer recognise "session expired" (the reason the team host is in the policy, `csp.go:34-46`). Stage 5's header list ("nosniff, frame, referrer, permissions policy") has no `Reporting-Endpoints`, so Chromium, which prefers `report-to` over `report-uri`, sends no reports. 07 §7.6.3 says gin keeps its CSP machinery, which contradicts 09 S2 / 05-http-api §8.1.
- **Fix:** Change `LegacyMount.DefaultCSP` to `DefaultCSP func(*http.Request) string` (ABO passes today's `contentSecurityPolicy()`, so the hashes and team host keep working), or state in 09 S2 that the books SPA fallback sets its own computed CSP before stage 5 fills defaults, and make 07 §7.6.3 say the same. Add `Reporting-Endpoints` to the stage 5 list, with the owner's report URL (`/api/v1/csp-report` for the legacy mount, `/api/core/v1/ui/csp-report` for `ui`). Extend `TestCSP_DefaultPerOwner` to assert that the legacy SPA response contains the inline-script hash and the configured team host.

### FE2-2: MAJOR. One-stream-per-browser relies on the Web Locks API, which browsers expose only in secure contexts, so it is missing on exactly the plain-HTTP deployments where the six-connection limit applies

- **Where:** 07 §7.9.2 bullet 2 (`07-ui-shell.md:739-744`), §7.14 (`:847-849`), §7.18 `tabs.spec.ts` (`:919`); 05-realtime §4.5 (`05-realtime.md:241-249`).
- **Evidence:** `navigator.locks` is defined only in secure contexts (HTTPS or loopback). The six-connections-per-host limit that FE-9 was about applies to HTTP/1.1, which in practice means plain-HTTP LAN deployments (HTTPS listeners negotiate HTTP/2, `server_lifecycle.go:903-1078`). So on `http://<lan-host>:8484` the leader election either throws (no `navigator.locks`) or, if guarded, every tab opens its own stream again. The e2e `tabs.spec.ts` runs against the example binary on loopback, which is a secure context, so it cannot catch this. Separately, a leader in a background tab is subject to timer throttling and to page freezing; a frozen page keeps its lock, which starves the followers.
- **Fix:** In 07 §7.9.2, specify the election as "Web Locks when `navigator.locks` exists; otherwise a `BroadcastChannel` heartbeat election (leader announces every 2 s, followers take over after 3 missed beats, ties broken by tab ID)". `BroadcastChannel` is available in insecure contexts. Release leadership on `pagehide` and `freeze` events. Run `tabs.spec.ts` against a non-loopback address (bind the server to the runner's LAN interface, or use a hostname resolving to it) so that `isSecureContext` is false, and assert the fallback.

### FE2-3: MAJOR. From S5 to S9 the Activity page reads native task REST but legacy `op.*` stream frames, and nothing says who feeds the legacy streams between S5 and S6

- **Where:** 09 S5 UI row (`09-abo-on-core.md:478`: "Activity/operations pages and `useOperationsStore` switch to generated task-client operations in the same build"), S6 UI row (`:490`: "No change"); 05-realtime §4.9 (`05-realtime.md:313-328`); 05-events migration row (`05-events.md:544`); 07 §7.9.2 bullet 3 (`07-ui-shell.md:745-748`).
- **Evidence:** `useOperationsStore` merges the REST timeline (`api.getOperationTimeline`, `useOperationsStore.ts:258`) with stream B events keyed by `op_id` (`:411-464`), and its call sites compare `def_id`, the legacy `type` and legacy `status` strings (`:14-31`). After S5 the REST side returns native runs (kinds `books.*`, native state vocabulary), while the stream stays on legacy frames until S9 (FE-10's order). Projection B maps task topics back to legacy names "byte-for-byte" (05-realtime §10 `TestLegacyProjection_FramesGolden`). One store then mixes two vocabularies for four stages. Also, the projections arrive only at S6, but the executor that produces run events moves at S5. At S5 the legacy `registry.EventHub` (`internal/operations/registry/bus.go`) is still stream B's source, and no file says that `core/tasks` publishes to it during S5.
- **Fix:** Pick one option and write it in 09 S5 and 07 §7.19. (a) **Recommended:** the S5 UI stays on the task **aliases** (legacy shape via `WithLegacyShape`) until S9, when REST and stream move together. Change 09 S5's UI row to "no change; Activity moves to native REST and stream together at S9". (b) Alternatively, S5 adds a client-side translation layer and a test that feeds one native REST payload and one projection-B frame for the same run into the store. Either way, add to 09 S5 (and 05-tasks §5T.6): "until S6, `core/tasks` publishes every run change to the legacy `registry.EventHub` so streams A/B keep working", with an S5 done-check "Activity page shows live progress on S5".

### FE2-4: MAJOR. Step 4 of 07 §7.19 (`renderWithProviders` onto `renderInShell`) is not shippable before step 5

- **Where:** 07 §7.19 steps 4–5 (`07-ui-shell.md:937-942`), §7.4.4 (`:371-399`); 09 S9 Moves (`09-abo-on-core.md:529`); 04 §8.1 row S9 (`04-package-mapping.md:663`: `@mediacore/shell` arrives at S9).
- **Evidence:** `renderInShell` mounts the **shell's** fake `ClientProvider`/`AuthProvider`/`RealtimeProvider` on a data router. Before step 5, ABO components still read ABO's own contexts: `useAuth` from `web/src/contexts/AuthContext.tsx`, ABO's `ToastProvider`, `OperationTrackerProvider` (`main.tsx:19-47`), all of which move into the shell only in step 5 (07 §7.11 rows). A component under test would therefore find no provider for its context. The stated benefit ("fixes every component that calls `useBlocker` under a non-data `MemoryRouter`", `:396-399`) also does not apply before step 5, because ABO uses `useUnsavedChangesBlocker`, not `useBlocker`, until step 5 deletes it. And 07 step 2 creates `@mediacore/shell` with `/testing` in UI-0, while 04 §8.1 and 09 UI-0 create the shell package only at S9.
- **Fix:** Either merge step 4 into step 5 (same PR series, after the providers move), or add `RenderInShellOptions.wrapper?: ComponentType<{children: ReactNode}>` so that ABO can inject its own providers during the gap, and state that step 4 keeps `MemoryRouter` semantics until step 5. Settle where `@mediacore/shell` is created: 07 step 2, 09 UI-0 Moves and 04 §8.1 must name the same stage.

### FE2-5: MAJOR. S0 introduces a second stale-build mechanism (`X-App-Build`) that 07 does not know, with a different ID source, no dev exemption, and a header that native routes will not carry

- **Where:** 09 S0 Endpoints and UI rows (`09-abo-on-core.md:372-373`), R-test text (`:356-358`), S9 rollback (`:531`); 07 §7.5.2 build-ID banner (`07-ui-shell.md:473-479`), §7.6.3 (`:627-630`); 05-http-api stage 5 (`:495`).
- **Evidence:** 09 S0 adds `X-App-Build: <build id>` on every response and an SPA check that compares it with "the bundle's own build id". 07 defines a different pair: `ui_build_id` in `/api/core/v1/modules` versus `import.meta.env.MEDIACORE_BUILD_ID`, written by the shell preset. Neither file says how ABO's pre-shell bundle obtains a build ID that equals the server's `X-App-Build` (ABO's `index.html` and Vite config have none today). S0 has no dev exemption, so `make build-api` plus `make web-dev` shows the reload offer permanently, which is the defect FE-15(c) fixed for 07's banner. At S2 the perimeter headers move to stage 5, whose list has no `X-App-Build`, and from S4 native `/api/core/v1/*` responses never pass through gin. The check therefore loses its signal during S4–S9, the window it exists for. 07 never says whether the S0 check is deleted or merged at S9, so a shell build would show two banners.
- **Fix:** Define one mechanism. Recommended: 09 S0 defines the build ID as a value generated once per `make build` (for example the git commit plus a build timestamp) and passes it to both `go build -ldflags` and Vite `define`/`<meta>`. Stage 5 writes `X-App-Build` on every response (05-http-api §4.2 header list). The check skips `""`/`dev`. 07 §7.5.2 reuses the same ID as `ui_build_id`, and the S9 PR deletes the S0 check in favour of the shell banner. Add these to 07 §7.6.3 and 09 S9 Moves.

### FE2-6: MAJOR. FE-3 is half-fixed: `NonSPA` drops today's `health` and `metrics` segments and never covers core's own probe paths, so `/health/` and `/Metrics` get 200 HTML

- **Where:** 07 §7.6.1 `Options.NonSPA` comment (`07-ui-shell.md:525-531`); 05-http-api §4.10 (`05-http-api.md:660-664`), §4.1 (`:469`); 05-health (`05-health.md:174-178`); 09 S8 Health row (`09-abo-on-core.md:516`).
- **Evidence:** Today `nonSPASegments` contains `auth, socket.io, ping, status, logout, health, metrics, public` (`internal/server/spa_fallback.go:74-83`). The comment there says why: a case or trailing-slash variant of a registered path must not answer `200 text/html`, or a client or monitor concludes the server is healthy. Both spec lists name only `auth, socket.io, ping, status, logout, public`. `NonSPA` is built "from every claimed prefix" plus legacy `Segments`, but `/health` (legacy exact route), `/metrics`, `/healthz`/`/livez`, `/readyz` (`Router.Infra` exact patterns) are neither prefix claims nor `Segments`. So `/health/` (a common monitor configuration) and `/metrics/` fall through to `index.html` with 200. The probe names also differ: 05-http-api §3.5 and §4.1 say `/healthz`, while 05-health and 09 S8 say `/livez`. `livez` is in no reserved list (03 §3.6 has `healthz`).
- **Fix:** In 05-http-api §4.10, make `Router.NonSPA()` include the first segment of every `Router.Infra` pattern and of every legacy exact route whose first segment is not an SPA page, plus `health` and `metrics` explicitly. Copy today's eight-segment list into 07 §7.6.1. Pick one liveness path name (`/livez`, as 05-health defines it) and use it in 05-http-api §3.5/§4.1, `httpapi.ReservedSegments` and 03 §3.6. Add `/health/`, `/HEALTH`, `/metrics/` and `/livez/` cases to `TestNonSPA_PredicateCoversClaims` and `TestHandler_NonSPASegments`.

### FE2-7: MAJOR. The shell route `/logout` collides with ABO's ABS `/logout` segment: a full page load of `/logout` gets a 404 problem+json after S9

- **Where:** 07 §7.4.3 shell routes and `ui.ReservedSegments` (`07-ui-shell.md:358-366`); 07 §7.6.1 `NonSPA` comment (`:528`, lists `logout`); 05-http-api §4.10 (`:661-662`, lists `logout`).
- **Evidence:** `logout` is a server segment today ("ABS logout", `spa_fallback.go:80`), and ABO's SPA has no `/logout` page (`web/src/App.tsx` route table, lines 204–447). After S9 the shell declares `/logout` as a page, but the kernel feeds ABO's `logout` compat segment into `ui.Options.NonSPA`. A typed URL, a refresh, or an `<a href="/logout">` therefore gets JSON 404 from `ui.Handler`. Only client-side navigation would reach the page. The same trap exists for any future shell page whose first segment an app declares as a compat segment, and nothing detects it.
- **Fix:** Drop `/logout` from the shell route table and `ui.ReservedSegments` (log-out is an action in the user menu that calls `POST /api/core/v1/auth/logout` and navigates to `/login`), or rename it to `/signout`. Add a boot check (kernel P8): `ui.ReservedSegments() ∩ Router.NonSPA segments = ∅`, otherwise `ProblemSPAOwner`-class error. Add a test for it next to `reservedSegments.test.ts`.

### FE2-8: MAJOR. Invite and temp-login links: 05-authn moved tokens to the URL fragment (SEC-21), but 07, 09 and 03 still route `/invite/:token`; invites created from S4 would not open in ABO's pre-shell SPA

- **Where:** 05-authn §4.13 (`05-authn.md:713-716`: the shell reads tokens from `/invite#token=…`, `/login/temp#token=…`); 07 §7.4.3 (`07-ui-shell.md:358`), §7.9.1 (`:721`); 09 S4 Endpoints (`09-abo-on-core.md:461`), S9 UI routes (`:530`); 03 §3.6 UI-route example (`03-architecture.md:331`).
- **Evidence:** ABO's page is `/invite/:token` (`App.tsx:204`; `AcceptInvite.tsx:6` reads the token from the path). If core's invite API (from S4) issues fragment links, ABO's legacy SPA, which serves the UI until S9, has no `/invite` route without a token and sends the visitor to `/` (`App.tsx:205`). Invites created on S4–S8 are then dead. If instead the shell keeps `/invite/:token`, the token reaches the server on every full page load (stage 2/4 redaction lists only "the books legacy invite path"). That is what SEC-21 removed. `/login/temp` is not in 07's shell route list at all.
- **Fix:** Pick the fragment form everywhere. 07 §7.4.3 lists `/invite` and `/login/temp` (fragment token) plus a client redirect `/invite/:token` → `/invite#token=:token` for links issued before the change. 03 §3.6 and 09 S9 change their examples. 09 S4's UI row adds "AcceptInvite reads `#token=` as well as the path parameter, in the same build that switches invite creation to core". Add a stage-4/stage-9 R-test cell: "invite created on cur opens in cur UI and in prev UI".

### FE2-9: MAJOR. `core.json` has a single producer, `core/examples/hello`, but no stage creates hello; the S4–S6 generated-client switch has nothing to generate from

- **Where:** 07 §7.8 bullet 1 (`07-ui-shell.md:664-667`), §7.3.1 (`:58`, workspace member `../examples/hello/web`); 06 `Description.Core` (`06-app-sdk.md:605`); 09 UI-0 Moves/Done (`09-abo-on-core.md:434,437`), S4 UI row (`:463`); 04 §8.1 (`04-package-mapping.md:656,663`).
- **Evidence:** 09 and 04 schedule no stage that creates `core/examples/hello` (`grep -n hello` in 09 and 04 returns only the S0 `ignore ./examples/hello/web` line). UI-0's done-check uses "a fixture route set", but S4 requires `@mediacore/api-client` to contain the real `/api/core/v1/auth|users|config|rbac` operations. Under FE-20 those come only from hello's `openapi` subcommand. 07 §7.3.1's workspace also lists hello's web package from day one.
- **Fix:** Add to 09 UI-0 (or S2, since `internal/openapi` depends on `httpapi`, 04 row UI-0 "after S2"): "create `core/examples/hello` (06 §6.12) with its `openapi` subcommand, emitting `core/web/packages/api-client/openapi/core.json`; core CI freshness gate". Add a matching row to 04 §8.1, and add to S4/S5/S6 done-checks "`core.json` contains the stage's native routes". Also reconcile 09 §9.3 ("UI-0 runs in parallel with S1–S3") with 04's "UI-0 after S2".

### FE2-10: MAJOR. At S9, `ui.MustFromFS(WebFS, "web/dist")` panics on every API-only build, because ABO's non-embed `WebFS` is an empty `embed.FS`

- **Where:** 07 §7.6.3 phase table row 2 and the following paragraph (`07-ui-shell.md:605-608`: "`web_embed.go` stays as is"), §7.12 (`:819-821`); 06 §6.12 hello (`06-app-sdk.md:1404,1416`).
- **Evidence:** `web_nonembed.go` @`748fd787a` declares `var WebFS embed.FS` (empty), and `main.go:34` uses `WebFS` unconditionally. `MustFromFS` "panics on error" and `FromFS` "fails if index.html or the meta tag is missing" (07 `:510-519`). So `make build-api`/`make run-api` (CLAUDE.md's quick-iteration path) and any test that boots `main`'s options crash at S9. 07 §7.12 says boot fails only "when the `embed_frontend` tag is set", which is not what `MustFromFS(WebFS, …)` does. hello already uses the right pattern (`UIBundle()` per build tag).
- **Fix:** In 07 §7.6.3, replace "`web_embed.go` stays as is" with: "ABO adopts the hello pattern at S9: `web_embed.go` defines `func UIBundle() ui.Bundle { return ui.MustFromFS(WebFS, "web/dist") }`, and `web_nonembed.go` returns `ui.Bundle{}`. `main.go` passes `UIBundle()`." Add an S9 done-check: "`make build-api && ./audiobook-organizer serve` answers `/` with the placeholder".

### FE2-11: MAJOR. FE-23 is half-fixed: the union that `module.ReservedIDs()` computes does not equal 03's reserved row, and two different mechanisms supply the compat segments

- **Where:** 03 §3.6 Module ID row (`03-architecture.md:320`); 05-http-api §4.1 (`05-http-api.md:474-479`), §3.5 `ReservedSegments` (`:442-445`), §8.2 `LegacyMount.Segments` (`:796`); 07 §7.4.3 (`07-ui-shell.md:364-369`); 06 `ReservedIDs`, `app.Options.ReservedAPINamespaces` (`06-app-sdk.md:136-139,527-530`).
- **Evidence:** 03 reserves `core, api, v<n>, auth, admin, events, health, healthz, readyz, metrics, system, settings, users, login, logout, setup, invite, activity, tasks, static, assets, ui, custom` and says a test asserts the union equals that row. The union of the owners' lists is `httpapi` {`api, v<n>, healthz, readyz, metrics, health, auth, events`} + `ui` {`activity, invite, login, logout, settings, setup, system, users`} + `rbac` {`core, custom`}. That union is missing `admin`, `tasks`, `static`, `assets` and `ui`, so the test fails as specified. `assets` is a real collision: `ui.Handler` serves `/assets/*` (07 `:549`), so a module `assets` would put its pages under the hashed-asset path. 07 even attributes `assets` to `httpapi.ReservedSegments`, which does not list it. The compat segments come from `app.Options.ReservedAPINamespaces` (03, 06; ABS `/api/<seg>` names) **and** from `LegacyMount.Segments` (05-http-api; fed into `NonSPA` as **top-level** segments). These are two different sets (`/api/libraries` vs `/ping`) under two names, and `Segments` is used for both purposes.
- **Fix:** Assign every 03 entry to an owner. `httpapi`: add `assets`, `static`, `ui` (the `/api/core/v1/ui/*` routes are under `api`, so `ui` is reserved for the SPA path space) and the probe names. `ui`: add `tasks` if the shell keeps a `/tasks` redirect, or delete `tasks` and `admin` from 03. Split the compat input into `LegacyMount.TopSegments` (top-level server paths → `NonSPA` + reserved IDs) and `app.Options.ReservedAPINamespaces` (second segment under `/api` → reserved IDs only), and say so in 03 §3.6, 05-http-api §4.1/§8.2 and 06.

### FE2-12: MINOR. The build ID is defined circularly: a compile-time `define` cannot hold a hash of the chunk names that the define's own value changes

- **Where:** 07 §7.6.3 last paragraph (`07-ui-shell.md:627-630`), §7.10 `define` (`:771`).
- **Evidence:** "`<meta name="mediacore-build" content="<hash of the emitted entry chunk names>">` and into the bundle as `import.meta.env.MEDIACORE_BUILD_ID`". The entry chunk's file-name hash depends on its contents, and its contents would contain `MEDIACORE_BUILD_ID`, which is derived from that file name.
- **Fix:** Either generate the ID before bundling (the per-build value of FE2-5, written into both the define and the meta tag), or drop the define and have the client read its own build ID at runtime from the `<meta>` of the `index.html` it was loaded with. That page is the stale one in a stale tab, so it gives exactly the bundle's ID.

### FE2-13: MINOR. The core-version row's semver rule is wrong for v0, and in-repo nothing ties `@mediacore/shell`'s package version to `version.Version`

- **Where:** 07 §7.5.2 core-version table (`07-ui-shell.md:454-460`), §7.5.3 (`:486-493`); 12 §12.4.1 v0 rules (`12-repo-split.md` §12.4.1), §12.4.2 "first published version" (owner may publish `v0.(Y+1).0` first), §12.4.3 `check_core_version.py`.
- **Evidence:** In v0, 12 allows incompatible changes on any minor bump. "`minor(C) > minor(S)` blocks" then misses `C.minor < S.minor`, a newer server that has removed a route the shell calls. That case is reachable after the split if the owner publishes v0 first. `C.core` is "the version of the `@mediacore/shell` package", but the in-repo lockstep check treats `file:` as "workspace" and checks nothing, and `check_core_version.py` covers only the Go constant. The shell's `package.json` version can therefore drift from `version.Version`.
- **Fix:** In 07 §7.5.2: "major 0: any minor difference is incompatible". In 12 §12.4.3: `check_core_version.py` also asserts that every `core/web/packages/*/package.json` `version` equals `version.Version`.

### FE2-14: MAJOR. `make web-deps` runs `npm run build --workspaces`, which npm runs alphabetically, so `@mediacore/shell` builds before `@mediacore/ui-kit` on a clean tree; the same flag also sweeps in hello's web package

- **Where:** 07 §7.3.2 web-deps bullet (`07-ui-shell.md:104-115`), §7.3.1 exports (`:66-71`); 09 S0 (`09-abo-on-core.md:369`); 12 S8 script (`12-repo-split.md:207`), §12.5 step 6 (`:330`).
- **Evidence:** See the experiment above: npm 10.9.9 ran a dependent package before its dependency. The workspace order is `api-client`, `shell`, `ui-kit`. The shell imports `@mediacore/ui-kit` types (`ShellConfig.theme`, 07 `:327`), and its `.d.ts` emit resolves the package through `exports.default` → `dist/`, which does not exist yet (the `development` condition is used only under `vite serve`/vitest, 07 `:68-70`). So the first Docker or CI build of S0/UI-0 fails. `--workspaces` also builds and packs `../examples/hello/web` (experiment item 3) inside ABO's Docker frontend stage and in 12's S8 tarball step.
- **Fix:** In 07 §7.3.2, replace `npm run build --workspaces` with an explicit ordered list: `npm run build -w @mediacore/api-client -w @mediacore/ui-kit -w @mediacore/shell` (npm runs `-w` in the order given), or use TS project references (`tsc -b`) for declarations. Apply the same `-w` list to 12's `npm pack` and `npm publish` lines. State that hello's web package is `"private": true`.

### FE2-15: MINOR. Every `web-*` target now depends on `make web-deps` (two `npm ci` runs plus a full core build), which slows the dev loop and races under `make -j`

- **Where:** 07 §7.3.2 (`07-ui-shell.md:106-107`); `Makefile:192-218` @`748fd787a` (today `web-test`, `web-lint`, `web-typecheck` and `web-dev` have no install prerequisite).
- **Evidence:** `npm ci` deletes `node_modules` first. `make web-test web-lint -j2` would run two `npm ci` in the same directories at once. `make web-dev` reads shell sources through the `development` condition, so it does not need `core/web` built at all.
- **Fix:** Make `web-deps` a stamp target (`web/node_modules/.web-deps.stamp`) that depends on both lockfiles and `core/web/packages/*/src/**`, so it is a no-op when up to date. Have `web-dev` depend only on the install half.

### FE2-16: MINOR. 09 S9's legacy SPA redirect list misses ABO's current home and three other pages, and `ui.Handler` has no hook for the promised `ui_route` usage counter

- **Where:** 09 S9 UI routes (`09-abo-on-core.md:530`); 07 §7.11 rows Dashboard, FileBrowser, Diagnostics (`07-ui-shell.md:794-796`); 07 §7.6.1 `Options` (`:524-546`).
- **Evidence:** ABO's routes include `/dashboard` (the home that `/` redirects to, `App.tsx:256-258`), `/files` (`:315`), `/diagnostics` (`:365`), `/operations` and `/maintenance` (`:322-323`). 07 maps them to `/`, `/books/files` and `/system/diagnostics`, but 09's list omits all five, so bookmarks and installed-app start URLs hit `NotFound`. 09 also says "server records full-page loads of legacy SPA paths on surface `ui_route`", but after S9 `ui.Handler` serves every unmatched GET and has no knowledge of legacy paths.
- **Fix:** Add `/dashboard → /`, `/files → /books/files`, `/diagnostics → /system/diagnostics`, `/operations → /activity` and `/maintenance → /system` to 09 S9 and to books' `redirects`. In 07 §7.6.1 state that ABO's `PageMiddleware` records `ui_route` for paths in books' `redirects` list (one `compat` call, no new `ui.Options` field).

### FE2-17: MINOR. The S4 permission-literal ban cannot be implemented as worded

- **Where:** 07 §7.9.1 FE-11 bullet (`07-ui-shell.md:713-720`), §7.18 `permissionLiterals.test.ts` (`:906`); 09 S4 UI row (`09-abo-on-core.md:463`).
- **Evidence:** "a vitest fails on any `'<word>.<word>'` permission literal outside generated files". A lexical test cannot tell a permission literal from any other dotted string. `git grep -nE "'[a-z]+\.[a-z_]+'"` over non-test, non-generated `web/src` @`748fd787a` finds 744 matches, mostly MUI palette keys (`'text.secondary'`, `'primary.main'`), plus the legacy event types (`'system.shutdown'`, `App.tsx:138`) that must stay until S9. Canonical atoms such as `'books:collections.manage'` (05-rbac §6.1) are not of that form, so the stated goal ("never hard-codes a permission string") is not what the test checks.
- **Fix:** Specify the test as an AST rule. It fails on any string literal passed to `usePermission`/`useAction`/`hasPermission`, assigned to a `permission`/`*_PERMISSION` property or constant, or used in the API-key scope list, unless the literal comes from `manifest.permissions` or the generated client. Implement it as an ESLint `no-restricted-syntax` selector, not a vitest grep.

### FE2-18: MINOR. Route names in 07 and 09 do not match the owning specs

- **Where / evidence:**
  - 07 §7.9.1 (`07-ui-shell.md:714-715`) and 09 S4 (`09-abo-on-core.md:461,463`) say `GET /api/core/v1/rbac/permissions`; 05-rbac §4.7 (`05-rbac.md:500`) registers `GET /permissions` under `/api/core/v1`.
  - 07 §7.9.1 (`:693`) says `/api/core/v1/auth/{status,me,login,logout,setup}`; 05-authn §4.13 has `/me`, not `/auth/me`, and also has `/auth/bootstrap`, which the shell's `/setup` flow needs.
  - 09 S5 (`:476`) says `tasks/{defs,…,events}`; 05-tasks §5T.3.10 (`05-tasks.md:817-830`) has `/definitions`, `/decide`, `/schedules` and `/pause`, and no HTTP route named `events` under tasks.
  - Because generated operation names come from these paths, the S4/S5 UI work would target operations that do not exist.
- **Fix:** Use 05-rbac's, 05-authn's and 05-tasks' paths verbatim in 07 and 09, or change the owners. Add `/auth/bootstrap` to 07 §7.9.1.

### FE2-19: MINOR. FE-17's count is still split, and 13 E4 describes a different UI-0 scope than 07 and 09

- **Where:** 13 E4 (`13-toolchain.md:189`), §13.7 (`:275`); 14 K4 (`14-risks-open-questions.md:20`); 07 §7.3.3 (`07-ui-shell.md:130`), §7.19 step 1 (`:928-930`); 09 UI-0 Done (`09-abo-on-core.md:437`).
- **Evidence:** 07 and 09 say 122 files (52 source, 70 test), matching `git grep -lE "from ['\"]react-router(-dom)?['\"]" 748fd787a -- web`. 13 and 14 say 121 (53 non-test), which counts a narrower grep (`react-router-dom` in `web/src` only). 13 E4 says UI-0 migrates "imports, data-router APIs, test harness". 07 step 1 says UI-0 is an import swap with the declarative `<BrowserRouter>`, and puts the data router and harness at steps 4–5 (S9). UI-0's done-check `grep -rl "react-router-dom" web/src` misses `web/tests/e2e` and `web/vite.config.ts:81-82`.
- **Fix:** Use "122 (52 source, 70 test)" and 07's grep in 13 and 14. Change 13 E4's migration scope to "imports and the chunk regex; the data router and harness rewrite follow at shell adoption (07 §7.19 steps 4–5)". Make the UI-0 done-check `! grep -rq react-router-dom web/package.json web/src web/tests web/vite.config.ts`.

### FE2-20: MINOR. 07 and 05-realtime describe the realtime client differently: redirect detection and the hook signature

- **Where:** 07 §7.9.2 bullet 1 (`07-ui-shell.md:734-735`: "sees … `response.redirected`"), §7.4.2 (`:341`: `useTopic<T>(topic, onEvent, opts?)`); 05-realtime §4.8 item 4 (`05-realtime.md:298-299`: `redirect: "manual"`, opaque redirect), §11 (`:436`: `useTopic(topics, {key})`).
- **Evidence:** With `redirect: "manual"` a response is `type: "opaqueredirect"` and `response.redirected` is never true. With the default `follow`, a cross-origin SSO redirect usually fails CORS as a `TypeError` before any `redirected` flag can be read. The two files thus specify incompatible detectors. The hook signatures differ in arity and in whether `topic` is a list.
- **Fix:** Normative text in 05-realtime §4.8 (manual redirect → `ApiAuthRedirectError`); 07 §7.9.2 refers to it. Use one hook signature, 07's `useTopic<T>(topic, onEvent, {key})`, in 05-realtime §11.

### FE2-21: MINOR. The leader's single stream has no defined subscription set; with `*` it carries every visible task log line to every tab

- **Where:** 07 §7.9.2 (`07-ui-shell.md:739-744`); 05-realtime §4.1 (`05-realtime.md:160-168`), §12 item 2 (`:442-444`).
- **Evidence:** `useTopic(…, {key})` is per tab, but one leader stream serves all tabs. Nothing says whether the leader reconnects whenever the union of tab subscriptions changes (which costs a `Last-Event-ID` replay each time) or subscribes `*` and filters on the client. With `*`, `core.task.log-appended.v1` for every run the user may view flows to every tab, whereas today the UI asks for one operation's log (`?operation=`, 05-realtime §2). 05-realtime's own open item 2 says a chatty log topic can evict the resumable window.
- **Fix:** In 07 §7.9.2, specify that the leader subscribes to `*` minus `core.task.log-appended.v1`. Log lines are fetched per run by a separate keyed stream that the viewing tab opens itself (counted in the same bucket), or over REST `GET /runs/{id}/log`. `useTopic` keys filter on the client.

### FE2-22: MINOR. 12 still offers GitHub Packages as the private-repo path, which 07 rejects (FE-14 residue)

- **Where:** 12 §12.5 step 6 (`12-repo-split.md:330-332`), §12.6 (`:346-347`); 07 §7.17 table (`07-ui-shell.md:883-888`).
- **Evidence:** 07: GitHub Packages "rejected: token on every machine"; tarballs are the private fallback. 12: "private repo → GitHub Packages, or GitHub Release tarballs".
- **Fix:** In 12 §12.5 and §12.6: "private repo → GitHub Release tarballs (07 §7.17); GitHub Packages is not used".

### FE2-23: MINOR. CSRF and cookie wording disagrees across 07, 05-http-api, 05-authn and 09; one claim would open a hole if implemented literally

- **Where:** 07 §7.9.1 (`07-ui-shell.md:698-699` "treated as absent", `:709-712` "Bearer and API-key requests exempt"); 05-http-api stage 7 (`05-http-api.md:497`: "No bypass is needed"); 05-authn §4.3 and §6 (`05-authn.md:517-521,776`: an unknown cookie on a non-Public route is 401 + cleared); 09 S2 (`09-abo-on-core.md:410`: "bearer, API-key and Basic requests are unaffected").
- **Evidence:**
  - **Basic credentials.** A browser attaches cached Basic credentials to cross-site requests just as it attaches cookies. Exempting Basic (09's wording) would be a CSRF hole. 05-http-api's stage correctly has no exemption.
  - **Unknown cookie.** 07's "treated as absent" contradicts 05-authn's "401 with the failure code".
  - **Host-rewriting proxy.** Over plain HTTP, browsers send no `Sec-Fetch-Site`. `CrossOriginProtection` then compares `Origin` with `r.Host`, so a plain-HTTP reverse proxy that rewrites `Host` (nginx's default `proxy_set_header Host $proxy_host`) gets every UI POST refused. That works today.
- **Fix:**
  - **07 §7.9.1:** refer to 05-http-api stage 7 and 05-authn §4.3/§6 instead of restating them.
  - **09 S2:** say "non-browser clients (no `Sec-Fetch-Site`/`Origin`) are unaffected; no credential type is exempt".
  - **05-http-api stage 7:** add that when the peer is a `TrustedProxies` member, `X-Forwarded-Host` replaces `r.Host` for the origin comparison (or require operators to list the public origin in `core.http.cors.allowed_origins`, with a health warning on the first refusal).

### FE2-24: MINOR. Stale and contradictory text left in 05-http-api and 07 by the revision (FE-20 and FE-28 half-applied)

- **Where / evidence:**
  - 05-http-api §7 (`05-http-api.md:745-747`): "Whether `openapi-typescript` runs under the repo's TypeScript pin is 07's open item (FE-20)". 07 closed O4 (`07-ui-shell.md:956-958`).
  - 05-http-api §13 item 6 (`:986-987`): the client stage is "09's decision". 09 decided (UI-0).
  - 05-http-api §7 (`:730-734`) has every app binary emit `api/openapi/<owner>.json` with no exception for `core`. 07 §7.8 and 06 `Description.Core` say apps never emit core.
  - 05-http-api §7 (`:754`) and §8.4 (`:860`) still name `@abo/books-api-client` as a package. FE-28 / 07 §7.8 made it the directory `web/src/modules/books/api/`.
  - 07 §7.6.1 (`07-ui-shell.md:568`): the code block declares `WriteManifestTS(w io.Writer, m app.UIManifest)`, while the next paragraph says it must take `ui.ModuleManifest`. `ui.ModuleManifest` is not declared in the block.
  - 07 §7.6.3 and 05-http-api §4.10 record `RouteInfo{…, RateClass: RateExempt}`, but `RouteInfo` (`05-http-api.md:447-457`) has no `RateClass` field.
- **Fix:** Delete or close the two stale sentences. Add "except `core`, emitted only by `core/examples/hello` (07 §7.8)" to 05-http-api §7. Rename the books client in §7/§8.4. Fix the `WriteManifestTS` signature and declare `ui.ModuleManifest` in 07 §7.6.1. Add `RateClass RateClass` to `RouteInfo`.

### FE2-25: MINOR. S9's preconditions name S4–S6 only, but shell-owned pages need S3, S7 and S8 native routes, and `/api/core/v1/modules` is not scheduled in any stage

- **Where:** 09 §9.3 order text (`09-abo-on-core.md:347`), S9 Preconditions (`:528`); 07 §7.11 (System, Settings generic tabs, `<SchemaForm>`, `07-ui-shell.md:792-793`), §7.4.5 (`:403-406`), §7.5.1 (`:425-427`).
- **Evidence:** The shell's System page calls `/api/core/v1/{system,backup,update,health}` (S8), the Integrations/AI tabs call the instance store (S7), and `<SchemaForm>` uses the config schema and `KeyState` API (S3). No stage's Endpoints row introduces `GET /api/core/v1/modules`, `/ui/diagnostics` or `/ui/csp-report`. If they arrive with S9, R-test cell b (cur UI against prev) shows a shell with no modules and a retry banner.
- **Fix:** 09 S9 Preconditions: "UI-0 and S3–S8 native routes". Add `/api/core/v1/modules`, `/ui/diagnostics` and `/ui/csp-report` to the stage that first ships them (S4 is natural, alongside `/rbac` permissions) so that they exist in the build before S9.

## Not attacked (fixed decisions)

D6 (core shell plus app UI contributions, one shell per binary), D8 (latest stable versions), D9 (free
only) and the round-1 cross-cutting resolutions (2: legacy mount owns `/` until shell adoption; 9:
single web build entry point) are taken as given. The findings challenge only how they are written down
and whether they work.

Counts: **0 BLOCKER, 12 MAJOR, 13 MINOR** (25 findings).
