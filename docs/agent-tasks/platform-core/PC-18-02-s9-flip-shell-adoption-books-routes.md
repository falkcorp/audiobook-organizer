<!-- file: docs/agent-tasks/platform-core/PC-18-02-s9-flip-shell-adoption-books-routes.md -->
<!-- version: 1.0.0 -->
<!-- guid: 554b0b13-b045-4e41-b888-eb7fb704cacf -->
<!-- last-edited: 2026-11-04 -->

# PC-18-02 — S9 flip, steps 4 and 5 in one PR: shell-classified files into `core/web`, `createShell`, `mediacore()` preset, `UIBundle()`, gin SPA serving deleted, `/books/` routes with redirects, fragment invite links

| Field | Value |
|---|---|
| ID | PC-18-02 |
| Title | S9 flip, steps 4 and 5 in one PR: shell-classified files into core/web, createShell, mediacore() preset, UIBundle(), gin SPA serving deleted, /books/ routes with redirects, fragment invite links |
| Tier | **T4 — Opus / xhigh–max.** One PR so no release candidate can carry step 4 without step 5's redirects; it changes who owns `/`, every ABO URL, the CSP, the invite link form and the theme boot, and a missed meta panics every `embed_frontend` build. |
| Wave | W18 (stage S9, release `S9`) — **the flip PR** |
| Depends on | PC-18-01 |
| Trailer | `Stage-Release: S9 flip` (sets `shipped_stage: S9` and `release: S9`) |
| Digest | `docs/plans/platform-core/digests/S8-S10.md` §3 (row `ui`), §4 (07 §7.6.1, §7.4.2), §5 rules 12–18, §6 S9 rows 4 and 9, §7 S9 |
| Owning spec | 07 §7.4.2, §7.4.3, §7.5, §7.6.3, §7.7, §7.10, §7.11, §7.19 steps 4–5; 09 S9 ("Flip PR", "Moves", "UI routes", "Rollback", "Done when"); 09 §9.2.1 rows 4 and 9; 09 §9.6 row 31 |

**Gate.** Changes `release:`: `check_soak.py` check (a) needs a current `S8` row at least 14 days old (OWNER-21).
Required on this PR besides the usual set: `compat-rehearsal` (cells a, b, c: S9 is an ABS-path stage) and
`unit-lifecycle`; the `docker-image` job must **boot the embedded bundle** (07 §7.19 step 4, FE8-8).

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../abo-pc-18-02" -b feat/pc-18-02-s9-flip origin/main
cd "$REPO/../abo-pc-18-02"
git worktree list
make web-deps
```

## Goal

Ship 07 §7.19 steps 4 and 5 in one PR (the plan's §(b) decision 3; 09 S9 "Moves"):

- **Step 4.** Cut the audiobook imports out of the shell-classified files of 07 §7.11 into books slots, then move them
  into `core/web` (shell or ui-kit); everything audiobook-specific moves under `web/src/modules/books/` (with an ESLint
  `no-restricted-imports` boundary); `web/src/main.tsx` becomes `createShell({ app, modules: [booksModule], legacyStorageKeys })`
  with the data router; `web/vite.config.ts` adopts `mediacore({ appId: 'abo', themeStorageKey: 'app-theme-mode',
  defaultMode: 'dark' })`; the hand-written inline boot script in `web/index.html` (`:13-30` at `748fd787a`) is deleted
  (exactly one inline script in the built `index.html`); `useUnsavedChangesBlocker` is deleted in favour of
  `useBlocker`. ABO adopts `UIBundle()` (`web_embed.go`: `ui.MustFromFS(WebFS, "web/dist")`; `web_nonembed.go`:
  `ui.Bundle{}`), and `main.go` passes `app.Options.UI` and `app.Options.UIOptions` (`PageMiddleware`: Cloudflare
  service-token recovery from `internal/cfaccess`; `ConnectSrcFrom`: the Access team host re-read per response; both
  read an `atomic.Pointer` books fills in its Start hook, no-ops until then). Books drops `ServesSPA`; gin's
  `NoRoute`/`setupStaticFiles` SPA serving, the `static_embed.go` SPA code and the gin `/api/v1/csp-report` handler
  are deleted **in this PR**; `ui.Handler` mounts from this build.
- **Step 5.** Server manifest and intersection on (§7.5); books' module pages under `/books/...`; client redirects from
  the legacy SPA paths; books fills `UIDescriptor.LegacyRedirects` (the kernel registers one `compat.Entry` per entry,
  `ui_route`); `manifest.gen.ts` exports the same list. Core issues invite links as `/invite#token=…`; books' legacy
  manifest serves `GET /invite/{token}` (Public, `RateAuth`) with a 302 to `/invite#token=<token>` and
  `Referrer-Policy: no-referrer`; `invite` joins `login` in `LegacyMount.SPAShared`.

## Files

Create: `web/src/modules/books/` (books module: pages, components, `storageKeys.ts`, `legacy/operationPolling.ts`,
`legacyApi.ts`, `api/`).

Modify: `web/src/main.tsx`, `web/src/App.tsx` (no hard-coded module routes left), `web/vite.config.ts`,
`web/index.html`, `web/src/` (the moves), `main.go`, `web_embed.go` (and `web_nonembed.go`),
`internal/server/static_embed.go`, `internal/server/spa_fallback.go`, `internal/server/csp.go`,
`internal/server/csp_report.go`, `internal/server/legacy_manifest.go`, `internal/books/module.go`, `ci/stage.txt`; and
the shell/ui-kit packages under `core/web/packages/` that receive the moved files (see Planner notes).

## Re-verify before editing

Counts at `748fd787a` (equal at this plan's HEAD for `web/` paths except the review-lane files). STOP on a zero-hit
edit target or a moved count.

```bash
sed -n '13,30p' web/index.html | grep -c 'localStorage.getItem("app-theme-mode")'   # 1 (the inline boot script to delete)
grep -c '<script' web/index.html                                                     # 2 (inline + module entry)
git grep -n 'func (s \*Server) setupStaticFiles\|s.setupStaticFiles(' -- internal/server | grep -v _test   # 2 (static_embed.go, server_lifecycle.go:2141)
git grep -n 'var nonSPASegments' -- internal/server/spa_fallback.go                  # 1
test -f internal/server/static_embed_recovery_test.go && echo ok                    # ok (its port must boot through app.New)
test -f internal/server/legacy_manifest.go && echo ok                               # PC-04-01; missing = STOP
grep -n 'useUnsavedChangesBlocker' -r web/src | grep -v test | wc -l                 # 2 (the hook and Settings.tsx)
grep -n 'export const ABS_REDIRECTS' web/src/utils/absRedirects.ts                   # 1 (the redirect pattern to follow)
grep -rln 'eventSourceManager' web/src | grep -v test                                # App.tsx, TopBar.tsx, Library.tsx, the manager (PC-18-03 replaces it)
test -d core/web/packages/shell && test -d core/web/packages/ui-kit && echo ok       # PC-18-01; missing = STOP
grep -n '^release: S8$' ci/stage.txt                                                 # 1
git grep -n 'ServesSPA' -- internal/books internal/server | head -3                   # >= 1 (dropped here)
git grep -n 'InviteLinkPath' -- main.go internal/books | head -2                      # >= 1 (S4's path form; switches to the fragment form here)
```

Generate the move list at your base: for every row of 07 §7.11, the current path, the destination, and the audiobook
imports that must be cut into a books slot first. The 35 non-test shell/ui-kit files and the eight literal storage keys
are informative counts; record the generated ones in the PR description. A file §7.11 does not classify: STOP and ask.

## Steps

1. Read 07 §7.3.2, §7.4.2, §7.4.3, §7.5, §7.6.2, §7.6.3, §7.7, §7.10, §7.11 (every row), §7.19 steps 4–5; 09 S9 in
   full; 05-http-api §4.10 (`Router.SPA`, P8 segment check), 05-authn §4.13 (invite link form option).
2. Write the tests below first (the R-test rows run real S8 binaries).
3. Cut and move in the order §7.19 step 4 gives. A shell-classified file that imports audiobook code is never moved
   unchanged: its audiobook part becomes a module slot first (nav arrays → books nav items; `ReviewBanner`,
   `usePendingFileOps` → books `banners`; dashboard counts → a books widget; `WelcomeWizard`'s gate → books
   `setup.isRequired`; `AuthContext`'s `* as api` import → the generated core client), and the `core/web` import rule
   proves the cut.
4. Storage keys are **read in place, never moved, renamed or copied**: books keys keep their literal names in books'
   module; the seven shell-bound keys go through `createShell({ legacyStorageKeys })` typed by `LegacyShellKey`;
   `app-theme-mode` through the preset's `themeStorageKey`. No audiobook key name enters `core/web`.
5. `vite.config.ts` adopts the preset in this PR, so the bundle has the metas `MustFromFS` requires; `appId: 'abo'`
   equals `createShell`'s `app.id` and `app.Options.AppID`.
6. Go side: `UIBundle()` per §7.6.3 (placeholder on an API-only build, never a panic); `main.go` changes from
   `server.SetEmbeddedFS(WebFS)` to `app.Options{UI: UIBundle(), UIOptions: …}`; delete gin's SPA serving, the
   `static_embed.go` SPA code and gin's `/api/v1/csp-report` handler; the CSP state becomes the handler's field; port
   `static_embed_recovery_test.go` to boot through `app.New`. Keep `ui.BasicExemptAssets` equal to HEAD's set.
7. Routes: module pages under `/books/...`; client redirects (`absRedirects.ts`' pattern) for `/library`, `/authors`,
   `/series`, `/works`, `/playlists`, `/collections`, `/review`, `/versions`, `/fingerprints`, `/dedup*` → `/books/...`,
   plus `/dashboard` → `/`, `/files` → `/books/files`, `/diagnostics` → `/system/diagnostics`, `/operations` →
   `/activity`, `/maintenance` → `/system`; books' `LegacyRedirects` lists the same entries.
8. Invites: set the link form to the fragment form (`auth.Options.InviteLinkForm`); add books' `GET /invite/{token}`
   302 with `Referrer-Policy: no-referrer` to `legacy_manifest.go`; add `invite` to `SPAShared`.
9. `ci/stage.txt`: `shipped_stage: S9`, `release: S9`.
10. Draft the release-note lines (09 §9.2.1) for the coordinator: row 4 "Invite links sent on this release do not open
    after a rollback below the release that moved sign-in to the core; send a new one." and row 9 "`/books/...`
    bookmarks need the new release."
11. **STOP conditions:** a moved file that still needs an ABO import after its slot cut; a storage key whose literal
    name would change; any build path where `MustFromFS` can run on a bundle without the preset metas; a route or
    redirect that would leave a legacy URL on `NotFound`.

## Code contract

`createShell`, `ShellConfig`, `LegacyShellKey` (07 §7.4.2), `ui.FromFS`/`MustFromFS`/`AppOptions`/`Handler`
(07 §7.6.1) as PC-18-01 built them. 06 §6.6 at S9: `UI.LegacyRedirects`, `Options.UI ui.Bundle`,
`Options.UIOptions ui.AppOptions`, `Description.UIManifests`, `ProblemUIAppIDMismatch`.

```go
// web_embed.go (//go:build embed_frontend)
func UIBundle() ui.Bundle { return ui.MustFromFS(WebFS, "web/dist") }
// web_nonembed.go (//go:build !embed_frontend)
func UIBundle() ui.Bundle { return ui.Bundle{} }
```

## Tests to write first (09 S9 Done when; 07 §7.18)

- `App.tsx` has no hard-coded module routes (a test over `App.tsx`; name coined here:
  `app.noModuleRoutes.test.ts`), `createShell.meta.test.tsx`, `TestBundle_MissingMeta`, `TestUIAppIDMismatch`,
  `TestABOManifestBootsAtS9`.
- A dummy second contribution renders with no edit to books UI (G6 UI half; name coined here:
  `secondModule.renders.test.tsx`), and the G3 intersection cases.
- `static_embed_recovery_test.go`'s port boots through `app.New` (CF recovery on page loads only; the Access team host
  in `connect-src`; a page load before books' Start hook serves `index.html` with no panic).
- API-only boot: `make build-api && ./audiobook-organizer serve` answers `/` with the placeholder.
- `TestLegacyInviteRedirect_SPASharedSegment`, `TestLegacyInviteLink_302NoToken`, `TestLegacyInviteLink_EndToEnd`
  (repeated at S9: a path-form link minted on S8 opens through the 302), `legacyStorageKeys.test.tsx`, e2e
  `theme-boot.spec.ts` (and the built `index.html` has exactly one inline script), `TestBasicExemptAssets_EqualsHEAD`
  and `TestBasicGate_ExemptSetEqualsHEAD`, `TestHandler_NonSPASegments`, `TestReservedSegmentsDisjointFromNonSPA`,
  `TestHandler_RecordsLegacyRedirectUse`, `TestBuildID_HeaderEqualsEmbeddedMeta` (image job), e2e `stale-build.spec.ts`.
- R-test rows: "S9 tab, roll back to S8, reload offered"; "a preference changed on S9 is kept after a rollback to S8";
  "an invite minted on S9, roll back to S8, opened signed out → the invite page, and accepted"; cells a, b, c.
- ABO's existing e2e specs (`web/tests/e2e`), with route URLs updated and the redirects tested.

## Acceptance

```bash
sed -n '1,40p' web/index.html | grep -c 'app-theme-mode'                          # 0 (hand-written boot script gone)
npm --prefix web run build && grep -o '<script' web/dist/index.html | wc -l          # 2: one inline (the preset's) + one module entry
grep -c '<script>' web/dist/index.html                                              # 1 (exactly one inline script)
git grep -n 'setupStaticFiles\|NoRoute(' -- internal/server | grep -v _test.go | wc -l   # 0
git grep -n 'useUnsavedChangesBlocker' -- web/src | wc -l                            # 0
grep -c '^shipped_stage: S9$' ci/stage.txt; grep -c '^release: S9$' ci/stage.txt   # 1 and 1
grep -rn -E 'app-theme-mode|itunes_import_settings|audiobook' core/web/packages --include=*.ts --include=*.tsx | grep -v node_modules | wc -l   # 0
make build-api   # then, on a fixture state dir: ./audiobook-organizer serve --port 18484 ; curl -sk -o /dev/null -w '%{http_code}\n' https://127.0.0.1:18484/ || curl -s -o /dev/null -w '%{http_code}\n' http://127.0.0.1:18484/   # 200, and the body is ui's placeholder page
npm --prefix web run typecheck && npm --prefix web test -- --run                    # exit 0
make test-e2e                                                                       # exit 0
make compat-rehearsal                                                               # exit 0 (cells a, b, c and the S9 rows)
make ci                                                                             # exit 0
make -C core ci                                                                     # exit 0
```

## Out of scope

The shell realtime client replacing `eventSourceManager` and the Activity page's move (PC-18-03); `renderWithProviders`
onto `renderInShell` and the remaining `api.ts` sections (PC-18-04); deleting `tools/cmd/opsgen` (it stays until the
last `ops.ts` importer is native); moving books to its own npm package.

## Rollback

Binary swap: the UI is embedded per build. Rows 4 and 9 of 09 §9.2.1 are the costs (S9 invites open after a rollback
to S4 r2–S8 only; `/books/...` bookmarks 404 on the older SPA); open S9 tabs are offered a reload by the stale-build
rule. A revert is admitted only as the exact `git revert` of this PR's commits.

## Repo rules

- Worktree per task; never the primary checkout or `main`; remove it after merge.
- Versioned headers on every new or changed file (moved files keep their guid and bump their version); one headerless
  `changelog.d/` fragment; the flip qualifies for an executive summary (`docs/process/executive-summaries.md`).
- Conventional commits, each with `Stage-Release: S9 flip` and `Co-Authored-By: Claude <noreply@anthropic.com>`;
  every commit builds (rebase merge).
- `gh pr merge <n> --rebase`; never squash; never `--admin` past a required check.
- Report with exact counts (files moved, keys kept, redirects) and `COMPLETED:` / `REMAINING:` / `BLOCKED:`.

## Planner notes

- The row's files omit the `core/web/packages/shell/` and `core/web/packages/ui-kit/` destinations of the moved files
  (also edited by PC-18-01; ordered by the chain) and `web_nonembed.go`, which §7.6.3 changes with `web_embed.go`.
- 07 §7.11 classifies `pages/ActivityLog.tsx`, `stores/useOperationsStore.ts` and `OperationsIndicator.tsx` as shell
  (`/activity`), so this PR moves them while PC-18-03 later rewrites the Activity page; PC-18-03's listed paths
  (`web/src/pages/Activity.tsx`, `web/src/stores/useOperationsStore.ts`) will not exist at its base. Decide whether
  the Activity files stay in `web/src` until PC-18-03 (then PC-18-02 leaves them in place) or move here.
- The API-only check uses `--port 18484` (the default is 8484, `cmd/root.go:526` at `748fd787a`); TLS may be on by default, hence the https-then-http probe.
