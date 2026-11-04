<!-- file: docs/agent-tasks/platform-core/PC-17-03-health-probes-startup-steps.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3d8f047f-cea9-4130-b4c8-455baebac731 -->
<!-- last-edited: 2026-11-04 -->

# PC-17-03 — Health and startup: sysinfo host parts, flight-recorder wiring and trace download; `/livez` and `/readyz`; `internal/startup` becomes `Registrar.StartupSteps`

| Field | Value |
|---|---|
| ID | PC-17-03 |
| Title | Health and startup: sysinfo host parts, flight-recorder wiring and trace download; /livez and /readyz; internal/startup becomes Registrar.StartupSteps |
| Tier | **T3 — Opus / high.** Startup ordering moves into the kernel (a wrong class makes a Degraded step fatal or the reverse), and probe paths must never answer HTML to a monitor. |
| Wave | W17 (stage S8, release `S8`) |
| Depends on | PC-17-02 |
| Trailer | `Stage-Release: S8 1` |
| Digest | `docs/plans/platform-core/digests/S8-S10.md` §3 (rows `health`, `kernel`), §4 (05-health 5H.3.1–5H.3.2), §5 rules 7, 9 |
| Owning spec | 05-health §5H.3 (all), §5H.4–§5H.10 ("Landing by stage": S8 row); 09 S8 "Moves", "Health"; 04 §8.3 (per-step table) |

**Gate.** Merges only after the `S7` soak row exists.

## START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../abo-pc-17-03" -b feat/pc-17-03-health-startup origin/main
cd "$REPO/../abo-pc-17-03"
git worktree list
make web-deps
```

## Goal

Land "the rest of `health`" (05-health §5H.10 "Landing by stage", row S8): `health.Startup` and
`Registrar.StartupSteps` replacing `internal/startup` (whose path is deleted); the probes `GET /livez` and
`GET /readyz` (`rbac.Public`, `Router.Infra` exact); `Snapshot` over every core component; the status topic
`core.system.status-changed.v1` (native stream only, never added to projection A); the watchdog ping; diagnostics
(`health/diagnostics`, with `core.diagnostics.export` and books' alias `diagnostics.export`); and the
**re-implemented** host and runtime parts of `internal/sysinfo` in `health/sysinfo`. In the same PR gin's SPA fallback
refuses `livez` and `readyz`, so `/livez/` and `/READYZ` answer 404, not `200 text/html`. ABO's `/health` shape stays
permanent, served by the books layer from the registry.

## Files

Create: `core/health/diagnostics/`, `core/internal/healthhttp/` (probe handlers; the `/api/core/v1/{health,
system/info,system/status,diagnostics}` handlers too, mounted with their aliases in PC-17-04).

Modify:
- `internal/startup/` — deleted; its rows become core rows plus books rows from `Registrar.StartupSteps`.
- `internal/sysinfo/` — host/runtime parts re-implemented in core; what stays: `RecentOperations`,
  `CollectSystemStatus`' library and import fields, the v1 `OperationLog` helpers (05-health §5H.10, divided file).
- `internal/server/` — start steps become books startup steps; `/health` served from the registry by the books layer.
- `internal/server/spa_fallback.go` — add `"livez"` and `"readyz"` to `nonSPASegments`.
- `cmd/root.go` — no `internal/startup` import left.
- `internal/books/module.go` — `Registrar.StartupSteps` rows (books' `searchindex` step, token `search_index`,
  Degraded), `LegacyTaskAliases` entry `diagnostics.export` → `core.diagnostics.export`.

## Re-verify before editing

Counts at `748fd787a` (equal at this plan's HEAD). Zero hits or a moved count: STOP.

```bash
M=github.com/falkcorp/audiobook-organizer
git grep -l "\"$M/internal/startup\"" -- '*.go' | grep -v _test.go         # 3: cmd/root.go, internal/server/server.go, internal/server/server_lifecycle.go
git grep -n '^var Table = \[\]Step{' -- internal/startup/steps.go            # 1 (:91)
sed -n '91,127p' internal/startup/steps.go | grep -c 'Name:'                 # 12 rows at 748fd787a (S2/S2b moved cmd/root.go's steps; regenerate at your base)
git grep -n 'var nonSPASegments = map\[string\]bool{' -- internal/server/spa_fallback.go   # 1 (:74)
git grep -n '"livez"\|"readyz"' -- internal/server/spa_fallback.go | wc -l   # 0 (this PR adds both)
git grep -n 'const ExportOpID = "diagnostics.export"' -- internal/diagnostics/service.go   # 1
git grep -l "\"$M/internal/sysinfo\"" -- '*.go' | grep -v _test.go | wc -l   # 5 (internal/server, internal/server/handlers/system, .../system/mocks)
git grep -n 'func (r \*Registry) Register\|func (r \*Registry) Ready' -- core/health | head -2   # >= 1 (S5 readiness core)
```

## Steps

1. Read 05-health §5H.3.1–§5H.3.6, §5H.4 (stop budget), §5H.5–§5H.10; 05-realtime §4.9 (projection A never gains the
   status topic); 05-http-api §4.10 (`Router.NonSPA()`); 04 §8.3 per-step table.
2. Write the tests below first, including `TestProbes_VariantsAreNotSPA` against gin's fallback.
3. `health.Startup`: a per-App table of core rows (store, telemetry → metrics, secure state dir, key guard,
   encryption → secrets, config load/validate, registration gates, http) and module rows from
   `Registrar.StartupSteps`; `Run(ctx, name, fn)` refuses an unknown name; a failed Degraded step registers component
   `core.startup.<token>` Degraded. `archtest.StartupSteps(t, pkgs...)` replaces `internal/startup/steps_test.go`'s
   AST scan (every `Run` literal is a row, every row is run). Keep each step's HEAD class (Fatal or Degraded) and token.
4. Probes per §5H.3.3: `/livez` 200 while the heartbeat ticked within `core.health.live_stall`; `/readyz` 200 iff
   startup complete, not shutting down, every Readiness component Up/Degraded and no Critical component Down; bodies
   carry tokens only. Add `livez` and `readyz` to gin's `nonSPASegments` in this PR.
5. Status topic `core.system.status-changed.v1`: declared by `health` from S8, bus only, `Realtime{Coalesce: 1s}`,
   permission `core:system.status`, payload `{ready, not_ready[]}`. Do **not** add it to projection A: stream A's
   `system.status` frame keeps HEAD's 5-second payload.
6. `health/sysinfo` re-implements memory, uptime and runtime status (no import-path-only move: 04 §8.6). Diagnostics:
   `internal/diagnostics` zip/section machinery → `health/diagnostics`; books keeps its `Contributor` sections;
   `core.diagnostics.export` is declared in core with no ABO ID, and books supplies `diagnostics.export` through
   `app.Options.LegacyTaskAliases` (no op ID in a `core/` file).
7. Flight-recorder wiring and the trace download join `health` (the recorder itself moved at S1).
8. Boot writers: if a step you move writes durably at boot, extend 06 §6.5.1's writer table and the crash-matrix
   instance in this PR.

## Code contract (05-health §5H.3.1–§5H.3.2; copy exactly)

```go
type StepClass uint8
const ( Fatal StepClass = iota + 1; Degraded )
type StartupStep struct { Name string; Class StepClass; Token string; Reason string }
func (s *Startup) Run(ctx context.Context, name string, fn func(context.Context) error) error // unknown name -> error
func (s *Startup) Degraded() []string
// Check{ID ComponentID; Title string; Critical, Readiness bool; Probe func(ctx) Result; Interval, Timeout time.Duration}
// (*Registry).Register(c Check) (Reporter, error); Snapshot(); Ready() (bool, []string); Live() bool; Wait(ctx, ids ...ComponentID) error
```

Routes (05-health §5H.3.3 table): `GET /livez`, `GET /readyz` (`rbac.Public`), `GET /api/core/v1/health`,
`GET /api/core/v1/system/info` (`core:system.manage`), `GET /api/core/v1/system/status` (`core:system.status`),
`POST /api/core/v1/diagnostics`, `GET /api/core/v1/diagnostics/{run}`. 06 §6.6 at S8: Registrar `StartupSteps`,
`Diagnostics`; ABO adds `core.diagnostics.export` to `Options.LegacyTaskAliases`.

## Tests to write first (05-health §5H.9; 09 S8 Done when)

`TestProbes_VariantsAreNotSPA` (`/livez/`, `/READYZ`, `/health/`, `/metrics/` → 404, never `text/html`, through gin's
fallback in this build), `TestReadyz_CriticalDownIs503_LivezStays200`, `TestReadyz_FlipsBeforeHTTPDrain`,
`TestProbes_BodyHasOnlyTokens`, `TestCheck_PanicAndTimeoutBecomeDown`, `TestStartup_UnknownStepRefused`,
`archtest.StartupSteps` self-test, `TestHeartbeat_StallFailsLivezAndStopsWatchdog`,
`TestStopBudget_PhasesNeverExceedTotal`, `TestDiagnostics_SecretsNeverSerialised`,
`TestDiagnostics_PatternRedaction`, `TestDiagnostics_ViewerFiltersSections`, `TestDiagnostics_DownloadOtherActor404`,
`TestDiagnostics_NarrowKeyOfActor403`, `TestDiagnostics_DemotedActor403`, `TestStartup_ExtendPerStepNeverTimer`,
`TestStopBudget_ContainerDefault8s`, `TestDiagnostics_PathsConfinedToRoot`, `TestNoGlobals_Health`, and
`TestProjectionTopicsExistAtStage` with the S8 catalog (projection A's topics equal at S6 and S8).

## Acceptance

```bash
test ! -e internal/startup && echo deleted                                       # deleted
git grep -n '/internal/startup"' -- '*.go' | wc -l                               # 0
git grep -n '"livez":\s*true\|"readyz":\s*true' -- internal/server/spa_fallback.go | wc -l   # 2
go build ./... && (cd core && go build ./...)                                    # exit 0 ("internal/startup deleted and the build passes")
(cd core && go test ./health/... ./internal/healthhttp/... ./archtest/... -race -count=1)   # all "ok"
go test ./internal/server/... ./internal/sysinfo/... ./internal/books/... ./cmd/... -race -count=1   # all "ok"
go test ./... -run 'TestProbes_VariantsAreNotSPA|TestReadyz_CriticalDownIs503_LivezStays200|TestProjectionTopicsExistAtStage' -count=1 2>&1 | grep -E '^(FAIL|--- FAIL)'   # no output
make ci                                                                          # exit 0
make -C core ci                                                                  # exit 0
```

`unit-lifecycle` triggers on this PR (`cmd/**`): the 12-minute fake migration is still not killed.

## Out of scope

Mounting `/api/core/v1/{health,system,…}` with their aliases and the kernel's `/metrics` (PC-17-04); the UI calls
(PC-17-05); moving `/system/status` (it stays a books route, §5H.10).

## Rollback

Binary swap. No stored state changes; the probes do not exist on S7 (monitors keep using permanent `/health`).

## Repo rules

- Worktree per task; never the primary checkout or `main`; remove it after merge.
- Versioned headers on new or changed files; one headerless `changelog.d/` fragment.
- Conventional commits, each with `Stage-Release: S8 1` and `Co-Authored-By: Claude <noreply@anthropic.com>`.
- `gh pr merge <n> --rebase`; never squash; never `--admin` past a required check.
- Report with exact counts and `COMPLETED:` / `REMAINING:` / `BLOCKED:`.

## Planner notes

- `internal/diagnostics/` (the zip machinery moving to `health/diagnostics`), `internal/server/handlers/system/`
  (an importer of `internal/sysinfo`, edited by PC-17-01 earlier in the wave) and the sysinfo mocks are not in the
  row's files but must change here; the chain orders the shared ones.
