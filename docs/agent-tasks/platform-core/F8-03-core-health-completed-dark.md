<!-- file: docs/agent-tasks/platform-core/F8-03-core-health-completed-dark.md -->
<!-- version: 1.1.0 -->
<!-- guid: d5d73704-a330-44d0-8759-af83c06c3893 -->
<!-- last-edited: 2026-11-05 -->

# F8-03 — Core health completed, dark: Snapshot, components, /livez and /readyz, /api/core/v1/health, the /system/* core routes, the status topic, Startup and Registrar.StartupSteps, the watchdog ping, health/sysinfo, the diagnostics bundle

| Field | Value |
|---|---|
| ID | F8-03 |
| Release | R2, flip label F8 (dark PR) |
| Tier | **T3** (Opus / high): probes and the startup supervisor; unauthenticated probes must never leak a token outside the golden set |
| Wave | W28 (beside F2-16, F4-05, F5-15, F6-01, F6-02, F7-02, F7-03, F7-06; file-disjoint; this row is W28's one owner of `core/kernel/health.go`) |
| Depends on | F2-10 (readiness core), F2-11 (`diagnostics` minimal owner registration and `InterimExport`), F4-04 (the `events` bus, for the status topic) |
| Spec | 05-health §5H.3 (all of it: §5H.3.1 registry, §5H.3.2 startup steps, §5H.3.3 probes and HTTP, §5H.3.4 supervisor signals, §5H.3.5 diagnostics bundle, §5H.3.6 sysinfo), §5H.4 (stop budget), §5H.5–§5H.8, §5H.9 (tests), §5H.10 ("Landing by release", row F8); 06 §6.4.10 (F8 row: `Registrar.StartupSteps`, `Registrar.Diagnostics`); 04 §8.1 F8 |

**Gate:** plan, then execute autonomously in a worktree with its own PR. **Dark:** nothing in ABO switches. HEAD's
`/health`, `/api/health`, `/api/v1/health` and `GET /api/v1/system/status` keep serving exactly as now; the core
routes are built and tested on `hello`/`coretest` but the kernel mounts them for ABO only at F8-04.

## START HERE (do this first, exactly)

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/pc-f8-03-health" -b agent/platform-core-f8-03-health origin/main
cd "$REPO/.worktrees/pc-f8-03-health"
git rebase origin/main
```

## Goal

Complete core `health` beyond F2's readiness core (05-health §5H.10, row F8: "everything else"): `Snapshot` and
`ComponentState`, the remaining core components, `GET /livez` and `GET /readyz` (exact `Router.Infra` patterns),
`GET /api/core/v1/health`, `GET /api/core/v1/system/info`, `GET /api/core/v1/system/status`, the diagnostics routes,
the health status topic on the bus, `Startup` with `StepClass`/`StartupStep` (the module member
`Registrar.StartupSteps` and `Registrar.Diagnostics` are the flip F8-04's, which owns `core/module/` in its wave), the
heartbeat and the systemd watchdog ping (start functions in `core/kernel/health.go`, called by F8-04), `health/sysinfo` (a re-implementation of HEAD's host parts, not a
move), and the diagnostics bundle (`Section`, `Writer`, `WriteJSONL`, `Viewer`, `Contributor`) that will replace
`diagnostics.InterimExport` at F8-04.

## Invariants

- **I1 (SEC-7).** Unauthenticated bodies (`/livez`, `/readyz`, and ABO's `/health` from F8) carry only fixed tokens
  from the golden unauthenticated set of §5H.3.1; no auth, config, backup, restore, storage or secrets posture token,
  no detail text, path, version or count ever appears there.
- **I2.** Probe paths are exact: `/livez/`, `/READYZ`, `/health/`, `/metrics/` give 404, never `text/html`
  (FE2-6).
- **I3.** `/readyz` is 200 iff startup is complete, not shutting down, every Readiness component Up or Degraded and no
  Critical component Down; `/livez` follows only the heartbeat (`core.health.live_stall`).
- **I4.** `Startup.Run` with an unknown step name is an error (fail-closed); `EXTEND_TIMEOUT_USEC` is sent once per
  progressing step, never on a timer (OPS2-17).
- **I5.** Diagnostics: secrets never serialised (typed redaction plus pattern redaction), sections filtered by the
  viewer at start **and** at download, the download refused to anyone but the run's actor or a `core:admin`; every
  path through an `os.Root`.
- **I6.** No package-level mutable state (`TestNoGlobals_Health`).
- **I7. Dark.** No ABO route, response or token set changes in this PR.

## Files

| Action | Path |
|---|---|
| create | `core/health/snapshot.go` (`Snapshot`, `ComponentState`, `Registry.Snapshot`, `Registry.Live`) |
| create | `core/health/probes.go` (heartbeat, `/livez`, `/readyz` handlers, the status topic) |
| create | `core/health/startup.go` (`StepClass`, `StartupStep`, `Startup`, `Run`, `Degraded`; `Supervisor` watchdog ping per §5H.3.4) |
| create | `core/health/sysinfo/` (`Info`, `Memory`, `RootSpace`, `Collect`) |
| create | `core/health/diagnostics/bundle.go` (the bundle, `Section`, `Writer`, `WriteJSONL`, `Viewer`, `Contributor`) |
| create | `core/internal/healthhttp/` (`/api/core/v1/health`, `/api/core/v1/system/info`, `/api/core/v1/system/status`, `/api/core/v1/diagnostics/{run}`) |
| modify | `core/kernel/health.go` (F2-10's file): the kernel functions that start the heartbeat and the watchdog ping and register the remaining core components. Nothing calls them from `core/kernel/boot.go` in this PR: the call is F8-04's (it owns `boot.go` in W36), so ABO's boot is unchanged; tests call them directly |
| (not this row) | `core/module/` (`Registrar.StartupSteps`, `Registrar.Diagnostics`, 06 §6.4.10 F8 row) and ABO's mounts: F8-04 |
| create | tests beside each; one `changelog.d/` fragment (headerless) |

## Re-verify before editing (expected results at your base)

```bash
# F2-10's readiness core. Expected: Registry, Register, Ready, Wait, Reporter present; Snapshot absent
grep -n 'func (r \*Registry) \(Register\|Ready\|Wait\|Snapshot\|Live\)' core/health/*.go
# F2-11's diagnostics minimal registration. Expected: InterimExport found
grep -rn 'InterimExport' core/health/diagnostics/
# The events bus (status topic). Expected: Bus and a topic declaration API
grep -rn 'type Bus struct\|func .*Topics' core/events/*.go | head
# HEAD's health handler and routes (unchanged by this PR). Expected: the three /health routes [evidence:
# internal/server/server_lifecycle.go:1749-1755] and HealthCheck in handlers/system
grep -rn '"/health"\|"/api/health"\|"/api/v1/health"' --include='*.go' internal/server/ | head
grep -n 'func .*HealthCheck' internal/server/handlers/system/handler.go
# sdnotify location after R1 (moved with the kernel lifecycle). Expected: core/internal/sdnotify exists
ls core/internal/sdnotify/
# library.Root for sysinfo.Collect. Expected: type Root found (R1-08)
grep -rn 'type Root ' core/library/
```

STOP if `Registry.Snapshot` already exists (someone landed part of this), if `core/internal/sdnotify` is absent, or if
`library.Root` is absent.

## Steps

1. Read 05-health §5H.3–§5H.9 in full; copy every type and signature exactly (code contract below).
2. Write the tests of §5H.9 first (next section).
3. Implement §5H.3.1's remaining members (`Snapshot`, `Live`, `ComponentState`, check scheduling with
   `Interval`/`Timeout`, panic and timeout becoming Down).
4. §5H.3.2 `Startup` (core rows: store, metrics, secure state dir, key guard, secrets, config load/validate,
   registration gates, http; books' `searchindex` becomes a books Degraded step at F8-04). Shape `Startup` so F8-04's
   `Registrar.StartupSteps` can hand module rows to it without changing it.
5. §5H.3.3 probes and routes; `/livez` and `/readyz` as `Router.Infra` exact patterns; the authenticated routes with
   their permissions (`core:system.manage`, `core:system.status`; diagnostics download with `Guards.Interactive` and
   the actor-or-admin rule, audited as `core.diagnostics.download`).
6. §5H.3.4 watchdog: `WATCHDOG=1` on the same heartbeat as `/livez`; `STOPPING=1` at shutdown start; both in
   functions of `core/kernel/health.go` that F8-04 calls from `boot.go` (05-health §5H.10 F8 row lands the watchdog
   ping with the flip). Do not edit `boot.go`.
7. §5H.3.5 diagnostics bundle: core sections and module `Contributor`s, typed and pattern redaction, `os.Root`
   confinement, run expiry (410).
8. §5H.3.6 sysinfo: host memory, uptime, runtime, root space for `[]library.Root`; no library counts (those stay
   books').
9. Register the status topic in the events catalog with its permission.
10. Wire it on `hello` and `coretest` only (dark in ABO). Headers, fragment, acceptance.

## Code contract (05-health §5H.3, verbatim excerpts; copy the rest from the spec)

```go
func (r *Registry) Snapshot() Snapshot
func (r *Registry) Live() bool
type Snapshot struct {
	Ready      bool
	Live       bool
	Components []ComponentState // sorted by ID
	Startup    []string         // degraded startup tokens
}
type ComponentState struct {
	ID       ComponentID
	Result   Result
	Critical bool
	Duration time.Duration
}
func (s *Startup) Run(ctx context.Context, name string, fn func(context.Context) error) error // unknown name -> error
func (s *Startup) Degraded() []string
const StartupStepBudget = 10 * time.Minute
func (s *Supervisor) ExtendStart(step string)
func (s *Supervisor) ExtendStop(d time.Duration) bool

package diagnostics
type Writer interface {
	JSON(name string, v any) error
	File(name string, r io.Reader) error // text files only; size-capped
}
func WriteJSONL[T any](w Writer, name string, items iter.Seq[T]) error
type Contributor interface{ Sections() []Section }

package sysinfo
func Collect(ctx context.Context, roots []library.Root) Info
```

`StepClass`, `StartupStep`, `Section`, `Viewer`, `Info`, `Memory` and `RootSpace` are defined field by field in
§5H.3.2, §5H.3.5 and §5H.3.6: copy them exactly.

| Route (05-health §5H.3.3) | Permission |
|---|---|
| `GET /livez` | `rbac.Public` |
| `GET /readyz` | `rbac.Public` |
| `GET /api/core/v1/health` | `core:system.manage` |
| `GET /api/core/v1/system/info` | `core:system.manage` |
| `GET /api/core/v1/system/status` | `core:system.status` |
| `GET /api/core/v1/diagnostics/{run}` | `core:system.manage`, `Guards.Interactive`, plus actor-or-`core:admin` |

## Tests (write first; spec names copied exactly, 05-health §5H.9)

`TestReadyz_CriticalDownIs503_LivezStays200`, `TestProbes_VariantsAreNotSPA`, `TestReadyz_FlipsBeforeHTTPDrain`,
`TestProbes_BodyHasOnlyTokens`, `TestCheck_PanicAndTimeoutBecomeDown`,
`TestRegistry_Wait_UnblocksOnUp`, `TestStartup_UnknownStepRefused` (and the `archtest.StartupSteps` self-test),
`TestHeartbeat_StallFailsLivezAndStopsWatchdog`, `TestStopBudget_PhasesNeverExceedTotal`,
`TestDiagnostics_SecretsNeverSerialised`, `TestDiagnostics_PatternRedaction`, `TestDiagnostics_ViewerFiltersSections`,
`TestDiagnostics_DownloadOtherActor404`, `TestDiagnostics_NarrowKeyOfActor403`, `TestDiagnostics_DemotedActor403`,
`TestStartup_ExtendPerStepNeverTimer`, `TestStopBudget_ContainerDefault8s`, `TestDiagnostics_PathsConfinedToRoot`,
`TestNoGlobals_Health`. Use `testing/synctest` where §5H.9 says synctest.

## Acceptance (commands and expected output)

```bash
# Core health tests. Expected: ok, no FAIL
(cd core && go test -count=1 -race ./health/... ./internal/healthhttp/...)

# TestHealth_UnauthenticatedTokenSetGolden exists from R0-06 (extended by R1-24 and F2-10); this row adds no cell
# (F8-04 adds the F8 cells) and must keep it green. Expected: ok
go test -count=1 -run TestHealth_UnauthenticatedTokenSetGolden ./...

# Dark: HEAD's health routes unchanged. Expected: ok
go test -count=1 ./internal/server/handlers/system/...

# Builds. Expected: exit 0
go build ./... && (cd core && go build ./...)

# Gates. Expected last lines: ✅ All CI checks passed! / exit 0
make ci
make -C core ci
```

## Out of scope

- Mounting the core health routes for ABO, deleting `/api/health` and `/api/v1/health`, switching callers, books'
  `/health` served from the registry, `books.memdb`, replacing `InterimExport` and deleting
  `app.Options.InterimBackends` (F8-04).
- `internal/sysinfo`'s library parts and `RecentOperations` (they stay books').
- The kernel `/metrics` mount (F8-04).
- `Registrar.StartupSteps`, `Registrar.Diagnostics` and their kernel calls (F8-04).
- `TestHealth_UnauthenticatedTokenSetGolden`'s cells: R0-06 owns the test; F8-04 adds the F8 cells.

## Rollback

Revert the PR. Nothing in ABO used it; no stored data.

## Repo rules

- Worktree off fresh `origin/main`; headers on every file.
- Conventional commit, for example `feat(health): complete core health dark (probes, startup, diagnostics, sysinfo)`,
  ending with `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; never squash; never
  `--admin`. No release or flip trailer.
- The diagnostics export walks library-scale data through module `Contributor`s: any per-item loop you add follows
  CLAUDE.md's concurrency rule (bounded worker pool); keep streaming through `WriteJSONL`.

