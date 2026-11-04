<!-- file: docs/specs/platform-core/07-ui-shell.md -->
<!-- version: 1.2.0 -->
<!-- guid: 651865f1-0fad-4e15-8169-ceaf4a573f13 -->
<!-- last-edited: 2026-11-04 -->

# 07 — UI shell: `@mediacore/*` npm packages and the Go `ui` package

Evidence commit: `748fd787a`. Raw data: [`raw/13-frontend.md`](../../plans/platform-core/raw/13-frontend.md),
[`raw/14-build-ci.md`](../../plans/platform-core/raw/14-build-ci.md) §3, §5,
[`raw/15-dependencies.md`](../../plans/platform-core/raw/15-dependencies.md) (npm),
[`raw/09-http-api.md`](../../plans/platform-core/raw/09-http-api.md) §5–§6,
[`raw/08-prior-art.md`](../../plans/platform-core/raw/08-prior-art.md) §2 row 6.
Decisions: D6, D8, D9; old decision 8 (02 §2.2); gate G3. Server-side module data comes from
[06-app-sdk](06-app-sdk.md) (`Manifest.UI`). Frontend tool versions are owned by
[13-toolchain](13-toolchain.md) §13.4; this file does not restate them (FE-21).

## 7.1 Purpose & scope

One React shell per binary (N6) that every app reuses: auth and session, layout and merged
navigation, settings frame, tasks/activity UI, users/roles, notifications, health/diagnostics,
theming, realtime and the API runtime. Modules contribute pages, nav entries, settings panels and the
other slots of §7.4.2 **at build time**; the server reports which modules are enabled; the shell
renders the intersection and refuses unknown or version-incompatible descriptors (G3). No remote JS
(old decision 8). The Go side (`core/ui`) serves an app's embedded build with core headers, once the
app has adopted the shell build (§7.6.3).

Out of scope: visual redesign of ABO pages; the native API itself (05-http-api); realtime server
semantics (05-realtime); serving under a URL sub-path (§7.15, O7).

## 7.2 Current state at HEAD

| Aspect | Today | Anchor |
|---|---|---|
| Entry and providers | `ErrorBoundary > BrowserRouter > ThemeProvider > AuthProvider > ToastProvider > OperationTrackerProvider > App` | `web/src/main.tsx:19-47` |
| Routes | hard-coded `<Routes>` table, 21 `React.lazy` pages, no route-level permission guard | `web/src/App.tsx:27-59,253-448` |
| Navigation | two hard-coded arrays, not data-driven or permission-filtered | `web/src/components/layout/Sidebar.tsx:53-86,88-100` |
| Router | `react-router-dom` 7, declarative (not data router); a custom hook re-implements `useBlocker` because there is no data router. **122 files** import `react-router` or `react-router-dom`: 52 source, 70 test/e2e (`git grep -lE "from ['\"]react-router(-dom)?['\"]" 748fd787a -- web`) | `web/package.json:34`, `web/src/hooks/useUnsavedChangesBlocker.ts:15` |
| Test harness | `renderWithProviders` wraps every render in `MemoryRouter`; 265 `renderWithProviders(` call sites; 65 further files use `MemoryRouter`/`BrowserRouter` directly (`git grep` at `748fd787a`) | `web/src/test/renderWithProviders.tsx:7` |
| API client | 8 hand-written service files, 9,811 lines: `api.ts` (8,747, `API_BASE = '/api/v1'`), `activityApi.ts`, `collectionApi.ts`, `fileOpsApi.ts`, `playlistApi.ts`, `readingApi.ts`, `versionApi.ts`, `eventSourceManager.ts` | `web/src/services/api.ts:24`; raw/13 §12 |
| Transport | `apiFetch` with own timeout and SSO-bounce detection (`ApiAuthRedirectError`); no global 401 interceptor: `AuthContext` treats 401 as unauthenticated only for its own status/me probe | `web/src/utils/apiFetch.ts:16,51`; `web/src/contexts/AuthContext.tsx:90-91` |
| Realtime | two streams per tab: `eventSourceManager` (`/api/events`, backoff 3 s → 60 s by `close()` + `new EventSource`, `onmessage` only) and the operations stream (`/api/v1/operations/events`) | `web/src/services/eventSourceManager.ts:25-29,119,135-146`; `App.tsx:120,137`; `useOperationsStore.ts:411` |
| Auth | `AuthProvider` (status → me), permissions advisory; hard-coded legacy permission atoms in 5 hooks/components and the API-key scope picker | `web/src/contexts/AuthContext.tsx:19-45`; `hooks/useLibraryPermission.ts:9,11`, `useCanOrganizeLibrary.ts:9`, `hooks/useCollections.ts:24`, `components/system/ThrottlePanel.tsx:24`, `components/settings/APIKeysTab.tsx:49-61` |
| Theme | MUI 9 CSS variables, `colorSchemeSelector: 'data-mui-color-scheme'`, default dark, audiobook-specific signal palette inside the theme | `web/src/theme.ts:154-165`, `:24-40` |
| Build | Vite 8 (rolldown), React Compiler via `@rolldown/plugin-babel` (passing it through `react()` is a silent no-op), `dedupe: ['react','react-dom']` (React #130 history), chunk groups `mui`/`vendor` whose regex names `react-router-dom` | `web/vite.config.ts:13-21,35,66-85` |
| TypeScript | TS 7 native is the typecheck gate; the `typescript` name is aliased to TS 6 because `typescript-eslint` needs the TS JS API "which TS 7 does not ship" | `web/package.json:8,53,66`; `CHANGELOG.md:2917-2923` |
| Embedding | `//go:build embed_frontend` + `//go:embed web/dist` in package main → `server.SetEmbeddedFS` | `web_embed.go:1,10-11`, `main.go:34`, `internal/server/static_embed.go:26-33` |
| SPA serving | gin `NoRoute`; `spaWillNotServe` 404s non-GET/HEAD, the `/api` prefix and whole segments `auth`, `socket.io`, `ping`, `status`, `logout`, `health`, `metrics`, `public`, after `normalizeNonSPAPath` (case, `//`, trailing slash); CF service-token recovery runs only on SPA page loads | `internal/server/server_lifecycle.go:2141`, `static_embed.go:59-124`, `spa_fallback.go:58-60,74-83,90,119`, `cf_recovery.go:36-60` |
| CSP | set on **every** response by `securityHeadersMiddleware`; inline scripts of `index.html` hashed into `script-src`; full directive set `cspBase`; reports to `/api/v1/csp-report`; the Access team host is re-read per response into `connect-src` through an `atomic.Pointer` | `internal/server/server_middleware.go:108-109`, `csp.go:57-67,83-87,129-137`, `csp_report.go:36` |
| CSRF | cookie sessions rely on `SameSite=Strict` only, plus one custom-header check on repairs | `handlers/auth.go:233,246`; `handlers/repairs/handler.go:27-28` |
| Classification | ~70–75 % of non-test LOC audiobook-specific, ~10–15 % generic shell, rest mixed (judgment) | raw/13 §12 |

## 7.3 Packages, workspaces and versions

### 7.3.1 Layout

```
core/web/                       npm workspace root (own package-lock.json; the post-split repo root)
  package.json                  { "private": true, "workspaces": ["packages/*", "../examples/hello/web"] }
  packages/shell/               @mediacore/shell     (React shell + createShell + /vite preset + /testing)
  packages/api-client/          @mediacore/api-client (fetch runtime + generated core client + openapi/core.json)
  packages/ui-kit/              @mediacore/ui-kit     (theme factory + presentational components)
  e2e/                          Playwright suite against the hello example binary
core/examples/hello/web/        hello's web package (workspace member), built to web/dist (06 §6.12)
```

**Exports.** Every package builds to `dist/` (JS by Vite library mode, `.d.ts` by TS 7
`tsc --emitDeclarationOnly`). Its `exports` map has two conditions: `"development": "./src/index.ts"`
and `"default"` pointing at `dist/`. The preset adds `development` to Vite's `resolve.conditions`
under `vite serve` and vitest only, so `make web-dev` and unit tests read shell sources (an edit is
visible without rebuilding `core/web`), while every production build and every published install
resolves `dist/`. A production build therefore consumes what a split app downloads (FE-13).

**Peers.** `react`, `react-dom`, `react-router`, `@mui/material`, `@emotion/react` and
`@emotion/styled` are **peerDependencies** of shell and ui-kit (context singletons). Icons are typed
as `ComponentType<SvgIconProps>` from `@mui/material`, so the shell imports nothing from
`@mui/icons-material` and needs no icons peer (FE-26); apps choose their own icon package.

**Compilation.** Packages are built with the same `reactCompilerPreset()` as apps (§7.10), because app
Babel plugins skip `node_modules` and uncompiled shell components would be unmemoized. The emitted
code imports `react/compiler-runtime`, which the `react` peer (≥ 19) provides; `react/compiler-runtime`
is in the singleton check (§7.3.2).

**Why a separate `core/web` workspace and not one repo-root workspace.** A root workspace
(`["web", "core/web/packages/*"]`, one lockfile) would avoid duplicate React by construction, but
`core/web` must become the split repository's root unchanged (12): with a root workspace, ABO's
lockfile would own core's dependency graph and the split would require re-locking every core package.
The cost of the chosen layout is the dedupe and singleton check below.

**Import boundary (npm twin of R1, FE-16).** An ESLint `no-restricted-imports` rule plus
`import/no-relative-packages` in `core/web` rejects any specifier that resolves outside `core/web`
(no `../../web/...`, no `@abo/*`). It runs in `make ci` from wave 0, not first at split criterion S8.
Shell-classified ABO files that import audiobook code are therefore cut loose **before** they move
(§7.11, §7.19).

### 7.3.2 How an app consumes them

* **In-repo phase (D3):** ABO's `web/package.json` declares
  `"@mediacore/shell": "file:../core/web/packages/shell"` (same for `api-client`, `ui-kit`). npm
  symlinks them. Because the symlink target resolves `react` from `core/web/node_modules`, a second
  React instance is possible; the `@mediacore/shell/vite` preset (§7.10) therefore extends
  `resolve.dedupe` to every peer above, and `core/web/scripts/check-singletons.mjs` (the exact-file
  sourcemap check described in `web/vite.config.ts:66-75`, promoted to a script) fails the build if
  any peer's files, or `react/compiler-runtime`, appear from two paths.
* **One entry point for web dependencies (FE-13, cross-cutting resolution 9).** `make web-deps` runs
  `scripts/ci/build_web.sh deps`: `npm ci` and `npm run build --workspaces` in `core/web`, then `npm ci`
  in `web/`. `make web-build`, `web-install`, `web-test`, `web-lint`, `web-typecheck` and `web-dev`
  (`Makefile:192-218`) depend on it. Every site that today runs `cd web && npm ci` calls it instead
  (the script directly where `make` is unavailable): `.github/workflows/ci.yml:379-383`,
  `e2e.yml:161-167,185`, `binary-smoke.yml:40,55`, `prerelease.yml:51` and `release-prod.yml:58`
  (goreleaser `pre-build-script`), `security.yml:81-82`, `vulnerability-scan.yml:59-62`, the reusable
  `frontend-ci.yml`, `Dockerfile:13-21`, `Dockerfile.build-cgo:14-19`, `scripts/ci_remote.py:516`, and
  the CLAUDE.md worktree recipe `npm ci --prefix web`. Each `cache-dependency-path` adds
  `core/web/package-lock.json`; path filters (`frontend-ci.yml:11-17`, `e2e.yml:68`) add `core/web/**`;
  Dependabot's npm entry adds `/core/web`. The list is the S0 checklist in 09; this file owns the
  mechanism.
* **`.dockerignore`** adds `**/node_modules` and `core/web/packages/*/dist`, so `COPY core/web/`
  never copies the host's `node_modules` (native rolldown/esbuild/oxc binaries) or a host-built `dist/`
  over the image's own install. The Dockerfile frontend stage copies every workspace member's
  `package.json` and lockfile before `npm ci`.
* **After the split:** the specifier becomes an exact registry version (§7.17); nothing else changes.

### 7.3.3 Versions (D8)

Versions of `react`, `@mui/material`, `vite`, `vitest`, `@vitejs/plugin-react`, `@playwright/test`,
`zustand`, `eslint`/`typescript-eslint` and Node are those of [13](13-toolchain.md) §13.4, including
its 14-day release-age rule; the shell's peer ranges follow them. Two decisions are this file's:

| Package | Decision |
|---|---|
| `react-router` | peer `react-router ^8` (data router). `react-router-dom` stops at 7.x. ABO migrates its 122 importing files (52 source, 70 test) **before** shell adoption (§7.19 step 1, FE-2, SIMP-25) |
| `typescript` | typecheck and declaration emit with TS 7; the `typescript` name stays aliased to TS 6 for the tools in the matrix below |

TypeScript 7 support matrix (from `npm view <pkg> peerDependencies`, 2026-11-04; FE review experiment
for `openapi-typescript`):

| Tool | Needs the TS JS API? | TS 7 status | Decision |
|---|---|---|---|
| `tsc` typecheck | — | native | **gate** for every package and app |
| `.d.ts` emit for packages | — | native `tsc --emitDeclarationOnly` | use TS 7; if a declaration-emit gap is found, fall back to `tsc6` for emit only and record it in [13-toolchain](13-toolchain.md) |
| `vite`, `vitest`, `@vitejs/plugin-react` | no (oxc/rolldown strip types); no `typescript` peer | unaffected | no `vitest --typecheck` (it would need a TS API) |
| `knip` | no (depends on `oxc-parser`/`oxc-resolver`) | unaffected | — |
| `typescript-eslint` | yes; peer `typescript >=4.8.4 <6.1.0` | **unsupported** | **exception**: keep `"typescript": "npm:@typescript/typescript6@^6.0.2"` for lint only, as ABO does today; revisit when typescript-eslint widens its peer |
| `openapi-typescript` 7.13.0 (chosen by 05-http-api §7) | yes; peer `typescript ^5.x` | without an override, `npm install` fails ERESOLVE; with the override below it installs and generates OpenAPI 3.1 correctly on TS 6.0.2; pointed at TS 7.0.2 it crashes (`dist/lib/ts.mjs:11`, no JS API) | every package that runs the generator carries `"overrides": {"openapi-typescript": {"typescript": "$typescript"}}`, so it uses the TS 6 alias (FE-20). No Go emitter is planned (SIMP-27); one is reconsidered only if a future `openapi-typescript` drops TS 6 |
| `typescript@7.0.2` itself | exports only `./lib/version.cjs` and `./unstable/*` | no stable programmatic API | nothing in the toolchain may import it programmatically |

Each package gets `"engines": {"node": ">=22.22.0"}` (`react-router` 8's floor; the repo pins Node 26,
raw/14 §5).

## 7.4 Extension API (TypeScript)

### 7.4.1 Generated module manifest

The Go manifest is the single source for IDs and versions. `app.Describe` (06 §6.4.5) emits each
module's UI manifest; the app's `openapi` subcommand writes it as `manifest.gen.ts` into the module's
web directory (§7.8) through `ui.WriteManifestTS(w, m)`, a few lines of Go printing a literal object:

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
  banners: [],
  topBarItems: [],
  userPage: [],
  activityPanes: [],
  setupSteps: [],
  permissions: ['hello:read', 'hello:admin'],
} as const;
```

The keys mirror `module.UIDescriptor` (06 §6.4.2). A freshness check in CI regenerates and diffs it,
so a client contribution can never name an ID the server does not declare without failing typecheck
(§7.4.2 types are keyed by these literals).

### 7.4.2 `@mediacore/shell` types and entry points

```ts
import type { ComponentType, ReactNode } from 'react';
import type { SvgIconProps, ThemeOptions } from '@mui/material';

export type Permission = `${string}:${string}`;

export interface ModuleManifest {
  readonly id: string;
  readonly version: string;
  readonly displayName: string;
  readonly pages: readonly string[];
  readonly nav: readonly string[];
  readonly settingsPanels: readonly string[];
  readonly widgets: readonly string[];
  readonly banners: readonly string[];
  readonly topBarItems: readonly string[];
  readonly userPage: readonly string[];
  readonly activityPanes: readonly string[];
  readonly setupSteps: readonly string[];
  readonly permissions: readonly Permission[];
}

type Lazy = () => Promise<{ default: ComponentType }>;
type Icon = ComponentType<SvgIconProps>;
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
  icon?: Icon;
  to: PageId<M> | { page: PageId<M>; search?: string };
  permission?: Perm<M>;    // hidden when the user lacks it
  order?: number;
  // A React hook (FE-22): must be named use*, have stable identity, and obey
  // the rules of hooks. The shell calls it inside a dedicated <NavBadge>
  // component per entry, so the React Compiler and rules-of-hooks accept it.
  useBadge?: () => number | undefined;
  children?: readonly NavContribution<M>[];
}

export interface SettingsPanelContribution<M extends ModuleManifest> {
  id: M['settingsPanels'][number];
  label: string;
  lazy?: Lazy;             // a module panel, or omit and pass schemaSection for the schema form (§7.4.5)
  schemaSection?: string;  // config section path, e.g. 'modules.hello'
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

export interface BannerContribution<M extends ModuleManifest> {
  id: M['banners'][number];
  lazy: Lazy;              // rendered by the shell's main layout above page content; renders null when idle
  permission?: Perm<M>;
  order?: number;
}

export interface TopBarContribution<M extends ModuleManifest> {
  id: M['topBarItems'][number];
  lazy: Lazy;
  permission?: Perm<M>;
  order?: number;
}

export interface UserPageExtensions<M extends ModuleManifest> {
  tabs?: readonly { id: M['userPage'][number]; label: string; lazy: Lazy; permission?: Perm<M> }[];
  rowActions?: readonly {
    id: M['userPage'][number];
    label: string;
    lazy: () => Promise<{ default: ComponentType<{ userId: string; onDone: () => void }> }>;
    permission?: Perm<M>;
  }[];
}

export interface ActivityPaneContribution<M extends ModuleManifest> {
  id: M['activityPanes'][number];
  label: string;
  lazy: Lazy;
  permission?: Perm<M>;
  order?: number;
}

export interface SetupContribution<M extends ModuleManifest> {
  // Asked after login (and on /setup) whether this module still needs first-run
  // setup; books: !config.root_dir && !config.setup_complete (App.tsx:87-89).
  isRequired: () => Promise<boolean>;
  steps: readonly { id: M['setupSteps'][number]; title: string; lazy: Lazy }[];
}

export interface TaskKindPresentation {
  label: string;
  color?: 'default' | 'primary' | 'secondary' | 'info' | 'success' | 'warning' | 'error';
  resultView?: Lazy; // renders a finished run's result in the Activity tasks pane
}

export interface ModuleUIInput<M extends ModuleManifest> {
  routes: readonly RouteContribution<M>[];
  nav: readonly NavContribution<M>[];
  settingsPanels: readonly SettingsPanelContribution<M>[];
  permissionsHints: PermissionsHints<M>;
  // optional
  widgets?: readonly WidgetContribution<M>[];
  banners?: readonly BannerContribution<M>[];
  topBarItems?: readonly TopBarContribution<M>[];
  userPageExtensions?: UserPageExtensions<M>;
  activityPanes?: readonly ActivityPaneContribution<M>[];
  setup?: SetupContribution<M>;
  themeExtend?: ThemeOptions;                                         // merged by the shell, per module (§7.7)
  taskKinds?: Readonly<Record<`${M['id']}.${string}`, TaskKindPresentation>>;
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
export function useTopic<T>(topic: string, onEvent: (data: T) => void, opts?: { key?: string }): void;
export function useResync(onResync: () => void): void;
export function useToast(): ToastApi;
export function useTask(runId: string | undefined): TaskRunView | undefined;
export function useApi(): import('@mediacore/api-client').Client;
export function storageKey(scope: string, name: string): string; // `${appId}.${scope}.${name}`
```

`createShell` renders, in order: `ErrorBoundary` → MUI `ThemeProvider` (CSS variables) → api-client
`ClientProvider` → `AuthProvider` → `RealtimeProvider` → `ToastProvider` → `TaskTrackerProvider` →
module `providers` → `RouterProvider` (react-router 8 data router built from shell routes plus
module routes under `/<id>/…`). The main layout renders module `banners` above page content and
`topBarItems` in the top bar; the Users page renders `userPageExtensions`; the Activity page renders
core panes plus `activityPanes`.

### 7.4.3 Shell routes and reserved segments

Shell routes: `/` (home), `/login`, `/logout`, `/setup`, `/invite/:token`, `/activity` (panes:
`tasks` default, `logs` for the system journal, `audit` for holders of `core:audit.view`, then module
panes; 05-logging §5), `/settings/*`, `/system/*` (status, diagnostics, UI diagnostics), `/users`, and
the `*` not-found route. This settles the tasks page at `/activity` (09 S9 and 05-logging already use
it), not `/system/tasks` (FE-16).

`ui.ReservedSegments()` (Go, package `ui`) lists the first segment of every shell route: `activity`,
`invite`, `login`, `logout`, `settings`, `setup`, `system`, `users`. Server path segments (`api`,
`assets`, `healthz`, `readyz`, `metrics`, …) are `httpapi.ReservedSegments`. The kernel validates
module IDs against `module.ReservedIDs()`, the union of both lists and `rbac.ReservedNamespaces`
(03 §3.6, 06 §6.4.1), so a module named `invite` or `activity` cannot shadow a shell page (FE-23).
A test generates the shell's own route list from the same set, so the two cannot drift.

### 7.4.4 `@mediacore/shell/testing`

```ts
export interface RenderInShellOptions {
  route?: string;                         // initial location, default '/'
  modules?: readonly ModuleUI[];          // module contributions to mount
  auth?: Partial<AuthState> | 'anonymous'; // default: an authenticated user
  permissions?: readonly Permission[];    // the user's effective permissions
  serverModules?: ModulesResponse;        // the /modules payload; default: every module enabled
  realtime?: FakeRealtime;                // push topics/resync in tests
  client?: Partial<import('@mediacore/api-client').Client>; // stubbed operations
}

export function renderInShell(ui: React.ReactElement, opts?: RenderInShellOptions): RenderResult & {
  router: ReturnType<typeof createMemoryRouter>;
  realtime: FakeRealtime;
};

export interface FakeRealtime {
  emit<T>(topic: string, data: T, key?: string): void;
  resync(): void;
  close(reason: 'shutdown' | 'credential' | 'error'): void;
}
```

Built on `createMemoryRouter` (a data router, so `useBlocker` works in tests) plus fake
`ClientProvider`/`AuthProvider`/`RealtimeProvider`. ABO's `renderWithProviders` is rewritten onto
it in its own PR (§7.19 step 4), which fixes every component that calls `useBlocker` under a
non-data `MemoryRouter` (FE-17).

### 7.4.5 Schema-driven settings forms

Generic settings (adapter instances such as Deluge, core sections, and module sections that pass
`schemaSection` instead of `lazy`) are rendered by an **in-house renderer** in `@mediacore/shell`,
`<SchemaForm section=… />`, driven by the JSON Schema that `config` publishes (05-config §1.10) and by
the key-state API (05-config `KeyState`). No form library is added. The renderer supports exactly:

| JSON Schema / state | Widget |
|---|---|
| `string` (+ `enum`) | text field (select) |
| `string` with `format: duration\|uri\|cidr\|path\|email` | text field with a client-side format check; server validation is authoritative |
| `integer`/`number` (+ `minimum`/`maximum`) | number field |
| `boolean` | switch |
| `array` of the above | chip list / repeatable rows |
| `writeOnly: true` (a `secrets.Ref` field) | password field showing "set" / "not set", never the value; clearing is an explicit action |
| key locked by an operator layer (`KeyState.Locked`, today `settingLocks.ts`) | read-only, with the lock reason and how to change it |
| `reload: restart` with a pending value (`KeyState.Pending`) | "restart required" badge |

Layout hints use one vendor keyword, `x-mediacore-ui: {group, order, widget}`, emitted by `config`
from a struct tag; `widget` may only name a widget in the table. This closes 05-config's open item 3
(FE-27). Anything richer is a module panel (`lazy`).

## 7.5 Server manifest and intersection (G3)

### 7.5.1 `GET /api/core/v1/modules`

Permission `rbac.AnyAuthenticated`. Served by the module catalog's route package (06 §6.4.6). Field
names follow the native JSON rule of 05-http-api §4.3, which is snake_case like every native type there
(`request_id`, `required_permission`, `next_cursor`); types are generated into `@mediacore/api-client`
(FE-12):

```json
{
  "core": { "version": "0.4.0" },
  "app": { "id": "abo", "display_name": "Audiobook Organizer", "version": "2.0.0", "ui_build_id": "b3f1c2…" },
  "restart_required": false,
  "modules": [
    { "id": "books", "version": "1.12.0", "display_name": "Audiobooks",
      "ui": { "pages": ["library", "book"], "nav": ["library"], "settings_panels": ["library"], "widgets": ["counts"],
              "banners": ["review"], "top_bar_items": [], "user_page": ["guest-keys", "merge"],
              "activity_panes": ["books-feed"], "setup_steps": ["library-root"] } }
  ],
  "disabled_modules": ["hello"]
}
```

`core.version` is `version.Version` (06 §6.4.2, MOD-12). `ui_build_id` is the served bundle's build
ID, `""` when no shell bundle is served (zero `Bundle`, or a legacy mount owns `/`, §7.6.3).
`disabled_modules` is present only for callers holding `core:admin`. A disabled module is absent from
`modules` (G1: no nav entry).

### 7.5.2 Algorithm (runs once after login and on every reconnect `resync`)

First, the shell compares its own compile-time core version `C.core` (the version of the
`@mediacore/shell` package it was built with, embedded by the preset as
`import.meta.env.MEDIACORE_CORE_VERSION`) with `S.core.version`:

| Case | Shell behaviour | Diagnostic kind |
|---|---|---|
| `major(C.core) != major(S.core)` or `minor(C.core) > minor(S.core)` | blocking "UI and server versions do not match" page; no module is mounted (the shell would call `/api/core/v1` routes the server lacks) | `core-version-incompatible` |

Then, for each module `S` in the server list and the compile-time entry `C = registry[S.id]`:

| Case | Shell behaviour | Diagnostic kind |
|---|---|---|
| no `C` and `S.ui` empty | nothing to render | — |
| no `C` and `S.ui` non-empty | module UI not in this bundle | `ui-missing` |
| `major(C.version) != major(S.version)` or `minor(C.version) > minor(S.version)` | module not mounted (client would call APIs the server lacks, or a major break) | `version-incompatible` |
| item in any `C` slot (routes, nav, settings panels, widgets, banners, top-bar items, Users-page extensions, activity panes, setup steps) whose ID ∉ the matching `S.ui` list | that item dropped | `undeclared-descriptor` |
| `C` with no matching `S` | not mounted (disabled or unknown module) | `module-not-enabled` (admin only) |
| `S.ui` ID with no client item | ignored | `descriptor-without-ui` (debug log) |

Additionally, when `app.ui_build_id` and the bundle's compile-time build ID are both non-empty, not
`"dev"`, and different (stale cached SPA after an upgrade), the shell shows a non-dismissable
"new version — reload" banner; module mounting proceeds with the rules above. The preset defines
`MEDIACORE_BUILD_ID = "dev"` under `vite serve`, so `make build-api` plus the Vite dev server never
shows the banner (FE-15). Diagnostics are shown to admins under **System → UI diagnostics**, logged
with `console.warn`, and reported once per kind per page load to `POST /api/core/v1/ui/diagnostics`
(AnyAuthenticated, rate-limited, body ≤ 4 KiB) so they appear in server logs and metrics.

The version rules are deliberately strict on minor: hiding a page beats calling a missing endpoint.
Within one in-repo build, server and SPA come from one commit. After the split, the Go pin and the npm
pins move in separate pull requests (§7.17), so skew can reach a released binary; §7.5.3 stops that in
CI, and the core-version row stops it at runtime.

### 7.5.3 Lockstep check

`scripts/ci/check_mediacore_lockstep.py`, run in every app's `make ci`, fails when the
`@mediacore/*` versions resolved in `web/package-lock.json` differ from the
`github.com/falkcorp/mediacore` version in `go.mod`. In-repo both are the workspace version
(`file:` specifiers and the zero pseudo-version under `replace` both count as "workspace"); after the
split both must be the same `X.Y.Z`. A half-bumped state (Go at X+1, shell at X) therefore fails CI
(FE-15).

## 7.6 Go side: package `ui`

### 7.6.1 API

```go
package ui

// Bundle is an app's built shell SPA.
type Bundle struct {
	fsys    fs.FS  // rooted at the dist directory
	buildID string // from <meta name="mediacore-build" content="..."> in index.html
	index   []byte
	scripts []string // CSP hash sources of inline <script> blocks in index.html
}

// FromFS roots fsys at dir, reads index.html, extracts the build ID and the
// sha256 hashes of inline scripts. It fails if index.html or the meta tag is
// missing: the meta tag is written by the shell preset, so only shell builds
// are Bundles. An app whose SPA is not yet a shell build passes the zero
// Bundle and serves its SPA from a legacy mount (§7.6.3).
func FromFS(fsys fs.FS, dir string) (Bundle, error)

// MustFromFS panics on error; for main packages with an embedded FS that
// was validated at build time.
func MustFromFS(fsys fs.FS, dir string) Bundle

func (b Bundle) IsZero() bool
func (b Bundle) BuildID() string

type Options struct {
	// NonSPA decides which unmatched paths answer 404 instead of index.html.
	// The kernel passes httpapi's Router.NonSPA() predicate (05-http-api
	// §4.10): every claimed prefix (/api/, facades) and every legacy compat
	// segment (ABO: auth, socket.io, ping, status, logout, public), normalised
	// exactly as today's normalizeNonSPAPath (lower case, path.Clean, query
	// dropped) for this decision only; the normalised path is never dispatched.
	NonSPA func(r *http.Request) bool

	// PageMiddleware wraps only index.html responses (page navigations), e.g.
	// ABO's Cloudflare service-token recovery (cf_recovery.go:36-60).
	PageMiddleware func(http.Handler) http.Handler

	// CSP: ExtraDirectives are appended to the default policy (§7.6.2);
	// ConnectSrcFrom returns extra connect-src hosts (bare DNS names, validated
	// as csp.go does today) and is re-evaluated whenever config changes (ABO:
	// its Access team host).
	ExtraDirectives []string
	ConnectSrcFrom  func() []string

	Config  *config.Handle[Settings] // core.ui.* (§7.15); hot keys swap the policy atomically
	Metrics *metrics.Scope
}

// Handler serves the SPA:
//   - hashed assets under /assets/: Cache-Control "public, max-age=31536000, immutable"
//   - a missing /assets/* file, or any unmatched path whose last segment has a
//     file extension: 404, never index.html (a stale tab's chunk 404 must not
//     become a MIME error)
//   - a method other than GET/HEAD: 404/405 problem+json (05-http-api §4.10)
//   - a NonSPA path (after normalisation): 404 problem+json, never HTML
//   - every other unmatched GET/HEAD: index.html, "no-store", with the full
//     CSP of §7.6.2, through PageMiddleware
//   - zero Bundle: the API-only placeholder page (today's static_nonembed.go behaviour)
// The policy lives in an atomic.Pointer refreshed on config change; nothing
// else is mutable after construction. Goroutine-safe; no package state (R5).
func Handler(b Bundle, opts Options) http.Handler

// ReservedSegments lists the first segment of every shell route (§7.4.3);
// module.ReservedIDs() includes them. Returns a fresh slice on every call
// (R5: no exported slice variable).
func ReservedSegments() []string

// WriteManifestTS prints a module's UI manifest as manifest.gen.ts (§7.4.1).
func WriteManifestTS(w io.Writer, m app.UIManifest) error
```

`ui` is at R4 L6 and `app` at L9, so `WriteManifestTS` cannot take `app.UIManifest`: the manifest
type it prints is `ui.ModuleManifest` (ID, version, display name, the UI ID lists, permission names),
and `app.Description.UIManifests` is a `[]ui.ModuleManifest`.

### 7.6.2 Content Security Policy

Responsibility is split (FE-4):

* **`core/ui` computes the shell policy.** The default is today's `cspBase` verbatim
  (`csp.go:57-67`), with `script-src 'self'` plus the inline-script hashes and `connect-src 'self'`
  plus `ConnectSrcFrom()`:
  `script-src 'self' <hashes>; connect-src 'self' <hosts>; default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; font-src 'self' data:; media-src 'self' blob:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'self'; report-uri /api/core/v1/ui/csp-report; report-to csp`,
  then `Options.ExtraDirectives`. `core.ui.csp.report_only` switches the header to
  `Content-Security-Policy-Report-Only`.
* **`httpapi` stage 5 writes a CSP on every response** whose handler set none (05-http-api §4.2,
  FE-4): the SPA handler's policy above, the legacy mount's `DefaultCSP` (ABO: today's `cspBase` with
  its report URI), otherwise `default-src 'none'; frame-ancestors 'self'`. Server-rendered HTML outside
  the SPA (the ABS OIDC consent form, `handlers/abs/openid.go:1055`; placeholder pages) is therefore
  never without one, and a handler that sets its own `Content-Security-Policy` keeps it (today's rule).
* **Reports** go to `POST /api/core/v1/ui/csp-report` (Public, `RateAuth` class, body ≤ 8 KiB,
  logged rate-limited as `csp_report.go` does), registered by `ui` on the core group, with
  `/api/v1/csp-report` as a legacy alias during the compatibility window.

`core.ui.csp.connect_src` and `report_only` are hot-reloadable: the handler swaps an
`atomic.Pointer[policy]` on config change, as `csp.go:129-137` does today. Nothing else in the handler
changes after construction (§7.14).

### 7.6.3 Mounting, route-chain placement and the ABO stages

**Ownership of `/` (FE-1, cross-cutting resolution 2).** Exactly one owner serves the `/` fallback:

| Phase | `app.Options.UI` | `/` owner | Notes |
|---|---|---|---|
| Before ABO adopts the shell build (09 S2 up to the shell-adoption stage) | zero `Bundle` | the books `LegacyMount` with `ServesSPA: true`; gin keeps `NoRoute` SPA serving, `spaWillNotServe`, CF recovery and ABO's CSP machinery unchanged | `httpapi` registers no `/` pattern while a legacy mount owns it; `ui.Handler` is not mounted; `ui_build_id` is `""` so the build-ID banner never shows |
| From shell adoption (09 S9) | `ui.MustFromFS(WebFS, "web/dist")` (the build now has the preset's meta tag) | `ui.Handler`, mounted by the kernel with `Router.SPA` (05-http-api §4.10) | in the same PR: books drops `ServesSPA`; gin's `NoRoute` SPA serving, `setupStaticFiles` and the `static_embed.go` SPA code are deleted; ABO passes `PageMiddleware` (CF recovery); its compat segments reach `NonSPA` through its legacy claims |

ABO's `web_embed.go` stays as is; `main.go:34` changes from `server.SetEmbeddedFS(WebFS)` to
`app.Options{UI: ...}` only at shell adoption.

**Route-chain placement (FE-25).** `ui.Handler` is mounted **outside** the route chain: it receives
only `httpapi` stages 0–9 of 05-http-api §4.10 (peer allowlist, request ID, tracing, recovery, access
log, security headers with the SPA's CSP, CORS, cross-origin protection, perimeter gate,
compression). It is not subject to the per-client rate limiter (a cold
load fetches index, entry, `mui`, `vendor` and per-page chunks), needs no permission, and is recorded as `RouteInfo{Kind: SPA, Perm: rbac.Public, RateClass:
RateExempt}` in `archtest.RoutesHavePermissions`' known infrastructure set. The kernel mounts it as
the least-specific pattern, so API, legacy and facade routes always
win, matching `setupStaticFiles`' "registered routes never see it" (`static_embed.go:29-31`). The CSP
state that is today a package global initialised in `init()` (`csp.go:83-87`) becomes a field of the
handler instance.

**`embed_frontend` semantics are kept unchanged** (raw/14 §3): each app has `ui_embed.go`
(`//go:build embed_frontend`, `//go:embed` of its dist directory, placed in the directory that
contains `web/`, as hello does in 06 §6.12.5) and `ui_nonembed.go` (`//go:build !embed_frontend`,
zero `ui.Bundle`). `make build`, goreleaser, Docker and e2e set the tag; `make build-api` and
`go test ./...` do not.

The build ID is written by the `@mediacore/shell/vite` preset into `index.html` as
`<meta name="mediacore-build" content="<hash of the emitted entry chunk names>">` and into the bundle
as `import.meta.env.MEDIACORE_BUILD_ID` (`"dev"` under `vite serve`), so server and client agree
without extra files.

## 7.7 Theming

```ts
// @mediacore/ui-kit
export interface MediaThemeInput {
  brand?: { primary?: string; secondary?: string };
  defaultMode?: 'light' | 'dark';           // default 'dark' (ABO today)
  extend?: import('@mui/material').ThemeOptions; // app tokens via MUI module augmentation
}
export function createMediaTheme(input?: MediaThemeInput, moduleExtends?: readonly import('@mui/material').ThemeOptions[]): import('@mui/material').Theme;
export const MODE_ATTRIBUTE = 'data-mui-color-scheme';
export function themeBootScript(storageKey: string): string; // inline pre-mount script body
```

* MUI 9 `createTheme({ cssVariables: { colorSchemeSelector: 'data-mui-color-scheme' }, colorSchemes: { light, dark } })`,
  as `web/src/theme.ts:154-165` does today; mode toggling flips one attribute, no re-render.
* The pre-mount boot script (no light-mode flash) is injected into `index.html` by the vite preset;
  the Go `ui` handler hashes it into CSP automatically (§7.6.2).
* Mode storage key: `storageKey('shell', 'themeMode')`; ABO passes its existing key so saved
  preferences carry over (the same trick `main.tsx:28-34` already uses).
* Module tokens stay in the module: each module's `themeExtend` (§7.4.2) is merged by the shell after
  the app's `extend`, in module ID order (FE-16). ABO's dedup signal palette (`theme.ts:24-40`) moves
  to `web/src/modules/books/theme.ts` and is passed as books' `themeExtend`.
* Contrast tests (`theme.contrast.test.ts`) move to ui-kit and run for both schemes and for every
  app's and module's extension in that app's tests.

## 7.8 API client and code generation

The OpenAPI 3.1 documents and the TypeScript client pipeline are owned by
[05-http-api](05-http-api.md) §7: types by `openapi-typescript` 7.13.0, calls by `openapi-fetch`
0.17.0. The shell consumes it as follows:

* **`core.json` has one producer (FE-20).** `core/examples/hello`'s `openapi` subcommand writes
  `app.Description.Core` (06 §6.4.5) to `core/web/packages/api-client/openapi/core.json`, and core CI
  freshness-gates it. Apps do not emit `core`: an app's `openapi` subcommand writes only its modules'
  documents and `manifest.gen.ts` files.
* `@mediacore/api-client` = types generated from `core.json` for every `/api/core/v1` route +
  `createCoreClient()`, whose middleware carries today's `apiFetch` semantics: own deadline
  (`ApiTimeoutError`), SSO login-page bounce detection (`ApiAuthRedirectError`,
  `web/src/utils/apiFetch.ts:51`), `credentials: 'include'`, `X-Request-ID` surfacing, RFC 9457
  `Problem` as the error type (05-http-api §3.3), and the 401 rule of §7.9. Each generated operation
  whose OpenAPI `x-mediacore-permission` is `public` is marked `skipAuthRedirect`. The shell creates
  one client in `ClientProvider`; `useApi()` returns it.
* **The generated client arrives before identity moves (FE-12).** `@mediacore/api-client` (runtime,
  generator, `make generate-api-client` in `core/web`) ships in a UI stage that precedes 09 S4, consumed
  by ABO through `file:`. 09 S4–S6 then switch UI calls straight to generated operations instead of
  rewriting `api.ts` calls twice.
* **Module clients are directories until the module is a package (FE-28).** ABO's books client is
  generated into `web/src/modules/books/api/` from `api/openapi/books.json`, on the same runtime; it
  becomes a package only when books does (§7.11). Module code never builds URLs by hand.
* The generator package carries the npm `overrides` entry of §7.3.3. The wave-0 spike that SIMP-27
  asked for was run by the frontend review (2026-11-04): 7.13.0 under the override on the TS 6 alias
  generates correctly from an OpenAPI 3.1 document with `type: ["string","null"]` and dotted component
  names. `make generate-api-client` failing is a CI failure; there is no fallback generator.
* Freshness gates (`make generate-api-client`, `make openapi-check`) follow 05-http-api; tygo and
  `opsgen` (raw/13 §8) are deleted once their routes are native.

## 7.9 Auth, session and realtime

### 7.9.1 Auth and session

* `AuthProvider` (from `AuthContext.tsx`) moves into the shell, calling `/api/core/v1/auth/{status,me,login,logout,setup}`
  (05-authn). `me.permissions` is the effective namespaced list; `usePermission` is advisory and fails
  closed on unknown/null, keeping today's `authEnabled: null` rule (`AuthContext.tsx:23-30`).
* **Session cookie (FE-6).** The core cookie session's default name is `<AppID>_session`, with the
  `__Host-` prefix whenever the cookie is `Secure` (which forces `Path=/` and no `Domain`), so two
  apps on one host never overwrite or receive each other's session; a cookie whose token is unknown to
  this app is treated as absent, not as a presented invalid credential (05-authn §4.3, §6). ABO keeps
  its configured `session_id`.
* **The 401 rule (FE-7).** The runtime's `onUnauthorized` (auth state reset, navigate to
  `/login?return=<safe path>`; `safeReturn` moves to the shell) runs only for a 401 whose problem
  `code` is one of `authentication_required`, `expired`, `revoked`, `inactive_user`,
  `invalid_credential`, on a request **not** marked `skipAuthRedirect`. Every Public auth route (login,
  setup, bootstrap, invite accept) is `skipAuthRedirect`, so a wrong password or bootstrap token shows
  its error in place and `/setup` never redirects to `/login`. A 503 `auth_unavailable` keeps the
  session and shows a retry banner; it never logs the user out. An `ApiAuthRedirectError` triggers a
  full reload so the SSO proxy can run.
* **CSRF.** Cookie-authenticated unsafe requests are checked by the `CrossOriginProtection` stage of
  05-http-api §4.2 (`Sec-Fetch-Site`/`Origin`; Bearer and API-key requests exempt). The runtime needs
  nothing extra: same-origin `fetch` sends `Sec-Fetch-Site: same-origin`, and the Vite dev proxy is
  same-origin from the browser's view. This closes 1.0.0's open item O5 (FE-5).
* **Permission atoms (FE-11).** The UI never hard-codes a permission string. The shell's Users and
  API-keys pages read canonical permissions, titles and descriptions from
  `GET /api/core/v1/rbac/permissions` (05-rbac `PermissionSpec.Title`); module hooks use the
  generated permission constants of their manifest (`manifest.permissions`). When 09 S4 switches the
  server to canonical names, the same build replaces ABO's five legacy constants
  (`useLibraryPermission.ts:9,11`, `useCanOrganizeLibrary.ts:9`, `useCollections.ts:24`,
  `ThrottlePanel.tsx:24`) and the `APIKeysTab` scope list (`:49-61`); a vitest fails on any
  `'<word>.<word>'` permission literal outside generated files.
* `/invite/:token` renders before auth gates; `/setup` and the first-run wizard (shell frame plus
  every enabled module's `setup` steps whose `isRequired()` is true) keep today's gate order
  (`App.tsx:200-251`).

### 7.9.2 Realtime client

The wire contract (endpoint `/api/core/v1/events`, framing, resume, caps, topics) is 05-realtime's.
The shell side:

* **Transport (FE-8).** A `fetch()` + `ReadableStream` SSE reader in `@mediacore/shell` (no new
  dependency, about 150 lines), not native `EventSource`. On every reconnect it schedules, it sends
  the last seen ID as the `Last-Event-ID` header (or `?last_event_id=`, which 05-realtime §4.3
  accepts), so backoff (3 s doubling to 60 s, today's `eventSourceManager.ts:25-29`) and resume work
  together. It sees the response status, `Retry-After` and `response.redirected`: a 401 goes through
  the rule of §7.9.1, a 429/503 waits `Retry-After`, an SSO redirect is an `ApiAuthRedirectError`. It
  dispatches by the envelope's `topic` field to `useTopic` subscribers, so named-event frames are not
  needed; 05-realtime drops the `event:` line from the native stream so that a plain `EventSource`
  fallback still receives every frame.
* **One stream per browser per app (FE-9).** Tabs elect a leader with the Web Locks API
  (`navigator.locks.request('<appId>.realtime', …)`); only the leader opens the stream and fans frames
  out over a `BroadcastChannel('<appId>.realtime')`; followers subscribe through it, and a new leader
  is elected when the leader's tab closes. Both APIs are native. This keeps one connection per app
  regardless of tab count (HTTP/1.1 allows 6 per host across tabs) and stays inside the hub's
  per-user caps (05-realtime §4.5).
* **When the UI moves to the native stream (FE-10).** 09 S6 ships the hub plus legacy projections
  with **no client change**; the UI keeps `eventSourceManager` and the operations stream until shell
  adoption (09 S9), when the shell's realtime client replaces both. The hooks are `useTopic` and
  `useResync` everywhere (05-realtime §11 uses the same names).
* **Resync and shutdown.** A `resync` frame calls every `useResync` handler and re-runs §7.5.2. The
  shutdown backdrop and reconnect counter (`App.tsx:215-241`) move to the shell, driven by
  `core.stream.closed.v1` with `reason: "shutdown"`, which the hub sends to every subscriber
  (05-realtime §5.4), so non-admin users see it too.

## 7.10 Build preset and code splitting

`@mediacore/shell/vite` exports `mediacore(options)` returning the Vite plugin list and config every
app and every core package uses, so the subtle choices documented in `web/vite.config.ts` live in one
place:

* `@vitejs/plugin-react` + `@rolldown/plugin-babel` with `reactCompilerPreset()` (not via `react()`'s
  `babel` option, which is a silent no-op under rolldown, `web/vite.config.ts:14-21`); the same preset
  builds the core packages in library mode (§7.3.1);
* `resolve.dedupe` for every peer singleton and `react/compiler-runtime` (§7.3.2);
* `resolve.conditions` gains `development` under `vite serve` and vitest only (§7.3.1);
* `codeSplitting.groups`: `mui` and `vendor` (react, react-dom, react-is, scheduler, react-router), as
  today. There is **no** `mediacore` group (FE-18): rolldown places every module matching a group in
  that chunk regardless of dynamic imports, so a group would pull the shell's lazy pages (Activity,
  Users, System, Settings) into one eagerly loaded chunk, and a `node_modules/@mediacore` test matches
  nothing in-repo (`file:` symlinks resolve to real paths) but everything after the split. Shell and
  module pages are `lazy`, and library-mode builds preserve those dynamic imports as separate chunks;
* `define`: `MEDIACORE_BUILD_ID` (`"dev"` under `vite serve`), `MEDIACORE_CORE_VERSION`;
* dev server proxy for `/api` to the Go server; build ID meta tag; theme boot script.

A CI bundle check (§7.18) loads `/login` with an empty cache and asserts that no shell-page or
module-page chunk is fetched, and enforces size budgets for the entry and `mui` chunks.

## 7.11 ABO's `web/` as an app package

ABO's `web/` stays the ABO app workspace and becomes: `src/main.tsx` = `createShell({ app, modules: [booksModule] }).mount('#root')`;
everything audiobook-specific moves under `src/modules/books/` (a directory with an ESLint
`no-restricted-imports` boundary mirroring R3, promoted to its own package only when ABO gains a
second module). Classification from raw/13 §11–§12, re-checked against the files. A shell-classified
file that imports audiobook code is **not** moved unchanged: its audiobook part becomes a module slot
first, and the core-web import rule (§7.3.1) proves the cut (FE-16).

| ABO piece | Destination | Notes |
|---|---|---|
| `pages/Login.tsx`, `AcceptInvite.tsx`, `Setup.tsx`, `NotFound.tsx` | shell | |
| `pages/Users.tsx` | shell `/users` | permissions and titles from `/api/core/v1/rbac/permissions` (§7.9.1); `GuestPhoneKeys` (CF guest tokens, stays in ABO per 09 S4) and `UserMergeDialog` (`Users.tsx:8,12-13`) become books `userPageExtensions` |
| `pages/UserMerge.tsx` | shell if 05-authn keeps account merge in core; otherwise a books `userPageExtensions` row action | depends on owner/CF-Access machinery (raw/10 G11) |
| `pages/ActivityLog.tsx` (3,320 LOC) + `stores/useOperationsStore.ts`, `operationGrouping.ts`, `utils/{operationPolling,followOperation,trackedOperations}.ts`, `components/layout/OperationsIndicator.tsx` | shell `/activity` (tasks pane over `/api/core/v1/tasks`) | audiobook op labels/colours become books `taskKinds`; the book feed, the `versionApi` undo preflight and `PendingFileOpsBanner` (`ActivityLog.tsx:7,60`) become a books `activityPanes` entry; `useOperationsStore`'s `* as api` import (`useOperationsStore.ts:7`) is rewired to the generated core client before the move |
| `pages/System.tsx` (+ `components/system` Storage, Quota, SystemInfo, Maintenance, ThrottlePanel) | shell `/system` | `ChapterConsolidationCard`, `ManualFixesCard` → books `widgets` (placement `system`) |
| `pages/Settings.tsx` | shell `/settings` frame | generic tabs (Security, API Keys, Plugins → Integrations, AI backends, Scheduled tasks, Performance, Temp Login, System Info) in shell; Library, iTunes Import, Metadata, Dedup, Paths, OpenLibraryDumps, BlockedHashes, Embedding → books `settingsPanels`; Deluge → generic adapter-instance form rendered by `<SchemaForm>` (§7.4.5) |
| `pages/Dashboard.tsx` | shell `/` home | library counts and scan trigger → books widget (placement `home`) |
| `pages/Diagnostics.tsx` | shell `/system/diagnostics` frame | AI-dedup diagnostics → books widget |
| `pages/FileBrowser.tsx` | books page `/books/files` | uses ui-kit `ServerFileBrowser` |
| `components/wizard/WelcomeWizard.tsx` (989 LOC) | shell first-run frame + books `setup` | the wizard gate `config.root_dir \|\| config.setup_complete` (`App.tsx:87-89`) becomes books' `setup.isRequired` |
| `contexts/AuthContext.tsx`, `components/toast/ToastProvider.tsx`, `stores/useAppStore.ts`, `lib/storageKeys.ts` | shell | `AuthContext`'s `* as api` import (`AuthContext.tsx:16`) is rewired to the generated core client before the move |
| `components/operations/OperationTrackerProvider.tsx` | shell `TaskTrackerProvider` (follow a run app-wide) + books provider for merge/combine/purge locks | |
| `components/layout/{MainLayout,TopBar,UserMenu,sidebarSelection}.tsx`, `Sidebar.tsx` minus its arrays | shell | `ReviewBanner` and `usePendingFileOps` (`MainLayout.tsx:9-10,25`) become books `banners`; `librarySubItems`/`menuItems` (`Sidebar.tsx:53-100`) become books `nav` (with children) plus shell entries; the Review badge becomes `useBadge: useReviewCount` where `const useReviewCount = () => useReviewStore(s => s.count)` (FE-22) |
| `components/common/*` (ConfigurableTable, ServerFileBrowser, PathLinks, BulkConfirmDialog, FilterTagBar), `ErrorBoundary`, `KeyboardShortcutsDialog`, `AnnouncementBanner` | ui-kit (presentational) / shell (stateful) | |
| hooks `useAsyncAction`, `useKeyboardShortcuts`, `useDebouncedSearch`, `useRowSelection` | ui-kit | `useUnsavedChangesBlocker` deleted at shell adoption: the react-router 8 data router provides `useBlocker` |
| `utils/apiFetch.ts`, `apiResponse.ts`, `permissionError.ts`, `apiKeyExpiry.ts`, `safeReturn.ts`, `settingLocks.ts` | api-client runtime / shell | |
| `services/eventSourceManager.ts` | replaced by the shell realtime client at shell adoption (§7.9.2) | |
| `services/api.ts` (8,747 LOC) | split: auth/users/operations/system/settings → generated core client (from the UI stage before 09 S4); books routes → generated books client in `modules/books/api/`; hand-written calls to `/api/v1` remain in `modules/books/legacyApi.ts` during the compatibility window and shrink route-group by route-group | |
| `services/activityApi.ts` (256), `collectionApi.ts` (193), `fileOpsApi.ts` (29), `playlistApi.ts` (126), `readingApi.ts` (63), `versionApi.ts` (212) | books: generated books client where the route is native, else `modules/books/legacyApi.ts` during the window (FE-28) | none is shell code |
| `theme.ts` | ui-kit `createMediaTheme` + books `themeExtend` (signal palette) | |
| `generated/ops.ts`, `types/generated/*` | replaced by OpenAPI-generated types | |
| all other pages/components (Library, BookDetail, Authors, Series, Works, Collections, Playlists, TrashedVersions, review workspace, …) | `modules/books/` | URLs move to `/books/...`; old paths and `ABS_REDIRECTS` (`web/src/utils/absRedirects.ts:17-25`) become `redirects` |

## 7.12 Lifecycle

Browser: load → read build ID → `GET /api/core/v1/auth/status` → (login) → `GET /api/core/v1/me`
→ `GET /api/core/v1/modules` → core-version check and intersect (§7.5.2) → module `setup.isRequired`
checks → build router → realtime leader election and stream (§7.9.2) → render. On `resync` or
reconnect: re-fetch `me` and `modules`, re-intersect; a module that disappeared (disabled and
restarted) unmounts and its routes show `NotFound`.

Server: `ui.FromFS` runs in the app's `main` (embedded bundle) before `app.New`; the kernel mounts
`ui.Handler` in P8 unless a legacy mount owns `/` (06 §6.5.1). A malformed shell bundle (no
`index.html`, no build meta) fails boot when the `embed_frontend` tag is set.

## 7.13 Error semantics

| Error path | Behaviour | Why |
|---|---|---|
| Shell core version incompatible with the server's | blocking mismatch page, no module mounted, diagnostic `core-version-incompatible` | the shell would call core routes that do not exist (FE-15) |
| Unknown module ID in client registry vs server | not mounted, admin diagnostic | G3; old decision 8 |
| Version-incompatible module | not mounted, diagnostic | G3 |
| Client descriptor ID not declared by server (any slot) | item dropped, diagnostic | G3 |
| Stale SPA build (both build IDs non-empty, not `dev`, different) | banner, keep working | reload is the user's choice; APIs still versioned |
| `/api/core/v1/modules` fails | shell renders only shell pages + retry banner (fail-closed for modules) | never guess which modules exist |
| Lazy chunk load error | route-level ErrorBoundary with reload button; the server answers a missing chunk with 404, never `index.html` | network blips and stale tabs after deploy |
| Page render throws | route-level ErrorBoundary; other routes keep working | as today (`App.tsx` wraps each route) |
| 401 with a session-ending code on a non-`skipAuthRedirect` request | auth reset + redirect to login | single session model |
| 401 on a Public auth route (login, setup, bootstrap, invite) | error shown in place, no redirect | those routes return 401 by design (FE-7) |
| 503 `auth_unavailable` | keep session, retry banner, no logout | "cannot check" is not "logged out" |
| SSO bounce (`ApiAuthRedirectError`) | full reload | proxy must handle it |
| Realtime 401 / 429 / 503 / redirect | 401 rule; wait `Retry-After`; SSO reload; status chip throughout | the fetch-based reader sees the status (FE-8) |
| Realtime disconnected | reconnect with backoff and `Last-Event-ID`; `resync` if the server cannot resume | resume is lossless within the hub's window |
| Missing/invalid `index.html` or build meta in an embedded shell bundle | boot fails (fail-closed) | a broken UI must not ship silently |
| Unmatched `/api/...`, NonSPA segment, non-GET/HEAD | 404 problem+json, never `index.html` | API and compat clients must not parse HTML (today's `spa_fallback.go` rule) |
| Diagnostics or CSP report endpoint rate-limited | client drops the report (fail-open) | reporting must not affect use |

## 7.14 Concurrency contract

Server: `ui.Handler` holds its policy in an `atomic.Pointer` swapped on config change and is otherwise
immutable after construction; goroutine-safe; no package state. Client: **one realtime connection per
browser per app** (Web Locks leader + `BroadcastChannel`, §7.9.2); api-client de-duplicates concurrent
identical GETs of the manifest; module stores must be safe under React 19 concurrent rendering (no
module-load-time singletons that capture user state; `storageKey` scoping per app).

## 7.15 Config keys

| Key | Type | Default | Hot-reload? |
|---|---|---|---|
| `core.ui.enabled` | bool | `true` (serve the bundle when present) | no |
| `core.ui.csp.connect_src` | []string (bare DNS names) | `[]` | yes (policy swapped atomically, §7.6.2) |
| `core.ui.csp.report_only` | bool | `false` | yes (same) |
| `core.ui.diagnostics.accept` | bool | `true` | yes |

There is no `core.ui.base_path` (FE-19). A UI-only base path cannot work while the API, cookies and
SSE stay absolute, and ABO has no base-path support today (no such key in `internal/config` at
`748fd787a`). Apps are served at `/`; operators who need a shared hostname use a host-based reverse
proxy. A real sub-path deployment needs `core.http.base_path` applied by `Router.Build` to every
pattern, the cookie path and the SSE URL together, with the shell reading `<base href>`; that is open
item O7, not a v1 feature.

## 7.16 Metrics

| Name | Type | Labels |
|---|---|---|
| `mediacore_ui_build_info` | gauge (1) | `build_id` |
| `mediacore_ui_responses_total` | counter | `kind` (`asset`/`asset404`/`index`/`nonspa404`/`method404`/`placeholder`), `status` |
| `mediacore_ui_diagnostics_total` | counter | `kind`, `module` |
| `mediacore_ui_csp_reports_total` | counter | `directive` |
| `mediacore_ui_manifest_requests_total` | counter | `outcome` |

## 7.17 Publishing after the split (D9: free only)

One mechanism, to be stated identically in [12](12-repo-split.md) (FE-14):

| Option | Cost | Install auth | Verdict |
|---|---|---|---|
| **npm registry**, public packages `@mediacore/*`, published by the same release job and tag `vX.Y.Z` as the Go module; apps pin **exact** versions (`"@mediacore/shell": "X.Y.Z"`) | free for public packages (14 Q-B recommends a public repo) | none | **chosen**: works with Dependabot's npm ecosystem and `npm ci` lockfiles |
| GitHub Release tarballs (`npm pack` attached to the tag), apps depend on the tarball URL | free | none for a public repo | fallback only if the repository stays private |
| GitHub Packages npm registry | free for public packages | a token with `read:packages` even for public packages | rejected: token on every machine |
| npm git dependency on a tag | free | none | rejected: npm cannot install a package from a subdirectory of a git repository |

Dependabot raises the Go module and the npm packages in **two** pull requests (one per ecosystem;
`groups` are per ecosystem). The lockstep check of §7.5.3 fails CI on the half-bumped state, so the
two are merged together. In-repo, apps use `file:` dependencies; there is no root workspace.

## 7.18 Tests

| Test | Where | Proves |
|---|---|---|
| `intersect.test.ts` (one case per §7.5.2 row, incl. the core-version row and every slot) | shell, vitest | G3, FE-15, FE-16 |
| `buildId.test.ts` | shell | banner skipped for `""` and `"dev"` |
| `defineModule.types.test.ts` (`// @ts-expect-error` on undeclared IDs in every slot) | shell, typecheck | compile-time descriptor safety |
| `nav.permissions.test.tsx` | shell | hidden nav for missing permission; `useBadge` called inside `<NavBadge>` |
| `auth.flow.test.tsx` | shell | session-ending 401 redirects; 401 on login/setup/bootstrap shows the error in place; 503 `auth_unavailable` keeps the session; SSO bounce reload |
| `realtime.resume.test.ts` | shell | resume after a scheduled reconnect replays from the last ID; `Retry-After` honoured; 401 routed through the auth rule |
| `realtime.leader.test.ts` | shell (fake `navigator.locks` + `BroadcastChannel`) | one stream for several tabs; leader handover |
| `schemaForm.test.tsx` | shell | every widget row of §7.4.5, locked and restart-required states, secret never echoed |
| `permissionLiterals.test.ts` | ABO web | no `'<word>.<word>'` permission literal outside generated files (FE-11) |
| `reservedSegments.test.ts` | shell + `core/ui` | shell route table equals `ui.ReservedSegments()` |
| `theme.contrast.test.ts` | ui-kit | contrast in both schemes |
| `runtime.errors.test.ts` | api-client | envelope decoding, timeout, bounce detection, `skipAuthRedirect` from `x-mediacore-permission: public` |
| `check-singletons.mjs` | core/web CI | one copy of each peer singleton and of `react/compiler-runtime` |
| ESLint import boundary | core/web `make ci` from wave 0 | no specifier outside `core/web` (FE-16) |
| bundle check | core/web e2e and ABO e2e | `/login` with an empty cache fetches no page chunk; entry and `mui` size budgets (FE-18) |
| `TestHandler_AssetsImmutable`, `_IndexNoStore`, `_CSPPolicyVerbatim`, `_CSPHotSwap`, `_API404JSON`, `_Placeholder` | `core/ui`, Go | server semantics |
| `TestHandler_NonSPASegments` (incl. case, `//`, trailing-slash variants, with the `Router.NonSPA()` predicate), `TestHandler_NonGETRefused`, `TestHandler_MissingAssetIs404`, `TestHandler_PageMiddlewareOnlyOnIndex` | `core/ui` | FE-3 |
| port of `static_embed_recovery_test.go` | `core/ui` with ABO's CF recovery as `PageMiddleware` | CF recovery still runs on page loads only |
| `TestBundle_MissingMeta` | `core/ui` | fail-closed for shell bundles |
| `TestHandler_OutsideRouteChain` | `core/ui` + `httpapi` | no rate limit or permission stage on SPA responses; listed in the infra set (FE-25) |
| e2e `shell.spec.ts` | `core/web/e2e`, Playwright (chromium, webkit) against `examples/hello` built with `-tags embed_frontend` | login, merged nav, version-mismatch hiding, core-version mismatch page, disabled module absent after restart (G1 UI half), stale-build banner |
| e2e `tabs.spec.ts` | same, served over plain HTTP/1.1 | six tabs all load and receive realtime (FE-9) |
| e2e `two-apps.spec.ts` | two example binaries on one host, different ports | signing in to one does not log out the other (FE-6) |
| ABO e2e (existing 39 specs) | `web/tests/e2e` | regression across every step of §7.19; route URLs updated with redirects tested |

## 7.19 Migration from ABO

Order; each step is shippable on its own and keeps the SPA served by the books legacy mount until
step 5 (FE-1, FE-2, SIMP-25):

1. **react-router 7 → 8 inside ABO**, still the declarative `<BrowserRouter>`: an import swap in the
   122 files (52 source, 70 test) plus the chunk regex at `vite.config.ts:81-82`. A PR series that
   precedes everything below; 13's exception for `react-router-dom` ends here, before `createShell`.
2. **Create `core/web`** (`@mediacore/ui-kit`, `@mediacore/api-client` runtime and generator,
   `@mediacore/shell` with `createShell` and `/testing`), the `make web-deps` entry point and every
   build site of §7.3.2, the `.dockerignore` lines, the lockstep check and the import-boundary rule.
3. **Generated core client consumed by ABO via `file:`** in the UI stage before 09 S4 (FE-12);
   09 S4–S6 switch `api.ts` sections straight to generated operations, and S4's build replaces the
   legacy permission constants (§7.9.1).
4. **Rewrite `renderWithProviders` onto `renderInShell`** (`createMemoryRouter`), its own PR (FE-17).
5. **Shell adoption (09 S9).** Cut the audiobook imports out of the shell-classified files into books
   slots (§7.11); move them into `core/web`; switch ABO `main.tsx` to `createShell` with the data
   router; delete `useUnsavedChangesBlocker`; the shell realtime client replaces `eventSourceManager`
   and the operations stream; the build gains the preset meta tag; `main.go` passes the bundle to
   `app.Options.UI`; books drops `ServesSPA` and gin's SPA serving is deleted in the same PR (§7.6.3).
6. **Server manifest and intersection on** (§7.5); ABO URLs move to `/books/...` with redirects.
7. Replace the remaining `api.ts` and sibling service sections by generated clients as their native
   routes ship (09).

## 7.20 Open items

| # | Item | Owner |
|---|---|---|
| O1 | TS 7 declaration emit completeness for the three packages (fallback recorded in 13) | 13-toolchain |
| O3 | Whether account merge (UserMerge) is core | 05-authn |
| O6 | Data-fetching library: none mandated; revisit TanStack Query only if module stores duplicate cache/resync logic | 07 |
| O7 | Sub-path deployment (`core.http.base_path` across `httpapi`, cookie path, SSE and `<base href>`), only on owner demand | 05-http-api, 05-authn, 07 |

Closed in 1.1.0: O2 (realtime path and the shutdown topic: `/api/core/v1/events`,
`core.stream.closed.v1`), O4 (`openapi-typescript` works on TS 6 under the override, §7.3.3), O5
(CSRF owned by 05-http-api §4.2's `CrossOriginProtection` stage). The schema-driven settings renderer
that 1.0.0 left undesigned is specified in §7.4.5 and owned by this file.

## Objections to 03

None open. 03 v1.4.0 §3.6 uses `/activity` as the shell UI-route example (the v1.1.0 objection).
Resolved earlier: the ui-kit SDK-row objection (FE-24) and the reserved-segment objection (FE-23).

## Review history

Round 1 (2026-11-04), findings applied to this file:

- FE-1 — `/` owned by the books legacy mount (`ServesSPA`) until shell adoption; `ui.Handler` and the meta-tag rule only for shell builds (§7.6.3).
- FE-2 — react-router 8 migration is step 1, before `createShell` (§7.19).
- FE-3 — `NonSPA` predicate from `Router.NonSPA()` with today's normalisation, `PageMiddleware`, method and missing-asset 404 rules, tests.
- FE-4 — full default CSP verbatim, `ExtraDirectives`, default CSP on non-SPA responses via `httpapi` stage 5, core CSP report route, hot reload via `atomic.Pointer`.
- FE-5 — CSRF pointed at 05-http-api's `CrossOriginProtection` stage; O5 closed.
- FE-6 — `<AppID>_session` / `__Host-` cookie, foreign cookie treated as absent; two-app e2e.
- FE-7 — 401 rule limited to session-ending codes and non-`skipAuthRedirect` requests; 503 `auth_unavailable` row.
- FE-8 — fetch + `ReadableStream` SSE reader with resume, status and `Retry-After` handling.
- FE-9 — one stream per browser per app (Web Locks + `BroadcastChannel`); six-tab e2e.
- FE-10 — S6 no client change, native stream at shell adoption; backdrop from `core.stream.closed.v1`; `useTopic`/`useResync`; O2 closed.
- FE-11 — no hard-coded permission atoms; S4 replaces the five constants and the key-scope list; literal-ban test.
- FE-12 — generated core client in a UI stage before 09 S4; `/modules` JSON in 05-http-api's snake_case.
- FE-13 — `make web-deps` / `scripts/ci/build_web.sh` for every entry point, cache paths, Dependabot, `.dockerignore`, `development` export condition; root-workspace alternative recorded.
- FE-14 — npm registry with exact versions, tarballs only as private fallback, two Dependabot PRs + lockstep.
- FE-15 — core-version row, lockstep check, build-ID banner skipped for `""`/`"dev"`.
- FE-16 — slots for banners, top bar, Users page, Activity panes, setup predicate, module theme; `/activity` settled; npm import-boundary rule from wave 0.
- FE-17 — 122 files (52 source, 70 test), verified; `@mediacore/shell/testing` `renderInShell`; harness rewrite step (265 call sites, verified; the review said 306).
- FE-18 — `mediacore` chunk group dropped; bundle check and budgets.
- FE-19 — `core.ui.base_path` removed; sub-path deployment recorded as O7.
- FE-20 — exact `overrides` text; Go emitter fallback deleted; `core.json` emitted only by hello; O4 closed.
- FE-21 — versions referenced from 13 §13.4, not restated.
- FE-22 — `useBadge` hook rendered in `<NavBadge>`.
- FE-23 — `ui.ReservedSegments()` (shell routes) feeding 03's `module.ReservedIDs()`.
- FE-24 — stale ui-kit objection removed.
- FE-25 — `ui.Handler` outside the route chain (05-http-api §4.10 stages 0–9), `Kind: SPA` Public + `RateExempt` infra entry.
- FE-26 — icons typed via `@mui/material`; packages compiled with `reactCompilerPreset()`; `react/compiler-runtime` in the singleton check.
- FE-27 — in-house `<SchemaForm>` renderer and `x-mediacore-ui` vocabulary (§7.4.5).
- FE-28 — rows for the six sibling `*Api.ts` files; books client is a directory.
- SIMP-25 — router migration precedes the shell stage (count corrected to FE-17's verified 122).
- SIMP-27 — no Go emitter; spike result recorded.
- MOD-20 — embed files beside `web/` (reference to 06 §6.12.5).
- Cross-cutting 2, 9 — `/` legacy until shell adoption; one web build entry point and `.dockerignore`.

Integration pass (cross-cluster requests, 2026-11-04):

- integration pass: Objection to 03 (UI-route example `/system/tasks`) removed: 03 v1.4.0 uses `/activity`.
