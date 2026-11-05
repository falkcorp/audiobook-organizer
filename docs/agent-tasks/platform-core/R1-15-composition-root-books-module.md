<!-- file: docs/agent-tasks/platform-core/R1-15-composition-root-books-module.md -->
<!-- version: 1.1.4 -->
<!-- guid: 3f22576e-6cf0-4db5-afca-5c9f763dca9c -->
<!-- last-edited: 2026-11-05 -->

# R1-15 — Composition root: ABO's main becomes app.New(ctx, opts, books.Module(store)) with internal/books, the atom table, MountLegacy(LegacyAuthGin), lifecycle and listeners, the 25 s shutdown budget, app.InstallProcessDefaults and the App's private registry, R1Values

| Field | Value |
|---|---|
| ID | `R1-15` |
| Tier | **T4** (Opus / xhigh): ABO's process entry point changes; listeners, stop order and process-global installs move together |
| Release / wave | R1 / W12 |
| Depends on | R1-13, R1-14, R1-10, R1-09, R1-07 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 06 §6.11.1, §6.4.10 R1 (app.Options R1 fields, R1Values), §6.5.2 (stop order); 05-health §5H.4 (core.shutdown.budget); 05-metrics §13 items 1-2; 05-logging §11 items 1, 4; 05-config §1.12.1 (R1Values); 05-rbac §4.9, §6.1 (atom table is books code); 04 §2.4 (internal/books); 09 §9.4 R1 Content: runtime |
| Branch | `agent/platform-core-r1-15-composition-root-books-module` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-35 (unless OWNER-01 overrode it) until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `main.go`
- `cmd/root.go`
- `internal/books/`
- `core/app/`
- `core/coretest/metrics.go`
- `internal/server/server.go`
- `internal/server/server_lifecycle.go`
- `internal/telemetry/telemetry.go`
- `internal/telemetry/errorhandler.go`
- `internal/telemetry/sdklog.go`
- `internal/metrics/`
- `internal/operations/registry/register.go`
- `internal/updater/register.go`
- `internal/telemetrypolicy/series_contract_test.go` (moved there by R1-10 from `internal/telemetry/contract/`)
- `internal/arch/layering_test.go`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-15" -b agent/platform-core-r1-15-composition-root-books-module origin/main
cd "$REPO/.worktrees/platform-core-r1-15"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

ABO's process entry point becomes the composition root (06 §6.11.1 step 2; 09 §9.4 R1 "Content: runtime"; 04 §2.4
`internal/books`): `main` (through `cmd`'s `serve`) builds `app.New(ctx, opts, books.Module(store))` and runs it.
Until R1-23 moves the store open into the kernel, `serve` still runs HEAD's pre-server steps through
`internal/startup` (store open, books' migrations, encryption, `loadConfigFromDB`; MOD3-1) and hands the open
store to `books.Module(store)` (06 §6.11.1 step 2; 03 §3.5.1 item 1: "`books.Module(store)` only on the
intermediate `main`s of R1").

In the same PR, because they move together (row notes; 05-metrics §13 items 1–2; 05-logging §11 items 1 and 4):

- `internal/books` (new, computed layer 6, imported only by `.` and `cmd`; enters `layerOf` here, 03 §3.5.1
  item 1): `Module(store)`, the manifest, the `Registrar` calls. `books.Register` builds today's `*server.Server`
  and mounts its gin engine with `Registrar.MountLegacy` (R1-14) under the declared legacy prefixes, with
  `Auth: LegacyAuthGin`, allowed through `app.Options.LegacyAuthGinUntil: "books"`, `ServesSPA: true`
  (06 §6.11.1 step 2; 05-http-api §8.1 R1).
- **The atom table** (books code, not stored data; 05-rbac §6.1, §4.9): the 13 atoms and the every-atom row →
  native scopes and the route requirement per atom; the manifest's `rbac.LegacyAtom(atom)` entries resolve through
  it (`TestManifest_SPermRoutesCarryAtomRequirement`, built with no catalog until F4).
- **Lifecycle and listeners**: the kernel owns listeners and the stop order of 06 §6.5.2 within one
  `core.shutdown.budget` = 25 s (8 s in a container when unset; 05-health §5H.4), below the unit's new stop
  timeouts (R1-27) and `KillMode=mixed`. `app.Options.R1Values`: ABO's `main` reads its effective config at boot
  (UI-saved blob values included) and passes the listener and perimeter values, which the kernel calls once at
  06 §6.5.2 start step 7 (05-config §1.12.1; 05-http-api §8.1 R1). Leave `R1Values.ShutdownBudget` at 0 (core's
  default); R1-33 reads `ABO_CORE_SHUTDOWN_BUDGET` into it and sets the compose and plist stop timeouts.
- **Process-global installs move to core**: `app.InstallProcessDefaults` (the logger, the OTel error handler, the
  meter and tracer providers and the propagator; 05-logging §3, §11 item 4) and the App's private registry as the
  served one: ABO's client_golang families (`internal/metrics`, the five `operations_*` families and the two
  process gauges included) register through `Provider.Registerer()`; `metrics.Register`'s `sync.Once` global
  disappears; the legacy gin `/metrics` route serves `Provider.Handler()` until F8 (05-metrics §6, §13 items 1–2).
  `internal/telemetry`'s install code (`otel.SetTracerProvider` `telemetry.go:287`, `otel.SetMeterProvider` `:461`,
  `errorhandler.go:121`, `sdklog.go:39`) is deleted; the six packages that create instruments through the globals
  keep their series because the installed globals are the App's provider (05-metrics §13 item 1).
- The `init()` registrations of `internal/operations/registry/register.go` and `internal/updater/register.go` are
  replaced by explicit construction in `internal/books` (row notes).

The 05-logging deletions (second `slog.SetDefault`, `log.SetOutput`, `setupFileLogging`) and the activity tap are
R1-18's, not this PR's.

## Invariants and tests

- `TestBooksMinCoreVersion` (09 §9.4 R1 classification; root `internal/books`, in root `make ci`): books'
  `Manifest.MinCoreVersion` ≤ core's `version.Version`.
- `TestAtomTable_Golden` (05-rbac §12): the 13 rows and the every-atom row; every requirement is `AllOf` all of its
  row's native scopes; no single-scope requirement is `AuthRequired`; `playlists.create` has none; every scope the
  table writes is in the core catalog or books' manifest (for R1, list the scopes the catalog will hold; the
  catalog lands at F2).
- `TestManifest_SPermRoutesCarryAtomRequirement` (05-http-api §12), in `internal/books` (it reads R1-14's
  manifest and this PR's atom table): every manifest entry recorded from an `s.perm(atom)` route has `Perm` equal to
  books' atom-table entry, built with no catalog.
- `TestOnlyCompositionRootImportsModule` (06 §6.10): no package other than `.`, `cmd`, `internal/books` imports
  `mediacore/module` or `mediacore/app` (also the ABO guard's assertion from R1-01).
- Series unchanged: `internal/telemetrypolicy`'s contract test (`series_contract_test.go`) now scrapes the
  **served** `Provider.Handler()` and its golden does not change; `TestDeployReferencedSeriesServed` stays green.
  **This row owns three 05-metrics §6/§12 tests** (spec names copied exactly):
  - `TestContractScrapesServedHandler` (in `internal/telemetrypolicy/series_contract_test.go`): the handler the
    contract test scrapes is the one the legacy gin `/metrics` route serves (the kernel's `Router.Infra` mount
    from F8).
  - `TestABOMetricsGatherNoDuplicate` (in `internal/books/`; this row creates it, F2-03 extends it): the kernel
    Provider together with ABO's real recorders on it (`app.InstallProcessDefaults`, ABO's legacy client_golang
    families registered through `Registerer()`, `opsmetrics.Default()` while the package exists, books'
    recorders); drive one legacy HTTP request on a sampled route through the production gin engine and, while ABO
    records any, one AI call through a fake endpoint; `Gatherer().Gather()` returns **no error** and no family has
    two owners (05-metrics §6). The task-run and core-route legs arrive with F2-03.
  - `TestFamilyTaken_ThroughGlobalProvider` (05-metrics §12; in `core/app/`'s tests, next to
    `InstallProcessDefaults`): after `app.InstallProcessDefaults`, an instrument created through
    `otel.GetMeterProvider()` in another scope with a name a core `Scope` instrument holds → `ErrFamilyTaken`, a
    no-op instrument, boot fails during boot and `metrics_family_taken` is raised after it; the identical
    instrument created twice in one scope is one instrument.

  05-metrics §6 builds these Providers through `coretest.MetricsProvider` (06 §6.4.8). No earlier row adds it: create
  it in `core/coretest/metrics.go` (this row's file) and build the Providers through it.
- `TestInstallProcessDefaults_RestoreAndExclusive` (05-logging §10; in `core/app/`'s tests, next to
  `InstallProcessDefaults`): after install, `otel.GetMeterProvider()`, `otel.GetTracerProvider()` and
  `otel.GetTextMapPropagator()` return the App's, an instrument created through the global appears in the App's
  scrape, and `slog.Default()` logs through the pipeline; restore puts all seven back; a second install errors; the
  OTel sink drops values. R1-36 does not own it: this row builds `app.InstallProcessDefaults`.
- `TestBooksDisabledInert` (06 §6.10; G1 for the real module): with books disabled in the config, `app.New` boots, books'
  `Register`, migrations, hooks and workers are never invoked, and the legacy mount is absent. Assert it by booting
  through `app.New` with a recording books stand-in at the module seam; R1-37's `coretest.AssertModuleInert` is not
  available at this row.
- `TestR1AppConfigFromBlob` (06 §6.10; COR4-1, R4-6): in R1 a value set only in the settings blob is effective in
  `AppConfig` after boot, and a `PUT /api/v1/config` that changes one field leaves every other non-default blob field
  byte-identical.
- Behaviour: the full Go suite, e2e and the ABS conformance suite pass unchanged; a `SIGTERM` stops within the
  budget in 06 §6.5.2's order (a kernel test with fake hooks records the order).

## Dependency check

R1-13, R1-14, R1-10, R1-09, R1-07 merged:
`test -f core/httpapi/legacy.go && test -d core/kernel && test -d core/metrics && echo ok`.

## Re-verify (expected at `f7ac5b11e`; R1-11 and R1-18 do not touch these lines)

```bash
sed -n 8,20p main.go                                                   # expect imports of cmd, internal/database, operations/registry, sdnotify, server
grep -n '^func init()' internal/operations/registry/register.go internal/updater/register.go   # expect one each
grep -n 'otel.SetTracerProvider\|otel.SetMeterProvider' internal/telemetry/telemetry.go        # expect :287 :461
grep -n 'otel.SetErrorHandler' internal/telemetry/errorhandler.go; grep -n 'otel.SetLogger' internal/telemetry/sdklog.go   # expect :121 and :39
grep -rn 'sync.Once' internal/metrics/*.go | grep -v _test | head -3   # the Register once-guard to delete
grep -rn 's\.perm(' internal/server/*.go | wc -l                       # 421 at f7ac5b11e: every one resolves through the atom table
test ! -e internal/books && echo NEW                                   # expect NEW
```

## Steps

1. Tests first (red): the six owned tests (`TestBooksMinCoreVersion`, `TestAtomTable_Golden`,
   `TestManifest_SPermRoutesCarryAtomRequirement`, `TestABOMetricsGatherNoDuplicate`,
   `TestFamilyTaken_ThroughGlobalProvider`, `TestContractScrapesServedHandler`), `TestBooksDisabledInert`,
   `TestR1AppConfigFromBlob`, the stop-order kernel test, `TestOnlyCompositionRootImportsModule`.
2. `internal/books`: `Module(store)`, manifest (R1 fields), `Register` building `*server.Server` and calling
   `MountLegacy`; the atom table; explicit construction replacing the two `init()` registrations; add
   `"internal/books": 6` to `layerOf`.
3. `cmd`'s `serve` and `main.go`: run HEAD's pre-server steps, build `app.Options` (with `R1Values` from the effective
   config, `LegacyAuthGinUntil: "books"`), call `app.New(ctx, opts, books.Module(store))` and `Run`; move listener
   start/stop out of `internal/server/server_lifecycle.go` into the kernel.
4. `app.InstallProcessDefaults` and the private registry: delete `internal/telemetry`'s installs, register
   `internal/metrics`' families through `Provider.Registerer()`, drop `metrics.Register`'s `sync.Once`, serve
   `Provider.Handler()` on the gin `/metrics` route.
5. Header bumps; fragment `changelog.d/<today>_composition_root.md` (`### Changed`: "The server starts through the core
   runtime; shutdown follows one ordered budget."). Commits: one per numbered step if each builds, else one.

## STOP conditions

- A served metric family would change name or labels (that is R1-17's HTTP change only).
- The stop order cannot meet 06 §6.5.2 because an ABO goroutine ignores ctx: list it; do not raise the budget.
- Any package below layer 6 would have to import `module` or `app` (inject a func instead, 03 §3.5.1 item 1).

## Code contract

`books.Module(store *database.PebbleStore) module.Module` (R1-23 drops the argument); `app.InstallProcessDefaults` as 05-logging §3 declares it; `app.Options.R1Values` with type `R1Values` (05-config §1.12.1).

## Acceptance

```bash
go test ./internal/books/ -run 'TestBooksMinCoreVersion|TestAtomTable_Golden|TestOnlyCompositionRootImportsModule|TestBooksDisabledInert|TestR1AppConfigFromBlob' -count=1 -v   # expect PASS
go test ./internal/books/ -run TestManifest_SPermRoutesCarryAtomRequirement -count=1 -v    # expect PASS
go test ./internal/books/ -run TestABOMetricsGatherNoDuplicate -count=1 -v            # expect PASS
go test ./internal/telemetrypolicy/ -run TestContractScrapesServedHandler -count=1 -v  # expect PASS
go -C core test ./app/ -run TestFamilyTaken_ThroughGlobalProvider -count=1 -v          # expect PASS
go test ./internal/telemetrypolicy/ -count=1                                         # expect ok, golden unchanged
git grep -n 'otel.Set\|prometheus.MustRegister\|DefaultRegisterer' -- internal/telemetry internal/metrics | grep -v _test | wc -l   # expect 0
make build-api && ./audiobook-organizer serve --help >/dev/null && echo BOOTS_HELP     # expect BOOTS_HELP
make test-e2e                                                                        # expect green
make ci && make -C core ci                                                           # expect exit 0
```

## Out of scope

The kernel opening the store (R1-23: `books.Module()` without the store argument); the perimeter move (R1-17);
sdnotify (R1-16); logging stage 1's deletions and the tap (R1-18); build identity (R1-19).

## Rollback

Revert the PR (one PR, so `main` and `internal/books` go back together). No stored data changes.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.


## Planner notes

- 05-logging §11 item 1 is split between this row (`app.InstallProcessDefaults`, with item 4) and R1-18 (the
  deletions, the activity tap, file output). The inventory cites item 1 for both rows; the brief draws the line
  explicitly so the two PRs do not both claim the second `slog.SetDefault`.
