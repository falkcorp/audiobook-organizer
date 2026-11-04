<!-- file: docs/specs/platform-core/07-ui-shell.md -->
<!-- version: 1.5.0 -->
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
navigation, settings frame, tasks/activity UI, users/roles, notification channel and rule admin pages
plus toasts (no inbox, bell or personal channels in v1: they are deferred with per-user delivery,
05-notify §1; SIMP2-11),
health/diagnostics, theming, realtime and the API runtime. Modules contribute pages, nav entries, settings panels and the
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
core/web/                       npm workspace root (own package-lock.json; becomes the split repository's npm root web/)
  package.json                  { "private": true, "workspaces": ["packages/*", "../examples/hello/web"] }
  packages/api-client/          @mediacore/api-client (fetch runtime + generated core client + openapi/core.json)  UI-0
  packages/ui-kit/              @mediacore/ui-kit     (theme factory + presentational components)              S9
  packages/shell/               @mediacore/shell      (React shell + createShell + /vite preset + /testing)     S9
  e2e/                          Playwright suite against the hello example binary                               S9
core/examples/hello/web/        hello's web package ("private": true), built to web/dist (06 §6.12)              S9
```

The right-hand column is the 09 stage that creates each member (04 §8.1): UI-0 creates the workspace
with `@mediacore/api-client` only; `ui-kit`, `shell`, the e2e suite and hello's web package arrive at
shell adoption (S9), when something first consumes them (FE2-4, MOD2-10). Until S9 the workspaces
glob matches only the existing members, and the `../examples/hello/web` entry is added in the S9 PR.

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
`core/web` must become the split repository's npm root (`web/`, 12) unchanged: with a root workspace, ABO's
lockfile would own core's dependency graph and the split would require re-locking every core package.
The cost of the chosen layout is the dedupe and singleton check below.

**Import boundary (npm twin of R1, FE-16).** An ESLint `no-restricted-imports` rule plus
`import/no-relative-packages` in `core/web` rejects any specifier that resolves outside `core/web`
(no `../../web/...`, no `@abo/*`). It runs in `make ci` from UI-0, when `core/web` is created, not
first at split criterion SC8.
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
  `scripts/ci/build_web.sh deps`, a POSIX `sh` script under 20 lines (the Docker frontend stage is
  `node:26-alpine`, which has no `bash`; MOD2-16): `npm ci` in `core/web`, then the core packages built
  **in dependency order with an explicit list**, `npm run build -w @mediacore/api-client
  -w @mediacore/ui-kit -w @mediacore/shell` (npm runs `-w` in the order given; `--workspaces` runs
  them in glob order, which builds `shell` before the `ui-kit` it imports, FE2-14, MOD2-17), then
  `npm ci` in `web/`. Until S9 the list is `-w @mediacore/api-client` alone (§7.3.1). Hello's web
  package is `"private": true` and is built only by core's own example target and e2e job, never by
  ABO's `web-deps`, Docker stage or `npm pack`/`npm publish` (12 uses the same three-package list).
* **`web-deps` is a stamp target (FE2-15).** It writes `web/node_modules/.web-deps.stamp` and depends
  on both lockfiles and every source file of the packages, `$(shell find core/web/packages -path
  '*/src/*' -type f)` (GNU make has no `**`, FE3-20), so it is a no-op when up to date and two
  targets under `make -j` never run `npm ci` in the same directory at once. `make web-build`,
  `web-install`, `web-test`, `web-lint` and `web-typecheck` (`Makefile:192-218`) depend on it.
  `make web-dev` depends only on its install half (`npm ci` in both directories, no core build),
  because `vite serve` reads shell sources through the `development` condition (§7.3.1).
* **Every build site calls the entry point.** Every site that today runs `cd web && npm ci` calls
  `make web-deps` instead (the script directly where `make` is unavailable):
  `.github/workflows/ci.yml:379-383`, `e2e.yml:161-167,185`, `binary-smoke.yml:40,55`,
  `prerelease.yml:51` and `release-prod.yml:58` (goreleaser `pre-build-script`), `security.yml:81-82`,
  `vulnerability-scan.yml:59-62`, `Dockerfile:13-21`, `Dockerfile.build-cgo:14-19`,
  `scripts/ci_remote.py:516`, and the CLAUDE.md worktree recipe `npm ci --prefix web`.
* **Reusable workflows cannot call it (MOD4-10).** Three jobs install and build `web/` inside external
  reusable workflows whose steps ABO cannot change: `ci.yml:127` (`reusable-ci-minimal.yml`,
  `run-frontend`), `frontend-ci.yml:64` and `nightly.yml:31` (`reusable-ci.yml`). Once `web/` depends on
  `file:../core/web/packages/api-client`, whose `exports` point at an unbuilt `dist/`, their typecheck
  and build fail. As a precondition of UI-0, those callers set `run-frontend: false` and drop their
  frontend half, and ABO-local jobs (as `ci.yml:372-383` already has) run the frontend checks after
  `make web-deps`. A github-common input that runs `build_web.sh` before install, with the
  `.github/ghcommon-ref.txt` bump in the same PR, is the alternative if the owner prefers to keep the
  reusable frontend half.
* **Caches, filters and triggers.** Each `cache-dependency-path` adds `core/web/package-lock.json`;
  path filters (`frontend-ci.yml:11-17`, `e2e.yml:68`) add `core/web/**`; `ci.yml`'s `changes`
  classifier sets `web=true` for `core/web/*` and `scripts/ci/build_web.sh` as well as `web/*` (today
  only `web/*` and workflows, `ci.yml:81`); the PR image job's trigger list is every input of the image,
  so no PR that changes one can skip it: `core/**`, `go.mod`/`go.sum` and `core/go.mod`/`core/go.sum`,
  both Dockerfiles, `.dockerignore`, `scripts/ci/build_web.sh`, `web/**` and every `//go:embed` input
  (MOD2-16, MOD4-8); Dependabot's npm entry adds `/core/web`. The list is the S0 checklist in 09; this file owns the mechanism. The Docker
  frontend-stage change below lands in the UI-0 PR that creates `core/web`, because a `COPY` of
  `core/web/package.json` fails before that file exists; S0 changes only the Go stage and
  `.dockerignore` (MOD3-13).
* **`.dockerignore`** adds `**/node_modules` and `core/web/packages/*/dist`, so `COPY core/web/`
  never copies the host's `node_modules` (native rolldown/esbuild/oxc binaries) or a host-built `dist/`
  over the image's own install.
* **Docker frontend stage, in order (MOD2-16).** (1) Copy `core/web/package.json`,
  `core/web/package-lock.json`, every member's `package.json` (hello's included, because `npm ci`
  needs every workspace manifest) and run `npm ci` in `core/web`; (2) copy the members' sources and run
  the ordered build above; (3) copy `web/package.json` and `web/package-lock.json`, run `npm ci` in
  `web/` (its `file:` dependencies resolve `exports` to the `dist/` built in step 2); (4) copy `web/`
  and build it. Copying only the manifests before building is not enough, because the `file:`
  dependencies point at built `dist/` directories. The stage declares an optional `ARG BUILD_ID`,
  which `build_web.sh` uses when set (the context has no `.git` to derive one from); the Go stage
  receives no build ID (§7.6.3, FE4-1, MOD4-9).
* **After the split:** the specifier becomes an exact registry version (§7.17); nothing else changes.

### 7.3.3 Versions (D8)

Versions of `react`, `@mui/material`, `vite`, `vitest`, `@vitejs/plugin-react`, `@playwright/test`,
`zustand`, `eslint`/`typescript-eslint` and Node are those of [13](13-toolchain.md) §13.4, including
its 14-day release-age rule; the shell's peer ranges follow them. Two decisions are this file's:

| Package | Decision |
|---|---|
| `react-router` | peer `react-router ^8.5.1`, 13's range (data router, FE3-20). `react-router-dom` stops at 7.x. ABO migrates its 122 importing files (52 source, 70 test) **before** shell adoption (§7.19 step 1, FE-2, SIMP-25) |
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
  legacyRedirects: [],
} as const;
```

The keys mirror `module.UIDescriptor` (06 §6.4.2), including `legacyRedirects`, the one Go-declared
list of legacy page URLs (FE3-17). A freshness check in CI regenerates and diffs it,
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
  readonly legacyRedirects: readonly { readonly from: string; readonly to: string }[];
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
  // No redirects field: the shell builds the client redirects from
  // manifest.legacyRedirects, generated from the Go UIDescriptor.LegacyRedirects,
  // so the client list and the server's ui_route recording cannot drift (FE3-17).
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
  // Literal localStorage keys of shell preferences that exist before the
  // shell (ABO: its themeMode, activity-source-prefs and activity-ops-pinned
  // keys), read and written in place so no preference resets at adoption;
  // storageKey() names only keys that do not exist today (FE4-4, §7.7).
  legacyStorageKeys?: Readonly<Partial<Record<'themeMode' | 'activitySourcePrefs' | 'activityOpsPinned', string>>>;
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

Shell routes: `/` (home), `/login`, `/login/temp`, `/setup`, `/invite`, `/activity` (panes: `tasks`
default, `logs` for the system journal, `audit` for holders of `core:audit.view`, then module panes;
05-logging §5), `/settings/*`, `/system/*` (status, diagnostics, UI diagnostics), `/users`, and the
`*` not-found route. This settles the tasks page at `/activity` (09 S9 and 05-logging already use
it), not `/system/tasks` (FE-16).

* **One-time tokens travel in the fragment (SEC-21, FE2-8, SEC2-6).** `/invite#token=…` and
  `/login/temp#token=…`: the page reads `location.hash`, removes it with `history.replaceState`, and
  sends the token in the request body (`POST /api/core/v1/auth/invites/lookup|accept`,
  `POST /api/core/v1/auth/temp-login/consume`, 05-authn §4.13). A full page load therefore never
  carries the token to the server, an access log, a trace or a `Referer`. For an app that already has
  a SPA, each link form starts with the first build whose SPA can open it.
* **Invite links keep the path form until the shell (FE4-2).** The legacy SPA opens an invite only at
  `/invite/:token` (its public branch tests `pathname.startsWith('/invite/')`; `web/src/App.tsx:201-206`
  @748fd787a); `/invite#token=` would land a signed-out invitee on `/login` with the fragment dropped
  (`App.tsx:249-250`) and a signed-in one on `NotFound`. So from S4, when invite creation moves to core,
  core mints `/invite/<token>` (the link form is the app's choice, 05-authn §4.13 owns the option) and
  the legacy `AcceptInvite` sends the path token in the body of the same core calls. As at HEAD, that
  token reaches the server in the request path, where the `DefaultPathRules` invite shape redacts it
  from logs and spans. The fragment form starts with the shell build (S9).
* **Legacy link forms are ABO's, served by books to the end of their window (FE3-5, SEC3-16).** Links
  in the old forms exist only for ABO, so `ui.Handler` has no code for them. Books' legacy manifest
  rewrites the existing `GET /auth/temp-login?token=` to a 302 to `/login/temp#token=<token>` from S4
  (the legacy SPA gains a `/login/temp` route in the same build, §7.9.1), and serves
  `GET /invite/{token}` (Public, `RateAuth`) with a 302 to `/invite#token=<token>` and
  `Referrer-Policy: no-referrer` from S9, when the shell page reads only the fragment and path-form
  links minted from S4 to S9 must still open. `invite` is in books' `LegacyMount.SPAShared` (with
  `login`), so it is a reserved ID but never `NonSPA`: `GET /invite` stays the SPA page before and after
  S9, the more specific legacy pattern wins over the `/` owner, and the P8 shell-segment check passes.
  These two routes are the only implementation for the whole window of 09 §9.6, counted from the
  release that starts each (S4 for temp login, S9 for invite). Both paths are in 05-http-api's `DefaultPathRules`, so the
  one request that carries a token is redacted (`TestLegacyInviteLink_302NoToken`).
* **No `/logout` page (FE2-7).** Signing out is an action in the user menu:
  `POST /api/core/v1/auth/logout`, then navigate to `/login`. `logout` is a server segment in ABO (the ABS
  logout route), and a shell page with that segment would get a 404 problem+json on a full page load.

`ui.ReservedSegments()` (Go, package `ui`) lists the first segment of every shell route: `activity`,
`invite`, `login`, `settings`, `setup`, `system`, `users`. Server path segments (`api`, `assets`,
`static`, `ui`, `livez`, `readyz`, `metrics`, `health`, …) are `httpapi.ReservedSegments`, which
05-http-api owns; `assets` belongs there because `ui.Handler` serves `/assets/*` (FE2-11). The kernel
validates module IDs against `module.ReservedIDs()`, the union of both lists and
`rbac.ReservedNamespaces`, plus the first segments of every manifest's `LegacyPrefixes` and
`app.Options.ReservedAPINamespaces` at P1, and against every mount's `TopSegments` and `SPAShared` at
P7 (03 §3.6, 06 §6.4.1), so a module named `invite` or `activity` cannot shadow a shell page (FE-23).
In P8 the kernel also refuses a shell segment that is in `Router.NonSPASegments()`, which is every
mount's `TopSegments` minus its `SPAShared` (06 §6.5.1, `ProblemSPASegmentCollision`, the one name and
one test of this check, FE3-9), so an app's compat segment can never shadow a shell page either
(FE2-7), while ABO's `login` and `invite`, which the mount shares with shell pages, pass (FE3-1).
`ui.ReservedSegments()` lands at S2 as a dependency-free leaf of package `ui`, so module IDs are checked
against the shell list from S2 (FE3-8). A test generates the shell's own route list from the same
set, so the two cannot drift.

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
it in the shell-adoption PR series (§7.19 step 4), **after** ABO's `AuthContext`, `ToastProvider` and
`OperationTrackerProvider` have moved into the shell: before that, a component under test would read
ABO's own contexts and find no provider, and ABO still uses `useUnsavedChangesBlocker`, not
`useBlocker` (FE2-4). From then on it fixes every component that calls `useBlocker` under a
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

Permission `rbac.AnyAuthenticated`. Served by `internal/modulehttp` (L6, over the kernel's read-only
`ModulesView`, 06 §6.4.6, FE3-16), which ships at 09 S4 together with `internal/uihttp`
(`/ui/diagnostics`, `/ui/csp-report`) and `GET /api/core/v1/permissions`, so every binary a shell build
can meet in an R-test already has them (FE2-25). The shell's own pages also need S4 (05-config's
`GET /api/core/v1/settings/schema`, `GET`/`PATCH /api/core/v1/settings` and `KeyState` for
`<SchemaForm>`, enforced by `core:settings.manage`/`<id>:settings.manage`, so not before S4's rbac;
FE3-11), S7
(instance store for the Integrations and AI tabs) and S8 (`/system`, `/backup`, `/update`, `/health`)
native routes, which is why shell adoption follows S8 (09 S9 preconditions). Field
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
ID (§7.6.3), `""` when no shell bundle is served (zero `Bundle`, or a legacy mount owns `/`).
`disabled_modules` is present only for callers holding `core:admin`. A disabled module is absent from
`modules` (G1: no nav entry).

### 7.5.2 Algorithm (runs once after login and on every reconnect `resync`)

First, the shell compares its own compile-time core version `C.core` (the version of the
`@mediacore/shell` package it was built with, embedded by the preset as
`import.meta.env.MEDIACORE_CORE_VERSION`) with `S.core.version`:

| Case | Shell behaviour | Diagnostic kind |
|---|---|---|
| `major(C.core) != major(S.core)`; or major 0 and `minor(C.core) != minor(S.core)`; or major ≥ 1 and `minor(C.core) > minor(S.core)` | blocking "UI and server versions do not match" page; no module is mounted (the shell would call `/api/core/v1` routes the server lacks, or, in v0, routes a newer server removed: 12 §12.4.1 allows incompatible changes on any v0 minor, FE2-13) | `core-version-incompatible` |

Then, for each module `S` in the server list and the compile-time entry `C = registry[S.id]`:

| Case | Shell behaviour | Diagnostic kind |
|---|---|---|
| no `C` and `S.ui` empty | nothing to render | — |
| no `C` and `S.ui` non-empty | module UI not in this bundle | `ui-missing` |
| `major(C.version) != major(S.version)`; or major 0 and the minors differ; or major ≥ 1 and `minor(C.version) > minor(S.version)` | module not mounted (client would call APIs the server lacks, or a major break; the v0 rule as for core) | `version-incompatible` |
| item in any `C` slot (routes, nav, settings panels, widgets, banners, top-bar items, Users-page extensions, activity panes, setup steps) whose ID ∉ the matching `S.ui` list | that item dropped | `undeclared-descriptor` |
| `C` with no matching `S` | not mounted (disabled or unknown module) | `module-not-enabled` (admin only) |
| `S.ui` ID with no client item | ignored | `descriptor-without-ui` (debug log) |

**Stale-build rule (one rule, one place; FE3-3, OPS3-11, FE4-9).** The rule lives in the API
runtime's response middleware: ABO's `apiFetch` from 09 S0 to UI-0; from UI-0 `@mediacore/api-client`'s
runtime exports it as `checkBuild(response)`, which the runtime's own middleware and ABO's `apiFetch`
(still used for legacy calls until they are native) both call, so there is one implementation while
the two transports coexist; it is never deleted, so the pre-shell bundle and the shell bundle run the
same code. 05-http-api §7 lists it among the runtime's middleware. Comparing every API response with
the bundle's define `MEDIACORE_BUILD_ID`, it offers a reload when:

1. the response carries `X-App-Build` and the value differs from the define; or
2. the response is `/api/core/v1/modules`, `app.ui_build_id` is `""` and `X-App-Build` is non-empty and
   not `"dev"`: the server serves a legacy SPA at `/` (an S9 tab after a rollback to S8); or
3. `X-App-Build` is missing on a 2xx or 4xx response that carries a `Reporting-Endpoints` header
   naming a `csp` group: HEAD sets that header on every response (`securityHeadersMiddleware`,
   `internal/server/server_middleware.go:102-109` @748fd787a) and from S2 stage 5 writes it with every
   CSP, so it marks a response of the app, here a pre-S0 server (an S0+ tab after a rollback past S0).
   A response without it (a reverse proxy's 502 during a restart, an Access interstitial) never counts.
   `X-Request-ID` is not the discriminator: HEAD writes none, and every build that writes it also writes
   `X-App-Build` (FE4-3).

A define of `""` or `"dev"` never triggers it. In the shell the offer is the non-dismissable "new
version — reload" banner, the only one, because there is one rule (FE2-5, OPS2-13); module mounting
proceeds with the rules above. There is one build ID and one source for it (§7.6.3). The preset defines
`MEDIACORE_BUILD_ID = "dev"` under `vite serve`, so `make build-api` plus the Vite dev server never shows
the banner (FE-15). Diagnostics are shown to admins under **System → UI diagnostics**, logged
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
(FE-15). Because "workspace" checks nothing in-repo, 12's `check_core_version.py` also asserts that
every `core/web/packages/*/package.json` `version` equals `version.Version`, so the `C.core` that the
preset embeds cannot drift from the Go constant (FE2-13).

## 7.6 Go side: package `ui`

### 7.6.1 API

```go
package ui

// Bundle is an app's built shell SPA.
type Bundle struct {
	fsys    fs.FS  // rooted at the dist directory
	buildID string // from <meta name="mediacore-build" content="..."> in index.html (the per-build ID, §7.6.3)
	index   []byte
	scripts []string // CSP hash sources of inline <script> blocks in index.html
}

// FromFS roots fsys at dir, reads index.html, extracts the build ID and the
// sha256 hashes of inline scripts. It fails if index.html or the meta tag is
// missing: the meta tag is written by the shell preset, so only shell builds
// are Bundles. An app whose SPA is not yet a shell build passes the zero
// Bundle and serves its SPA from a legacy mount (§7.6.3).
func FromFS(fsys fs.FS, dir string) (Bundle, error)

// MustFromFS panics on error. Call it only from the embed_frontend file of
// an app (ui_embed.go); the !embed_frontend file returns ui.Bundle{} instead,
// because a non-embed build's embed.FS is empty and would panic (FE2-10).
func MustFromFS(fsys fs.FS, dir string) Bundle

func (b Bundle) IsZero() bool
func (b Bundle) BuildID() string

// AppOptions are the app's own contributions to the Handler the kernel
// builds. The app passes them as app.Options.UIOptions (06 §6.4.5), a
// compile-time value like Authenticators, and the kernel merges them into
// Options in P8 (FE3-4). The zero value adds nothing.
type AppOptions struct {
	// PageMiddleware wraps only index.html responses (page navigations), e.g.
	// ABO's Cloudflare service-token recovery (cf_recovery.go:36-60, from
	// internal/cfaccess). AppOptions is built in main before app.New, so a
	// func that needs state built during boot (ABO: the CF config and token
	// store, the Access team host) reads an atomic.Pointer the module fills in
	// its Start hook, and is a pass-through (or returns an empty list) until
	// then; P8 only wraps it (FE4-5). ui_route recording is not app code: see
	// Options.OnRedirectUse.
	PageMiddleware func(http.Handler) http.Handler

	// CSP: ExtraDirectives are appended to the default policy (§7.6.2).
	// ConnectSrcFrom returns extra connect-src hosts (bare DNS names,
	// validated as csp.go does today); it is called for every index.html
	// response and must be cheap (ABO: its Access team host, read from an
	// atomic pointer as csp.go:129-137 does today, so a changed team host
	// takes effect on the next page load with no restart).
	ExtraDirectives []string
	ConnectSrcFrom  func() []string
}

type Options struct {
	AppOptions // from app.Options.UIOptions

	// NonSPA decides which unmatched paths answer 404 instead of index.html.
	// The kernel passes httpapi's Router.NonSPA() predicate (05-http-api
	// §4.10): every claimed prefix (/api/, facades), the first segment of
	// every Router.Infra pattern (livez, readyz, metrics), health and metrics
	// explicitly, auth and events, and every mount's LegacyMount.TopSegments
	// minus its SPAShared (03 §3.6; never the first segments of
	// Manifest.LegacyPrefixes, which are reserved IDs only, FE3-1). For ABO
	// that covers today's eight segments, auth, socket.io, ping, status,
	// logout, health, metrics, public (spa_fallback.go:74-83), while login and
	// invite stay SPA pages (SPAShared), so a case or trailing-slash variant of
	// a probe (/health/, /HEALTH, /metrics/, /livez/) never answers 200 HTML
	// (FE2-6). Paths are normalised exactly as today's normalizeNonSPAPath
	// (lower case, path.Clean, query dropped) for this decision only; the
	// normalised path is never dispatched.
	NonSPA func(r *http.Request) bool

	Config  *config.Handle[Settings] // core.ui.* (§7.15); hot keys swap the policy atomically
	Metrics *metrics.Scope

	// Redirects are every enabled module's UIDescriptor.LegacyRedirects, and
	// OnRedirectUse records one use of the compat entry (surface ui_route)
	// the kernel registered for each at P7 (09 §9.2.9). The handler calls it
	// for a full page load whose path matches a From pattern, before serving
	// index.html, so the recording is core-generic and the same Go list feeds
	// manifest.gen.ts and the client redirects (FE2-16, FE3-17, FE4-5). Both
	// are set by the kernel; nil OnRedirectUse records nothing.
	Redirects     []Redirect
	OnRedirectUse func(from string)
}

// Handler serves the SPA:
//   - hashed assets under /assets/: Cache-Control "public, max-age=31536000, immutable"
//   - a missing /assets/* file, or any unmatched path whose last segment has a
//     file extension: 404, never index.html (a stale tab's chunk 404 must not
//     become a MIME error)
//   - a method other than GET/HEAD: 404/405 problem+json (05-http-api §4.10)
//   - a NonSPA path (after normalisation): 404 problem+json, never HTML
//   - any other existing regular file of the bundle's fsys (Vite's public/
//     files at the dist root, e.g. /favicon.svg, which index.html
//     references): served with Cache-Control "no-cache" (FE3-14); this rule
//     is checked before the extension rule above. index.html itself is never
//     served by this rule: GET /index.html takes the index rule below
//     ("no-store", full CSP, through PageMiddleware), as HEAD runs its SPA
//     middleware before every NoRoute file (static_embed.go:59-66, FE4-11)
//   - every other unmatched GET/HEAD: index.html, "no-store", with the full
//     CSP of §7.6.2, through PageMiddleware
//   (the pre-fragment /invite/<token> link is not the shell's: books' legacy
//   route answers it, §7.4.3)
//   - zero Bundle: the API-only placeholder page (today's static_nonembed.go behaviour)
// The policy lives in an atomic.Pointer refreshed on config change; nothing
// else is mutable after construction. Goroutine-safe; no package state (R5).
func Handler(b Bundle, opts Options) http.Handler

// ReservedSegments lists the first segment of every shell route (§7.4.3);
// module.ReservedIDs() includes them. Returns a fresh slice on every call
// (R5: no exported slice variable). It and the plain types ModuleManifest,
// UIIDs and Redirect below form the dependency-free leaf of package ui that
// lands at S2, so internal/modulehttp (L6, S4) has a type for modules[].ui;
// the rest of the package lands at S9 (FE3-8, FE4-6).
func ReservedSegments() []string

// ModuleManifest is one module's UI manifest: what /api/core/v1/modules
// reports and what manifest.gen.ts prints (§7.4.1). It lives in ui (L6), not
// in app (L9), so ui never imports app (R4); app.Description.UIManifests is a
// []ui.ModuleManifest (COR2-18). S2 leaf type (FE4-6).
type ModuleManifest struct {
	ID          string
	Version     string
	DisplayName string
	UI          UIIDs    // the module.UIDescriptor lists, copied by the kernel
	Permissions []string // the module's permission names
}

// UIIDs mirrors module.UIDescriptor (06 §6.4.2) field by field.
type UIIDs struct {
	Pages, Nav, SettingsPanels, Widgets, Banners, TopBarItems,
	UserPage, ActivityPanes, SetupSteps []string
	LegacyRedirects []Redirect // module.UIDescriptor.LegacyRedirects (FE3-17)
}

// Redirect is one legacy page path and its shell path.
type Redirect struct{ From, To string }

// WriteManifestTS prints a module's UI manifest as manifest.gen.ts (§7.4.1).
func WriteManifestTS(w io.Writer, m ModuleManifest) error
```

### 7.6.2 Content Security Policy

Responsibility is split (FE-4):

* **`core/ui` computes the shell policy.** The default is today's `cspBase` verbatim
  (`csp.go:57-67`), with `script-src 'self'` plus the inline-script hashes and `connect-src 'self'`
  plus `ConnectSrcFrom()`:
  `script-src 'self' <hashes>; connect-src 'self' <hosts>; default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; font-src 'self' data:; media-src 'self' blob:; object-src 'none'; base-uri 'none'; form-action 'self'; frame-ancestors 'self'; report-uri /api/core/v1/ui/csp-report; report-to csp`,
  then `Options.ExtraDirectives`. `core.ui.csp.report_only` switches the header to
  `Content-Security-Policy-Report-Only`. ABO's `contentSecurityPolicy()` and its team-host pointer stay
  in ABO until S9 (they are the legacy mount's `DefaultCSP` below) and then become ABO's
  `AppOptions.ConnectSrcFrom`; nothing of ABO's CSP code moves into core `httpapi` (FE3-10, 04).
* **`httpapi` stage 5 writes a CSP on every response** whose handler set none (05-http-api §4.2,
  FE-4): the SPA handler's policy above; for the legacy mount, its `DefaultCSP func(*http.Request)
  string`, computed **per request** (ABO passes today's `contentSecurityPolicy()`, `csp.go:95-106`,
  `129-138`: `script-src 'self'` with the hash of `index.html`'s inline theme script and `connect-src
  'self'` with the Access team host, re-read per response; a static `cspBase` would block the
  pre-paint script and the Access expiry redirect, FE2-1); otherwise `default-src 'none';
  frame-ancestors 'self'`. Server-rendered HTML outside the SPA (the ABS OIDC consent form,
  `handlers/abs/openid.go:1055`; placeholder pages) is therefore never without one, and a handler that
  sets its own `Content-Security-Policy` keeps it (today's rule). Stage 5 also writes
  `Reporting-Endpoints: csp="<owner's report URL>"` (`/api/v1/csp-report` for the legacy mount,
  `/api/core/v1/ui/csp-report` for `ui`), because Chromium prefers `report-to` over `report-uri`
  (`server_middleware.go:109` today). 05-http-api's `TestCSP_DefaultPerOwner` asserts that the legacy
  SPA response carries the inline-script hash and the configured team host.
* **Reports** go to `POST /api/core/v1/ui/csp-report` (Public, `RateAuth` class, body ≤ 8 KiB,
  logged rate-limited as `csp_report.go` does), registered on the core group by `internal/uihttp`
  from S4 (06 §6.4.6; COR2-19, FE2-25). Until shell adoption the legacy SPA reports to gin's
  `/api/v1/csp-report`; from S9, when gin's handler is deleted, `/api/v1/csp-report` becomes a legacy
  alias of the core route for the compatibility window.

`core.ui.csp.connect_src` and `report_only` are hot-reloadable: the handler swaps an
`atomic.Pointer[policy]` on config change, as `csp.go:129-137` does today. Nothing else in the handler
changes after construction (§7.14).

### 7.6.3 Mounting, route-chain placement and the ABO stages

**Ownership of `/` (FE-1, cross-cutting resolution 2).** Exactly one owner serves the `/` fallback:

| Phase | `app.Options.UI` | `/` owner | Notes |
|---|---|---|---|
| Before ABO adopts the shell build (09 S2 up to S9) | zero `Bundle` | the books `LegacyMount` with `ServesSPA: true`; gin keeps `NoRoute` SPA serving and CF recovery | from S2 the CSP and `Reporting-Endpoints` headers are written by `httpapi` stage 5 from the mount's per-request `DefaultCSP` (today's computed policy, §7.6.2), not by gin's removed middleware (FE2-1); `httpapi` registers no `/` pattern while a legacy mount owns it, and its `ServesSPA` fallback consults `Router.NonSPA()` before handing a request to gin, so one predicate serves both owners and the S8 probes `/livez/`, `/READYZ` never answer HTML (FE3-13, 05-http-api §4.10); `ui.Handler` is not mounted; `ui_build_id` is `""`; the stale-build rule (§7.5.2) runs in ABO's `apiFetch`, fed by the same build ID (below) |
| From shell adoption (09 S9) | `UIBundle()`, ABO's adoption of the hello pattern (below) | `ui.Handler`, mounted by the kernel with `Router.SPA` (05-http-api §4.10) | in the same PR: books drops `ServesSPA`; gin's `NoRoute` SPA serving, `setupStaticFiles` and the `static_embed.go` SPA code are deleted (the latter moves to `ui`); ABO passes `app.Options.UIOptions` (`PageMiddleware`: CF recovery from `internal/cfaccess`; `ConnectSrcFrom`: its Access team host; both read pointers books fills in its Start hook; FE3-4, FE4-5), which the kernel merges into `ui.Options`, adding its own `ui_route` recorder over books' `LegacyRedirects` from the URL-move release (§7.19 step 5); its compat segments reach `NonSPA` through its mount's `TopSegments` (not `SPAShared`); the stale-build rule keeps running, now in `@mediacore/api-client`'s runtime, and the shell banner renders it (FE3-3) |

**ABO adopts the hello pattern at S9 (FE2-10).** `web_embed.go` (`//go:build embed_frontend`)
defines `func UIBundle() ui.Bundle { return ui.MustFromFS(WebFS, "web/dist") }`, `web_nonembed.go`
(`//go:build !embed_frontend`) returns `ui.Bundle{}`, and `main.go:34` changes from
`server.SetEmbeddedFS(WebFS)` to `app.Options{UI: UIBundle()}`. Calling `MustFromFS(WebFS, …)`
unconditionally would panic on every API-only build, because the non-embed `WebFS` is an empty
`embed.FS`. S9 done-check: `make build-api && ./audiobook-organizer serve` answers `/` with the
placeholder.

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

**One build ID, one source: the embedded bundle (FE2-5, FE2-12, OPS2-13, FE3-2, FE4-1, MOD4-9).**
The build ID is generated by `scripts/ci/build_web.sh`, before Vite runs, and nowhere else. It uses
`BUILD_ID` when the caller sets it; otherwise it derives `<git rev-parse --short HEAD>-<commit unix
time>[-dirty]` from the checkout, so a rebuild of one commit gets one ID; where there is no `.git` and
no `BUILD_ID` (a Docker frontend stage built without the optional `ARG BUILD_ID`), it generates
`<UTC timestamp>-<8 random hex>`. No Go link step receives it, so no build path (Makefile,
Dockerfiles, goreleaser's `pre-build-script` inside the external reusable release workflow) has to
carry an environment variable to the linker, and goreleaser's config is unchanged:

* `build_web.sh` passes it to Vite, which writes it into the bundle as
  `import.meta.env.MEDIACORE_BUILD_ID` (`define`) and into `index.html` as
  `<meta name="mediacore-build" content="…">` (before S9 ABO's own `vite.config.ts` does this, from S9
  the `@mediacore/shell/vite` preset does). It is never a hash of chunk names, which would be circular.
* The server reads the ID from the bundle it embeds: from S2 `httpapi` stage 5 writes
  `ui.Bundle.BuildID()` (S9) or the `ServesSPA` mount's `LegacyMount.BuildID` (before S9; ABO reads it
  from its embedded `web/dist/index.html`) as `X-App-Build` on every response, legacy mount, 404 and
  410 included, otherwise `dev` (05-http-api §4.2, §8.1). Before S2 ABO's `main` reads the meta from
  `WebFS` and writes `X-App-Build` itself (09 S0). A server therefore always reports the ID of the
  bundle it serves; a mismatch between them cannot exist, and nothing is compared at boot.
* The PR image job asserts that the meta tag equals the `X-App-Build` header
  (`TestBuildID_HeaderEqualsEmbeddedMeta`).
* `/api/core/v1/modules` reports the served shell bundle's ID as `ui_build_id` (`""` before S9), and the
  one stale-build rule of §7.5.2 compares the header (and that field) with the define, in ABO's
  `apiFetch` from S0 and through the api-client runtime's `checkBuild` from UI-0; it is never deleted,
  so a tab left open across a rollback from S9 is still offered a reload (FE3-3, OPS3-11).
* `"dev"` under `vite serve` and in `make build-api` binaries (no embedded bundle), so `make build-api`
  plus `make web-dev` never shows the check.

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
* Mode storage key: `storageKey('shell', 'themeMode')` for a new app; ABO passes its existing key in
  `createShell({ legacyStorageKeys: { themeMode, activitySourcePrefs, activityOpsPinned } })`, with the
  literal values of today, so saved preferences carry over (the same trick `main.tsx:28-34` already
  uses). ABO's `localStorage` keys are read in place, never moved, renamed or copied, during the
  extraction, so a rollback from S9 to S8 keeps every preference and nothing deletes the old keys
  (FE3-18, FE4-4).
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
  `app.Description.Core` (06 §6.4.5) to `core/openapi/core.json`, and core CI freshness-gates it. The
  file lives outside `core/web` because that npm workspace is first created at UI-0; UI-0's api-client
  generator reads it from there (MOD3-4). The hello skeleton lands at S2 (06 §6.4.10), so `core.json`
  is producible from S2, before UI-0 needs it; a PR that adds a core route regenerates it in the same PR, and S4–S6
  done-checks include "`core.json` contains the stage's native routes" (FE2-9, MOD2-10). Apps do not
  emit `core`: an app's `openapi` subcommand writes only its modules' documents and `manifest.gen.ts`
  files (`manifest.gen.ts` from S9, with package `ui`).
* `@mediacore/api-client` = types generated from `core.json` for every `/api/core/v1` route +
  `createCoreClient()`, whose middleware carries today's `apiFetch` semantics: own deadline
  (`ApiTimeoutError`), SSO login-page bounce detection (`ApiAuthRedirectError`,
  `web/src/utils/apiFetch.ts:51`), `credentials: 'include'`, `X-Request-ID` surfacing, RFC 9457
  `Problem` as the error type (05-http-api §3.3), the 401 rule of §7.9, and the one stale-build rule
  of §7.5.2, carried over from ABO's `apiFetch` as the exported `checkBuild(response)` that `apiFetch`
  itself calls from UI-0, and never deleted (FE3-3, FE4-9). Each generated operation
  whose OpenAPI `x-mediacore-permission` is `public` is marked `skipAuthRedirect`. The shell creates
  one client in `ClientProvider`; `useApi()` returns it.
* **The generated client arrives before identity moves (FE-12).** `@mediacore/api-client` (runtime,
  generator, `make generate-api-client` in `core/web`) ships in a UI stage that precedes 09 S4, consumed
  by ABO through `file:`. 09 S4–S6 then switch UI calls straight to generated operations instead of
  rewriting `api.ts` calls twice.
* **Module clients are directories until the module is a package (FE-28).** ABO's books client is
  generated into `web/src/modules/books/api/` from `api/openapi/books.json`, on the same runtime; it
  becomes a package only when books does (§7.11). Module code never builds URLs by hand.
* The generator package carries the npm `overrides` entry of §7.3.3. The early spike that SIMP-27
  asked for was run by the frontend review (2026-11-04): 7.13.0 under the override on the TS 6 alias
  generates correctly from an OpenAPI 3.1 document with `type: ["string","null"]` and dotted component
  names. `make generate-api-client` failing is a CI failure; there is no fallback generator.
* Freshness gates (`make generate-api-client`, `make openapi-check`) follow 05-http-api; tygo is
  deleted once its routes are native. `opsgen` (raw/13 §8) generates the legacy `ops.ts` enums. They
  have more than one consumer: `useOperationsStore` and `services/api.ts:14-15,590`, and
  `utils/operationPolling.ts:7` with its 16 non-test importers, among them books pages that keep
  legacy calls after S9 (`Library.tsx`, the review lanes, `ITunesImport.tsx`, `stagedMetadataApply.ts`,
  `MaintenanceTab.tsx`, `ThrottlePanel.tsx`) (@748fd787a). So `opsgen` and its freshness gate stay
  until the last `ops.ts` importer is native, which is the end of §7.19 step 6, not S9; 05-http-api §7
  and 04's `opsgen` row state the same rule. At S5, when its input `internal/operations/state` moves to
  `mediacore/tasks`, `opsgen` is repointed to `tasks`' state constants and the gate proves `ops.ts`
  byte-identical (FE3-6, FE4-7).

## 7.9 Auth, session and realtime

### 7.9.1 Auth and session

* `AuthProvider` (from `AuthContext.tsx`) moves into the shell, calling
  `/api/core/v1/auth/{status,login,logout,setup,bootstrap}` and `/api/core/v1/me` (05-authn §4.13
  paths verbatim; `/auth/bootstrap` serves the `/setup` flow, FE2-18). `me.permissions` is the effective namespaced list; `usePermission` is advisory and fails
  closed on unknown/null, keeping today's `authEnabled: null` rule (`AuthContext.tsx:23-30`).
* **Session cookie and CSRF (FE-5, FE-6).** Cookie naming (`<AppID>_session`, `__Host-` when
  `Secure`, so two apps on one host never share a session), the handling of an unknown cookie and the
  cross-origin check are owned by 05-authn §4.3/§6 and 05-http-api stage 7; this file does not restate
  them (FE2-23). No credential type is exempt from the cross-origin check. Same-origin `fetch` sends
  `Sec-Fetch-Site: same-origin` from a trustworthy origin, so production needs nothing extra. The Vite
  dev proxy is the exception: with `changeOrigin: true` (`web/vite.config.ts:39-43`) it rewrites `Host`
  to the target, and a dev server opened at a LAN address (phone testing) is not a trustworthy origin,
  so the browser sends no `Sec-Fetch-Site` and `Origin` ≠ `Host` would refuse every POST; the preset's
  proxy therefore also rewrites `Origin` to the target origin, in dev only (§7.10, FE3-19). ABO keeps
  its configured `session_id`.
* **The 401 rule (FE-7).** The runtime's `onUnauthorized` (auth state reset, navigate to
  `/login?return=<safe path>`; `safeReturn` moves to the shell) runs only for a 401 whose problem
  `code` is one of `authentication_required`, `expired`, `revoked`, `inactive_user`,
  `invalid_credential`, on a request **not** marked `skipAuthRedirect`. Every Public auth route (login,
  setup, bootstrap, invite accept) is `skipAuthRedirect`, so a wrong password or bootstrap token shows
  its error in place and `/setup` never redirects to `/login`. A 503 `auth_unavailable` keeps the
  session and shows a retry banner; it never logs the user out. An `ApiAuthRedirectError` triggers a
  full reload so the SSO proxy can run.
* **Permission atoms (FE-11).** The UI never hard-codes a permission string. The shell's Users and
  API-keys pages read canonical permissions, titles and descriptions from
  `GET /api/core/v1/permissions` (05-rbac §4.7, `PermissionSpec.Title`); module hooks use the
  generated permission constants of their manifest (`manifest.permissions`). When 09 S4 switches the
  server to canonical names, the same build replaces ABO's five legacy constants
  (`useLibraryPermission.ts:9,11`, `useCanOrganizeLibrary.ts:9`, `useCollections.ts:24`,
  `ThrottlePanel.tsx:24`) and the `APIKeysTab` scope list (`:49-61`). The ban is an **AST rule**, an
  ESLint `no-restricted-syntax` selector, not a lexical grep (a grep for dotted strings matches 744 MUI
  palette keys and legacy event names, FE2-17): it fails on a string literal passed to
  `usePermission`/`useAction`/`hasPermission`, assigned to a `permission` or `*_PERMISSION` property or
  constant, or placed in the API-key scope list, unless it comes from `manifest.permissions` or the
  generated client.
* `/invite` and `/login/temp` render before auth gates and read their token from the fragment
  (§7.4.3); `/setup` and the first-run wizard (shell frame plus every enabled module's `setup` steps
  whose `isRequired()` is true) keep today's gate order (`App.tsx:200-251`). From 09 S4, when invite
  creation switches to core, invite links keep the path form `/invite/<token>` that the legacy SPA
  routes today (§7.4.3, FE4-2): ABO's legacy `AcceptInvite` takes the token from the path, as now, and
  sends it in the body of `POST /api/core/v1/auth/invites/lookup|accept`; the fragment form, and the
  books 302 that turns path-form links into it, start with the shell build (S9), whose `/invite` page
  reads only `#token=` (`TestLegacyInviteLink_EndToEnd`). The same S4 build adds a
  `/login/temp` route to the legacy SPA's unauthenticated **and** authenticated branches that reads
  `#token=`, removes it with `history.replaceState` and calls
  `POST /api/core/v1/auth/temp-login/consume` (05-authn §4.13), because admin password reset and temp
  login mint `/auth/temp-login?token=` links that books' legacy route now 302s to `/login/temp#token=`,
  and today's unauthenticated `*` route would drop the fragment (`web/src/App.tsx:249-250`; FE3-5,
  `TestLegacyTempLoginLink_EndToEnd`). Previous-release tabs keep working on the `/api/v1/auth/me`
  alias, whose `permissions` field stays the reverse-mapped legacy atoms (05-rbac §6.3, FE3-7).

### 7.9.2 Realtime client

The wire contract (endpoint `/api/core/v1/events`, framing, resume, caps, topics) is 05-realtime's.
The shell side:

* **Transport (FE-8).** A `fetch()` + `ReadableStream` SSE reader in `@mediacore/shell` (no new
  dependency, about 150 lines), not native `EventSource`. On every reconnect it schedules, it sends
  the last seen ID as the `Last-Event-ID` header (or `?last_event_id=`, which 05-realtime §4.3
  accepts), so backoff (3 s doubling to 60 s, today's `eventSourceManager.ts:25-29`) and resume work
  together. It sees the response status and `Retry-After`: a 401 goes through the rule of §7.9.1, a
  429/503 waits `Retry-After`. SSO-redirect detection is 05-realtime §4.8's normative rule (`fetch`
  with `redirect: "manual"`; an opaque-redirect response is an `ApiAuthRedirectError`), which the
  reader implements verbatim; `response.redirected` is never consulted (FE2-20). It
  dispatches by the envelope's `topic` field to `useTopic` subscribers, so named-event frames are not
  needed; 05-realtime drops the `event:` line from the native stream so that a plain `EventSource`
  fallback still receives every frame.
* **One stream per browser per app (FE-9, FE2-2).** Tabs elect a leader; only the leader opens the
  stream and fans frames out over a `BroadcastChannel('<appId>.realtime')`, and followers subscribe
  through it. The election uses the Web Locks API (`navigator.locks.request('<appId>.realtime', …)`)
  when `navigator.locks` exists. Browsers expose it only in secure contexts, and the six-connection
  limit bites exactly on plain-HTTP LAN deployments (HTTPS listeners negotiate HTTP/2), so otherwise
  the election is a `BroadcastChannel` heartbeat: the leader announces every 2 s, followers take over
  after 3 missed beats, ties are broken by the lowest tab ID. In both modes the leader releases
  leadership on `pagehide` and `freeze`, so a frozen background tab cannot starve the others. This
  keeps one connection per app regardless of tab count (HTTP/1.1 allows 6 per host across tabs) and
  stays inside the hub's per-user caps (05-realtime §4.5).
* **Last resort (FE3-15, 05-realtime §4.8 item 7).** When neither `navigator.locks` nor
  `BroadcastChannel` exists, there is no way to elect or fan out, so each **visible** tab opens its own
  stream and closes it on `visibilitychange` to hidden and on `pagehide`; at most one connection per
  visible tab.
* **The leader's subscription set (FE2-21).** The leader subscribes to `*` minus
  `core.task.log-appended.v1`, and `useTopic` keys filter on the client. Log lines are fetched by the
  tab that shows a run: a keyed stream that tab opens itself for that run (counted in the same
  per-user bucket) or `GET /api/core/v1/tasks/runs/{id}/log` over REST, so a chatty log topic neither
  reaches every tab nor evicts the resumable window (05-realtime open item 2).
* **When the UI moves to the native stream (FE-10, FE2-3).** 09 S6 ships the hub plus legacy
  projections with **no client change**; the UI keeps `eventSourceManager` and the operations stream
  until shell adoption (09 S9), when the shell's realtime client replaces both. The Activity page's
  REST side does not move ahead of its stream: from S5 to S9 `useOperationsStore` and `ActivityLog`
  keep their hand-written `api.ts` operations calls on the legacy paths (served by the task aliases,
  whose legacy shape is `LegacyPath.Shape`, 05-http-api §3.1, and is not in the generated client),
  with the legacy enums from `opsgen`'s `ops.ts` (kept until its last importer is native, §7.8); REST and stream move to the
  native vocabulary and the generated core client together at S9, so one store never mixes native runs
  with legacy `op.*` frames (FE3-6). Until S6 provides the projections, a **books-registered bus
  subscriber** feeds the three legacy streams, A (`/api/events`), B (`/api/v1/operations/events`) and
  C (`/api/v3/ops/events`), so live progress continues on S5; core never calls ABO code (05-realtime
  §4.9, FE3-12). It forwards only kinds that have a legacy def ID, translated back to that ID so HEAD's
  per-event filters see what they see today, never a `core.*` kind, and blanks run views for stream A
  (SEC3-9). The hooks
  are `useTopic<T>(topic, onEvent, {key})` and `useResync` everywhere (05-realtime §11 uses the same
  signature).
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
* `define`: `MEDIACORE_BUILD_ID` (the per-build value from `build_web.sh`, §7.6.3; `"dev"` under
  `vite serve`), `MEDIACORE_CORE_VERSION`;
* dev server proxy for `/api` to the Go server, which rewrites both `Host` (`changeOrigin`) and
  `Origin` to the target origin, dev only, so a dev server opened at a LAN address passes the
  cross-origin stage (FE3-19); build ID meta tag; theme boot script.

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
| `pages/Users.tsx` | shell `/users` | permissions and titles from `/api/core/v1/permissions` (§7.9.1); `GuestPhoneKeys` (CF guest tokens, stays in ABO per 09 S4) and `UserMergeDialog` (`Users.tsx:8,12-13`) become books `userPageExtensions` |
| `pages/UserMerge.tsx` | books `userPageExtensions` row action | account merge stays in ABO (`internal/usermerge`; auth exposes only the `StageUserMerge` participant, 05-authn §3.1), so it is books UI (FE3-18; O3 closed) |
| `pages/ActivityLog.tsx` (3,320 LOC) + `stores/useOperationsStore.ts`, `operationGrouping.ts`, `utils/{operationPolling,followOperation,trackedOperations}.ts`, `components/layout/OperationsIndicator.tsx` | shell `/activity` (tasks pane over `/api/core/v1/tasks`) | audiobook op labels/colours become books `taskKinds`; the book feed, the `versionApi` undo preflight and `PendingFileOpsBanner` (`ActivityLog.tsx:7,60`) become a books `activityPanes` entry; from S5 to S9 `useOperationsStore`'s `* as api` import (`useOperationsStore.ts:7`) keeps its hand-written legacy-path calls and `opsgen`'s enums; at S9 it is rewired to the generated core client against native routes and moves to native REST and stream together (§7.9.2, FE2-3, FE3-6) |
| `pages/System.tsx` (+ `components/system` Storage, Quota, SystemInfo, Maintenance, ThrottlePanel) | shell `/system` | `ChapterConsolidationCard`, `ManualFixesCard` → books `widgets` (placement `system`); calls the S8 native routes (`/system`, `/backup`, `/update`, `/health`, FE2-25) |
| `pages/Settings.tsx` | shell `/settings` frame | generic tabs (Security, API Keys, Plugins → Integrations, AI backends, Scheduled tasks, Performance, Temp Login, System Info) in shell; Library, iTunes Import, Metadata, Dedup, Paths, OpenLibraryDumps, BlockedHashes, Embedding → books `settingsPanels`; Deluge → generic adapter-instance form rendered by `<SchemaForm>` (§7.4.5); needs the S4 settings schema/`KeyState` API (`/api/core/v1/settings`, `/settings/schema`, FE3-11) and the S7 instance store (FE2-25) |
| `pages/Dashboard.tsx` | shell `/` home | library counts and scan trigger → books widget (placement `home`) |
| `pages/Diagnostics.tsx` | shell `/system/diagnostics` frame | AI-dedup diagnostics → books widget |
| `pages/FileBrowser.tsx` | books page `/books/files` | uses ui-kit `ServerFileBrowser` |
| `components/wizard/WelcomeWizard.tsx` (989 LOC) | shell first-run frame + books `setup` | the wizard gate `config.root_dir \|\| config.setup_complete` (`App.tsx:87-89`) becomes books' `setup.isRequired` |
| `contexts/AuthContext.tsx`, `components/toast/ToastProvider.tsx`, `stores/useAppStore.ts` | shell | `AuthContext`'s `* as api` import (`AuthContext.tsx:16`) is rewired to the generated core client before the move |
| `lib/storageKeys.ts` (21 keys, 13 of them audiobook-specific, `storageKeys.ts:7-30` @748fd787a) | split (FE4-4) | the books keys (`itunes_import_settings`, the `library*`, `metadata-review-*` and `dedup-*` keys) move to `web/src/modules/books/storageKeys.ts` with their literal values; the shell keys (`themeMode`, `activity-source-prefs`, `activity-ops-pinned`) stay ABO literals passed through `createShell({ legacyStorageKeys })` (§7.4.2, §7.7), so no audiobook key name enters `core/web` (R1) and no preference resets |
| `components/operations/OperationTrackerProvider.tsx` | shell `TaskTrackerProvider` (follow a run app-wide) + books provider for merge/combine/purge locks | |
| `components/layout/{MainLayout,TopBar,UserMenu,sidebarSelection}.tsx`, `Sidebar.tsx` minus its arrays | shell | `ReviewBanner` and `usePendingFileOps` (`MainLayout.tsx:9-10,25`) become books `banners`; `librarySubItems`/`menuItems` (`Sidebar.tsx:53-100`) become books `nav` (with children) plus shell entries; the Review badge becomes `useBadge: useReviewCount` where `const useReviewCount = () => useReviewStore(s => s.count)` (FE-22) |
| `components/common/*` (ConfigurableTable, ServerFileBrowser, PathLinks, BulkConfirmDialog, FilterTagBar), `ErrorBoundary`, `KeyboardShortcutsDialog`, `AnnouncementBanner` | ui-kit (presentational) / shell (stateful) | |
| hooks `useAsyncAction`, `useKeyboardShortcuts`, `useDebouncedSearch`, `useRowSelection` | ui-kit | `useUnsavedChangesBlocker` deleted at shell adoption: the react-router 8 data router provides `useBlocker` |
| `utils/apiFetch.ts`, `apiResponse.ts`, `permissionError.ts`, `apiKeyExpiry.ts`, `safeReturn.ts`, `settingLocks.ts` | api-client runtime / shell | |
| `services/eventSourceManager.ts` | replaced by the shell realtime client at shell adoption (§7.9.2) | |
| `services/api.ts` (8,747 LOC) | split: auth/users/operations/system/settings → generated core client (from the UI stage before 09 S4); books routes → generated books client in `modules/books/api/`; hand-written calls to `/api/v1` remain in `modules/books/legacyApi.ts` during the compatibility window and shrink route-group by route-group | |
| `services/activityApi.ts` (256), `collectionApi.ts` (193), `fileOpsApi.ts` (29), `playlistApi.ts` (126), `readingApi.ts` (63), `versionApi.ts` (212) | books: generated books client where the route is native, else `modules/books/legacyApi.ts` during the window (FE-28) | none is shell code |
| `theme.ts` | ui-kit `createMediaTheme` + books `themeExtend` (signal palette) | |
| `generated/ops.ts`, `types/generated/*` | replaced by OpenAPI-generated types | `ops.ts` and `opsgen` stay until the last `ops.ts` importer is native (end of §7.19 step 6; the Activity page stops importing it at S9, books pages through `operationPolling` later; FE3-6, FE4-7) |
| all other pages/components (Library, BookDetail, Authors, Series, Works, Collections, Playlists, TrashedVersions, review workspace, …) | `modules/books/` | URLs move to `/books/...`; old paths and `ABS_REDIRECTS` (`web/src/utils/absRedirects.ts:17-25`) become books' Go `UIDescriptor.LegacyRedirects` (exported through `manifest.gen.ts`; the kernel registers a `ui_route` entry per row and `ui.Handler` records full page loads that match one, FE3-17, FE4-5; filled in the step-5 release, FE4-8), including the pages the shell absorbs: `/dashboard → /` (today's home, `App.tsx:256-258`), `/files → /books/files`, `/diagnostics → /system/diagnostics`, `/operations → /activity`, `/maintenance → /system` (FE2-16) |

## 7.12 Lifecycle

Browser: load → read build ID → `GET /api/core/v1/auth/status` → (login) → `GET /api/core/v1/me`
→ `GET /api/core/v1/modules` → core-version check and intersect (§7.5.2) → module `setup.isRequired`
checks → build router → realtime leader election and stream (§7.9.2) → render. On `resync` or
reconnect: re-fetch `me` and `modules`, re-intersect; a module that disappeared (disabled and
restarted) unmounts and its routes show `NotFound`.

Server: `UIBundle()` runs in the app's `main` before `app.New`; only its `embed_frontend` variant
calls `ui.MustFromFS`, so a malformed shell bundle (no `index.html`, no build meta) fails boot exactly
when the tag is set, and an API-only build passes the zero `Bundle` and serves the placeholder
(FE2-10). The kernel mounts `ui.Handler` in P8, with `app.Options.UIOptions` merged into its
options, unless a legacy mount owns `/` (06 §6.5.1, FE3-4). There is no boot-time build-ID check:
the served bundle is the only source of the ID (§7.6.3, FE4-1).

## 7.13 Error semantics

| Error path | Behaviour | Why |
|---|---|---|
| Shell core version incompatible with the server's | blocking mismatch page, no module mounted, diagnostic `core-version-incompatible` | the shell would call core routes that do not exist (FE-15) |
| Unknown module ID in client registry vs server | not mounted, admin diagnostic | G3; old decision 8 |
| Version-incompatible module | not mounted, diagnostic | G3 |
| Client descriptor ID not declared by server (any slot) | item dropped, diagnostic | G3 |
| Stale SPA build (the §7.5.2 rule: header differs, or the server serves a legacy SPA, or an app response without `X-App-Build`) | banner, keep working | reload is the user's choice; APIs still versioned |
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
browser per app** (Web Locks leader, or a `BroadcastChannel` heartbeat election outside secure
contexts, fanning out over `BroadcastChannel`, §7.9.2), and at most one per visible tab in a browser
without `BroadcastChannel` (the last-resort mode, FE3-15); api-client de-duplicates concurrent
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
| `buildId.test.ts` | api-client runtime (`checkBuild`) + ABO `apiFetch` + shell | the §7.5.2 rule: header mismatch, legacy-SPA server (`ui_build_id` `""` with a non-dev header) and a header-less 2xx/4xx carrying `Reporting-Endpoints: csp=…` offer a reload; a header-less 502 without it does not; skipped for `""` and `"dev"` defines; `apiFetch` and the runtime call the one `checkBuild`; the define and the meta tag carry the same ID (FE3-2, FE3-3, FE4-3, FE4-9) |
| `TestBuildID_HeaderEqualsEmbeddedMeta` | 06 §6.10; PR image job | `X-App-Build` equals the embedded meta for a `make build` binary, a Docker image (with and without `BUILD_ID`) and a goreleaser artefact; no build path links an ID (FE3-2, FE4-1) |
| R-test row "S9 tab, roll back to S8, reload offered" | 09 §9.10 | an open S9 shell tab is offered a reload by the stale-build rule after a rollback to S8 (FE3-3, OPS3-11) |
| `defineModule.types.test.ts` (`// @ts-expect-error` on undeclared IDs in every slot) | shell, typecheck | compile-time descriptor safety |
| `nav.permissions.test.tsx` | shell | hidden nav for missing permission; `useBadge` called inside `<NavBadge>` |
| `auth.flow.test.tsx` | shell | session-ending 401 redirects; 401 on login/setup/bootstrap shows the error in place; 503 `auth_unavailable` keeps the session; SSO bounce reload |
| `realtime.resume.test.ts` | shell | resume after a scheduled reconnect replays from the last ID; `Retry-After` honoured; 401 routed through the auth rule; an opaque redirect is an `ApiAuthRedirectError` (05-realtime §4.8) |
| `realtime.leader.test.ts` | shell (fake `navigator.locks` + `BroadcastChannel`, a run with `navigator.locks` undefined, and a run with both undefined) | one stream for several tabs; leader handover; heartbeat election without Web Locks (takeover after 3 missed beats); leadership released on `pagehide`/`freeze`; the leader's set excludes `core.task.log-appended.v1`; with neither API, one stream per visible tab, closed when hidden (FE2-2, FE2-21, FE3-15) |
| `schemaForm.test.tsx` | shell | every widget row of §7.4.5, locked and restart-required states, secret never echoed |
| ESLint permission-literal rule (`no-restricted-syntax`) | ABO web `make ci` | no string literal in a permission position (`usePermission`/`useAction`/`hasPermission` argument, `permission`/`*_PERMISSION` property or constant, API-key scope list) unless from `manifest.permissions` or the generated client (FE-11, FE2-17) |
| `reservedSegments.test.ts` | shell + `core/ui` | shell route table equals `ui.ReservedSegments()`; no `/logout` page |
| `TestReservedSegmentsDisjointFromNonSPA` | kernel (06 §6.10; the one test of the P8 check, FE3-9) | a `TopSegments` entry that is a shell segment fails boot with `ProblemSPASegmentCollision`; a `SPAShared` shell segment (`login`, `invite`) passes (FE2-7, FE3-1) |
| `TestLegacyInviteLink_302NoToken` | ABO books legacy routes + `httpapi` | from S4: `GET /auth/temp-login?token=` answers 302 to `/login/temp#token=…`; from S9: `GET /invite/<token>` answers 302 to `/invite#token=…` with `Referrer-Policy: no-referrer`; before S9 `GET /invite/<token>` serves the legacy SPA page; `GET /invite` serves the SPA page throughout; the access log and span show the redacted path (SEC2-6, FE3-5, SEC3-16, FE4-2) |
| `TestLegacyTempLoginLink_EndToEnd` | ABO e2e, S4 done-check | admin reset on the S4 build → link → the legacy SPA's `/login/temp` route consumes the fragment → logged in (FE3-5) |
| `TestLegacyInviteLink_EndToEnd` | ABO e2e, S4 done-check, repeated at S9 | an invite created on the build in which core auth decides requests mints `/invite/<token>`; opened signed out, it shows the invite page, accepting it signs the invitee in; opened signed in, it shows the invite page, not `NotFound`; at S9 a path-form link minted on S8 still opens through the 302 (FE4-2) |
| `theme.contrast.test.ts` | ui-kit | contrast in both schemes |
| `runtime.errors.test.ts` | api-client | envelope decoding, timeout, bounce detection, `skipAuthRedirect` from `x-mediacore-permission: public` |
| `check-singletons.mjs` | core/web CI | one copy of each peer singleton and of `react/compiler-runtime` |
| ESLint import boundary | core/web `make ci` from UI-0 | no specifier outside `core/web` (FE-16) |
| bundle check | core/web e2e and ABO e2e | `/login` with an empty cache fetches no page chunk; entry and `mui` size budgets (FE-18) |
| `TestHandler_AssetsImmutable`, `_IndexNoStore` (incl. `GET /index.html`: `no-store`, full CSP, through `PageMiddleware`, FE4-11), `_CSPPolicyVerbatim`, `_CSPHotSwap`, `_API404JSON`, `_Placeholder`, `_ServesRootPublicFile` (a `dist/` root file such as `/favicon.svg` is served with `no-cache`, FE3-14) | `core/ui`, Go | server semantics |
| `TestHandler_NonSPASegments` (incl. case, `//`, trailing-slash variants, `/health/`, `/HEALTH`, `/metrics/`, `/livez/`, with the `Router.NonSPA()` predicate), `TestHandler_NonGETRefused`, `TestHandler_MissingAssetIs404`, `TestHandler_PageMiddlewareOnlyOnIndex` | `core/ui` | FE-3, FE2-6 |
| port of `static_embed_recovery_test.go` | ABO, booted through `app.New` with `app.Options.UIOptions` (not a hand-built `ui.Handler`) | CF recovery still runs on page loads only, and the Access team host is in `connect-src` of the shell's policy (FE3-4); a page load during boot, before books' Start hook fills the pointers, serves `index.html` with no panic (FE4-5) |
| `TestHandler_RecordsLegacyRedirectUse` | `core/ui` + kernel | a full page load of a `LegacyRedirects` `From` path calls `OnRedirectUse` once with that pattern; an API call, an asset or a shell path does not; with no `LegacyRedirects` nothing is registered (FE4-5, FE4-8) |
| `TestBundle_MissingMeta` | `core/ui` | fail-closed for shell bundles |
| API-only boot | ABO S9 done-check | `make build-api && ./audiobook-organizer serve` answers `/` with the placeholder (FE2-10) |
| `TestHandler_OutsideRouteChain` | `core/ui` + `httpapi` | no rate limit or permission stage on SPA responses; listed in the infra set (FE-25) |
| e2e `shell.spec.ts` | `core/web/e2e`, Playwright (chromium, webkit) against `examples/hello` built with `-tags embed_frontend` | login, merged nav, version-mismatch hiding, core-version mismatch page, disabled module absent after restart (G1 UI half), stale-build banner |
| e2e `tabs.spec.ts` | same, served over plain HTTP/1.1 on a **non-loopback** address (the runner's LAN interface, or a hostname resolving to it), so `isSecureContext` is false | six tabs all load and receive realtime over one stream, through the heartbeat election (FE-9, FE2-2) |
| e2e `two-apps.spec.ts` | two example binaries on one host, different ports | signing in to one does not log out the other (FE-6) |
| ABO e2e (existing 39 specs) | `web/tests/e2e` | regression across every step of §7.19; route URLs updated with redirects tested |

## 7.19 Migration from ABO

Order; each step is shippable on its own and keeps the SPA served by the books legacy mount until
step 4 (FE-1, FE-2, SIMP-25):

1. **react-router 7 → 8 inside ABO (UI-0)**, still the declarative `<BrowserRouter>`: an import swap
   in the 122 files (52 source, 70 test) plus the chunk regex at `vite.config.ts:81-82`. A PR series
   that precedes everything below; 13's exception for `react-router-dom` ends here, before
   `createShell`. Done when `! grep -rq react-router-dom web/package.json web/src web/tests
   web/vite.config.ts` (FE2-19).
2. **Create `core/web` (UI-0)** with `@mediacore/api-client` (runtime and generator) only, the
   `make web-deps` entry point and every build site of §7.3.2, the `.dockerignore` lines, the lockstep
   check and the import-boundary rule. `ui-kit` and `shell` are not created here (04 §8.1 and 09 UI-0
   name the same stage; FE2-4).
3. **Generated core client consumed by ABO via `file:`** from UI-0, generated from the `core.json`
   that the S2 hello skeleton emits into `core/openapi/core.json` (§7.8); 09 S4–S6 switch `api.ts`
   sections straight to generated operations, S4's build replaces the legacy permission constants and
   adds the legacy `/login/temp` route while invite links keep their path form (§7.9.1, FE4-2), and from S5 to S9 the Activity page keeps its
   hand-written legacy-path calls (§7.9.2, FE3-6).
4. **Shell adoption (09 S9), one PR series.** Create `@mediacore/ui-kit` and `@mediacore/shell`
   (with `/vite` and `/testing`) and add hello's web package to the workspace; cut the audiobook
   imports out of the shell-classified files into books slots (§7.11) and move them into `core/web`;
   switch ABO `main.tsx` to `createShell` with the data router; **then** rewrite `renderWithProviders`
   onto `renderInShell`, which needs the providers to have moved (FE-17, FE2-4); delete
   `useUnsavedChangesBlocker`; the shell realtime client replaces `eventSourceManager` and the
   operations stream, and the Activity page moves to native REST and stream together with the
   generated core client (it no longer imports `ops.ts`; `opsgen` stays for books' `operationPolling` users, §7.8); the preset writes the build-ID meta tag, and the
   stale-build rule keeps running in the api-client runtime (§7.5.2, §7.6.3); ABO adopts the
   `UIBundle()` pattern and `main.go` passes it to `app.Options.UI` together with `app.Options.UIOptions`
   (FE3-4); books drops `ServesSPA` and gin's SPA serving is deleted in the same PR; invite links switch
   to the fragment form and books' legacy `/invite/{token}` 302 starts, beside the `/auth/temp-login`
   302 that has served since S4, both manifest routes (§7.4.3, §7.6.3, FE4-2). Preconditions: UI-0 and the S4–S8 native routes the
   shell pages call (§7.5.1, FE2-25). Done when, among the e2e checks, `make build-api &&
   ./audiobook-organizer serve` answers `/` with the placeholder (FE2-10).
5. **Server manifest and intersection on** (§7.5); ABO URLs move to `/books/...` with redirects
   (§7.11). Books fills `UIDescriptor.LegacyRedirects` in this release, not before, so the kernel's
   `ui_route` entries exist, page loads are recorded and 09's C9 clock starts only once a legacy page
   path is really a redirect; steps 4 and 5 may ship in one release (FE4-8).
6. Replace the remaining `api.ts` and sibling service sections by generated clients as their native
   routes ship (09).

## 7.20 Open items

| # | Item | Owner |
|---|---|---|
| O1 | TS 7 declaration emit completeness for the three packages (fallback recorded in 13) | 13-toolchain |
| O6 | Data-fetching library: none mandated; revisit TanStack Query only if module stores duplicate cache/resync logic | 07 |
| O7 | Sub-path deployment (`core.http.base_path` across `httpapi`, cookie path, SSE and `<base href>`), only on owner demand | 05-http-api, 05-authn, 07 |

Closed in 1.4.0: O3 (account merge stays in ABO, `internal/usermerge`; `UserMerge.tsx` is a books row
action, §7.11, FE3-18).

Closed in 1.1.0: O2 (realtime path and the shutdown topic: `/api/core/v1/events`,
`core.stream.closed.v1`), O4 (`openapi-typescript` works on TS 6 under the override, §7.3.3), O5
(CSRF owned by 05-http-api §4.2's `CrossOriginProtection` stage). The schema-driven settings renderer
that 1.0.0 left undesigned is specified in §7.4.5 and owned by this file.

## Objections to 03

None open. 03 v1.6.0 §3.6 carries the shell segment list of §7.4.3 (no `logout`, `livez` instead of
`healthz`, `assets` under `httpapi`), the split compat inputs (FE2-11), `SPAShared` (`login`, `invite`)
with `NonSPA` = `TopSegments` minus `SPAShared`, and `ui.ReservedSegments` from S2 (FE3-1, FE3-8); 03 v1.4.0 already used
`/activity` as the shell UI-route example (the v1.1.0 objection).
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

Round 2 (2026-11-04), findings applied to this file:

- FE2-1 — legacy-phase CSP written by stage 5 from the mount's per-request `DefaultCSP` (today's computed policy with hash and team host); `Reporting-Endpoints` per owner (§7.6.2, §7.6.3).
- FE2-2 — leader election falls back to a `BroadcastChannel` heartbeat outside secure contexts; release on `pagehide`/`freeze`; `tabs.spec.ts` on a non-loopback address.
- FE2-3 — Activity page stays on the legacy-shape task aliases from S5 to S9 and moves REST and stream together at S9; `core/tasks` feeds the legacy hub until S6 (§7.9.2, §7.11, §7.19).
- FE2-4 — `renderInShell` rewrite moved inside the shell-adoption series after the providers move; `ui-kit`/`shell` created at S9, UI-0 creates `api-client` only.
- FE2-5 / OPS2-13 — one build ID generated once per build, shared by the S0 `X-App-Build` check (header written by stage 5 from S2, missing header = mismatch) and the S9 shell banner; S0 check deleted at S9. (Superseded in round 3 by FE3-3: the check is never deleted.)
- FE2-6 — `NonSPA` covers today's eight segments, infra probe segments and legacy exact routes; `livez`; extra test cases.
- FE2-7 — `/logout` page dropped (user-menu action); kernel P8 disjointness check; tests.
- FE2-8 / SEC2-6 — `/invite` and `/login/temp` with fragment tokens; `ui.Handler` 302s the old `/invite/<token>` form with `no-referrer`; S4 `AcceptInvite` reads `#token=`; test. (Superseded in round 3 by FE3-5: books' legacy route answers the old form.)
- FE2-9 / MOD2-10 — `core.json` producible from the S2 hello skeleton and regenerated in the PR adding a core route; hello web package joins at S9.
- FE2-10 — ABO adopts the `UIBundle()` pattern at S9; `MustFromFS` only in the embed file; API-only done-check.
- FE2-11 — shell segment list and `httpapi` attribution (`assets`, `static`, `ui`, `livez`) aligned with 03 v1.5.0.
- FE2-12 — build ID generated before bundling, never a hash of chunk names.
- FE2-13 — v0 rule: any minor difference is incompatible (core and module rows); package versions tied to `version.Version` by 12's check.
- FE2-14 / MOD2-17 — ordered `-w` build list; hello's web package private and outside ABO's build.
- FE2-15 — `web-deps` stamp target; `web-dev` depends on the install half only.
- FE2-16 — legacy redirects for `/dashboard`, `/files`, `/diagnostics`, `/operations`, `/maintenance`; `ui_route` recorded by ABO's `PageMiddleware`.
- FE2-17 — permission-literal ban specified as an ESLint AST rule.
- FE2-18 — route names verbatim from 05-authn/05-rbac (`/me`, `/auth/bootstrap`, `/permissions`).
- FE2-19 — UI-0 done-check grep covers `package.json`, `src`, `tests`, `vite.config.ts`.
- FE2-20 — redirect detection defers to 05-realtime §4.8; one `useTopic` signature.
- FE2-21 — leader subscribes to `*` minus `core.task.log-appended.v1`; logs fetched per run.
- FE2-23 — cookie and CSRF rules referenced from 05-authn/05-http-api instead of restated; no credential type exempt.
- FE2-24 / COR2-18 — `WriteManifestTS(w, ui.ModuleManifest)` with `ModuleManifest` declared in the block.
- FE2-25 — `/modules` and `internal/uihttp` ship at S4; shell pages' S3/S7/S8 dependencies stated; S9 preconditions.
- COR2-19 — CSP-report and UI-diagnostics routes in `internal/uihttp`, not `ui`.
- MOD2-16 — POSIX `sh` `build_web.sh`; Docker frontend stage spelled out; `ci.yml` classifier and image-job trigger paths.
- MOD2-21 — "wave 0" replaced by UI-0 and split criterion SC8.
- SIMP2-11 — v1 shell notification scope: channel and rule admin pages plus toasts; no inbox, bell or personal channels.

Round 3 (2026-11-04), findings applied to this file:

- R3-5 / FE3-1 — `NonSPA` is every mount's `TopSegments` minus `SPAShared` (ABO `login`, `invite`), never the `LegacyPrefixes` first segments (§7.4.3, §7.6.1, §7.6.3); P8 test text.
- FE3-2 / MOD3-14 — one `BUILD_ID` per release shared by Vite, the Makefile, both Docker stages and goreleaser, carried into core as `app.Options.BuildID`; P8 build-ID check; missing header counts only on app responses (`X-Request-ID`) (§7.3.2, §7.5.2, §7.6.3).
- FE3-3 / OPS3-11 — one stale-build rule in the API runtime (ABO `apiFetch`, then `@mediacore/api-client`), never deleted; legacy-SPA server case; S9 rollback R-test row (§7.5.2, §7.8, §7.19).
- FE3-4 — `ui.AppOptions` (`PageMiddleware`, `ConnectSrcFrom`, `ExtraDirectives`) passed as `app.Options.UIOptions` and merged by the kernel; recovery test boots through `app.New`.
- FE3-5 / SEC3-16 — legacy `/invite/{token}` and `/auth/temp-login` 302s are books manifest routes from S4 for the whole window (`invite` in `SPAShared`); `ui.Handler` has no 302 code; legacy `/login/temp` page at S4; tests.
- FE3-6 — Activity page keeps hand-written legacy-path calls S5–S9; `opsgen` kept until S9; `WithLegacyShape` removed.
- FE3-7 — note that the `/api/v1/auth/me` alias keeps legacy atoms in `permissions` (05-rbac §6.3).
- FE3-8 — `ui.ReservedSegments` is an S2 leaf.
- FE3-9 — one name and one test for the P8 shell-segment check.
- FE3-10 — ABO's CSP code stays in ABO until S9, then becomes `ConnectSrcFrom`.
- FE3-11 — shell settings pages need the S4 `/api/core/v1/settings*` routes.
- FE3-12 / SEC3-9 — one S5 bridge: a books bus subscriber feeding streams A, B and C, legacy def IDs only.
- FE3-13 — the `ServesSPA` fallback consults `Router.NonSPA()` from S2.
- FE3-14 — `dist/` root files served with `no-cache`; test.
- FE3-15 — last-resort per-visible-tab realtime mode; §7.14 wording; test case.
- FE3-16 — `/modules` served by `internal/modulehttp`.
- FE3-17 — `UIDescriptor.LegacyRedirects` is the one list; `manifest.gen.ts` exports it; `ModuleUIInput.redirects` removed.
- FE3-18 — `localStorage` keys read in place; O3 closed; `UserMerge.tsx` a books row action.
- FE3-19 — dev proxy rewrites `Origin` too.
- FE3-20 — split-repo npm root wording; stamp prerequisite via `find`; `react-router ^8.5.1`.
- MOD3-4 — `core.json` at `core/openapi/core.json`.
- MOD3-13 — Docker frontend-stage change lands at UI-0.
- R3-1 — checked: no dual-format machinery in this file; nothing to remove.


Round 4 (2026-11-04), findings applied to this file:

- FE4-1 / MOD4-9 / COR4-18 — meta-only build ID (per FE4-1's disposition): `build_web.sh` is the only generator (`BUILD_ID` if set, else a git-derived ID, else timestamp plus random hex); no ldflags, no `app.Options.BuildID`, no P8 check; Docker frontend stage keeps an optional `ARG BUILD_ID`; §7.3.2, §7.6.3, §7.12, §7.13 and the test rows rewritten.
- FE4-2 — invite links keep the `/invite/<token>` path form the legacy SPA routes until the shell build; the fragment form and books' `/invite/{token}` 302 start at S9 (temp-login 302 stays from S4); §7.4.3, §7.9.1, §7.19; `TestLegacyInviteLink_302NoToken` staged, `TestLegacyInviteLink_EndToEnd` added.
- FE4-3 — stale-build case 3 discriminator is a `Reporting-Endpoints` `csp` group, not `X-Request-ID`; `buildId.test.ts` row.
- FE4-4 — `lib/storageKeys.ts` split (books keys to `modules/books/`), `createShell({ legacyStorageKeys })` keeps the shell keys' literal values; no copies.
- FE4-5 — `AppOptions` funcs read pointers filled in a Start hook; `ui_route` recording moved into `ui.Handler` (`Options.Redirects`, `OnRedirectUse`, entries registered by the kernel at P7); boot-time page-load and recorder tests.
- FE4-6 — `ModuleManifest`, `UIIDs`, `Redirect` are S2 leaf types beside `ReservedSegments`.
- FE4-7 — `opsgen` stays until the last `ops.ts` importer is native (end of step 6), repointed to `tasks`' constants at S5 with a byte-identical gate; §7.8, §7.9.2, §7.11, §7.19.
- FE4-8 — books fills `LegacyRedirects` in the URL-move release (step 5), so `ui_route` recording and the C9 clock start there.
- FE4-9 — one `checkBuild(response)` in the api-client runtime from UI-0, called by ABO's `apiFetch` too.
- FE4-11 — `GET /index.html` always takes the index rule (`no-store`, full CSP, `PageMiddleware`); test case.
- MOD4-8 — the PR image job's triggers cover every image input (`core/**`, both modules' `go.mod`/`go.sum`, Dockerfiles, `.dockerignore`, `web/**`, every `//go:embed` input).
- MOD4-10 — `ci.yml:127`, `frontend-ci.yml:64` and `nightly.yml:31` named; UI-0 precondition: reusable callers drop their frontend half (`run-frontend: false`) and ABO-local jobs run after `make web-deps`, or a github-common pre-install input.
- R4-1 — checked: no store or grant machinery in this file; nothing to change.
