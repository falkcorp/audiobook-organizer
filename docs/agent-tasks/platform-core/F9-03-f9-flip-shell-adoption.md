<!-- file: docs/agent-tasks/platform-core/F9-03-f9-flip-shell-adoption.md -->
<!-- version: 1.1.5 -->
<!-- guid: b0913d46-08c4-4812-bdf9-48c9be6bf2e7 -->
<!-- last-edited: 2026-11-05 -->

# F9-03 — F9 flip, one PR: main.tsx on createShell with the data router, renderInShell, the shell realtime client, vite.config.ts on mediacore(…), UIBundle in app.Options.UI, ServesSPA dropped and gin's SPA serving deleted, the server manifest and intersection on, ABO URLs under /books/

| Field | Value |
|---|---|
| ID | F9-03 |
| Release | R2 if ready, else R3 unchanged (09 §9.3; plan §(l) item 7) |
| Tier | **T4** (Opus / xhigh–max): every flip PR is T4; this one changes every page path and the owner of `/` |
| Wave | W41 (alone: the only PR of its wave; it owns `main.go`, `internal/server/`, books' registration and `core/kernel/kernel.go` in W41). Runs inside an announced **move window** (04 key `F9 move internal/server -> mediacore/ui`) |
| Depends on | F8-04, F9-02, F9-04, F9-05 |
| Spec | 07 §7.19 steps 4–5 (one release; the order inside the series), §7.11, §7.6.3 (ownership of `/`, the Basic-gate asset exemption, ABO adopts the hello pattern, route-chain placement, `embed_frontend` semantics), §7.5 (server manifest and intersection), §7.9.2 (realtime client), §7.10 (preset), §7.18; 09 §9.4 R2 flips table row F9, §9.6 row 21; 05-http-api §8.1 (F9 row), §4.10; 03 §3.8 F9; 06 §6.4.10 ("Shell" row: `Options.UI`, `Options.UIOptions`, `Description.UIManifests`), §6.10 (`TestABOManifestBootsWithShell`) |

**Gate:** plan, then execute autonomously in a worktree with its own PR. **One atomic PR** (09 §9.2.1). It merges
alone in W41. If F8-04 has merged and this PR is not ready when R2-01 is due, F9 moves to R3 unchanged (R2-01 records
it); then this PR merges after OWNER-05 and before OWNER-06, and its rollback is `make rollback` to R2.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f9-03-shell-flip" -b agent/platform-core-f9-03-shell-flip origin/main
cd "$REPO/.worktrees/pc-f9-03-shell-flip"
git rebase origin/main
npm ci --prefix web
```

## Goal (07 §7.19 steps 4–5, in this order inside the PR's commits; FE8-8)

1. ABO's `vite.config.ts` adopts `mediacore(…)` (the preset writes the UI build-ID meta, the `mediacore-app` meta,
   the theme meta and boot script from ABO's `themeStorageKey`/`defaultMode`); ABO's hand-written inline theme script
   in `web/index.html` is deleted in the same commit, so exactly one boot script runs and is CSP-hashed (FE7-9).
   This commit precedes (or is) the one that passes `UIBundle()`, because `ui.FromFS` refuses a bundle without the
   preset's metas.
2. `main.tsx` = `createShell({ app, modules: [booksModule] }).mount('#root')` with the react-router 8 data router;
   `booksModule = defineModule(manifest, …)` gathers the contributions F9-02 (`web/src/modules/books/layout/`), F9-04
   (`web/src/modules/books/pages/`) and F9-05 (`web/src/modules/books/activity/`, `storageKeys.ts`) left; everything
   else audiobook-specific moves under `web/src/modules/books/` with its ESLint boundary (07 §7.11);
   `useUnsavedChangesBlocker` deleted (the data router's `useBlocker`). **Re-export files:** F9-02, F9-04 and F9-05
   left a one-line re-export at each moved file's old `web/src/` path; re-point every importer (plan base: 148 for
   F9-05's pieces, 19 for F9-04's pages, plus the layout's) to the shell, ui-kit, api-client or books path and delete
   every re-export file in this PR.
3. `renderWithProviders` rewritten onto `renderInShell` (after the providers moved, FE-17).
4. The shell realtime client replaces the F6-rewritten `eventSourceManager` (§7.9.2); the Activity page in the shell
   on the generated core client (it no longer imports `ops.ts` or `operationPolling`).
5. ABO adopts the hello pattern: `web_embed.go` (`//go:build embed_frontend`) defines
   `func UIBundle() ui.Bundle { return ui.MustFromFS(WebFS, "web/dist") }`, `web_nonembed.go` returns `ui.Bundle{}`,
   and `main.go` passes `app.Options{UI: UIBundle(), UIOptions: …}` instead of `server.SetEmbeddedFS(WebFS)`;
   `UIOptions.PageMiddleware` = CF recovery from `internal/cfaccess`, `ConnectSrcFrom` = the Access team host, both
   read pointers books fills in its Start hook (FE3-4, FE4-5).
6. Books drops `ServesSPA`; gin's `NoRoute` SPA serving, `setupStaticFiles`, the `static_embed.go` SPA code and the
   `/api/v1/csp-report` handler are deleted; the kernel mounts `ui.Handler` with `Router.SPA`; books' unprefixed
   segments reach `NonSPA` through its mount's `TopSegments`; `ui.BasicExemptAssets` becomes the SPA's Basic-gate
   asset exemption (`TestBasicGate_ExemptSetGolden` repeated).
7. Server manifest and intersection on (§7.5); ABO URLs under `/books/…`; **no old-URL redirect** (D13): a former
   bookmark lands on `NotFound`; `ABS_REDIRECTS` stays books code with targets updated to the new paths.

## Invariants

- **I1.** `make build-api && ./audiobook-organizer serve` answers `/` with the placeholder (FE2-10).
- **I2.** `TestABOManifestBootsWithShell`: the kernel boots ABO's real shell-era manifest (books with its
  `TopSegments`, `SPAShared` `login`, the shell bundle) with no P8 problem.
- **I3.** The image job boots the embedded bundle; `X-App-Build` equals the embedded meta
  (`TestBuildID_HeaderEqualsEmbeddedMeta`).
- **I4.** The Basic gate's exempt set is unchanged across the switch (`TestBasicGate_ExemptSetGolden`,
  `TestBasicExemptAssets_EqualsHEAD`).
- **I5.** Exactly one owner of `/`; probe variants never answer HTML (`TestHandler_NonSPASegments`,
  `TestProbes_VariantsAreNotSPA`).
- **I6.** No old page path is redirected; every e2e spec uses the new paths; each `ABS_REDIRECTS` row reaches its
  new target (07 §7.18 "ABO e2e").
- **I7.** CF recovery runs on page loads only and the Access team host is in `connect-src` (the port of
  `static_embed_recovery_test.go`, booted through `app.New` with `app.Options.UIOptions`).

## Files

| Action | Path |
|---|---|
| modify / move | `web/` (`main.tsx`, `vite.config.ts`, `index.html`, `src/modules/books/`, test utils, e2e specs' paths) |
| modify | `main.go`, `core/app/options.go` (`UI`, `UIOptions` live) |
| modify / delete | `internal/server/` (`setupStaticFiles`, `static_embed.go`, `static_nonembed.go`, `NoRoute` SPA serving, `/api/v1/csp-report`, `SetEmbeddedFS`) |
| modify / create | `internal/books/module.go` (drop `ServesSPA`, `TopSegments`, `SPAShared`), `internal/books/ui.go` (books' UI manifest and `UIOptions` pointers) |
| modify | `web_embed.go`, `web_nonembed.go` (repository root; 07 §7.6.3: ABO's `UIBundle()` lives there) |
| modify | `core/kernel/kernel.go` (mount `ui.Handler` with `Router.SPA` for ABO; this PR owns it in W41) |
| create | one `changelog.d/` fragment (headerless) |

## Re-verify before editing (expected results at your base)

```bash
# Root embed files. Expected: web_embed.go (embed_frontend) and web_nonembed.go defining WebFS
grep -n 'go:build\|WebFS' web_embed.go web_nonembed.go
# main.go hands the FS to the server. Expected: 1 hit [evidence: main.go:34]
grep -n 'SetEmbeddedFS' main.go
# gin's SPA serving. Expected: setupStaticFiles in static_embed.go and static_nonembed.go; NoRoute in both
grep -n 'func (s \*Server) setupStaticFiles\|NoRoute(' internal/server/static_embed.go internal/server/static_nonembed.go
# The csp-report handler. Expected: 1 route
grep -rn 'csp-report' --include='*.go' internal/server/ | grep -v _test
# ServesSPA on books' mount. Expected: 1 hit
grep -rn 'ServesSPA' --include='*.go' internal/books/
# The shell and its preset. Expected: createShell and the vite preset present (F9-01)
grep -rn 'export function createShell' core/web/packages/shell/src/ ; ls core/web/packages/shell/vite/index.mjs
# The three cut rows' outputs. Expected: each directory present
ls -d core/web/packages/shell/src/layout core/web/packages/shell/src/pages core/web/packages/shell/src/providers \
   web/src/modules/books/layout web/src/modules/books/pages web/src/modules/books/activity
# The re-export files they left at old paths (each one or a few export lines). Read the list; this PR deletes them all
grep -rlE "^export .* from '@mediacore/(shell|ui-kit|api-client)" web/src | sort
# ABO's hand-written theme script. Expected: an inline <script> in web/index.html [evidence: lines 13-30]
grep -n '<script' web/index.html
```

STOP if F9-01's, F9-02's, F9-04's or F9-05's outputs are missing, or if `SetEmbeddedFS` is not where `main.go` hands
the FS over.

## Steps

1. Write the tests first (below); record the old→new page-path table (07 §7.11 last row) in the PR description.
2. Commit 1: `vite.config.ts` on `mediacore(…)`, the inline theme script deleted.
3. Commit 2: `web/src/modules/books/` (the cut rows' contributions are already there), every importer re-pointed and
   every re-export file deleted, `main.tsx` on `createShell` with the data router, `useUnsavedChangesBlocker` deleted, `renderInShell`, the shell realtime client, the Activity page in the
   shell; every route under `/books/`; `ABS_REDIRECTS` targets updated.
4. Commit 3: Go side: `web_embed.go`/`web_nonembed.go` `UIBundle()`, `main.go` passes `UI` and `UIOptions`, books drops
   `ServesSPA` and declares `TopSegments`/`SPAShared`, gin's SPA serving and `csp-report` deleted, `core/kernel/kernel.go`
   mounts `ui.Handler`.
5. Each commit builds and passes vitest; the PR as a whole passes e2e and the image job.
6. Update the e2e specs' paths; add a not-found check for a former path. Headers, fragment, acceptance.

## Code contract

```go
//go:build embed_frontend
func UIBundle() ui.Bundle { return ui.MustFromFS(WebFS, "web/dist") }

//go:build !embed_frontend
func UIBundle() ui.Bundle { return ui.Bundle{} }
```

```ts
createShell({ app, modules: [booksModule] }).mount('#root');
```

## Tests (write first; spec names copied exactly)

- `TestABOManifestBootsWithShell` (06 §6.10; this row owns it).
- The shell's done-check: `make build-api && ./audiobook-organizer serve` answers `/` with the placeholder (07 §7.18
  "API-only boot").
- `TestBasicGate_ExemptSetGolden` (repeated, unchanged set) and `TestBasicExemptAssets_EqualsHEAD`.
- `TestBasicGate_SPAOptionsExemptAssets` (05-http-api §12; this row owns it): from the shell flip,
  `Router.SPA(h, SPAOptions{BasicExemptAssets: ui.BasicExemptAssets})` exempts exactly HEAD's asset set on the shell,
  and `TestBasicGate_ExemptSetGolden` repeated in this PR passes.
- `TestUIAppIDMismatch` (06 §6.10 and 07 §7.18; this row owns it): a bundle whose `mediacore-app` meta differs from
  `app.Options.AppID` fails P8 with `ProblemUIAppIDMismatch`; ABO's own bundle, with an equal meta, boots.
- `TestBuildID_HeaderEqualsEmbeddedMeta` through the image job.
- The port of `static_embed_recovery_test.go` booted through `app.New` with `app.Options.UIOptions`.
- e2e: `theme-boot.spec.ts`, `stale-build.spec.ts` (ABO half), `stale-build-rollback.spec.ts`,
  `TestTempLoginLink_FragmentEndToEnd` and `TestInviteLink_FragmentEndToEnd` repeated at shell adoption, the bundle
  check, and the existing ABO specs on the new paths.
- `TestFormerPagePathIsNotFound` (name coined here): `/library` renders the shell's `NotFound`; `/books/library`
  renders the library.

## Acceptance (commands and expected output)

```bash
# API-only boot answers / with the placeholder. Expected: 200, then a count >= 1 (if core's placeholder page uses
# other wording, grep for the marker F9-01's TestHandler_Placeholder asserts)
make build-api && (./audiobook-organizer serve & echo $! > /tmp/f9.pid; sleep 5; \
  curl -s -o /tmp/root.html -w '%{http_code}\n' localhost:8484/; kill $(cat /tmp/f9.pid)); grep -c -i 'placeholder\|api-only' /tmp/root.html

# Gate tests. Expected: ok
go test -count=1 -run 'TestABOManifestBootsWithShell|TestBasicGate_ExemptSetGolden|TestBasicGate_SPAOptionsExemptAssets|TestUIAppIDMismatch' ./...
(cd core && go test -count=1 -run 'TestUIAppIDMismatch|TestBasicGate_SPAOptionsExemptAssets' ./...)
(cd core && go test -count=1 -run 'TestBasicExemptAssets_EqualsHEAD|TestHandler_' ./ui/...)

# No gin SPA serving left. Expected: 0
grep -rn 'setupStaticFiles\|SetEmbeddedFS\|ServesSPA' --include='*.go' . | grep -v _test | wc -l

# No re-export file left from the cut rows. Expected: 0
grep -rlE "^export .* from '@mediacore/(shell|ui-kit|api-client)" web/src | wc -l

# Full build with the embedded UI, then frontend and e2e. Expected: build ok; vitest and Playwright green
make build && make web-test && make test-e2e

# Clean tree after the build (FE7-1). Expected: no output
git status --porcelain

# Gates. Expected last lines: ✅ All CI checks passed! / exit 0
make ci
make -C core ci
```

The `docker-image` required check must boot the embedded bundle on this PR.

## Out of scope

- 07 §7.19 step 6 (the remaining `api.ts` sections), deleting `ops.ts`/opsgen (R3-02).
- Any server route change other than the SPA owner and `csp-report`.
- The release note's path list (R2-01 assembles it from 09 §9.6 row 21).

## Rollback

Before its release ships: revert the PR. In R2: R2's `make rollback-restore`. If F9 shipped in R3: `make rollback` to
R2 (09 §9.4 F9 row; plan §(b)).

## Repo rules

- Worktree off fresh `origin/main`; `npm ci --prefix web`; headers on every changed Go and TS file.
- `git mv` for moved files; the PR description lists `old ==> new` paths and the page-path table.
- Conventional commits ending with `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`;
  never squash; never `--admin` past a required check. No release or flip trailer (09 §9.2.1).
- No old-URL redirect may be added (D13).

## Planner notes

- **Settings' generic tabs.** 07 §7.11 classifies the Settings frame's generic tabs (Security, API Keys, Plugins →
  Integrations, AI backends, Scheduled tasks, Performance, Temp Login, System Info) as shell; no cut row lists their
  files (F9-04 passes them into the frame from ABO). Unless a cut row takes them, this PR moves them into the shell,
  which 07 §7.19 step 4 allows ("one PR series") but makes this PR larger.
