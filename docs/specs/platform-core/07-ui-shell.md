<!-- file: docs/specs/platform-core/07-ui-shell.md -->
<!-- version: 1.0.0 -->
<!-- guid: 651865f1-0fad-4e15-8169-ceaf4a573f13 -->
<!-- last-edited: 2026-11-04 -->

# 07 — UI shell: `@mediacore/*` npm packages and the Go `ui` package

Evidence commit: `748fd787a`. Raw data: [`raw/13-frontend.md`](../../plans/platform-core/raw/13-frontend.md),
[`raw/14-build-ci.md`](../../plans/platform-core/raw/14-build-ci.md) §3, §5,
[`raw/15-dependencies.md`](../../plans/platform-core/raw/15-dependencies.md) (npm),
[`raw/09-http-api.md`](../../plans/platform-core/raw/09-http-api.md) §5–§6,
[`raw/08-prior-art.md`](../../plans/platform-core/raw/08-prior-art.md) §2 row 6.
Decisions: D6, D8, D9; old decision 8 (02 §2.2); gate G3. Server-side module data comes from
[06-app-sdk](06-app-sdk.md) (`Manifest.UI`).

## 7.1 Purpose & scope

One React shell per binary (N6) that every app reuses: auth and session, layout and merged
navigation, settings frame, tasks/jobs UI, users/roles, notifications, health/diagnostics, theming,
realtime and the API runtime. Modules contribute pages, nav entries, settings panels and a few
optional pieces **at build time**; the server reports which modules are enabled; the shell renders
the intersection and refuses unknown or version-incompatible descriptors (G3). No remote JS
(old decision 8). The Go side (`core/ui`) serves an app's embedded build with core headers.

Out of scope: visual redesign of ABO pages; the native API itself (05-http-api); realtime server
semantics (05-realtime).

## 7.2 Current state at HEAD

| Aspect | Today | Anchor |
|---|---|---|
| Entry and providers | `ErrorBoundary > BrowserRouter > ThemeProvider > AuthProvider > ToastProvider > OperationTrackerProvider > App` | `web/src/main.tsx:19-47` |
| Routes | hard-coded `<Routes>` table, 21 `React.lazy` pages, no route-level permission guard | `web/src/App.tsx:27-59,253-448` |
| Navigation | two hard-coded arrays, not data-driven or permission-filtered | `web/src/components/layout/Sidebar.tsx:53-86,88-100` |
| Router | `react-router-dom` 7, declarative (not data router); a custom hook re-implements `useBlocker` because there is no data router | `web/package.json:34`, `web/src/hooks/useUnsavedChangesBlocker.ts:15`; 52 non-test files import `react-router-dom` (grep) |
| API client | hand-written `services/api.ts` (8,747 lines), `API_BASE = '/api/v1'` | `web/src/services/api.ts:24` |
| Transport | `apiFetch` with own timeout and SSO-bounce detection (`ApiAuthRedirectError`) | `web/src/utils/apiFetch.ts:16,51` |
| Realtime | `eventSourceManager` singleton, backoff 3 s → 60 s; separate operations `EventSource` | `web/src/services/eventSourceManager.ts:25-29,119`; `api.ts:733-740` |
| Auth | `AuthProvider` (status → me), permissions advisory | `web/src/contexts/AuthContext.tsx:19-45`; raw/13 §9 |
| Theme | MUI 9 CSS variables, `colorSchemeSelector: 'data-mui-color-scheme'`, default dark, audiobook-specific signal palette inside the theme | `web/src/theme.ts:154-165`, `:24-40` |
| Build | Vite 8 (rolldown), React Compiler via `@rolldown/plugin-babel` (passing it through `react()` is a silent no-op), `dedupe: ['react','react-dom']` (React #130 history), chunk groups `mui`/`vendor` | `web/vite.config.ts:13-21,35,79-85` |
| TypeScript | TS 7 native is the typecheck gate; the `typescript` name is aliased to TS 6 because `typescript-eslint` needs the TS JS API "which TS 7 does not ship" | `web/package.json:8,53,66`; `CHANGELOG.md:2917-2923` |
| Embedding | `//go:build embed_frontend` + `//go:embed web/dist` in package main → `server.SetEmbeddedFS` | `web_embed.go:1,10-11`, `main.go:34`, `internal/server/static_embed.go:26-33` |
| CSP | inline scripts of `index.html` are hashed into `script-src` at startup | `internal/server/static_embed.go:50`, `internal/server/csp.go:95-105,164` |
| Classification | ~70–75 % of non-test LOC audiobook-specific, ~10–15 % generic shell, rest mixed (judgment) | raw/13 §12 |

## 7.3 Packages, workspaces and versions

### 7.3.1 Layout

```
core/web/                       npm workspace root (own package-lock.json; the post-split repo root)
  package.json                  { "private": true, "workspaces": ["packages/*", "examples/*"] }
  packages/shell/               @mediacore/shell     (React shell + createShell + /vite preset + /testing)
  packages/api-client/          @mediacore/api-client (fetch runtime + generated core client)
  packages/ui-kit/              @mediacore/ui-kit     (theme factory + presentational components)
  e2e/                          Playwright suite against the hello example binary
core/examples/hello/web/        @mediacore-examples/hello (workspace member via "examples/*" symlink)
```

Every package builds to `dist/` (JS by Vite library mode, `.d.ts` by TS 7 `tsc --emitDeclarationOnly`)
and its `exports` point at `dist/`, **in-repo and published alike**: what ABO consumes in the
monorepo is byte-for-byte what a split app will download, so the split (12) changes a dependency
specifier, not behaviour. React, react-dom, react-router, `@mui/material`, `@emotion/react` and
`@emotion/styled` are **peerDependencies** of shell and ui-kit (context singletons).

### 7.3.2 How an app consumes them

* **In-repo phase (D3):** ABO's `web/package.json` declares
  `"@mediacore/shell": "file:../core/web/packages/shell"` (same for `api-client`, `ui-kit`). npm
  symlinks them. Because the symlink target resolves `react` from `core/web/node_modules`, a second
  React instance is possible; the `@mediacore/shell/vite` preset (§7.10) therefore extends
  `resolve.dedupe` to every peer above, and `core/web/scripts/check-singletons.mjs` (the exact-file
  sourcemap check described in `web/vite.config.ts:66-75`, promoted to a script) fails the build if
  any peer's files appear from two paths.
* `make web-build` depends on a new `core-web-build` target (`npm ci && npm run build --workspaces`
  in `core/web`). The Dockerfile frontend stage copies `core/web/` as well as `web/` and builds core
  first (`Dockerfile:13-21` today copies only `web/`).
* **After the split:** the specifier becomes a release tarball URL (§7.17); nothing else changes.

### 7.3.3 Versions (D8), checked with `npm view <pkg> version` on 2026-11-04

| Package | Latest stable | ABO today (raw/15) | Shell policy |
|---|---|---|---|
| `react`, `react-dom` | 19.3.1 | 19.3.1 | peer `^19.3.1` |
| `@mui/material` | 9.5.0 | 9.5.0 (range `^9.3.1`) | peer `^9.5.0` |
| `vite` | 8.3.5 | 8.3.4 | `^8.3.5` (dev) |
| `typescript` | 7.0.2 | 7.0.2 via `@typescript/native` alias | typecheck + declaration emit with 7.0.2 |
| `vitest` | 5.1.0 | 5.0.5 | `^5.1.0` |
| `@playwright/test` | 1.64.1 | 1.64.1 | `^1.64.1` |
| `react-router` | **8.5.1** (`latest`; `react-router-dom` stops at 7.18.5, tag `version-7`) | `react-router-dom` 7.18.5 | peer `react-router ^8.5.1` (data router); ABO migrates its 52 importing files |
| `zustand` | 5.0.16 | 5.0.16 | shell internal stores |
| `eslint` / `typescript-eslint` | 10.14.0 / 8.72.1 | same | see TS 7 matrix |
| `@vitejs/plugin-react` | 6.2.1 | 6.1.0 | `^6.2.1` |

TypeScript 7 support matrix (from `npm view <pkg> peerDependencies` the same day):

| Tool | Needs the TS JS API? | TS 7 status | Decision |
|---|---|---|---|
| `tsc` typecheck | — | native | **gate** for every package and app |
| `.d.ts` emit for packages | — | native `tsc --emitDeclarationOnly` | use TS 7; if a declaration-emit gap is found, fall back to `tsc6` for emit only and record it in [13-toolchain](13-toolchain.md) |
| `vite` 8.3.5, `vitest` 5.1.0, `@vitejs/plugin-react` | no (oxc/rolldown strip types); no `typescript` peer | unaffected | no `vitest --typecheck` (it would need a TS API) |
| `knip` 6.44.1 | no (depends on `oxc-parser`) | unaffected | — |
| `typescript-eslint` 8.72.1 | yes; peer `typescript >=4.8.4 <6.1.0` | **unsupported** | **exception**: keep `"typescript": "npm:@typescript/typescript6@^6.0.2"` (6.0.2 latest of that wrapper) for lint only, as ABO does today; revisit when typescript-eslint widens its peer |
| `openapi-typescript` 7.13.0 (chosen by 05-http-api §7) | yes; peer `typescript ^5.x` | unsupported; TS 6 alias also outside the peer range | npm `overrides` + verify; Go emitter fallback (§7.8, O4) |
| `typescript@7.0.2` itself | exports only `./lib/version.cjs` and `./unstable/*` | no stable programmatic API | nothing in the toolchain may import it programmatically |

Node: Vite requires `^20.19.0 || >=22.12.0`; `react-router` 8 requires `>=22.22.0`; the repo pins
Node 26 everywhere (raw/14 §5), which satisfies both. Each package gets `"engines": {"node": ">=22.22.0"}`.

## 7.4 Extension API (TypeScript)

### 7.4.1 Generated module manifest

The Go manifest is the single source for IDs and versions. `app.Describe` (06 §6.4.5) emits each
module's UI manifest; the app's `openapi` subcommand writes it as `manifest.gen.ts` into the module's
web package (§7.8):

```ts
// generated — do not edit
export const manifest = {
  id: 'hello',
  version: '1.0.0',
  displayName: 'Hello',
  pages: ['greeting'],
  nav: ['hello'],
  settingsPanels: ['hello'],
  widgets: [],
  permissions: ['hello:read', 'hello:admin'],
} as const;
```

A freshness check in CI regenerates and diffs it, so a client contribution can never name an ID the
server does not declare without failing typecheck (§7.4.2 types are keyed by these literals).

### 7.4.2 `@mediacore/shell` types and entry points

```ts
import type { ComponentType, ReactNode } from 'react';
import type { SvgIconComponent } from '@mui/icons-material';

export type Permission = `${string}:${string}`;

export interface ModuleManifest {
  readonly id: string;
  readonly version: string;
  readonly displayName: string;
  readonly pages: readonly string[];
  readonly nav: readonly string[];
  readonly settingsPanels: readonly string[];
  readonly widgets: readonly string[];
  readonly permissions: readonly Permission[];
}

type Lazy = () => Promise<{ default: ComponentType }>;
type PageId<M extends ModuleManifest> = M['pages'][number];
type Perm<M extends ModuleManifest> = M['permissions'][number] | `core:${string}`;

export interface RouteContribution<M extends ModuleManifest> {
  id: PageId<M>;
  path: string;            // relative to /<module id>/, e.g. 'library/:id'
  lazy: Lazy;              // code-split; wrapped in Suspense + ErrorBoundary by the shell
  permission?: Perm<M>;    // advisory guard: renders "no access" instead of the page
  title?: string;
}

export interface NavContribution<M extends ModuleManifest> {
  id: M['nav'][number];
  label: string;
  icon?: SvgIconComponent;
  to: PageId<M> | { page: PageId<M>; search?: string };
  permission?: Perm<M>;    // hidden when the user lacks it
  order?: number;
  badge?: () => number | undefined; // a hook; e.g. books' pending-review count
  children?: readonly NavContribution<M>[];
}

export interface SettingsPanelContribution<M extends ModuleManifest> {
  id: M['settingsPanels'][number];
  label: string;
  lazy: Lazy;
  permission?: Perm<M>;
  order?: number;
}

export interface PermissionsHints<M extends ModuleManifest> {
  view: readonly Perm<M>[];                 // any-of: needed to show the module at all
  actions?: Readonly<Record<string, Perm<M>>>; // named UI actions -> permission, for useAction()
}

export interface WidgetContribution<M extends ModuleManifest> {
  id: M['widgets'][number];
  placement: 'home' | 'system' | 'diagnostics';
  lazy: Lazy;
  permission?: Perm<M>;
  order?: number;
}

export interface TaskKindPresentation {
  label: string;
  color?: 'default' | 'primary' | 'secondary' | 'info' | 'success' | 'warning' | 'error';
  resultView?: Lazy; // renders a finished run's result in the Tasks page
}

export interface ModuleUIInput<M extends ModuleManifest> {
  routes: readonly RouteContribution<M>[];
  nav: readonly NavContribution<M>[];
  settingsPanels: readonly SettingsPanelContribution<M>[];
  permissionsHints: PermissionsHints<M>;
  // optional
  widgets?: readonly WidgetContribution<M>[];
  taskKinds?: Readonly<Record<`${M['id']}.${string}`, TaskKindPresentation>>;
  setupSteps?: readonly { id: string; title: string; lazy: Lazy }[]; // first-run wizard
  redirects?: readonly { from: string; to: string }[];               // legacy URLs (09)
  providers?: readonly ComponentType<{ children: ReactNode }>[];     // module-wide contexts
}

export interface ModuleUI<M extends ModuleManifest = ModuleManifest> extends ModuleUIInput<M> {
  readonly id: M['id'];
  readonly version: M['version'];
  readonly manifest: M;
}

export function defineModule<const M extends ModuleManifest>(manifest: M, ui: ModuleUIInput<M>): ModuleUI<M>;

export interface ShellConfig {
  app: { id: string; displayName: string; logo?: ComponentType };
  modules: readonly ModuleUI[];
  theme?: import('@mediacore/ui-kit').MediaThemeInput;
  basePath?: string; // default '/', must equal the server's core.ui.base_path
}

export interface Shell {
  mount(target: string | Element): void;
  unmount(): void;
}

export function createShell(config: ShellConfig): Shell;

// Hooks available to module code
export function useAuth(): AuthState;
export function usePermission(p: Permission): boolean;
export function useAction(moduleId: string, action: string): boolean;
export function useTopic<T>(topic: string, onEvent: (data: T) => void): void;
export function useResync(onResync: () => void): void;
export function useToast(): ToastApi;
export function useTask(runId: string | undefined): TaskRunView | undefined;
export function useApi(): import('@mediacore/api-client').Client;
export function storageKey(scope: string, name: string): string; // `${appId}.${scope}.${name}`
```

`createShell` renders, in order: `ErrorBoundary` → MUI `ThemeProvider` (CSS variables) → api-client
`ClientProvider` → `AuthProvider` → `RealtimeProvider` → `ToastProvider` → `TaskTrackerProvider` →
module `providers` → `RouterProvider` (react-router 8 data router built from shell routes plus
module routes under `/<id>/…`). The reserved shell routes are `/`, `/login`, `/invite/:token`,
`/setup`, `/settings/*`, `/system/*`, `/users`, `*`.

## 7.5 Server manifest and intersection (G3)

### 7.5.1 `GET /api/core/v1/modules`

Permission `rbac.AnyAuthenticated`. Response (camelCase, types generated into `@mediacore/api-client`):

```json
{
  "core": { "version": "0.4.0" },
  "app": { "id": "abo", "displayName": "Audiobook Organizer", "version": "2.0.0", "uiBuildId": "b3f1c2…" },
  "restartRequired": false,
  "modules": [
    { "id": "books", "version": "1.12.0", "displayName": "Audiobooks",
      "ui": { "pages": ["library", "book"], "nav": ["library"], "settingsPanels": ["library"], "widgets": ["counts"] } }
  ],
  "disabledModules": ["hello"]
}
```

`disabledModules` is present only for callers holding `core:admin`. A disabled module is absent from
`modules` (G1: no nav entry).

### 7.5.2 Algorithm (runs once after login and on every reconnect `resync`)

For each module `S` in the server list and the compile-time entry `C = registry[S.id]`:

| Case | Shell behaviour | Diagnostic kind |
|---|---|---|
| no `C` and `S.ui` empty | nothing to render | — |
| no `C` and `S.ui` non-empty | module UI not in this bundle | `ui-missing` |
| `major(C.version) != major(S.version)` or `minor(C.version) > minor(S.version)` | module not mounted (client would call APIs the server lacks, or a major break) | `version-incompatible` |
| item in `C.routes/nav/settingsPanels/widgets` whose ID ∉ `S.ui` | that item dropped | `undeclared-descriptor` |
| `C` with no matching `S` | not mounted (disabled or unknown module) | `module-not-enabled` (admin only) |
| `S.ui` ID with no client item | ignored | `descriptor-without-ui` (debug log) |

Additionally `app.uiBuildId` ≠ the bundle's compile-time build ID (stale cached SPA after an upgrade)
shows a non-dismissable "new version — reload" banner; module mounting proceeds with the rules above.
Diagnostics are shown to admins under **System → UI diagnostics**, logged with `console.warn`, and
reported once per kind per page load to `POST /api/core/v1/ui/diagnostics` (AnyAuthenticated,
rate-limited, body ≤ 4 KiB) so they appear in server logs and metrics.

The version rule is deliberately strict on minor: an app ships server and SPA from one commit, so
skew only arises from caching or a mixed deployment, and in both cases hiding a page beats calling a
missing endpoint.

## 7.6 Go side: package `ui`

```go
package ui

// Bundle is an app's built SPA.
type Bundle struct {
	fsys    fs.FS  // rooted at the dist directory
	buildID string // from <meta name="mediacore-build" content="..."> in index.html
	index   []byte
	scripts []string // CSP hash sources of inline <script> blocks in index.html
}

// FromFS roots fsys at dir, reads index.html, extracts the build ID and the
// sha256 hashes of inline scripts. It fails if index.html or the meta tag is
// missing.
func FromFS(fsys fs.FS, dir string) (Bundle, error)

// MustFromFS panics on error; for main packages with an embedded FS that
// was validated at build time.
func MustFromFS(fsys fs.FS, dir string) Bundle

func (b Bundle) IsZero() bool
func (b Bundle) BuildID() string

type Options struct {
	BasePath      string   // core.ui.base_path
	ConnectSrc    []string // extra CSP connect-src hosts (validated as bare DNS names, as csp.go does today)
	ReportOnly    bool     // core.ui.csp.report_only
	Metrics       metrics.Scope
}

// Handler serves the SPA:
//   - hashed assets under /assets/: Cache-Control "public, max-age=31536000, immutable"
//   - index.html and every unmatched non-API GET: "no-store", with CSP
//     script-src 'self' + the inline-script hashes
//   - unmatched /api/... paths: 404 core JSON error envelope, never HTML
//   - zero Bundle: the API-only placeholder page (today's static_nonembed.go behaviour)
// The handler is goroutine-safe and holds no package-level state (R5).
func Handler(b Bundle, opts Options) http.Handler
```

The kernel mounts `ui.Handler(opts.UI, …)` as the lowest-priority route (`/` catch-all), so API,
legacy and facade routes always win, matching `setupStaticFiles`' "registered routes never see it"
(`internal/server/static_embed.go:29-31`). The CSP state that is today a package global initialised in
`init()` (`internal/server/csp.go:83-87`) becomes a field of the handler instance.

**`embed_frontend` semantics are kept unchanged** (raw/14 §3): each app's `main` package has
`ui_embed.go` (`//go:build embed_frontend`, `//go:embed` of its dist directory) and `ui_nonembed.go`
(`//go:build !embed_frontend`, zero `ui.Bundle`). `make build`, goreleaser, Docker and e2e set the
tag; `make build-api` and `go test ./...` do not. For ABO: `web_embed.go` stays as is and
`main.go:34` changes from `server.SetEmbeddedFS(WebFS)` to `app.Options{UI: ui.MustFromFS(WebFS, "web/dist")}`
(with a zero bundle when `WebFS` is the empty non-embed FS).

The build ID is written by the `@mediacore/shell/vite` preset into `index.html` as
`<meta name="mediacore-build" content="<hash of the emitted entry chunk names>">` and into the bundle
as `import.meta.env.MEDIACORE_BUILD_ID`, so server and client agree without extra files.

## 7.7 Theming

```ts
// @mediacore/ui-kit
export interface MediaThemeInput {
  brand?: { primary?: string; secondary?: string };
  defaultMode?: 'light' | 'dark';           // default 'dark' (ABO today)
  extend?: import('@mui/material').ThemeOptions; // app/module tokens via MUI module augmentation
}
export function createMediaTheme(input?: MediaThemeInput): import('@mui/material').Theme;
export const MODE_ATTRIBUTE = 'data-mui-color-scheme';
export function themeBootScript(storageKey: string): string; // inline pre-mount script body
```

* MUI 9 `createTheme({ cssVariables: { colorSchemeSelector: 'data-mui-color-scheme' }, colorSchemes: { light, dark } })`,
  as `web/src/theme.ts:154-165` does today; mode toggling flips one attribute, no re-render.
* The pre-mount boot script (no light-mode flash) is injected into `index.html` by the vite preset;
  the Go `ui` handler hashes it into CSP automatically (§7.6).
* Mode storage key: `storageKey('shell', 'themeMode')`; ABO passes its existing key so saved
  preferences carry over (the same trick `main.tsx:28-34` already uses).
* Module-specific tokens stay in the module: ABO's dedup signal palette (`theme.ts:24-40`) moves to
  `web/src/modules/books/theme.ts` as an MUI augmentation passed through `extend`.
* Contrast tests (`theme.contrast.test.ts`) move to ui-kit and run for both schemes and for every
  app's `extend` in that app's tests.

## 7.8 API client and code generation

The OpenAPI 3.1 documents and the TypeScript client pipeline are owned by
[05-http-api](05-http-api.md) §7: one document per owner emitted by the app binary's `openapi`
subcommand (which calls `app.Describe`, 06 §6.4.5), types by `openapi-typescript` 7.13.0, calls by
`openapi-fetch` 0.17.0. The shell consumes it as follows:

* `@mediacore/api-client` = generated `paths` for every `/api/core/v1` route + `createCoreClient()`
  whose middleware carries today's `apiFetch` semantics — own deadline (`ApiTimeoutError`), SSO
  login-page bounce detection (`ApiAuthRedirectError`, `web/src/utils/apiFetch.ts:51`),
  `credentials: 'include'`, `X-Request-ID` surfacing, RFC 9457 `Problem` as the error type
  (05-http-api §3.3). The shell creates one client in `ClientProvider`; `useApi()` returns it; a
  401 triggers `onUnauthorized` (§7.9).
* Each module ships its own generated client package (`@abo/books-api-client`, 05-http-api §7) on the
  same middleware; module code never builds URLs by hand.
* `manifest.gen.ts` (§7.4.1) is written by the same `openapi` subcommand from
  `Description.UIManifests` through `ui.WriteManifestTS(w, m)` — a few lines of Go printing a
  literal object, so it needs no TypeScript API.
* **Toolchain risk (raised to 05-http-api/13):** `openapi-typescript` 7.13.0 declares
  `peerDependencies: { typescript: "^5.x" }` (`npm view`, 2026-11-04), while the repo's `typescript`
  name resolves to 6.0.2 (exception E1 in 13-toolchain) and TS 7 has no programmatic API. A
  package-level npm `overrides` entry is required for a clean `npm ci`; if the generator actually
  breaks on TS 6, the fallback is a Go emitter over the same OpenAPI document (same output shape,
  so consumers do not change). Open item O4.
* Freshness gates (`make generate-api-client`, `make openapi-check`) follow 05-http-api; tygo and
  `opsgen` (raw/13 §8) are deleted once their routes are native.

## 7.9 Auth, session and realtime

* `AuthProvider` (from `AuthContext.tsx`) moves into the shell, calling `/api/core/v1/auth/{status,me,login,logout,setup}`
  (05-authn). `me.permissions` is the effective namespaced list; `usePermission` is advisory and fails
  closed on unknown/null, keeping today's `authEnabled: null` rule (`AuthContext.tsx:23-30`).
* Session is the core cookie session; any 401 from the runtime calls `onUnauthorized` → auth state
  reset → navigate to `/login?return=<safe path>` (`safeReturn` util moves to the shell). An
  `ApiAuthRedirectError` triggers a full reload so the SSO proxy can run.
* `/invite/:token` renders before auth gates and `/setup` + first-run wizard (shell frame + module
  `setupSteps`) keep today's gate order (`App.tsx:200-251`).
* **Realtime:** one `EventSource` per tab to the core stream (05-realtime; path
  `/api/core/v1/events`), opened only when authenticated; the server filters by permission, the
  shell dispatches by topic to `useTopic` subscribers; reconnect with `Last-Event-ID` and today's
  backoff (3 s doubling to 60 s, `eventSourceManager.ts:25-29`); a `resync` event calls every
  `useResync` handler and re-runs §7.5.2. The shutdown backdrop and reconnect counter
  (`App.tsx:215-241`) move to the shell, driven by topic `core.system.shutdown.v1`.

## 7.10 Build preset and code splitting

`@mediacore/shell/vite` exports `mediacore(options)` returning the Vite plugin list and config every
app uses, so the subtle choices documented in `web/vite.config.ts` live in one place:

* `@vitejs/plugin-react` + `@rolldown/plugin-babel` with `reactCompilerPreset()` (not via `react()`'s
  `babel` option, which is a silent no-op under rolldown, `web/vite.config.ts:14-21`);
* `resolve.dedupe` for every peer singleton (§7.3.2);
* `codeSplitting.groups`: `mui`, `vendor` (react, react-dom, react-is, scheduler, react-router),
  `mediacore` (shell, ui-kit, api-client); every module route is `lazy`, so a module's pages are
  separate chunks loaded on first navigation;
* dev server proxy for `/api` to the Go server; build ID meta tag; theme boot script.

## 7.11 ABO's `web/` as an app package

ABO's `web/` stays the ABO app workspace and becomes: `src/main.tsx` = `createShell({ app, modules: [booksModule] }).mount('#root')`;
everything audiobook-specific moves under `src/modules/books/` (a directory with an ESLint
`no-restricted-imports` boundary mirroring R3, promoted to its own package only when ABO gains a
second module). Classification from raw/13 §11–§12, re-checked against the files:

| ABO piece | Destination | Notes |
|---|---|---|
| `pages/Login.tsx`, `AcceptInvite.tsx`, `Setup.tsx`, `NotFound.tsx`, `Users.tsx` | shell | Users page reads module permissions from `/api/core/v1/rbac/permissions` titles (05-rbac `PermissionDef.Title`) |
| `pages/UserMerge.tsx` | shell if 05-authn keeps account merge in core; otherwise a books settings panel | depends on owner/CF-Access machinery (raw/10 G11) |
| `pages/ActivityLog.tsx` (3,320 LOC) + `stores/useOperationsStore.ts`, `operationGrouping.ts`, `utils/{operationPolling,followOperation,trackedOperations}.ts`, `components/layout/OperationsIndicator.tsx` | shell **Tasks** page `/system/tasks` over `/api/core/v1/tasks` | audiobook op labels/colours become books `taskKinds` |
| `pages/System.tsx` (+ `components/system` Storage, Quota, SystemInfo, Maintenance, ThrottlePanel) | shell `/system` | `ChapterConsolidationCard`, `ManualFixesCard` → books `widgets` (placement `system`) |
| `pages/Settings.tsx` | shell `/settings` frame | generic tabs (Security, API Keys, Plugins → Integrations, AI backends, Scheduled tasks, Performance, Temp Login, System Info) in shell; Library, iTunes Import, Metadata, Dedup, Paths, OpenLibraryDumps, BlockedHashes, Embedding → books `settingsPanels`; Deluge → generic adapter-instance form rendered from the config schema (05-config, 05-download) |
| `pages/Dashboard.tsx` | shell `/` home | library counts and scan trigger → books widget (placement `home`) |
| `pages/Diagnostics.tsx` | shell `/system/diagnostics` frame | AI-dedup diagnostics → books widget |
| `pages/FileBrowser.tsx` | books page `/books/files` | uses ui-kit `ServerFileBrowser` |
| `components/wizard/WelcomeWizard.tsx` (989 LOC) | shell first-run frame + books `setupSteps` | |
| `contexts/AuthContext.tsx`, `components/toast/ToastProvider.tsx`, `stores/useAppStore.ts`, `lib/storageKeys.ts` | shell | |
| `components/operations/OperationTrackerProvider.tsx` | shell `TaskTrackerProvider` (follow a run app-wide) + books provider for merge/combine/purge locks | |
| `components/layout/{MainLayout,TopBar,UserMenu,sidebarSelection}.tsx`, `Sidebar.tsx` minus its arrays | shell | `librarySubItems`/`menuItems` (`Sidebar.tsx:53-100`) become books `nav` (with children) plus shell entries; Review badge → `badge: () => useReviewStore(s => s.count)` |
| `components/common/*` (ConfigurableTable, ServerFileBrowser, PathLinks, BulkConfirmDialog, FilterTagBar), `ErrorBoundary`, `KeyboardShortcutsDialog`, `AnnouncementBanner` | ui-kit (presentational) / shell (stateful) | |
| hooks `useAsyncAction`, `useKeyboardShortcuts`, `useDebouncedSearch`, `useRowSelection` | ui-kit | `useUnsavedChangesBlocker` deleted — react-router 8 data router provides `useBlocker` |
| `utils/apiFetch.ts`, `apiResponse.ts`, `permissionError.ts`, `apiKeyExpiry.ts`, `safeReturn.ts`, `settingLocks.ts` | api-client runtime / shell | |
| `services/eventSourceManager.ts` | shell realtime | |
| `services/api.ts` (8,747 LOC) | split: auth/users/operations/system/settings → generated `@mediacore/api-client`; everything else → generated books client; hand-written legacy calls to `/api/v1` remain in `modules/books/legacyApi.ts` during the compatibility window and shrink route-group by route-group | |
| `theme.ts` | ui-kit `createMediaTheme` + books augmentation (signal palette) | |
| `generated/ops.ts`, `types/generated/*` | replaced by OpenAPI-generated types | |
| all other pages/components (Library, BookDetail, Authors, Series, Works, Collections, Playlists, TrashedVersions, review workspace, …) | `modules/books/` | URLs move to `/books/...`; old paths and `ABS_REDIRECTS` (`web/src/utils/absRedirects.ts:17-25`) become `redirects` |

## 7.12 Lifecycle

Browser: load → read build ID → `GET /api/core/v1/auth/status` → (login) → `GET /api/core/v1/me`
→ `GET /api/core/v1/modules` → intersect (§7.5.2) → build router → open realtime → render. On
`resync` or reconnect: re-fetch `me` and `modules`, re-intersect; a module that disappeared (disabled
and restarted) unmounts and its routes show `NotFound`.

Server: `ui.FromFS` runs during `app.New` P8; a malformed bundle (no `index.html`, no build meta)
fails boot when the `embed_frontend` tag is set.

## 7.13 Error semantics

| Error path | Behaviour | Why |
|---|---|---|
| Unknown module ID in client registry vs server | not mounted, admin diagnostic | G3; old decision 8 |
| Version-incompatible module | not mounted, diagnostic | G3 |
| Client descriptor ID not declared by server | item dropped, diagnostic | G3 |
| Stale SPA build | banner, keep working | reload is the user's choice; APIs still versioned |
| `/api/core/v1/modules` fails | shell renders only shell pages + retry banner (fail-closed for modules) | never guess which modules exist |
| Lazy chunk load error | route-level ErrorBoundary with reload button | network blips after deploy |
| Page render throws | route-level ErrorBoundary; other routes keep working | as today (`App.tsx` wraps each route) |
| 401 from any call | auth reset + redirect to login | single session model |
| SSO bounce (`ApiAuthRedirectError`) | full reload | proxy must handle it |
| Realtime disconnected | reconnect with backoff; status chip; `resync` on reconnect | SSE is lossy across reconnects without resume |
| Missing/invalid `index.html` in embedded bundle | boot fails (fail-closed) | a broken UI must not ship silently |
| Unmatched `/api/...` path | 404 JSON envelope, never `index.html` | API clients must not parse HTML (today's `spa_fallback.go` rule) |
| Diagnostics report endpoint rate-limited | client drops the report (fail-open) | diagnostics must not affect use |

## 7.14 Concurrency contract

Server: `ui.Handler` is immutable after construction and goroutine-safe; no package state. Client:
one realtime connection per tab; api-client de-duplicates concurrent identical GETs of the manifest;
module stores must be safe under React 19 concurrent rendering (no module-load-time singletons that
capture user state; `storageKey` scoping per app).

## 7.15 Config keys

| Key | Type | Default | Hot-reload? |
|---|---|---|---|
| `core.ui.enabled` | bool | `true` (serve the bundle when present) | no |
| `core.ui.base_path` | string | `/` | no |
| `core.ui.csp.connect_src` | []string (bare DNS names) | `[]` | yes |
| `core.ui.csp.report_only` | bool | `false` | yes |
| `core.ui.diagnostics.accept` | bool | `true` | yes |

## 7.16 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_ui_build_info` | gauge (1) | `build_id` |
| `mediacore_ui_responses_total` | counter | `kind` (`asset`/`index`/`fallback`/`api404`/`placeholder`), `status` |
| `mediacore_ui_diagnostics_total` | counter | `kind`, `module` |
| `mediacore_ui_manifest_requests_total` | counter | `outcome` |

## 7.17 Publishing after the split (D9: free only)

| Option | Cost | Install auth | Verdict |
|---|---|---|---|
| GitHub Release assets: `npm pack` tarballs attached to the same tag `vX.Y.Z` as the Go module; apps depend on the tarball URL | free | none for a public repo | **chosen**: one version for Go and npm, immutable, no registry account; Renovate's regex manager with the GitHub-releases datasource (free) bumps it |
| GitHub Packages npm registry | free for public packages | a token with `read:packages` even for public packages, in CI and on every developer machine | fallback if semver-range resolution becomes necessary |
| npm git dependency on a tag | free | none | rejected: npm cannot install a package from a subdirectory of a git repository |

## 7.18 Tests

| Test | Where | Proves |
|---|---|---|
| `intersect.test.ts` (one case per §7.5.2 row) | shell, vitest | G3 |
| `defineModule.types.test.ts` (`// @ts-expect-error` on undeclared IDs) | shell, typecheck | compile-time descriptor safety |
| `nav.permissions.test.tsx` | shell | hidden nav for missing permission, `badge` hook |
| `auth.flow.test.tsx`, `realtime.resync.test.tsx` | shell | 401 redirect, SSO bounce reload, Last-Event-ID resume |
| `theme.contrast.test.ts` | ui-kit | contrast in both schemes |
| `runtime.errors.test.ts` | api-client | envelope decoding, timeout, bounce detection |
| `check-singletons.mjs` | core/web CI | one copy of each peer singleton |
| `TestHandler_*` (assets immutable, index no-store, CSP hashes, api404 JSON, placeholder) | `core/ui`, Go | server semantics |
| `TestBundle_MissingMeta` | `core/ui` | fail-closed boot |
| e2e `shell.spec.ts` | `core/web/e2e`, Playwright (chromium, webkit) against `examples/hello` built with `-tags embed_frontend` | login, merged nav, version-mismatch hiding, disabled module absent after restart (G1 UI half), stale-build banner |
| ABO e2e (existing 39 specs) | `web/tests/e2e` | regression across the move; route URLs updated with redirects tested |

## 7.19 Migration from ABO

Order (each step shippable): (1) create `core/web` packages by moving the shell-classified files
(§7.11) unchanged in behaviour, add `createShell`, keep ABO routes registered as one big books module
with today's URLs; (2) switch ABO `main.tsx` to `createShell`; (3) react-router 7 → 8 (52 files) and
data router; delete `useUnsavedChangesBlocker`; (4) server manifest + intersection on; (5) move ABO
URLs to `/books/...` with redirects; (6) replace `api.ts` sections by generated clients as their
native routes ship (09).

## 7.20 Open items

| # | Item | Owner |
|---|---|---|
| O1 | TS 7 declaration emit completeness for the three packages (fallback recorded in 13) | 13-toolchain |
| O2 | Realtime path and topic names (`/api/core/v1/events`, `core.system.shutdown.v1`) | 05-realtime |
| O3 | Whether account merge (UserMerge) is core | 05-authn |
| O4 | `openapi-typescript` peer range vs the TS 6/7 split: `overrides` entry or Go emitter fallback | 05-http-api, 13-toolchain |
| O5 | CSRF protection model for cookie sessions consumed by the runtime | 05-authn |
| O6 | Data-fetching library: none mandated; revisit TanStack Query only if module stores duplicate cache/resync logic | 07 |

## Objections to 03

1. **`@mediacore/ui-kit` should be in the SDK list of 03 §3.1.** Apps import it directly (theme
   factory, shared components), so its API is as stability-relevant as `@mediacore/shell`. Proposed:
   add it next to `@mediacore/shell` and `@mediacore/api-client` in the SDK row.
