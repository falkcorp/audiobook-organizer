<!-- file: docs/agent-tasks/platform-core/PC-18-01-shell-ui-kit-packages-go-ui.md -->
<!-- version: 1.0.0 -->
<!-- guid: 37d408fa-a2fe-45c1-a6ac-ce9bfe3f3ea5 -->
<!-- last-edited: 2026-11-04 -->

# PC-18-01 — `@mediacore/ui-kit` and `@mediacore/shell` (with `/vite` and `/testing`), hello's web package and `ui_embed.go`, and the rest of the Go `ui` package (dark for ABO)

| Field | Value |
|---|---|
| ID | PC-18-01 |
| Title | @mediacore/ui-kit and @mediacore/shell with /vite and /testing, hello's web package and ui_embed.go, and the rest of the Go ui package (dark for ABO) |
| Tier | **T3 — Opus / high.** The shell contract every app module builds against, and the bundle metas that `ui.FromFS` enforces fail-closed; a wrong meta panics every `embed_frontend` build later. |
| Wave | W18 (stage S9, release `S9`) |
| Depends on | OWNER-21 (the `S8` soak row) |
| Trailer | `Stage-Release: S9 1` |
| Digest | `docs/plans/platform-core/digests/S8-S10.md` §3 (row `ui` (rest)), §4 (07 §7.6.1, §7.4.2), §5 rules 12–13, 17–18 |
| Owning spec | 07 §7.3 (layout, consumption, versions), §7.4 (extension API, routes and reserved segments, testing), §7.5 (intersection, G3), §7.6.1–§7.6.2 (Go `ui`, CSP), §7.7 (theming), §7.9.2 (realtime client), §7.10 (presets), §7.18, §7.19 step 4; 09 S9 "Moves" |

**Gate.** Merges only after the `S8` soak row exists. ABO does not consume either package in this PR ("dark for
ABO"): `web/` and `main.go` do not change.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../abo-pc-18-01" -b feat/pc-18-01-shell-packages origin/main
cd "$REPO/../abo-pc-18-01"
git worktree list
make web-deps
```

## Goal

Create the two remaining `core/web` workspace packages and the Go half they need, all unused by ABO:

- `@mediacore/ui-kit` (theme factory `createMediaTheme`, presentational components and hooks) and `@mediacore/shell`
  (`createShell`, `ShellConfig`, `defineModule`/`ModuleUI`, the §7.5.2 intersection with the core-version row,
  routes and reserved segments, the realtime client of §7.9.2, the `LegacyShellKey` storage contract), with
  `@mediacore/shell/vite` (the unbuilt presets `mediacore(options)` and `mediacoreLibrary({ name })`, plain ESM plus a
  hand-written `index.d.mts`) and `@mediacore/shell/testing` (`renderInShell`, `FakeRealtime`).
- hello's web package `core/examples/hello/web/` (in the workspace, with its own `.gitignore`), `core/examples/hello/
  ui_embed.go` (`//go:build embed_frontend`, `ui.MustFromFS`) and its non-embed twin returning `ui.Bundle{}`.
- the rest of Go package `ui` (`core/ui/`, beside S2's `segments.go`): `Bundle`, `FromFS`, `MustFromFS`, `Handler`,
  `Options`, `AppOptions`, `BasicExemptAssets`, CSP per §7.6.2.
- core's embed-output check: `core/scripts/ci/embed_build_outputs.txt` and `core/scripts/ci/check_embed_outputs.py`.

## Files

Create: `core/web/packages/ui-kit/`, `core/web/packages/shell/` (with `vite/index.mjs`, `vite/index.d.mts`,
`src/testing/`), `core/examples/hello/web/` (with `.gitignore` like `core/web/.gitignore`),
`core/examples/hello/ui_embed.go` (+ the `!embed_frontend` twin), `core/scripts/ci/embed_build_outputs.txt`,
`core/scripts/ci/check_embed_outputs.py`, `core/ui/` (the rest of the package).

Modify: `core/web/package.json` (workspaces `["packages/*", "../examples/hello/web"]`), `core/web/package-lock.json`,
`core/Makefile` (hello web build, `web-test`, e2e and `readiness` hooks).

## Re-verify before editing

```bash
test -d core/web/packages/api-client && echo ok                  # UI-0 (PC-08-02); missing = STOP
test -f core/ui/segments.go && echo ok                           # S2 (PC-04-02); missing = STOP
test -d core/web/packages/shell && echo EXISTS                    # must print nothing; else the task may be applied
grep -n '"workspaces"' core/web/package.json                     # 1 line; packages/* only, no ../examples/hello/web yet
test -f core/web/.gitignore && echo ok                           # UI-0
test -f scripts/ci/embed_build_outputs.txt && test -f scripts/ci/check_embed_context.py && echo ok   # ABO's S0 pair to mirror
grep -n 'react-router' web/package.json                          # react-router 8 (UI-0); no react-router-dom
grep -c '<script' web/index.html                                  # 2 at 748fd787a (ABO untouched by this PR)
```

## Steps

1. Read 07 §7.3–§7.10 and §7.18 in full (the types of §7.4.2, the route table of §7.4.3, §7.4.4 testing, §7.5.2
   algorithm, §7.6.1 Go API, §7.6.2 CSP, §7.7 theming, §7.9.2 realtime, §7.10 presets); 05-realtime §4 (the stream the
   client reads); 05-http-api §4.10 (`Router.SPA`, `Router.NonSPA()`).
2. Write the tests below first.
3. Peers of ui-kit and shell (§7.3.1): `react`, `react-dom`, `react-router`, `@mui/material`, `@emotion/react`,
   `@emotion/styled`, `@mui/icons-material ^9.5.0`. Both build with `mediacoreLibrary`, which carries the React
   Compiler preset; `exports` has `"development": "./src/index.ts"` and `"default"` → `dist/`, plus the shell's
   `"./vite": "./vite/index.mjs"` outside `dist/`. `@mediacore/ui-kit` imports neither `@mediacore/api-client` nor
   `@mediacore/shell` (its build config may import only `@mediacore/shell/vite`). The import-boundary ESLint rule
   rejects any specifier outside `core/web`. No audiobook name or storage key appears in `core/web`.
4. `mediacore({ appId, themeStorageKey, defaultMode, … })` writes the `mediacore-build`, `mediacore-app` and
   `mediacore-theme` metas and the one theme boot script; it refuses an `appId` or `themeStorageKey` outside its
   pattern. The shell defines `MEDIACORE_CORE_VERSION` in both modes (§7.5.2, FE7-4).
5. `createShell(config)` checks `config.app.id` against the `mediacore-app` meta (blocking page and nothing mounted
   on mismatch; one warning when the meta is absent), runs §7.5.2 (core-version row first, then one row per module and
   slot: `version-incompatible` modules not mounted, undeclared descriptors dropped), reads legacy storage keys only
   through `legacyStorageKeys` typed by `LegacyShellKey`, and uses the data router.
6. Go `ui`: `FromFS` fails on missing `index.html`, build meta, app meta or theme meta; a legacy-style `index.html` with
   the build meta but no theme meta is refused; `MustFromFS` is called only from an `embed_frontend` file; `Handler`
   gives immutable caching for hashed assets, `no-store` and the full CSP (with inline-script hashes) for `index.html`,
   `PageMiddleware` on index loads only, JSON 404 under `/api/`, `Router.NonSPA()` refusals, non-GET refused.
7. hello's web package: a minimal app built with `mediacore({ appId: 'hello', … })`; `make -C core` builds it to
   `core/examples/hello/web/dist` for `ui_embed.go`; list that output in `core/scripts/ci/embed_build_outputs.txt`, and
   make `check_embed_outputs.py` accept an embed input excluded from the build context only when it is listed there
   (the core twin of ABO's `check_embed_context.py`).
8. After `make web-deps && make web-build`, a hello build, `make -C core readiness` and a core e2e run,
   `git status --porcelain` is empty (§7.3.1 "Ignored outputs").

## Code contract (copy exactly)

```ts
// 07 §7.4.2
export type LegacyShellKey = 'activitySourcePrefs' | 'activityOpsPinned' | 'tableConfigPrefix'
  | 'dismissedAnnouncements' | 'welcomeWizardCompleted' | 'opsIndicatorCollapse' | 'showAdvanced';
export interface ShellConfig { app: { id: string; displayName: string; logo?: ComponentType }; modules: readonly ModuleUI[];
  theme?: Omit<MediaThemeInput, 'defaultMode'>; legacyStorageKeys?: Readonly<Partial<Record<LegacyShellKey, string>>>; }
export function createShell(config: ShellConfig): Shell;
// 07 §7.4.4
export function renderInShell(ui: React.ReactElement, opts?: RenderInShellOptions): RenderResult & {
  router: ReturnType<typeof createMemoryRouter>; realtime: FakeRealtime; };
```

```go
// 07 §7.6.1
func FromFS(fsys fs.FS, dir string) (Bundle, error)
func MustFromFS(fsys fs.FS, dir string) Bundle
func (b Bundle) IsZero() bool
func (b Bundle) BuildID() string
func (b Bundle) AppID() string
type AppOptions struct { PageMiddleware func(http.Handler) http.Handler; ExtraDirectives []string; ConnectSrcFrom func() []string }
func Handler(b Bundle, opts Options) http.Handler
```

`MediacoreOptions` and `RenderInShellOptions` are declared field for field in 07 §7.10 and §7.4.4; copy them.

## Tests to write first (07 §7.18 names)

`createShell.meta.test.tsx`, `intersect.test.ts` (one case per §7.5.2 row, the core-version row and every slot; this
is G3: a module descriptor with an incompatible version is not mounted, an undeclared item is dropped),
`defineModule.types.test.ts`, `nav.permissions.test.tsx`, `auth.flow.test.tsx`, `realtime.resume.test.ts`,
`realtime.leader.test.ts`, `schemaForm.test.tsx`, `reservedSegments.test.ts`, `theme.contrast.test.ts`,
`presets.test.mjs`, `shortcuts.test.tsx`, `check-singletons.mjs`, `runtime.errors.test.ts` if the api-client runtime
changes; Go: `TestBundle_MissingMeta`, `TestHandler_AssetsImmutable`, `TestHandler_IndexNoStore`,
`TestHandler_CSPPolicyVerbatim`, `TestHandler_CSPHotSwap`, `TestHandler_API404JSON`, `TestHandler_Placeholder`,
`TestHandler_ServesRootPublicFile`, `TestHandler_NonSPASegments`, `TestHandler_NonGETRefused`,
`TestHandler_MissingAssetIs404`, `TestHandler_PageMiddlewareOnlyOnIndex`, `TestHandler_RecordsLegacyRedirectUse`,
`TestHandler_OutsideRouteChain`, `TestBasicExemptAssets_EqualsHEAD`, and `TestUIAppIDMismatch` (kernel).
e2e `shell.spec.ts`, `tabs.spec.ts` and `two-apps.spec.ts` against hello need the shell pages that PC-18-02 moves in;
add them there if hello cannot sign in yet (see Planner notes).

## Acceptance

```bash
(cd core/web && npm ci && npm run build --workspaces && npm test --workspaces -- --run)   # exit 0
(cd core/web && npx eslint packages)                                                     # exit 0 (import boundary)
grep -rn -E 'audiobook|abo\.|app-theme-mode' core/web/packages | grep -v node_modules | wc -l   # 0
(cd core && go test ./ui/... ./kernel/... -run 'TestBundle_MissingMeta|TestHandler_|TestUIAppIDMismatch|TestBasicExemptAssets_EqualsHEAD' -count=1)   # ok
(cd core && go vet -tags embed_frontend ./examples/hello/...)                             # exit 0 after the hello web build
python3 core/scripts/ci/check_embed_outputs.py                                           # exit 0
git diff --stat origin/main -- web main.go internal | tail -1                             # empty: ABO untouched
git status --porcelain                                                                   # empty after the builds
make -C core ci                                                                          # exit 0
make ci                                                                                  # exit 0
```

## Out of scope

Moving ABO's shell-classified files, `createShell` in ABO, `UIBundle()`, deleting gin's SPA serving, the `/books/`
routes and redirects, fragment invite links (PC-18-02); ABO's realtime switch (PC-18-03); `renderWithProviders`
(PC-18-04); publishing to npm (07 §7.17, after the split).

## Rollback

Binary swap; nothing in ABO uses these packages yet.

## Repo rules

- Worktree per task; never the primary checkout or `main`; remove it after merge.
- Versioned headers on every new file (TS, MJS, JSON where the standard allows, Go, Python); one headerless
  `changelog.d/` fragment.
- Conventional commits, each with `Stage-Release: S9 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
- `gh pr merge <n> --rebase`; never squash; never `--admin` past a required check.
- Report with exact counts and `COMPLETED:` / `REMAINING:` / `BLOCKED:`.

## Planner notes

- 07 §7.19 step 4 and §7.11 have the shell's own pages (login, setup, users, layout, `AuthContext`, toasts) arrive by
  **moving** ABO's shell-classified files, which is PC-18-02. This PR therefore has no sign-in or layout UI for hello,
  and the hello e2e specs of §7.18 (`shell.spec.ts`, `tabs.spec.ts`, `two-apps.spec.ts`) cannot pass until PC-18-02;
  the inventory gives neither row those e2e specs. Either move them to PC-18-02's done-tests or let PC-18-01 carry
  minimal shell pages that PC-18-02 replaces; the spec does not choose.
- The realtime client (§7.9.2) is built here because `createShell` and `renderInShell`'s `FakeRealtime` need it;
  PC-18-03 only switches ABO's consumers.
