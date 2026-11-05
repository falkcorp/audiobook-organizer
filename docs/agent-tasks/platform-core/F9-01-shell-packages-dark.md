<!-- file: docs/agent-tasks/platform-core/F9-01-shell-packages-dark.md -->
<!-- version: 1.1.2 -->
<!-- guid: 1cd3e2f0-6d5d-4edb-b717-11340b83d272 -->
<!-- last-edited: 2026-11-05 -->

# F9-01 — Shell packages, dark: @mediacore/ui-kit and @mediacore/shell (with /vite and /testing), hello's web package, the rest of core ui (Handler, FromFS, the policy builder, UIBundle), hello's ui_embed.go

| Field | Value |
|---|---|
| ID | F9-01 |
| Release | R2, flip label F9 (dark PR; F9 ships in R2 if ready, else in R3 unchanged) |
| Tier | **T3** (Opus / high): new packages with a build-time preset and a fail-closed bundle loader; the CSP policy and the stale-build rule are security and availability contracts |
| Wave | W31 (beside F3-08, F4-07, F5-01, F5-02, F5-03, F5-04, F5-05, F5-06, F5-07, F5-08, F5-09, F5-10, F5-11, F5-12, F5-13, F5-19, F6-04, F7-05, F7-06; file-disjoint; F3-07 merges first and this PR rebases after it) |
| Depends on | R1-30 (`core/web` workspace with `@mediacore/api-client`), F6-02 (core realtime hub: the shell's realtime client targets `/api/core/v1/events`) |
| Spec | 07 §7.3 (layout, exports, peers, compilation, import boundary, §7.3.2 build sites, §7.3.3 versions), §7.4 (extension API: `defineModule`, `ShellConfig`, slots, §7.4.3 shell routes, §7.4.4 `/testing`, §7.4.5 schema forms), §7.5 (server manifest and intersection), §7.6 (Go `ui`: §7.6.1 API, §7.6.2 CSP, §7.6.3 mounting), §7.7 theming, §7.9 auth and realtime client, §7.10 build preset, §7.18 tests, §7.19 step 4; 06 §6.4.10 ("Shell" row), §6.12.5 (hello's `main.go` and `ui_embed.go`); 04 §8.1 F9 |

**Gate:** plan, then execute autonomously in a worktree with its own PR. **Dark:** ABO's `web/` does not import the
new packages yet and ABO's `main` passes no `app.Options.UI`; the SPA is still served by the books legacy mount with
`ServesSPA: true` (07 §7.6.3). Only hello and `core/web` change behaviour.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f9-01-shell-pkgs" -b agent/platform-core-f9-01-shell-pkgs origin/main
cd "$REPO/.worktrees/pc-f9-01-shell-pkgs"
git rebase origin/main
npm ci --prefix web           # CLAUDE.md: Playwright from the worktree
```

## Goal

1. `core/web/packages/ui-kit` (`@mediacore/ui-kit`): `createMediaTheme`, presentational components (07 §7.3.1,
   §7.7); built with `mediacoreLibrary`; imports neither `@mediacore/api-client` nor `@mediacore/shell`.
2. `core/web/packages/shell` (`@mediacore/shell`): `createShell`, `defineModule` and the slot types of §7.4.2, the
   shell routes and reserved segments (§7.4.3), the intersection algorithm over `GET /api/core/v1/modules` (§7.5),
   the auth/session rules and the realtime client with leader election (§7.9), schema-driven settings forms (§7.4.5),
   `/vite` (the two presets of §7.10, **unbuilt** plain ESM with a hand-written `index.d.mts`) and `/testing`
   (§7.4.4, `renderInShell`).
3. `core/examples/hello/web/` (hello's web package, `"private": true`, built to `web/dist`, with its own
   `.gitignore`) and `core/examples/hello/ui_embed.go` / `ui_nonembed.go` exactly as 06 §6.12.5 shows; hello's
   `main.go` passes `UI: hello.UIBundle()`.
4. The rest of Go `core/ui` (07 §7.6.1): `Bundle`, `FromFS`, `MustFromFS`, `IsZero`, `BuildID`, `AppID`,
   `AppOptions`, `Options`, `Handler`, `BasicExemptAssets`, `WriteManifestTS`, and the CSP policy builder (§7.6.2).
   `ReservedSegments`, `ModuleManifest` and `UIIDs` exist since R1. `ui.Handler` emits two of 07 §7.16's families,
   `<namespace>_ui_build_info` (gauge 1, label `build_id`) and `<namespace>_ui_responses_total` (counter, labels
   `kind` = `asset`/`asset404`/`index`/`nonspa404`/`method404`/`placeholder` and `status`). The other three `_ui_`
   families belong to F4-05's routes.
5. `core/web/package.json` workspaces gain `packages/ui-kit`, `packages/shell` and `../examples/hello/web`;
   `core/scripts/ci/embed_build_outputs.txt` lists hello's `web/dist`.

## Invariants

- **I1 (import boundary, FE-16).** No specifier in `core/web` resolves outside `core/web`; ui-kit imports neither
  api-client nor shell (the two ESLint rules of §7.3.1 pass).
- **I2.** One copy of each peer singleton and of `react/compiler-runtime` (`check-singletons.mjs`).
- **I3 (fail-closed bundle).** `ui.FromFS` refuses a bundle without the build meta, the theme meta or the
  `mediacore-app` meta (`TestBundle_MissingMeta`); a zero `Bundle` serves the API-only placeholder.
- **I4.** SPA responses are outside the route chain (no rate limit, no permission stage), assets are immutable,
  `index.html` is `no-store` with the full CSP (`TestHandler_*`).
- **I5.** `git status --porcelain` is empty after `make web-deps && make web-build` and after a hello build
  (FE7-1).
- **I6. Dark for ABO.** `web/` and ABO's Go code are unchanged except for workspace-level build wiring that
  §7.3.2 requires.

## Files

| Action | Path |
|---|---|
| create | `core/web/packages/ui-kit/` |
| create | `core/web/packages/shell/` (including `vite/index.mjs`, `vite/index.d.mts`, `src/testing/`) |
| modify | `core/web/package.json`, `core/web/package-lock.json` |
| create | `core/examples/hello/web/` (with `.gitignore`), `core/examples/hello/ui_embed.go` and its untagged twin `core/examples/hello/ui_nonembed.go` |
| create / modify | `core/ui/handler.go`, `core/ui/bundle.go` (and the CSP policy builder file beside them, if separate) |
| modify | `core/scripts/ci/embed_build_outputs.txt` |
| modify | `core/examples/hello/cmd/hello/main.go` (pass `UI: hello.UIBundle()`, 06 §6.12.5) |
| create | `core/web/e2e/` (the shell's Playwright suite, 07 §7.3.1 and §7.18) |
| create | one `changelog.d/` fragment (headerless) |

## Re-verify before editing (expected results at your base)

```bash
# R1-30's workspace. Expected: package.json with workspaces naming packages/*; api-client present; no ui-kit/shell yet
cat core/web/package.json; ls core/web/packages/
# core/ui leaf from R1. Expected: ReservedSegments, ModuleManifest, UIIDs present; Handler and FromFS absent
grep -n 'func ReservedSegments\|type ModuleManifest\|type UIIDs\|func Handler\|func FromFS' core/ui/*.go
# hello skeleton. Expected: main.go present; no ui_embed.go yet
ls core/examples/hello/ core/examples/hello/cmd/hello/
# The realtime endpoint the shell client targets (F6-02). Expected: /api/core/v1/events served by core/internal/eventhttp
grep -rn '/api/core/v1/events' core/internal/eventhttp/*.go | head -3
# ABO web unchanged so far. Expected: main.tsx still uses BrowserRouter from react-router (R1 moved it to v8)
grep -n 'BrowserRouter' web/src/main.tsx
```

STOP if `core/web/packages/ui-kit` or `shell` already exists, if `core/ui` lacks the R1 leaf, or if
`/api/core/v1/events` is not served (F6-02).

## Steps

1. Read 07 §7.3–§7.10 and §7.18 in full; copy every TypeScript and Go signature exactly.
2. Write the tests of §7.18 that belong to these packages first (below).
3. ui-kit (theme factory, presentational components, the hooks 07 §7.11 assigns to ui-kit, typed structurally:
   `ServerFileBrowser` takes `{ browse, include?, exclude? }` props and declares its own `BrowseEntry`/`BrowseResult`).
4. shell (createShell, defineModule, slots, routes, intersection, auth flow, realtime client with leader election
   over `navigator.locks` and `BroadcastChannel`, schema forms, storage keys under the app ID, `/testing`).
5. `/vite` presets (app preset with the meta tags, theme meta and boot script; `mediacoreLibrary` with the
   `reactCompilerPreset()`), unbuilt, loaded by Node resolution.
6. hello's web package on the shell, with one route, one nav entry and its settings panel (06 §6.12.4).
7. Go `core/ui`: `Bundle`, `FromFS`/`MustFromFS` (fail-closed metas), `Handler` (assets immutable, index no-store,
   non-SPA 404 JSON, method refusals, placeholder for a zero bundle), the CSP builder (§7.6.2, hot swap through
   `core.ui.*`), `BasicExemptAssets` equal to HEAD's set, `WriteManifestTS`. `Handler` registers
   `_ui_build_info` and counts every response in `_ui_responses_total` by `kind` and `status` (07 §7.16). These are
   in the core `ui` metrics scope: add its `testdata/series.golden` (or extend it, if F4-05's counters created it),
   asserted by `metricstest.AssertSeries` (R1-10; 05-metrics §5 item 5).
8. hello: `ui_embed.go` / `ui_nonembed.go` and `UI: hello.UIBundle()` in its `main.go`; core's Makefile target
   builds `web/` before `go build -tags embed_frontend -o bin/hello ./examples/hello/cmd/hello`.
9. `embed_build_outputs.txt`, workspaces, lockfile (`npm install` inside `core/web` only), headers, fragment.

## Code contract (07 §7.6.1 and §7.4.2, verbatim excerpts; copy the rest from the spec)

```go
package ui
func FromFS(fsys fs.FS, dir string) (Bundle, error)
func MustFromFS(fsys fs.FS, dir string) Bundle
func (b Bundle) IsZero() bool
func (b Bundle) BuildID() string
func (b Bundle) AppID() string
func Handler(b Bundle, opts Options) http.Handler
func BasicExemptAssets(path string) bool
func WriteManifestTS(w io.Writer, m ModuleManifest) error
```

```ts
export function defineModule<const M extends ModuleManifest>(manifest: M, ui: ModuleUIInput<M>): ModuleUI<M>;
export interface ShellConfig {
  app: { id: string; displayName: string; logo?: ComponentType };
  modules: readonly ModuleUI[];
  // … the remaining fields exactly as §7.4.2 lists them
}
```

## Tests (write first; spec names copied exactly, 07 §7.18)

shell (vitest): `intersect.test.ts`, `buildId.test.ts` (shell half), `defineModule.types.test.ts`,
`nav.permissions.test.tsx`, `auth.flow.test.tsx`, `realtime.resume.test.ts`, `realtime.leader.test.ts`,
`schemaForm.test.tsx`, `reservedSegments.test.ts`, `shortcuts.test.tsx`, `createShell.meta.test.tsx`,
`shellStorageKeys.test.tsx`. ui-kit: `theme.contrast.test.ts`. core/web CI: `check-singletons.mjs`,
`presets.test.mjs`, the ESLint import boundary. Go `core/ui`: `TestHandler_AssetsImmutable`, `TestHandler_IndexNoStore`,
`TestHandler_CSPPolicyVerbatim`, `TestHandler_CSPHotSwap`, `TestHandler_API404JSON`, `TestHandler_Placeholder`, `TestHandler_ServesRootPublicFile`,
`TestHandler_NonSPASegments`, `TestHandler_NonGETRefused`, `TestHandler_MissingAssetIs404`,
`TestHandler_PageMiddlewareOnlyOnIndex`, `TestBundle_MissingMeta`,
`TestHandler_OutsideRouteChain`, `TestBasicExemptAssets_EqualsHEAD`; `metricstest.AssertSeries` over the `ui` scope's
`testdata/series.golden`, which proves `_ui_build_info` and `_ui_responses_total` carry exactly 07 §7.16's labels
(05-metrics §12). core e2e (Playwright against hello built
with `-tags embed_frontend`): `shell.spec.ts`, `tabs.spec.ts`, `two-apps.spec.ts`, `stale-build.spec.ts` (core
half), the bundle check.

## Acceptance (commands and expected output)

```bash
# core/web builds from clean in the §7.3.2 order and its tests pass. Expected: exit 0, no failing test
make -C core web-test

# Go ui. Expected: ok
(cd core && go test -count=1 ./ui/...)

# hello embedded build and its e2e. Expected: binary built; Playwright suite green
make -C core e2e

# Clean tree after builds (FE7-1). Expected: no output
make web-deps && make web-build && git status --porcelain

# ABO unchanged. Expected: ok; vitest green
go test -count=1 ./internal/server/... && make web-test

# Gates. Expected last lines: ✅ All CI checks passed! / exit 0
make ci
make -C core ci
```

If `make -C core web-test` or `make -C core e2e` is named differently at your base, use the targets R1-30 and R1-03
created (`grep -E '^[a-z-]+:' core/Makefile`) and record the names in the PR.

## Out of scope

- Any change to ABO's `web/` beyond workspace wiring (F9-02 cuts shell-classified files; F9-03 switches `main.tsx`).
- `app.Options.UI` in ABO's `main.go`, `ServesSPA`, gin's SPA serving (F9-03).
- Publishing the npm packages (after the split, 12).

## Rollback

Revert the PR; ABO never imported the packages.

## Repo rules

- Worktree off fresh `origin/main`; `npm ci --prefix web` in the worktree (CLAUDE.md); headers on every new or changed
  file in `//` line comments for TS/JS (as `web/src/main.tsx` has); `package.json` and lockfiles carry none,
  as `web/package.json` carries none at HEAD.
- TypeScript rules: `.standards/instructions/typescript.md`.
- Conventional commit, for example `feat(shell): ui-kit, shell and hello web package (dark)`, ending with
  `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; never squash; never `--admin`.

