<!-- file: docs/agent-tasks/platform-core/R1-13-kernel-app-module-httpapi-skeleton.md -->
<!-- version: 1.0.0 -->
<!-- guid: 49cec14b-15f1-4aaa-9773-e49334cd61a5 -->
<!-- last-edited: 2026-11-05 -->

# R1-13 — Kernel, app, module and the httpapi skeleton, internal/openapi, and the examples/hello skeleton that emits core/openapi/core.json

| Field | Value |
|---|---|
| ID | `R1-13` |
| Tier | **T4** (Opus / xhigh): the boot-phase machinery every later writer hangs off; lifecycle and stop order are normative |
| Release / wave | R1 / W09 |
| Depends on | R1-12, R1-09, R1-07, R1-10 (each done before you start: a task merged on `main`, an OWNER checkpoint recorded by the owner) |
| Spec | 06 §6.4.10 R1 row, §6.5.1, §6.5.2, §6.11.1, §6.12 (hello); 03 §3.3 (app, module, kernel); 05-http-api §3, §4.8; 04 §8.1 R1 |
| Branch | `agent/platform-core-r1-13-kernel-app-module-httpapi-skeleton` |

**Gate:** plan, then execute autonomously in a worktree with its own PR. Every merge passes `make ci`, e2e and the named required checks of 09 §9.2.1 that exist at your base. The R1 freeze is on from R1-01 until OWNER-03 cuts R1 (09 §9.2.1): nothing in this task changes that.

**Files the inventory assigns to this task** (a path ending in `/` is a whole directory; touch nothing outside this list without recording it under the PR's "Scope notes"):

- `core/kernel/`
- `core/app/`
- `core/module/`
- `core/httpapi/`
- `core/internal/openapi/`
- `core/examples/hello/`
- `core/openapi/core.json`

## START HERE

```bash
REPO=<your clone>   # the audiobook-organizer checkout you work from
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/platform-core-r1-13" -b agent/platform-core-r1-13-kernel-app-module-httpapi-skeleton origin/main
cd "$REPO/.worktrees/platform-core-r1-13"
git rebase origin/main
npm ci --prefix web        # CLAUDE.md: Playwright and vitest from this worktree
```

Never run `go work init` in the checkout or the worktree (CLAUDE.md; 03 §3.2 item 1): verify with `go build ./...` and `go -C core build ./...` (once `core/` exists). Line numbers below were read at the planning base `f7ac5b11e`; the spec's anchors are at `748fd787a`. Where they differ the brief says so. Re-run every grep at your base before editing; a count that differs only because a listed predecessor merged is expected, and the brief says what to do with it.

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
  `Logger`) (06 §6.4.5, §6.4.10). `app.BinaryID` is R1-19's; `OpenOffline`/`OfflineCommands` are R1-24/R1-25's;
  `InstallProcessDefaults` is R1-15's.
- `httpapi` (L6): routes, groups, `Handle` with a required `rbac.Requirement` (zero rejected), `Build` with
  `ServeMux` pattern conflict detection (05-http-api §3, §4.8), the listener lifecycle, the problem shape; the
  perimeter stages are R1-17's and `MountLegacy` is R1-14's.
- `internal/openapi`: builds OpenAPI 3.1 from a leaf route-description type; it never imports `httpapi`
  (`httpapi` → `internal/openapi`, 04 §8.1 R1, MOD3-4); schemas through `internal/jsonschemax`.
- `examples/hello`: module + app + one route `GET /api/hello/v1/greeting`, declared `rbac.Public` and listed in
  `Manifest.PublicRoutes` until core auth exists (row notes; 06 §6.12.2), `MinCoreVersion: "0.1.0"`, and the
  OpenAPI subcommand that writes `core/openapi/core.json` (06 §6.12; 07 §7.19 step 3 consumes that file). Hello's
  keyspace and seed migration are R1-20's (`core/examples/hello/storage.go`).

## Invariants and tests (write first; spec names from 06 §6.10 and 05-http-api §12)

- Boot order is 06 §6.5.1's table and nothing else; start/stop order is 06 §6.5.2's: `TestBootZeroModules`,
  `TestStartOrder`, `TestStartRollbackReverseOrder` (a failing hook stops every started hook in reverse),
  `TestRegistrarClosedAfterRegister`, `TestNewCleansUpCoreGoroutines`, `TestDescribeInMemoryNoIO`,
  `TestRestartRequest_ExitsRestartCode` (exit 75 after the stop order, never `os.Exit(0)`).
- IDs: `TestReservedIDsIncludeLegacyTopSegments`, `TestReservedSegmentsDisjointFromNonSPA`.
- Routes: `TestHandle_RejectsZeroPermission`, `TestHandle_RejectsForeignScope`, `TestBuild_PatternConflictIsError`,
  `TestBuild_PrefixCollision_FacadeVsFacade`, `TestBuild_RejectsCamelCaseJSON`, `TestProblem_ShapeGolden`,
  `TestUnmatched_APIPathIsProblemNotHTML`, `TestMethodNotAllowed_HasAllow`, `TestDecode_StrictUnknownField`.
- OpenAPI: `TestOpenAPI_Golden` over hello (the golden is `core/openapi/core.json` itself; a test regenerates it
  in a temp dir and diffs).
- Hello: `TestHelloEndToEnd` for the R1 surface (boot, `GET /api/hello/v1/greeting` 200, clean stop); the parts
  that need storage, tasks or auth are added by the rows that land them.
- No process-global state in core (03 R5): the core archtest's `TestNoGlobals` if it exists at your base,
  otherwise a grep in the acceptance block; `signal.NotifyContext` only in `core/examples/hello/cmd/` (a `main`).
- Core guard control: once `app` and `kernel` exist the in-module control becomes `app` → `kernel`
  (03 §3.5.1 item 2, third step). Set it in this PR and say so.
- Any 06 §6.10 row whose subject this PR lands and that is not listed above: write it too, or list it in the PR as
  deferred with the row that brings its dependency.

## Dependency check

R1-12, R1-09, R1-07, R1-10 merged: `test -d core/rbac && test -d core/health && test -d core/log && test -d core/metrics && echo ok`.

## Re-verify

```bash
S=docs/specs/platform-core
sed -n '/^### 6.5.1/,/^### 6.5.2/p' $S/06-app-sdk.md | grep -c '^| P'    # count the phase rows; your kernel implements the rows whose members exist
grep -n 'GET /api/hello/v1/greeting' $S/06-app-sdk.md | head -2         # expect the §6.12.2 heading
test ! -e core/kernel && test ! -e core/app && test ! -e core/openapi/core.json && echo NEW   # expect NEW
git diff --stat origin/main -- . ':!core' | tail -1                       # at the end of the task: expect no ABO file changed
```

## Steps

1. Write the tests above (red), with `TestOpenAPI_Golden` reading `core/openapi/core.json`.
2. `core/module`, then `core/internal/openapi` (leaf route-description type), then `core/httpapi` (routes, `Build`,
   listeners, problem shape), then `core/kernel` (phases that exist, start/stop order, supervisor with the readiness
   notifier, restart request, shutdown budget), then `core/app` (`New`, `Run`, `Describe`, the R1 `Options` fields).
3. `core/examples/hello`: module, app `main` (the only `signal.NotifyContext` in core), the greeting route, the OpenAPI
   subcommand; generate and commit `core/openapi/core.json`.
4. Core layering guard: layers for the new packages (module L7, kernel L8, app and examples L9, httpapi L6,
   internal/openapi by its importer rule) and the control `app` → `kernel`.
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
go -C core run ./examples/hello/cmd/hello openapi --out "$(mktemp -d)/core.json" && echo GEN   # expect GEN (exact subcommand per 06 §6.12.5)
git diff --exit-code -- core/openapi/core.json                                             # expect no diff after regenerating in place
git grep -n 'slog.SetDefault\|otel.Set\|http.DefaultServeMux\|signal.Notify' -- core ':!core/examples/*/cmd/**' | wc -l   # expect 0
git diff --name-only origin/main | grep -v '^core/' | grep -v '^changelog.d/' | wc -l      # expect 0
make ci && make -C core ci                                                                 # expect exit 0
```

## Out of scope

`MountLegacy`'s behaviour (R1-14); the perimeter (R1-17); the ABO composition root (R1-15); sdnotify (R1-16);
storage (R1-20); build identity (R1-19); offline opens (R1-24, R1-25).

## Rollback

Revert the PR; ABO does not import any of it yet.

## Repo rules (every PR of this workstream)

- **File headers.** Every new file gets the four-line header (`file`, `version: 1.0.0`, a fresh `guid` from `python3 -c 'import uuid;print(uuid.uuid4())'`, `last-edited`); every modified file gets its patch version bumped and `last-edited` set to today, keeping its `guid`. A moved file keeps its `guid` and gets its `file:` line set to the new path. `changelog.d/` and `todo.d/` fragments carry no header (CLAUDE.md).
- **Changelog.** Add one headerless fragment under `changelog.d/` (`scriv create`, or by hand); `changelog-check.yml` requires it. Do not edit `CHANGELOG.md`.
- **Commits.** Conventional commits (`type(scope): summary`). End every commit body with
  `Co-Authored-By: Claude <noreply@anthropic.com>`. 09 names no release or flip trailer (09 §9.2.1: there is no release-gating commit trailer), so add none.
- **PR and merge.** `git push -u origin <branch>`, `gh pr create`, then after every required check is green on an up-to-date branch `gh pr merge <n> --rebase`. Never squash. Never merge with `--admin` past a required check (14 Q-I as OWNER-01 answered it).
- **Concurrency (CLAUDE.md).** A loop over a library-scale collection with per-item I/O, hashing or subprocess work uses a bounded worker pool (`errgroup` + `SetLimit`); never unbounded goroutines.
- **No `go.work`.** Never run `go work init` in a checkout or worktree; the genproto probe runs only in a throwaway copy under `mktemp -d` (03 §3.2 item 1).
- **Status honesty.** The PR description ends with `COMPLETED: <n> — <list>`, `REMAINING: <n> — <list>`, `BLOCKED: <n> — <list>` over this brief's numbered steps.
