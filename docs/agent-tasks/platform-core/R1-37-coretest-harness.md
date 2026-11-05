<!-- file: docs/agent-tasks/platform-core/R1-37-coretest-harness.md -->
<!-- version: 1.0.5 -->
<!-- guid: 8ff510eb-5437-44c5-a272-58a710c5dbc7 -->
<!-- last-edited: 2026-11-05 -->

# R1-37 — coretest harness (06 §6.4.8): New, Boot, the With… options, Harness accessors, Client, AssertPermission, AssertModuleInert, AssertRoutesHavePermissions with archtest.RoutesHavePermissions over hello, ContractGoldens

| Field | Value |
|---|---|
| ID | `R1-37` |
| Tier | **T3** (Opus / high): the public test kit every later row tests through; the API-stability gate freezes it |
| Release / wave | R1 / W15 |
| Depends on | R1-13, R1-14 (each merged on `main` before you start), R1-15 |
| Spec | 06 §6.4.8 (`coretest` block: copy it), §6.10 (`TestBootZeroModules`, `TestDisabledModuleInert`, `TestSpecGoBlocksCompile`), §6.4.10 R1 row; 03 §3.5 R6 (every route declares a permission; `archtest.RoutesHavePermissions` over the built router and the legacy manifest); 05-http-api §12 (`archtest.RoutesHavePermissions`, `TestContractGoldens_ValidateAgainstSchema`), §7 (`coretest.ContractGoldens`), §4.10 (the SPA/infra known set) |
| Branch | `agent/platform-core-r1-37-coretest-harness` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the
named required checks of 09 §9.2.1 that exist at your base.

**Files the inventory assigns to this task** (touch nothing outside this list without recording it under the PR's
"Scope notes"):

- `core/coretest/harness.go`
- `core/coretest/client.go`
- `core/coretest/assert.go`
- `core/coretest/harness_test.go`
- `core/archtest/routes_permissions.go`
- `internal/books/routes_permissions_test.go` (create)

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-37" -b agent/platform-core-r1-37-coretest-harness origin/main
cd "$REPO/.worktrees/platform-core-r1-37"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and
`go -C core build ./...`. R1-15 (`core/coretest/metrics.go`) and R1-21 (`core/coretest/crashmatrix/`) add files to
the same package in W14, the wave before this one, so both are on `main` at your base; keep the package compiling with
them. This row's W15 sibling, R1-23, touches no `core/coretest/` or `core/archtest/` file.

## Goal

Land 06 §6.4.8's in-process `App` harness, so every later row's tests boot core through one public kit instead of
calling `app.New` by hand, and the R6 permission gate exists as both an archtest and a harness assertion.

What exists at R1 decides what each member can do now. Build each member with the spec's exact signature; where the
subsystem it reads lands later, the member checks only what exists at your base, and the row that lands the subsystem
extends it (F2-16 already extends `harness.go` with `AssertModuleInert`'s held-runs cell).

| Member (06 §6.4.8) | At this row |
|---|---|
| `Harness`, `New`, `Boot`, `Option` | full: in-memory Pebble (`vfs.NewMem`), a temp state directory (`Options.StateDir`), no listeners; requests go to `App.Handler()`; zero modules without `WithModules` (G7); `t.Cleanup` shutdown; `New` fails the test on a boot error, `Boot` returns it |
| `WithModules`, `WithDisabled`, `WithConfig` | full, over what R1's kernel reads (`WithDisabled` writes `core.modules.<id>.enabled=false` into the boot record; `WithConfig` is the config overrides layer that R1-13's `app.Options` accepts) |
| `WithUser`, `Harness.As`, `Harness.Anonymous`, `AssertPermission` | see "Authentication at R1" below |
| `Harness.App`, `Client`, `Client.Do`, `Client.GetJSON` | full |
| `AssertRoutesHavePermissions` and `archtest.RoutesHavePermissions` | full, over the built router: native routes, the legacy manifest (R1-14) and the SPA/infra known set |
| `AssertModuleInert` | the R1 cells: no route answers under `/api/<id>/` or the module's legacy prefixes (404, not 403); no hook or worker of the module ran; its keyspace data is unchanged. Later cells (task kinds and schedules, held runs, `GET /api/core/v1/modules`, `RecordingFS`, outbox backlog, op-ledger gate) are added by the rows that land those subsystems |
| `ContractGoldens` | SPEC-GATED: see step 0 |
| `WithAdapters`, `WithRuns`, `RunTask`, `RunResult`, `Storage`, `FS`, `MetricsProvider` | not this row's (see "Out of scope") |

**Authentication at R1.** 06 §6.4.10's R1 row has the `auth` leaf types (R1-12: `auth.Principal`,
`auth.WithPrincipal`/`PrincipalFrom`) but no core authentication; hello's greeting is `rbac.Public` until R2's identity
flip (06 §6.4.10, the hello paragraph). Check at your base:

```bash
git grep -n 'PrincipalFrom(' -- core/httpapi | wc -l     # >= 1: the router reads a bound principal at stage 13
```

- If the router authorises native routes from `auth.PrincipalFrom(ctx)`: `WithUser(name, perms...)` records a
  principal holding exactly `perms`; `As(name)` returns a `Client` whose requests carry that principal through
  `auth.WithPrincipal` before `App.Handler()`; `Anonymous()` carries none; `AssertPermission` runs its two requests (a
  user lacking `perm` gets 403; a user holding exactly `perm` gets neither 401 nor 403).
- If it does not (0 hits): build the members with the exact signatures; `WithUser` records the principal, and `As`
  and `AssertPermission` fail the test with
  `coretest: <member> needs core authentication, which lands at R2's identity flip`. Do not add a seam to
  `core/httpapi` (not this row's files). Record which case applied in the PR description. F4-08, the identity flip,
  completes `WithUser`, `As` and `AssertPermission` over core auth's principal at stage 13 (it lists
  `core/coretest/harness.go` and `assert.go`); keep the signatures exact so its change is only the bodies.

## Code contract (copy exactly from 06 §6.4.8)

```go
package coretest

type Harness struct{ /* unexported */ }

func New(t testing.TB, opts ...Option) *Harness

type Option func(*settings)

func WithModules(mods ...module.Module) Option
func WithDisabled(ids ...module.ID) Option                   // sets core.modules.<id>.enabled=false in the boot record
func WithConfig(kv map[string]any) Option                    // config overrides layer
func WithUser(name string, perms ...rbac.Permission) Option  // pre-seeded principal holding exactly perms

func Boot(t testing.TB, opts ...Option) (*Harness, error)

func (h *Harness) App() *app.App
func (h *Harness) As(user string) *Client          // authenticated client for a seeded user
func (h *Harness) Anonymous() *Client

type Client struct{ /* unexported */ }

func (c *Client) Do(req *http.Request) *http.Response
func (c *Client) GetJSON(t testing.TB, path string, out any) int // returns status

func AssertPermission(t testing.TB, h *Harness, method, path string, perm rbac.Permission)
func AssertRoutesHavePermissions(t testing.TB, h *Harness)
func AssertModuleInert(t testing.TB, h *Harness, id module.ID)
```

`archtest.RoutesHavePermissions`: the spec names it (03 R6; 05-http-api §12) but gives no signature. Coin one in
`core/archtest/routes_permissions.go` (signature coined here), taking the route records of a built router, for
example `func RoutesHavePermissions(t testing.TB, routes iter.Seq[httpapi.RouteInfo])`, so that
`AssertRoutesHavePermissions` is a thin wrapper that passes the harness's built router's `Routes()`. `archtest` is
test support (layer −1, 03 §3.5.1 item 1's mapping), so it may import `httpapi`; no non-test core package may import
`archtest` or `coretest`.

What it enforces (03 R6; 05-http-api §4.10, §8.2): every route has a non-zero `Perm` (`rbac.Requirement`), or
explicit `rbac.Public` / `rbac.AnyAuthenticated`; a legacy route is accepted only if it is in the manifest with a
permission, or `Public` on the reviewed list, or `OwnAuth` naming a registered auth method; the SPA owner and the
`Router.Infra` routes are the known infra set (`RouteInfo{Kind: SPA, Perm: rbac.Public, RateClass: RateExempt}`);
`RateExempt` on a non-Public route fails. The failure names each offending method and pattern.

## Invariants (each has a test below)

- I1. `New` with no `WithModules` boots core with zero modules (G7) and serves `/api/core/v1/*` with health ready.
- I2. The harness never calls `slog.SetDefault` or any process-global setter; it hands core R1-09's injected log
  capture (03 R5; 05-logging §11 item 5). Two harnesses in one test process share nothing, so tests can run with
  `t.Parallel()`.
- I3. No listener opens; no real timer starts outside a `synctest` bubble (06 §6.4.8, last paragraph).
- I4. `archtest.RoutesHavePermissions` fails on a route with no permission, on a legacy route missing from the
  manifest, and on `RateExempt` outside the infra set; it passes over hello and over ABO's real router.
- I5. Every exported signature equals 06 §6.4.8's text; nothing is added to the exported surface that the spec does
  not name, except `archtest.RoutesHavePermissions` (named by 03 R6, signature coined).

## Dependency check

```bash
git grep -n 'func New(ctx context.Context' -- core/app | wc -l          # expect >= 1 (R1-13's app.New)
git grep -n 'func (r \*Router) Routes()' -- core/httpapi | wc -l        # expect 1 (R1-13)
git grep -n 'func (r \*Router) MountLegacy(' -- core/httpapi | wc -l   # expect 1 (R1-14 filled its behaviour)
test -f internal/server/legacy_manifest.go && echo MANIFEST_OK           # expect MANIFEST_OK (R1-14)
test -f core/coretest/logcapture.go && echo LOGCAPTURE_OK                # expect LOGCAPTURE_OK (R1-09)
test -f core/coretest/metrics.go && test -d internal/books && echo R1-15_OK   # expect R1-15_OK (R1-15)
git grep -n 'func New(t testing.TB' -- core/coretest | wc -l            # expect 0 (this row creates it)
git grep -n 'RoutesHavePermissions' -- core | wc -l                     # expect 0
```

If a predecessor check fails, STOP and report which output is missing.

## Steps

0. **SPEC-GATED: `ContractGoldens`.** 06 §6.4.8 and 05-http-api §7 say it "marshals every route's declared examples",
   but 05-http-api §3.1's `Route` struct and §3.5's `RouteInfo` declare no examples field. Check:

   ```bash
   S=docs/specs/platform-core
   grep -c -E '^\s*(Examples?|Example[A-Z][A-Za-z]*)\s|func Examples?\(' $S/05-http-api.md
   ```

   (It counts Go declarations of an `Example`/`Examples` field or an `Examples(...)` route option in the spec's code
   blocks.)

   If this prints `0` (it does at the plan base), do **not** build `ContractGoldens` and do not write
   `TestContractGoldens_ValidateAgainstSchema`: report "ContractGoldens SPEC-GATED: no route example declaration in
   05-http-api §3" in the PR and continue with the other steps. Missing spec text: a `Route`/`RouteOption` field (or
   `RawDoc` member) that declares a route's request and response examples. If a declaration exists, build it as
   06 §6.4.8 states (jsonschema-go `Resolve` + `Validate` against the route's generated schema) with its test.
1. Write the tests below (red).
2. `core/coretest/harness.go`: `Harness`, `settings`, `Option`, `New`, `Boot`, `WithModules`, `WithDisabled`,
   `WithConfig`, `WithUser`, `App`, `As`, `Anonymous`. Build `app.Options` for an in-memory store and a
   `t.TempDir()` state directory; inject R1-09's log capture as the App's logger.
3. `core/coretest/client.go`: `Client`, `Do` (runs the request through `App.Handler()` with `httptest`), `GetJSON`.
4. `core/archtest/routes_permissions.go`, then `core/coretest/assert.go`: `AssertRoutesHavePermissions`,
   `AssertPermission`, `AssertModuleInert` (R1 cells).
5. **ABO's half of R6.** Add `internal/books/routes_permissions_test.go` (name coined here): boot ABO's real App
   (`app.New` with `books.Module(...)` as R1-15 composes it; R1-15 is a dependency, so `internal/books/` exists at
   your base) and run `archtest.RoutesHavePermissions` over its router, so the legacy manifest is checked
   against what is served (03 R6; 05-http-api §8.2). Record the file under "Scope notes".
6. **`TestSpecGoBlocksCompile`** (06 §6.10): R1-13 boots the extracted R1 hello blocks through `app.New` and says this
   row switches the boot to `coretest.New`. Make that switch in R1-13's test file and record the file under "Scope
   notes". Change nothing else in it.
7. Headers; one fragment `changelog.d/<today>_coretest_harness.md` (`### Added`: "Internal: core's in-process test
   harness and the route-permission gate."). Commit: `feat(core/coretest): in-process harness and RoutesHavePermissions`.

## Tests first

In `core/coretest/harness_test.go` (spec names copied exactly from 06 §6.10; the hello module is
`core/examples/hello`):

- `TestBootZeroModules` — G7: core boots, serves `/api/core/v1/*`, health ready, with no module.
- `TestDisabledModuleInert` — G1 via `AssertModuleInert` on hello (R1 cells; F2-16 adds the held-runs cell).
- `TestHarness_TwoInstancesIsolated` (name coined here) — two harnesses in parallel, distinct stores, state
  directories and log captures; `slog.Default()` unchanged after both (I2).
- `TestRoutesHavePermissions_Hello` (name coined here) — `AssertRoutesHavePermissions` passes over hello.
- `TestRoutesHavePermissions_RejectsFixtures` (name coined here) — a fixture router with a zero-permission route, a
  legacy route missing from its manifest and a `RateExempt` non-Public route: each fails, naming the route (I4).
- `TestAssertPermission_Hello` (name coined here) — only if the "Authentication at R1" check found the principal seam:
  a fixture route with a real permission answers 403 to a user lacking it and not 401/403 to a user holding exactly it.
  Otherwise the test asserts that `As` fails with the message above.

## Acceptance

```bash
go build ./... && go -C core build ./...                                                     # expect exit 0
go -C core vet ./coretest/... ./archtest/...                                                 # expect exit 0
go -C core test -race -count=1 ./coretest/ ./archtest/                                       # expect ok x2
go -C core test -count=1 -run 'TestBootZeroModules|TestDisabledModuleInert' -v ./coretest/ | grep -c '^--- PASS'   # expect 2
go test -count=1 -run 'TestRoutesHavePermissions' ./internal/books/                          # expect ok
git grep -n 'slog.SetDefault' -- core/coretest | wc -l                                       # expect 0
git grep -n 'mediacore/coretest"\|mediacore/archtest"' -- 'core/*.go' ':!*_test.go' ':!core/coretest' ':!core/archtest' | wc -l   # expect 0 (no non-test core importer)
make -C core ci                                                                              # expect exit 0
make ci                                                                                      # expect exit 0 and "All CI checks passed"
```

## STOP conditions

- A predecessor's output is missing (Dependency check).
- A member would need a type that does not exist at your base (for example `integrations.Descriptor`, `tasks.Kind`,
  `storage.Inspector`). Do not create stand-in types; leave the member to its owner row and report it.
- Booting the harness needs a change in `core/kernel`, `core/app` or `core/httpapi`. Report what is missing; do not
  edit those packages here.
- The legacy manifest fails `archtest.RoutesHavePermissions` on ABO's real router. Report the routes; the manifest is
  R1-14's.

## Out of scope

`WithAdapters` (needs `integrations.Descriptor`, F7). `WithRuns`, `RunTask`, `RunResult` and `AssertModuleInert`'s
held-runs cell (F2-16). `MetricsProvider` (R1-15, `core/coretest/metrics.go`). `crashmatrix` and its `Scenario`/`Run`
(R1-21). `Harness.Storage()` and `Harness.FS()`: their types `storage.Inspector` and `RecordingFS` are not defined in
any spec section. `coretest/fakes` (the capability rows). Store conformance suites (`<pkg>/<pkg>test`, each with its
interface).

## Rollback

Revert the PR. Tests that later rows wrote against the harness go with it, so revert those first.

## Planner notes

- **`ContractGoldens` is SPEC-GATED** (step 0): no route example declaration exists in 05-http-api §3, so
  `TestContractGoldens_ValidateAgainstSchema` has no input yet.
- **Files outside the row:** R1-13's `TestSpecGoBlocksCompile` file (step 6, which R1-13 assigns to this row).
- **Unowned members and cells:** `Harness.Storage()` (`storage.Inspector`) and `Harness.FS()` (`RecordingFS`) have no
  type in any spec section; `WithAdapters` has no owner at F7; `AssertModuleInert`'s `GET /api/core/v1/modules`,
  `RecordingFS`, outbox and op-ledger cells have no named extender. The authentication half of `WithUser`, `As` and
  `AssertPermission`, if R1's router has no principal seam, is F4-08's (see Goal).

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green, `gh pr merge <n> --rebase` (enqueues it; the merge queue re-tests it on top of `main`, D15). Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
