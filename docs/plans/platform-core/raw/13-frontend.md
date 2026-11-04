<!-- file: docs/plans/platform-core/raw/13-frontend.md -->
<!-- version: 1.0.0 -->
<!-- guid: a6aa38c3-904f-4a72-be33-0020f6518b2f -->
<!-- last-edited: 2026-11-03 -->

# Raw findings 13: frontend (web/)

Anchors are repo-relative `path:LINE`, taken at commit 748fd787a.
All counts are from `find`/`wc -l` over `web/src` on that tree.

## 1. Stack and pinned versions (`web/package.json`)

| Item | Value | Anchor |
|---|---|---|
| Framework | React ^19.3.1 + react-dom ^19.3.1 | web/package.json:32-33 |
| UI kit | @mui/material ^9.3.1, @mui/icons-material ^9.5.0, @emotion/react ^11.11.1, @emotion/styled ^11.11.0 | web/package.json:27-31 |
| Router | react-router-dom ^7.18.5 (declarative `<Routes>`/`<Route>`, NOT data router) | web/package.json:34, web/src/main.tsx:9 |
| State | zustand ^5.0.16 (only state lib; no redux/react-query/swr) | web/package.json:35 |
| HTTP | native `fetch` through `apiFetch` wrapper. `axios` is in devDependencies only (unused by app code; not verified exhaustively) | web/package.json:56, web/src/utils/apiFetch.ts:1 |
| Build tool | Vite ^8.2.4 (rolldown-based), `@vitejs/plugin-react` ^6.0.5, React Compiler 1.0 via `@rolldown/plugin-babel` + `babel-plugin-react-compiler` | web/package.json:54,68, web/vite.config.ts:8-21 |
| Type checker | TypeScript 7 native (`@typescript/native`, npm alias of typescript ^7.0.2) is the gate; TS 6 kept as `typecheck:ts6` | web/package.json:8,10,53 |
| Unit test | vitest ^5.0.3, jsdom 30.0.1, Testing Library (react 16.3.3, user-event, jest-dom 7) | web/package.json:62,69 |
| E2E | @playwright/test ^1.64.1 | web/package.json:44 |
| Lint/format | eslint ^10.14, typescript-eslint, prettier ^3.9.10, knip 6.44.1 (dead-export report only) | web/package.json |
| Node pin | 26 (CI + Dockerfile `node:26-alpine`); no `.nvmrc`, no `engines` field | .github/workflows/ci.yml:130, Dockerfile:13 |
| Package version | `audiobook-organizer-web` 1.9.0, `private`, ESM | web/package.json:2-5 |

`npm run build` = native-tsc `--noEmit` typecheck, then `vite build` (web/package.json:8). A type error therefore fails `make web-build`, `make build`, the Docker build.

## 2. Build tool config

| Concern | Detail | Anchor |
|---|---|---|
| Dev server | port 5173; proxies only `/api` to the Go server on localhost:8484 | web/vite.config.ts:38-44 |
| Path alias | `@` -> `src` | web/vite.config.ts:27 |
| Dedupe | `react`, `react-dom` pinned (React error #130 history) | web/vite.config.ts:35 |
| Output | `web/dist`, sourcemaps on, chunk warn limit 600 kB | web/vite.config.ts:47-54 |
| Chunking | `codeSplitting.groups`: `mui` (all `@mui/`), `vendor` (react, react-dom, react-is, scheduler, react-router*) | web/vite.config.ts:79-85 |
| Vitest | jsdom, setup `src/test/setup.ts`, globals, 30 s test timeout, v8 coverage thresholds lines 30 / functions 20 / branches 20 / statements 25, `fsModuleCache` off under `CI` | web/vitest.config.ts:9-35 |
| Test setup | RTL cleanup, `asyncUtilTimeout` 10 s, sessionStorage cleared per test | web/src/test/setup.ts:19-30 |

## 3. Embedding `web/dist` into the Go binary

| Piece | Behavior | Anchor |
|---|---|---|
| Embed file | `//go:build embed_frontend`, `//go:embed web/dist`, exports `var WebFS embed.FS` in package main | web_embed.go:1,10-11 |
| Non-embed twin | `//go:build !embed_frontend`, `WebFS` is an empty `embed.FS` | web_nonembed.go:1,10 |
| Hand-off | `server.SetEmbeddedFS(WebFS)` | main.go:34 |
| Embed serving | `SetEmbeddedFS` stores the FS; `setupStaticFiles` serves the `web/dist` subtree as SPA fallback, running optional `spaMiddleware` first so registered routes (API, ABS surface) win | internal/server/static_embed.go:26-33 |
| Non-embed | `SetEmbeddedFS` is a no-op (API-only build serves a placeholder at `/`) | internal/server/static_nonembed.go:19-21, Makefile:159 |
| Make wiring | `make build` = `web-build` then `go build -tags "embed_frontend $(GO_BUILD_TAGS)"`; `make build-api` omits the tag | Makefile:153-163 |
| Release | goreleaser builds with `tags: [embed_frontend]` (frontend must be pre-built: `release-*` targets depend on `web-build`) | .goreleaser.yml (builds[0].tags), Makefile:843,850 |
| Docker | frontend-builder stage `npm ci && npm run build`, output copied to `./web/dist` before `go build -tags "embed_frontend ..."` | Dockerfile:13-21,67-75 |

Consequence for a shared core: the embed lives in package `main` (root), not in a library package. `internal/server` only receives an `embed.FS`, so the SPA can be swapped per app by passing a different FS.

## 4. Entry point, providers, gating (`web/src/main.tsx`, `App.tsx`)

Provider nesting (outer to inner): `ErrorBoundary` > `BrowserRouter` > MUI `ThemeProvider` (+`CssBaseline`) > `AuthProvider` > `ToastProvider` > `OperationTrackerProvider` > `App` (web/src/main.tsx:19-47).

`App` render gates, in order (web/src/App.tsx):
1. `/invite/*` renders `AcceptInvite` standalone, before auth/wizard gates (App.tsx:200-207).
2. Wait for `auth.initialized` and wizard-check (App.tsx:210).
3. `requiresLogin = auth.requiresAuth && !auth.isAuthenticated` -> only `/login` + catch-all redirect (App.tsx:214,247-251).
4. Else `MainLayout` + `Suspense` + route table (App.tsx:253-448); `WelcomeWizard` mounted when not requiresLogin (App.tsx:245).
5. Server-shutdown backdrop with reconnect counter (App.tsx:215-241), driven by SSE event `system.shutdown` (App.tsx:137-141) and polling `/api/v1/health` (App.tsx:165).
6. Side effects in App: opens operations SSE on login (App.tsx:120), keyboard shortcuts (App.tsx:72), review store bootstrap.

## 5. Route table (path -> component), `web/src/App.tsx`

All authenticated routes are wrapped individually in `ErrorBoundary`; non-Library pages are `React.lazy` (App.tsx:27-59). No route-level RBAC guard exists: access control is the server's job, the UI only hides controls (see section 9).

| Path | Component file | Anchor | Class |
|---|---|---|---|
| `/` | redirect -> `/dashboard` | App.tsx:256 | shell |
| `/dashboard` | web/src/pages/Dashboard.tsx | App.tsx:258 | mixed |
| `/library` | web/src/pages/Library.tsx | App.tsx:266 | audiobook |
| `/fingerprints` | web/src/pages/Library.tsx (same component, different filter) | App.tsx:274 | audiobook |
| `/library/:id` | web/src/pages/BookDetail.tsx | App.tsx:282 | audiobook |
| `/works` | web/src/pages/Works.tsx | App.tsx:290 | audiobook |
| `/system` | web/src/pages/System.tsx | App.tsx:298 | mixed |
| `/settings` | web/src/pages/Settings.tsx | App.tsx:306 | mixed |
| `/login` (authed) | redirect -> `/dashboard` | App.tsx:313 | shell |
| `/files` | web/src/pages/FileBrowser.tsx | App.tsx:315 | mixed |
| `/operations` | redirect -> `/activity` | App.tsx:322 | shell |
| `/maintenance` | redirect -> `/system` | App.tsx:323 | shell |
| `/authors/dedup`, `/books/dedup`, `/dedup`, `/dedup/labels` | web/src/pages/DedupRedirect.tsx (to `/review?tab=`) | App.tsx:326-336 | audiobook |
| `/series` | web/src/pages/Series.tsx | App.tsx:338 | audiobook |
| `/authors` | web/src/pages/Authors.tsx | App.tsx:346 | audiobook |
| `/authors/:id` | web/src/pages/AuthorDetail.tsx | App.tsx:357 | audiobook |
| `/diagnostics` | web/src/pages/Diagnostics.tsx | App.tsx:365 | mixed |
| `/activity` | web/src/pages/ActivityLog.tsx | App.tsx:373 | shell (mostly) |
| `/playlists`, `/playlists/:id` | web/src/pages/Playlists.tsx, PlaylistDetail.tsx | App.tsx:381,389 | audiobook |
| `/collections`, `/collections/:id` | web/src/pages/Collections.tsx, CollectionDetail.tsx | App.tsx:397,405 | audiobook |
| `/setup` | web/src/pages/Setup.tsx | App.tsx:413 | shell |
| `/users` | web/src/pages/Users.tsx (+ `UserMerge.tsx` dialog) | App.tsx:421 | shell |
| `/versions` | web/src/pages/TrashedVersions.tsx | App.tsx:429 | audiobook |
| `/review` | web/src/components/review/ReviewWorkspace.tsx | App.tsx:437 | audiobook |
| `ABS_REDIRECTS` rows (`/config`, `/config/api-keys`, `/audiobookshelf*`) -> `/settings[#api-keys]`, `/dashboard` | web/src/utils/absRedirects.ts:17-25 | App.tsx:444 | audiobook-compat |
| `*` | web/src/pages/NotFound.tsx | App.tsx:447 | shell |
| pre-auth: `/login` | web/src/pages/Login.tsx | App.tsx:249 | shell |
| public: `/invite/:token` | web/src/pages/AcceptInvite.tsx | App.tsx:204 | shell |

## 6. Navigation definition

Hard-coded arrays in `web/src/components/layout/Sidebar.tsx`, not data-driven and not role-filtered (a grep for role/permission in Sidebar returns nothing).

| Array | Entries | Anchor |
|---|---|---|
| `librarySubItems` (collapsible Library group) | All Books, In Progress, Finished (query-param `?search=read_status:*`), Fingerprints, Series, Authors | Sidebar.tsx:53-86 |
| `menuItems` | Dashboard, File Browser, Works, Playlists, Collections, Activity, Review, Diagnostics, System, Users, Settings | Sidebar.tsx:88-100 |
| Rendering | first item rendered above Library group, `menuItems.slice(1)` below; Review icon gets a live pending-count Badge from `useReviewStore` | Sidebar.tsx:112-124,286 |
| Selection logic | `sidebarSelection.ts` (matchPath + matchSearch) | web/src/components/layout/sidebarSelection.ts |
| Shell chrome | `MainLayout.tsx`, `TopBar.tsx` (search, theme toggle via `useColorScheme`, `OperationsIndicator`, `UserMenu`) | web/src/components/layout/ |

Generic-vs-specific: of 17 nav entries about 7 are shell (Dashboard-ish, Activity, Diagnostics, System, Users, Settings, File Browser), the rest audiobook domain.

## 7. Theme

MUI 9 `createTheme` with CSS variables: `cssVariables: { colorSchemeSelector: 'data-mui-color-scheme' }`, `colorSchemes` light+dark (web/src/theme.ts:158-161). Theme is a module constant `appTheme` (main.tsx:11,29-33), default mode dark, mode persisted under `STORAGE_KEYS.APP_THEME_MODE`; an inline script in `web/index.html` applies it pre-mount. Contrast tests: web/src/theme.contrast.test.ts, theme.slot-merge.test.ts. Theme has an audiobook-specific piece: a categorical palette for dedup evidence signals (theme.ts:24). Palettes exported at theme.ts:152. Brand colors are not parameterized: an app-specific theme would replace or wrap `theme.ts`.

## 8. API client

| Aspect | Finding | Anchor |
|---|---|---|
| Style | Hand-written, single 8,747-line module with 275 exported functions and 264 exported interfaces/types, `API_BASE = '/api/v1'` | web/src/services/api.ts:24 |
| Siblings | activityApi.ts, collectionApi.ts, fileOpsApi.ts, playlistApi.ts, readingApi.ts, versionApi.ts (hand-written, 8 service files total, 9,811 LOC) | web/src/services/ |
| Transport | `apiFetch` wrapper: `ApiTimeoutError` (own deadline), `ApiAuthRedirectError` (detects SSO login-page bounce disguised as 200: redirected + cross-origin and a content signal) | web/src/utils/apiFetch.ts:15-70 |
| Credentials | cookie session, `credentials: 'include'` on auth and list calls | web/src/services/api.ts:3355,3607,3648,1369 |
| Error model | `buildApiError(response, fallback)` | web/src/services/api.ts:116 |
| Generated types (1): tygo | `tygo.yaml` maps Go wire-shape files to TS: `internal/repairs/wire.go` -> web/src/types/generated/repairs.ts; `internal/database/{bookcore,bookfilecore,scan_state,wire_types}.go` -> web/src/types/generated/database.ts. `time.Time` -> `string`, `json.RawMessage` -> `unknown`. `make generate-types` regenerates; `make types-check` (scripts/ci/check_generated_types.sh) fails CI if stale | tygo.yaml:12-40, Makefile:560-571 |
| Generated types (2): opsgen | `tools/cmd/opsgen` emits web/src/generated/ops.ts from `internal/operations/state/state.go` (v2 statuses) and `v3.go` (v3 run states); `make generate-ops`; `make ci` runs `ops-check` | web/src/generated/ops.ts:1-9, Makefile:548-555 |
| Contract goldens | Go side `internal/wirecontract/contract_test.go` marshals a fully populated sample to JSON goldens in `web/src/types/contract/testdata/` (13 files); `contract.test.ts` assigns them to the generated TS types so a drift fails typecheck/vitest | web/src/types/contract/contract.test.ts:1-40 |
| Coverage of generation | Only a handful of wire structs are generated (BookCore, BookFileCore, ScanState, StageStatus, repairs rows, run statuses). The other ~260 interfaces in api.ts are hand-mirrored from Go JSON, so the generated set is a small seed, not the API surface | web/src/services/api.ts, tygo.yaml |
| No OpenAPI/Swagger | none found in web/; the wire contract is Go structs -> tygo and manual interfaces | UNKNOWN beyond web/ (not searched server-side) |

## 9. Auth handling in the UI

| Piece | Behavior | Anchor |
|---|---|---|
| Context | `AuthProvider` exposes `initialized`, `authEnabled`, `requiresAuth`, `isAuthenticated`, `user`, login/logout/setupAdmin | web/src/contexts/AuthContext.tsx:19-45,144-157 |
| Bootstrap | retries `GET /api/v1/auth/status`, then `GET /api/v1/auth/me` | AuthContext.tsx:41-90; api.ts:3353,3605 |
| Login/logout/setup | `POST /auth/login` (username, password, remember_me), `POST /auth/logout`, `POST /auth/setup` (first admin) | api.ts:3383,3645,3364 |
| Shape | `AuthUser { id, username, email, roles[], status, created_at, permissions[] }`; `normalizeAuthUser` fails closed to `[]` | api.ts:1106-1124 |
| Permission checks | advisory only: `useLibraryPermission` (`library.delete`, `library.organize`) fail-closed except auth-off or first-run; Users page filters merge targets by `roles.includes('admin')` | web/src/hooks/useLibraryPermission.ts:4-30, web/src/pages/Users.tsx:519 |
| First run | `/setup` page + welcome wizard (web/src/components/wizard/WelcomeWizard.tsx, 989 LOC) | App.tsx:413,245 |
| Invites | public `/invite/:token` (AcceptInvite), Users page invite list with `role_id` | Users.tsx:80,415,669 |
| Other | API keys tab, temp-login tab, guest phone keys (settings) | web/src/components/settings/APIKeysTab.tsx, TempLoginTab.tsx |

RBAC in UI = role names + a flat `permissions: string[]`; permission constants are audiobook-flavoured (`library.*`, `collections.manage`).

## 10. Realtime client

| Channel | URL | Consumer | Anchor |
|---|---|---|---|
| Generic event stream | `/api/events` (default arg), auto-reconnect with backoff, gives up after max attempts, status listeners | `eventSourceManager` singleton; used by App (system.shutdown) and TopBar | web/src/services/eventSourceManager.ts:31,185; App.tsx:137 |
| Operations stream | `/api/v1/operations/events`, event names `op.created`, `op.updated`, `op.log`, `op.terminal` | `openOperationsSSE` in api.ts, consumed by `useOperationsStore.openSSE` | api.ts:733-740; web/src/stores/useOperationsStore.ts:407-490 |
| Polling | `GET /operations/timeline`, `GET /operations/v2/:id`, `utils/operationPolling.ts`, `followOperation.ts` | stores/hooks | api.ts:680,702 |

Transport is SSE (native `EventSource`) only; no WebSocket. The operations store handles the "browser gave up" case by re-opening (useOperationsStore.ts:475-490).

## 11. State (zustand stores + context)

| Store | Purpose | Class | LOC | Anchor |
|---|---|---|---|---|
| useOperationsStore | operation list, SSE, log tails, grouping, undo | shell (jobs/tasks) | 511 (+ operationGrouping.ts 327) | web/src/stores/useOperationsStore.ts:233 |
| useAppStore | global loading, toast notifications (cap + 5 s auto-remove), error | shell | 95 | web/src/stores/useAppStore.ts:49 |
| useLibraryCache | cached library page data | audiobook | 101 | web/src/stores/useLibraryCache.ts:59 |
| useReviewStore | pending-review count/queue for Review badge | audiobook | 135 | web/src/stores/useReviewStore.ts:63 |
| AuthContext | session/user | shell | 188 | web/src/contexts/AuthContext.tsx |
| ToastProvider | toast UI | shell | 84 | web/src/components/toast/ToastProvider.tsx |
| OperationTrackerProvider | follows merge/combine/purge ops app-wide | mixed (operation kinds are audiobook) | ~824 w/ panel | web/src/components/operations/OperationTrackerProvider.tsx |
| localStorage keys | `STORAGE_KEYS` registry | shell | 31 | web/src/lib/storageKeys.ts:7,31 |

## 12. Classification of pages/components

Legend: S = generic shell, A = audiobook-specific, M = mixed. LOC = non-test lines.

### Pages (web/src/pages, 24 non-helper TSX, 18,686 LOC incl. helper .ts)

| File | LOC | Class | Why |
|---|---|---|---|
| Login.tsx | 273 | S | auth |
| AcceptInvite.tsx | 337 | S | invite flow |
| Setup.tsx | 176 | S | first-admin setup |
| Users.tsx / UserMerge.tsx | 838 / 569 | S | users, roles, invites, account merge (usersInvites.ts helper) |
| NotFound.tsx | 36 | S | |
| ActivityLog.tsx | 3,320 | S/M | operations list, logs, cancel/retry/discard, undo; some audiobook op kinds and tag colors |
| System.tsx | 89 | M | tabs: System Info, Storage, Quota, Maintenance (components/system) |
| Settings.tsx | 1,414 | M | tabs Library, iTunes Import, Metadata, Dedup, Paths, Performance, Security, API Keys, Plugins, Tools, System Info, Temp Login (Settings.tsx:811-822); Security/API Keys/Plugins/Tools/System Info/Temp Login are generic, rest audiobook |
| Diagnostics.tsx | 1,044 | M | DB health stats, AI results, op status; generic parts plus AI-dedup diagnostics |
| Dashboard.tsx | 882 | M | system status/storage generic; counts of authors/series/books and scan trigger audiobook |
| FileBrowser.tsx | 78 | M | server file browser + `addImportPath` (import folders) |
| Library.tsx | 2,808 | A | |
| BookDetail.tsx | 1,392 | A | |
| Authors.tsx / AuthorDetail.tsx | 1,351 / 222 | A | |
| Series.tsx | 1,273 | A | |
| Works.tsx | 223 | A | |
| Collections.tsx / CollectionDetail.tsx | 387 / 507 | A | |
| Playlists.tsx / PlaylistDetail.tsx | 267 / 263 | A | |
| TrashedVersions.tsx | 551 | A | |
| DedupRedirect.tsx | 66 | A | |

### Components (non-test src files / LOC)

| Dir | Files | LOC | Class |
|---|---|---|---|
| components/review | 69 | 30,480 | A (dedup/metadata/regroup/repairs review workspace; `repairs` lane is generated-type backed) |
| components/audiobooks | 23 | 7,547 | A |
| components/settings | 19 | 6,132 | M (AIBackends, Plugins, APIKeys, TempLogin, ScheduledTasks, Maintenance, Performance = S; Deluge, ITunes*, OpenLibraryDumps, Metadata*, Dedup*, BlockedHashes, Embedding = A) |
| components/library | 8 | 4,063 | A |
| components/bookdetail | 11 | 3,972 | A |
| components/system | 5 | 3,351 | M (Storage, Quota, SystemInfo, Maintenance, ThrottlePanel; ChapterConsolidationCard/ManualFixesCard are A) |
| components/layout | 7 | 1,887 | S (Sidebar nav list is the A part; OperationsIndicator S) |
| components/common | 7 | 1,381 | S (ConfigurableTable, ServerFileBrowser, PathLinks, BulkConfirmDialog, FilterTagBar) |
| components/wizard | 1 | 989 | M (first-run wizard; steps audiobook-flavoured) |
| components/operations | 2 | 824 | S/M (tracker provider, trash-cleanup panel) |
| components/collections | 4 | 587 | A |
| components/versions, tools, activity, toast | 1,1,2,1 | 259,115,91,84 | A / M / S / S |
| components (top-level) | 31 files incl. ErrorBoundary, KeyboardShortcutsDialog, AnnouncementBanner, AIJobsPanel, OperationActivityPanel, CacheStatsPanel, ChangeLog | 4,780 | mixed: ErrorBoundary/Announcement/Shortcuts/OperationActivityPanel/AIJobsPanel/CacheStats S; BookGrid, TagComparison, MetadataHistory, CoverLightbox, FilterPanel, ReviewBanner, AudioSampleCompare, PendingFileOpsBanner A |

### Non-component code

| Dir | Files | LOC | Class |
|---|---|---|---|
| services | 8 | 9,811 | api.ts mixed (auth, operations, system, settings = S; audiobooks, authors, series, versions, dedup, metadata, iTunes = A); activityApi/eventSourceManager S; collectionApi/playlistApi/readingApi/versionApi/fileOpsApi A |
| hooks | 22 | 4,587 | mostly A (library*, collections, metadata, columns); S: useAsyncAction, useKeyboardShortcuts, useUnsavedChangesBlocker, useDebouncedSearch, useRowSelection |
| utils | 30 | 4,265 | mixed; S: apiFetch, apiResponse, operationPolling, followOperation, trackedOperations, safeReturn, settingLocks, apiKeyExpiry, permissionError; A: titleSortKey, coverUrl, mediaFormat, namingPatternPreview, tag*, queryGrammar, searchParser, absRedirects, pathAliases |
| stores | 5 | 1,169 | see section 11 |
| lib / config / contexts | 3 / 2 / 1 | 340 / 942 / 188 | lib A (reviewKinds, reviewPayload) + S (storageKeys); config A (columnDefinitions, metadataApplyFields); contexts S |
| types / generated | 3 / 1 | 1,149 / 373 | A except `ops.ts` (S: operation statuses) |

Totals: 297 non-test TS/TSX files, 109,489 LOC non-test; 256 test files, 60,367 LOC of tests (web/src). Largest files: api.ts 8,747; ActivityLog.tsx 3,320; Library.tsx 2,808.

Rough split by LOC (non-test): audiobook-specific (review, audiobooks, library, bookdetail, collections, versions, 13 A pages) is about 70-75%; generic shell (auth, users, nav chrome, toast, operations store/tracker, activity, apiFetch, event source, common table/browser) about 10-15%; mixed (Settings, System, Dashboard, Diagnostics, api.ts, wizard) the remainder. This is a judgment estimate from the per-dir table above, not a measured number.

## 13. Tests

| Surface | Setup | Count / anchor |
|---|---|---|
| Vitest | `make web-test` -> `npm run test -- --run` | Makefile:214-218; 256 `*.test.ts(x)` in web/src; plus web/tests/unit (2 files) |
| Contract | Go-marshalled goldens vs generated TS | web/src/types/contract/contract.test.ts |
| Playwright e2e | `web/tests/e2e/playwright.config.ts`; projects `chromium`, `webkit`, `benchmark`, `chromium-record`; `webServer` builds frontend, builds Go with `-tags embed_frontend`, boots `serve` on a temp Pebble DB at 127.0.0.1 | web/tests/e2e/playwright.config.ts:69-145; 39 `*.spec.ts` files, 58 files under web/tests |
| Spec discovery guard | `check-spec-discovery.mjs` runs before e2e | web/package.json:14 |
| Smoke / visual | `tests/smoke/routes.mjs`, `tests/visual/compare-layout.mjs` | web/package.json:19-20 |
| Lint-ish | `make web-lint`, `make web-lint-memory` (python leak scan), knip report job | Makefile:219-228, .github/workflows/ci.yml:358 |
| Not in `make ci` | `make ci` runs only vitest for the frontend (via `test-all-short`), not `web-lint`, e2e, or a standalone typecheck; typecheck only happens inside `npm run build` and `web-typecheck` | Makefile:634,785 |

## 14. Reuse-relevant observations (evidence only)

1. The shell is coupled to the audiobook app at three seams: the hard-coded route table (App.tsx:253-448), the hard-coded nav arrays (Sidebar.tsx:53-100), and the single-file API client (api.ts, 8.7k lines mixing both domains). There is no plugin/registry for pages or nav items.
2. The generic shell pieces that are already self-contained and portable: `AuthContext`, `apiFetch` (+ SSO bounce detection), `eventSourceManager`, `useOperationsStore` + `openOperationsSSE`, `ToastProvider`, `ErrorBoundary`, `storageKeys`, theme scaffolding (CSS-variable MUI), `ConfigurableTable`, `ServerFileBrowser`, Users/Login/AcceptInvite/Setup pages, ActivityLog (operations UI).
3. Wire types reach TS through three channels (tygo for 2 Go packages, opsgen for operation states, hand-written interfaces for the rest); all CI-checked except the hand-written part, which is only protected by the contract goldens for the generated subset.
4. Server contract the shell depends on (from client code): `/api/v1/auth/{status,setup,login,logout,me}`, `/api/v1/health`, `/api/events`, `/api/v1/operations/{timeline,v2/:id,events}`, `/api/v1/system/*`.
5. Embedding is done in package main by build tag, with `internal/server` accepting an `embed.FS`; a second app would supply its own `web/dist` FS the same way.
