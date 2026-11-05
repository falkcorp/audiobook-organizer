<!-- file: docs/agent-tasks/platform-core/R1-13-kernel-app-module-httpapi-skeleton.md -->
<!-- version: 1.1.8 -->
<!-- guid: 49cec14b-15f1-4aaa-9773-e49334cd61a5 -->
<!-- last-edited: 2026-11-05 -->

# R1-13 — Kernel, app, module and the httpapi skeleton, internal/openapi, and the examples/hello skeleton that emits core/openapi/core.json

| Field | Value |
|---|---|
| ID | `R1-13` |
| Tier | **T4** (Opus / xhigh): the boot-phase machinery every later writer hangs off; lifecycle and stop order are normative |
| Release / wave | R1 / W12 |
| Depends on | R1-12, R1-09, R1-07, R1-10, R1-36 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 06 §6.4.10 R1 row, §6.5.1, §6.5.2, §6.11.1, §6.12 (hello); 03 §3.3 (app, module, kernel); 05-http-api §3, §4.8; 04 §8.1 R1 |
| Branch | `agent/platform-core-r1-13-kernel-app-module-httpapi-skeleton` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-35 until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `core/kernel/`
- `core/app/`
- `core/app/noglobals_test.go` (create; inside `core/app/`, named so the `TestNoGlobals` scan has a listed home)
- `core/module/`
- `core/httpapi/`
- `core/internal/openapi/`
- `core/examples/hello/`
- `core/openapi/core.json`
- `core/internal/archtest_layers_test.go`: control edit only (step 4)

**The shared wiring files this row creates, by name.** Later rows name these files in their own file lists (plan
§(d): one owner per wave), so create exactly these paths and keep each one's role:

| File | Holds |
|---|---|
| `core/kernel/boot.go` | the boot phases of 06 §6.5.1 and the services build order (R1-20, R1-21, R1-24 and R1-28 add their boot calls here) |
| `core/kernel/kernel.go` | per-module `Registrar` and `Deps` construction, collision checks, core route mounts |
| `core/module/manifest.go` | `Manifest` and its R1 fields |
| `core/module/registrar.go` | `Registrar` and its R1 members |
| `core/module/deps.go` | `Deps` |
| `core/app/options.go` | `app.Options` and `R1Values` |

From R2 on a dark row adds only its own `core/kernel/<subsystem>.go` and package files; its stage's wiring row
(F2-16) or its flip adds the boot calls, members and mounts here.

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-13" -b agent/platform-core-r1-13-kernel-app-module-httpapi-skeleton origin/main
cd "$REPO/.worktrees/platform-core-r1-13"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `ac4368bdb`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

## Goal

Land the boot machinery every later R1 and R2 writer hangs off, **core only** (no ABO file changes): `module`,
`kernel`, `app`, the `httpapi` skeleton, `internal/openapi`, and the `examples/hello` skeleton whose OpenAPI
subcommand writes `core/openapi/core.json` (06 §6.4.10 R1 row; 06 §6.5.1, §6.5.2; 06 §6.11.1 step 1 "core boots
with zero modules"; 06 §6.12; 03 §3.3 rows `app`, `module`, `kernel`; 05-http-api §3, §4.8; 04 §8.1 R1).

What R1-13 lands, by package (each member exactly as its spec section names it; a member whose package lands
later is **absent, not stubbed**, and the kernel phase that consumes it is skipped, 06 §6.4.10):

- `module` (L7): the `Module` interface, `Manifest` with the R1 fields of 06 §6.4.10 (`ID`, `Version`,
  `DisplayName`, `MinCoreVersion`, `DefaultEnabled`, `Settings`, `UI` (IDs only), `LegacyPrefixes`,
  `PublicRoutes`; `Storage` and `ResetHook` arrive with R1-20/R1-25), `Registrar` with the R1 members that need
  no storage (`Context`, `Deps`, `HTTP`, `MountLegacy` (the declaration; its behaviour is R1-14's), `AuthMethod`
  (recording only), `LogPathRules`, `LogOtherUserData`, `Hook`, `Worker`, `Err`), `Deps` (`Module`, `Log`,
  `Metrics`, `ExtendStart`), `module.ReservedIDs()` (03 §3.6), `ValidationError` (06 §6.4.7).
- `kernel` (L8): the boot-phase runner of 06 §6.5.1 restricted to the phases whose members exist, the run and
  stop order of 06 §6.5.2 (start steps 1, 2, 3, 7, 8 and their reverse), the supervisor with a **readiness
  notifier interface** (name coined here: `kernel.ReadinessNotifier` with `Ready()`, `Status(string)`,
  `Stopping()`), a no-op default, which R1-16 implements with sdnotify (05-health §5H.10 Landing R1),
  `Lifecycle.RequestRestart(reason)` exiting with `core.lifecycle.restart_exit_code` (default 75) after the stop
  order (06 §6.5.2 "Restart request"), and the one `core.shutdown.budget` (05-health §5H.4).
- `app` (L9): `New(ctx, Options, ...module.Module) (*App, error)`, `(*App).Run(ctx) error`, `Handler`, `Modules`,
  `ResolveStateDir`, `ExtendStart`, `Describe` (`Core`, `OpenAPI`), and the R1 `Options` fields that need no
  storage (`AppID`, `MetricNamespace`, `ServiceName`, `ReservedAPINamespaces`, `DisplayName`, `Version`,
  `EnvPrefix`, `StateDir`, `ConfigFile`, `Args`, `Env`, `LegacyAuthGinUntil`, `R1Values` with type `R1Values`,
  `Logger`) (06 §6.4.5, §6.4.10). `app.BinaryID` is R1-19's; `OpenOffline`/`OfflineCommands` are R1-25's (R1-24 adds
  `storage prune-checkpoints` to the list);
  `InstallProcessDefaults` is R1-15's.
- `httpapi` (L6): routes, groups, `Handle` with a required `rbac.Requirement` (zero rejected), `Build` with
  `ServeMux` pattern conflict detection (05-http-api §3, §4.8), the listener lifecycle, the problem shape and the
  legacy error envelope, the `Bind`/`Decode` helpers, `Page`/`Cursor`, the SPA claim (`Router.SPA`,
  `RouteInfo{Kind: SPA}`) and the `NonSPA` predicate (05-http-api §4.10); the perimeter stages are R1-17's and
  `MountLegacy` is R1-14's.
- `internal/openapi`: builds OpenAPI 3.1 from a leaf route-description type; it never imports `httpapi`
  (`httpapi` → `internal/openapi`, 04 §8.1 R1, MOD3-4); schemas through `internal/jsonschemax`.
- `examples/hello`: module + app + one route `GET /api/hello/v1/greeting`, declared `rbac.Public` and listed in
  `Manifest.PublicRoutes` until core auth exists (row notes; 06 §6.12.2), `MinCoreVersion: "0.1.0"`, and the
  `openapi` subcommand that writes `core/openapi/core.json` (06 §6.12; 07 §7.19 step 3 consumes that file; the
  command line is defined below). Hello's
  keyspace and seed migration are R1-20's (`core/examples/hello/storage.go`).

**The `openapi` command (name coined here; 06 §6.12.5 shows a `main` with no argument handling, and 06 and 05-http-api
say only that hello's `openapi` subcommand writes `core/openapi/core.json`).** `hello openapi [--out <file>]` is reached
through `app.Options.Args`: it builds `Description.Core` in memory (no I/O besides the write, `TestDescribeInMemoryNoIO`)
and writes it to `<file>`, defaulting to `core/openapi/core.json` resolved from the module root. R1-30's generator and
F9-01 call exactly this command line, so do not rename the flag. The freshness gate is `make openapi-check`
(05-http-api §4.6): it runs `hello openapi --out <tmp>` and `diff`s the result against the committed
`core/openapi/core.json`, failing on any difference. The `core-ci` target lives in `core/Makefile`, which is **not** in
this row's files: record under the PR's "Scope notes" that R1-30 (W21, the owner of `core/Makefile` in its wave) adds the
`openapi-check` target and its place in `make -C core ci`. This row's own check is `TestOpenAPI_Golden` and the
acceptance below.

## Invariants and tests (write first; spec names from 06 §6.10 and 05-http-api §12)

- Boot order is 06 §6.5.1's table and nothing else; start/stop order is 06 §6.5.2's: `TestBootZeroModules`,
  `TestStartOrder`, `TestStartRollbackReverseOrder` (a failing hook stops every started hook in reverse),
  `TestRegistrarClosedAfterRegister`, `TestNewCleansUpCoreGoroutines`, `TestDescribeInMemoryNoIO`,
  `TestRestartRequest_ExitsRestartCode` (exit 75 after the stop order, never `os.Exit(0)`).
  06 §6.10 pairs and rows of the same subject: `TestStopOrder` (a bus handler and an outbox consumer of hello never
  observe the module after its Stop hook, instrumented hooks under `synctest`; the bus and outbox halves are asserted
  over the hooks this row has, the rest by the rows that land them), `TestStopTimeoutAbandons` (a hook that outlives
  `core.shutdown.budget` is abandoned and the stop order continues; `synctest` timeouts),
  `TestValidation_<ProblemKind>` (a table, one row per `module.ValidationError` kind this row implements: each
  validation path fails closed with the right kind) and `TestValidation_AllProblemsJoined` (several problems are
  reported together), `TestWorkerPolicies` (`DegradeOnExit` versus `StopAppOnExit`, panic recovery) and
  `TestRegistrarContextCancelled` (late calls are recorded; `r.Context()` is cancelled with `app.New`'s ctx).
- `TestSpecGoBlocksCompile` (06 §6.10, COR-16) for the R1 blocks: every Go block of 06 marked as part of the hello
  example that exists at this row is compiled against core in CI and booted through `app.New` (a manifest-level
  refusal fails the test, not only a compile error). **The test reads a committed snapshot, never the spec.** The
  blocks live as files under `core/examples/hello/testdata/specblocks/` (directory name coined here), one `.go.txt`
  file per block plus `blocks.json` (name coined here) recording each block's spec file, heading and sha256. A
  committed script, `core/examples/hello/testdata/specblocks/regen.py` (name coined here; Python, CLAUDE.md rule 4),
  takes the spec file's path as an argument, extracts the marked blocks and rewrites the snapshot; `--check` exits
  non-zero when the snapshot differs from the spec. The test copies the snapshot into a temp module and builds and
  boots it; it opens nothing outside `core/`, so a spec-only edit cannot break `core-ci`, and the test survives the
  repo split. `--check` is not wired into any required check (a spec edit refreshes the snapshot in its own PR).
  R1-37 builds `coretest.New` and switches the boot to it; each later row that grows hello re-runs `regen.py` and
  commits the refreshed snapshot with its blocks.
- IDs: `TestReservedIDsIncludeLegacyTopSegments`, `TestReservedSegmentsDisjointFromNonSPA`.
- Routes: `TestHandle_RejectsZeroPermission`, `TestHandle_RejectsForeignScope`, `TestBuild_PatternConflictIsError`,
  `TestBuild_PrefixCollision_FacadeVsFacade`, `TestBuild_RejectsCamelCaseJSON`, `TestProblem_ShapeGolden`,
  `TestUnmatched_APIPathIsProblemNotHTML`, `TestMethodNotAllowed_HasAllow`, `TestDecode_StrictUnknownField`.
- Routes, the rest of 05-http-api §12 for the skeleton (each one line, from the spec row):
  `TestBind_TagsCompiledAtRegistration` (a bad binding tag gives `ErrBadBinding` at boot),
  `TestPage_OffsetDefaultHasTotal` and `TestCursor_FilterMismatch` (offset paging carries a total by default; a cursor
  presented with a different filter is refused; SIMP-8), `TestLegacyError_ShapeGolden` (the legacy error envelope
  byte-for-byte for the router's legacy-prefix 404; its pair `TestProblem_ShapeGolden` is above),
  `TestUnclean_PathNotRedirectedByMux` (`//ping` is not 301-redirected; the `/` owner decides),
  `TestSPA_NotRateLimitedAndPublic` (200 cold-load requests to the `/` owner are not 429 and
  `RouteInfo{Kind: SPA}` is present; the rate limit itself is R1-17's stage, so assert the route's `Kind` and the
  absence of any rate class on it here), `TestSPA_SingleOwnerOfRoot` (a legacy `ServesSPA` plus `Router.SPA` gives
  `ErrPrefixCollision`; FE-1), `TestNonSPA_PredicateCoversClaims` (`/socket.io`, `/API/V1/x`, `POST /library`,
  `/health/`, `/HEALTH`, `/metrics/` and `/livez/` never reach the SPA handler; `/assets/x.js` and `GET /login` do),
  `TestNonSPA_LegacyFallbackConsultsPredicate` (with the legacy mount owning `/`, `/livez/` and `/READYZ` get problem
  404, never gin's `NoRoute` page; the legacy half is exercised with a stand-in mount until R1-14, whose
  `MountLegacy` then re-runs it), `TestStage2_RouteSurvivesRequestClone` (a stage between 2 and the mux that clones the
  request with `r.WithContext` leaves a native route's `http_route` and span name correct; the perimeter's real stage
  2 is R1-17's, so this row drives a test-only cloning stage over `Build`'s router and R1-17 repeats the cell over the
  real chain with a legacy route).
- OpenAPI: `TestOpenAPI_Golden` over hello (the golden is `core/openapi/core.json` itself; a test runs `hello openapi
  --out` into a temp dir and diffs it against the committed file, failing on any difference) and
  `TestOpenAPI_IsValid31` (the generated document validates against the OpenAPI 3.1 meta-schema).
- Hello: `TestHelloEndToEnd` for the R1 surface (boot, `GET /api/hello/v1/greeting` 200, clean stop); the parts
  that need storage, tasks or auth are added by the rows that land them.
- No process-global state in core (03 R5): **this row owns `TestNoGlobals`** (05-metrics §12: no
  `prometheus.DefaultRegisterer/DefaultGatherer/MustRegister/Register`, `promhttp.Handler()`,
  `otel.Set*`/`otel.Get*Provider`/`otel.Tracer`/`otel.Meter` in core, except `otel.SetMeterProvider`,
  `otel.SetTracerProvider` and `otel.SetTextMapPropagator` inside `app.InstallProcessDefaults`, which R1-15 adds).
  It is a source scan of every non-test `.go` file under `core/`; write it in `core/app/noglobals_test.go` (file
  name coined here, in this row's files; this row does not touch the `core/archtest/` package). Also no `slog.SetDefault` and
  `signal.NotifyContext` only in `core/examples/hello/cmd/` (a `main`).
- Kernel metrics: **this row owns `TestKernelFamiliesGathered`** (05-metrics §12, a `kernel` test): one zero-module
  boot on a fresh Provider; the scrape of that App's `Provider.Handler()` carries every kernel-emitted family that
  exists at this row, `<namespace>_kernel_boot_phase_seconds`, matching a core golden
  (`core/kernel/testdata/kernel_families.golden`, name coined here). The storage families
  `<namespace>_storage_level` and `<namespace>_storage_checkpoint_bytes` do not exist until R1-20; write the golden
  as a list R1-20 extends. The kernel is a core metrics scope, so also add its `core/kernel/testdata/series.golden`
  (family, type and label set), asserted by `metricstest.AssertSeries` (built by R1-10; 05-metrics §6 item 5).
- Core guard control: once `app` and `kernel` exist the in-module control becomes `app` → `kernel`
  (03 §3.5.1 item 2, third step; MOD3-8). Set it in this PR and say so. The file is
  `core/internal/archtest_layers_test.go`, created by R1-01 with the whole layer table; this row edits only the
  control.
- The coretest harness (`coretest.New`, `Boot`, the `With...` options, `AssertPermission`, `AssertModuleInert`,
  `archtest.RoutesHavePermissions`) is **R1-37's**, not this row's; this row's tests boot through `app.New` directly
  and list no `core/coretest/` file.
- Any 06 §6.10 row whose subject this PR lands and that is not listed above: write it too, or list it in the PR as
  deferred with the row that brings its dependency.

## Dependency check

R1-12, R1-09, R1-07, R1-10, R1-36 merged: `test -d core/rbac && test -d core/health && test -d core/log && test -d core/metrics && test -f core/log/logging.go && echo ok`. `app.New` constructs R1-36's `log.Logging` and passes `Logging.Module(id)` as `Deps.Log`.

## Re-verify

```bash
S=docs/specs/platform-core
sed -n '/^### 6.5.1/,/^### 6.5.2/p' $S/06-app-sdk.md | grep -c '^| P'    # expect 29 phase rows; your kernel implements the rows whose members exist
grep -n 'GET /api/hello/v1/greeting' $S/06-app-sdk.md | head -2         # expect the §6.12.2 heading
test ! -e core/kernel && test ! -e core/app && test ! -e core/openapi/core.json && echo NEW   # expect NEW
git diff --stat origin/main -- . ':!core' | tail -1                       # at the end of the task: expect no ABO file changed
```

## Steps

1. Write the tests above (red), with `TestOpenAPI_Golden` reading `core/openapi/core.json`.
2. `core/module` (`manifest.go`, `registrar.go`, `deps.go`, plus `module.go` for the interface and
   `ReservedIDs`), then `core/internal/openapi` (leaf route-description type), then `core/httpapi` (routes, `Build`,
   listeners, problem shape), then `core/kernel` (`boot.go`: phases that exist and the services build order;
   `kernel.go`: per-module construction, collisions, mounts; plus the supervisor with the readiness notifier, the
   restart request and the shutdown budget), then `core/app` (`options.go` with the R1 `Options` fields; `New`,
   `Run`, `Describe`).
3. `core/examples/hello`: module, app `main` (the only `signal.NotifyContext` in core), the greeting route, the OpenAPI
   subcommand; generate and commit `core/openapi/core.json`. Write `testdata/specblocks/regen.py`, run it on
   `docs/specs/platform-core/06-app-sdk.md` and commit the snapshot.
4. Core layering guard (`core/internal/archtest_layers_test.go`): the layer table is complete from R1-01 and needs
   no change for the new packages; set the in-module control to `app` → `kernel` and nothing else in the file.
5. Header bumps; fragment `changelog.d/<today>_core_kernel_app.md` (`### Added`: "Internal: the core runtime
   skeleton (module, kernel, app, httpapi) and the hello example."). Commit(s) per package; every commit builds.

## STOP conditions

- A member would need a package that lands later (storage, config, auth, tasks): leave it absent and skip its
  phase; never stub it.
- Anything would require an ABO file change (this row is core only; R1-15 wires ABO).
- `internal/openapi` would need to import `httpapi`.

## Code contract

Exactly the members named in Goal, with the signatures of 06 §6.4.1–§6.4.7 (and 05-http-api §3 for `httpapi`); `kernel.ReadinessNotifier` (coined) is `interface{ Ready() error; Status(string) error; Stopping() error }`.

## Acceptance

```bash
go -C core build ./... && go -C core vet ./... && go -C core test ./... -race -count=1     # expect ok
go -C core test ./app/ ./kernel/ -run 'TestNoGlobals|TestKernelFamiliesGathered' -count=1 -v | grep -c '^--- PASS'   # expect 2
ls core/kernel/boot.go core/kernel/kernel.go core/module/manifest.go core/module/registrar.go core/module/deps.go core/app/options.go   # expect all six
T=$(mktemp -d); go -C core run ./examples/hello/cmd/hello openapi --out "$T/core.json" && diff "$T/core.json" core/openapi/core.json && echo FRESH   # expect FRESH (diff exits non-zero on a stale core.json)
git grep -n 'slog.SetDefault\|otel.Set\|http.DefaultServeMux\|signal.Notify' -- core ':!core/examples/*/cmd/**' ':!*_test.go' | wc -l   # expect 0 (test files hold the literals the scans look for)
python3 core/examples/hello/testdata/specblocks/regen.py --check docs/specs/platform-core/06-app-sdk.md && echo SNAPSHOT_FRESH   # expect SNAPSHOT_FRESH
grep -rn 'docs/specs' core --include='*.go' | wc -l                                        # expect 0 (no Go code in core reads the spec)
git diff --name-only origin/main | grep -v '^core/' | grep -v '^changelog.d/' | wc -l      # expect 0
make ci && make -C core ci                                                                 # expect exit 0
```

## Out of scope

`MountLegacy`'s behaviour (R1-14); the coretest harness and `archtest.RoutesHavePermissions` (R1-37); the `make openapi-check` target in `core/Makefile` (R1-30); the perimeter (R1-17); the ABO composition root (R1-15); sdnotify (R1-16);
storage (R1-20); build identity (R1-19); offline opens (R1-25) and the prune command (R1-24).

## Rollback

Revert the PR; ABO does not import any of it yet.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green, `gh pr merge <n> --rebase` (enqueues it; the merge queue re-tests it on top of `main`, D15). Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
