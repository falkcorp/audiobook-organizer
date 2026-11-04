<!-- file: docs/agent-tasks/platform-core/PC-12-06-taskhttp-tasklint-datamigrate-hello.md -->
<!-- version: 1.0.0 -->
<!-- guid: abc41fe1-85ba-40ab-bf4b-114e5042ecca -->
<!-- last-edited: 2026-11-04 -->

# PC-12-06 — Task HTTP and aliases (unmounted until the flip), tasklint, storage/datamigrate and the migration runner, hello.count

| Field | Value |
|---|---|
| ID | PC-12-06 |
| Tier | **T2 Sonnet/high** — routes and tooling against a contract PC-12-01/PC-12-02 already fixed; no new semantics |
| Wave | W12 (S5, release `S5r1`, dark for ABO) |
| Depends on | PC-12-02 |
| Serialized | … → PC-12-02 → **PC-12-06** → PC-12-08 |
| Trailer | `Stage-Release: S5 1` |
| Digest | `docs/plans/platform-core/digests/S5.md` §3 (`internal/taskhttp`, `archtest/tasklint`, `storage/datamigrate`, `examples/hello`), §5 rules 16–18 |
| Owning spec | 05-tasks §5T.3.10 (route table, compat paragraph); 09 S5 "Endpoints"; 04 §8.1 row S5; 05-storage §5S.3.6; 06 §6.12.3 (`hello.count`), §6.12 test table (`TestSecondModuleNoAudiobookEdit`); 05-http-api §3.1 (`Alias`, `LegacyPath`) |

## Goal

1. `core/internal/taskhttp`: the native `/api/core/v1/tasks/` routes, registered by the kernel only for an App whose
   kernel built an executor (so hello and `coretest` serve them; ABO does not before PC-12-08), plus a separate,
   uncalled alias-registration function for the legacy task paths that PC-12-08 calls at the flip.
2. `core/archtest/tasklint`: `tools/cmd/oplint`'s rules re-implemented for `tasks` definitions.
3. `core/storage/datamigrate` and its runner `core/internal/storagemigrate`, `BootBlocking` mode only.
4. `hello.count` and the G6/G7 proof `TestSecondModuleNoAudiobookEdit`.

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-12-06" -b feat/pc-12-06-task-http origin/main
cd "$REPO/../audiobook-organizer-pc-12-06"
git rebase origin/main
make web-deps
git worktree list
```

## Exact files

Create:

- `core/internal/taskhttp/` — `routes.go`, `aliases.go`, `handlers_*.go`, tests.
- `core/archtest/tasklint/` — an `analysis.Analyzer` (name `tasklint`) plus `testdata/`.
- `core/storage/datamigrate/` — 05-storage §5S.3.6 types (`Mode`, `BootBlocking`, `DataMigration`, `StepWriter`,
  `VerifyReport`, `DerivedBuildSpec`, `DerivedBuildTask`). `Online` exists only as the constant; no code path runs it.
- `core/internal/storagemigrate/` — the `BootBlocking` runner (`tasks.ForEach` over the persisted shard plan, one batch
  per chunk with the shard cursor).
- `core/examples/hello/count.go` — exactly 06 §6.12.3.
- `internal/books/second_module_test.go` — `TestSecondModuleNoAudiobookEdit`.

Modify:

- `tools/cmd/oplint/` — `main.go` also runs `tasklint.Analyzer` (import `github.com/falkcorp/mediacore/archtest/tasklint`),
  so `make oplint` (a `make ci` gate) checks `pkg/ops` and `tasks` definitions. Existing oplint rules unchanged.
- `core/openapi/core.json` — regenerated with the native task routes (hello's OpenAPI subcommand).
- `docs/compat-surfaces.md` and `internal/books/compat-surfaces.md` — one ledger section per alias group below (rows
  are inert until PC-12-08 registers the aliases; keep the embedded copy byte-identical, `internal/books/compat_test.go`
  checks it).

## Re-verify greps (ABO paths identical at `748fd787a` and the plan checkout)

```bash
sed -n 222,252p internal/server/handlers/opsv3/handler.go | grep -c 'g\.\(GET\|POST\)('      # expect 12 (/api/v3/ops routes)
grep -n 'const Prefix = "/api/v3/ops"' internal/server/handlers/opsv3/handler.go           # expect 1
grep -c 'protected.GET("/op-defs' internal/server/wire_operations_routes.go                 # expect 2
grep -c 'protected.\(GET\|POST\|PUT\)("/tasks' internal/server/wire_operations_routes.go    # expect 3
grep -n "^oplint:" Makefile && grep -c "oplint" Makefile                                    # expect the target; >= 3 mentions
test -d core/internal/executor && grep -rn "func (.*) WriteRuns(" core/internal/executor --include=*.go | wc -l   # PC-12-02; expect >= 1
test -f core/examples/hello/module.go || ls core/examples/hello/*.go | head -3              # PC-04-02 created hello; expect files listed
test -f core/openapi/core.json && echo ok                                                    # expect ok
test -f internal/books/compat-surfaces.md && echo ok                                         # PC-07-06; expect ok
test ! -e core/internal/taskhttp && test ! -e core/storage/datamigrate && echo ok            # expect ok
```

## Steps

1. Write the tests first (list below).
2. **Native routes** (`core/internal/taskhttp/routes.go`), group `/api/core/v1/tasks`, exactly 05-tasks §5T.3.10's
   table; every route has a permission (R6):

   | Route | Permission |
   |---|---|
   | `GET /definitions`, `GET /definitions/{kind}` | `rbac.AnyAuthenticated` + per-def `View` filter |
   | `GET /definitions/{kind}/runs`, `GET /runs`, `GET /runs/{id}`, `GET /runs/{id}/rows`, `GET /runs/{id}/items`, `GET /runs/{id}/log` | per-def `View`; every `RunView` through `tasks.RedactRunView` |
   | `POST /definitions/{kind}/runs` | per-def `Start`; copies `Idempotency-Key` into `StartRequest.IdempotencyKey`; a duplicate within `core.tasks.idempotency_ttl` answers `200` with the existing `RunView` and header `Idempotent-Replayed: true` |
   | `POST /runs/{id}/cancel` | the run's actor or `Cancel` |
   | `POST /runs/{id}/approve` | `Approve` + verified method |
   | `POST /runs/{id}/decide` (`resume`, `finish`, `drop`) | `Approve`/`Start` + verified method |
   | `GET /counts`, `GET /timeline` | `core:tasks.view` |
   | `GET /schedules`, `PATCH /schedules/{kind}` | view / `core:tasks.manage`; PATCH writes the kind's `ScheduleConfig` key through `config` (a key locked by an operator layer → 409) |
   | `POST /pause`, `POST /unpause` | `core:tasks.manage` |

   No events route. Start of an unknown kind (after alias resolution) → `404 tasks.unknown_kind`.
3. Kernel: register these routes only when the kernel built an executor (PC-12-02's rule). ABO builds none before
   PC-12-08, so ABO serves none of them yet. Confirm with `TestKernel_NoTasksBackendWithoutTaskKinds` staying green.
4. **Aliases** (`aliases.go`): `func RegisterLegacyAliases(g *httpapi.Group)` (name coined here) declaring, with
   `httpapi.Alias` and one `LegacyPath.Ledger` row each, the legacy paths onto the native routes:
   - `/api/v3/ops/*`: the 11 non-stream routes of `opsv3/handler.go:230-248` (`GET /ops/defs`, `/ops/defs/:id`,
     `/ops/runs`, `/ops/runs/:id`, `/ops/runs/:id/rows`, `/ops/counts`, `/ops/timeline`; `POST /ops/defs/:id/runs`,
     `/ops/runs`, `/ops/runs/:id/cancel`, `/ops/runs/:id/approve`). `GET /api/v3/ops/events` is **not** an alias: it stays
     HEAD's broker (fed by the books bridge from PC-12-08) until S6's projection C.
   - `/api/v1/op-defs` (2): `GET /op-defs`, `GET /op-defs/:id` → `GET /definitions[/{kind}]`.
   - `/api/v1/tasks` (3): `GET /tasks` → `GET /schedules`; `PUT /tasks/:name` → `PATCH /schedules/{kind}`;
     `POST /tasks/:name/run` → `POST /definitions/{kind}/runs`.
   - **None** under `/api/v1/operations` and none for `/api/v1/maintenance-window/*` (05-tasks §5T.3.10; R11-3). Add a
     test asserting this.
   Legacy bodies through `LegacyPath.Shape`; def IDs through `FormerKinds`/`LegacyDefID`. Nothing calls
   `RegisterLegacyAliases` in this PR; its test registers it on a `coretest` boot.
5. **tasklint**: port `tools/cmd/oplint/analyzer.go`'s rules to the `tasks` spec types (`tasks.TaskSpec`, `BatchSpec`,
   `FixerSpec` fields that run with a `Writer` in scope). Add `testdata` mirroring oplint's. Wire it into
   `tools/cmd/oplint/main.go` beside the existing analyzer.
6. **datamigrate + storagemigrate**: types exactly as 05-storage §5S.3.6; registration refuses `ModeInvalid`, zero
   `Compat`, zero `Parallelism`, `Online` (deferred: "not built in the extraction"); the runner commits each chunk's
   writes with the shard's cursor in one batch every `core.storage.data_migration.chunk_bytes` (4 MiB) or `chunk_items`
   (2 000). If `module.Manifest.DataMigrations` and the kernel's P4b call do not exist yet, add them (06 §6.6 S5 row);
   no module declares one in this PR.
7. **hello.count**: copy 06 §6.12.3 verbatim; add `"hello.count"` to hello's `TaskLedger`.
8. `TestSecondModuleNoAudiobookEdit` in `internal/books` (06 §6.12 table): `coretest.New(t, WithModules(books.Module(…),
   hello.New()))` boots and runs `hello.count` to `succeeded`; the PR touches no `internal/` audiobook package other
   than that test file (step 10's diff check).
9. Regenerate `core/openapi/core.json` with hello's OpenAPI subcommand (06 §6.12; the S2 skeleton added it). Commit it.
10. Acceptance; headers; `changelog.d/` fragment.

## Code contract

```go
// 05-tasks §5T.3.10 — Client methods the handlers call
Start(ctx, kind, StartRequest) (RunRef, error); Get(ctx, RunID) (RunView, error); Runs(ctx, RunQuery) iter.Seq2[RunView, error]
Timeline(ctx, TimelineQuery) ([]RunView, error); Log(ctx, RunID, PageQuery) iter.Seq2[LogLine, error]
// 05-http-api §3.1
func Alias(g *Group, legacy LegacyPath)
// 05-storage §5S.3.6
func DerivedBuildTask(module, name string, s DerivedBuildSpec) tasks.Definition
```

## Tests to write first

- `TestSecondModuleNoAudiobookEdit` (06; row done-test).
- `TestNativeStores_RoundTrip`'s `hello.count` half: `hello.count` on a zero-module `coretest` (05-tasks §5T.9).
- `TestCoreJSONHasTaskRoutes` (name coined here): `core/openapi/core.json` contains every native path of step 2 (row
  done-test "core.json contains the stage's native routes").
- `TestStart_IdempotencyKeyHeaderReplays`, `TestStart_ConcurrentSameIdempotencyKeyOneRun`,
  `TestRunView_CurrentRedactedWithoutUsersView`, `TestRunRedaction_SameOnStreamAndREST` (REST half) — 05-tasks §5T.9 /
  05-realtime §10 names.
- `TestTaskAliases_NoneUnderOperations` (name coined here): `RegisterLegacyAliases` registers 16 aliases (11 + 2 + 3) and
  none whose pattern starts with `/api/v1/operations` or `/api/v1/maintenance-window`.
- `TestDataMigrationResumesExactly`, `TestDataMigrationRewritesInPlaceNotRevisited`, `TestDataMigrationShardsDisjoint`
  (`-race`), `TestDataMigrationVerifyBlocksBarrier` (05-storage test table).
- tasklint: `analysistest.Run` over its `testdata`.

## Acceptance

```bash
(cd core && go build ./... && go vet ./internal/taskhttp/... ./archtest/tasklint/... ./storage/datamigrate/... ./internal/storagemigrate/... ./examples/hello/...)   # exit 0
(cd core && go test -race ./internal/taskhttp/... ./archtest/tasklint/... ./storage/datamigrate/... ./internal/storagemigrate/... ./examples/hello/... -count=1)   # all ok
go test ./internal/books/ -run TestSecondModuleNoAudiobookEdit -count=1 -v | grep -c '^--- PASS'   # expect 1
grep -c '"/api/core/v1/tasks/' core/openapi/core.json        # expect >= 15 distinct native paths
make oplint                                                   # prints "oplint passed"
git diff --name-only origin/main | grep '^internal/' | grep -v '^internal/books/second_module_test.go$\|^internal/books/compat-surfaces.md$' | wc -l   # expect 0
make -C core ci            # exit 0
make ci                    # exit 0
```

ABO serves no `/api/core/v1/tasks` route until PC-12-08, so there is no live `curl` check in this task.

## Out of scope

- Calling `RegisterLegacyAliases`, removing HEAD's gin opsv3/op-defs/tasks routes, books' `/api/v1/operations*` handlers
  on `Deps.Tasks` (PC-12-08).
- `Online` data migrations, read adapters, `<module>.datamigrate.<name>` kinds (deferred build).
- Any parallelism change.

## Rollback

Revert the PR (binary swap). ABO mounts nothing new; hello and `coretest` are not shipped behaviour.

## Repo rules

- File headers on every new or changed file (`core.json` keeps whatever header convention the S2 skeleton set); the
  two compat ledgers stay byte-identical; `changelog.d/` fragment without a header.
- Commits end with `Stage-Release: S5 1` then `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`
  after required checks and OWNER-12's row; never squash. `docs/compat-surfaces.md` is shared with contract PRs (plan
  §(d)): do not dispatch while a W19 PR touching it is open.

## Planner notes

- 05-tasks §5T.3.10 and 09 S5 count `/api/v3/ops/*` aliases as 12 at the evidence commit, but one of HEAD's 12 routes is
  `GET /api/v3/ops/events`, which the same section says is not a tasks route (it becomes projection C at S6). This brief
  registers 11 aliases there; the spec count should read 11 + the stream.
- The datamigrate runner needs `module.Manifest.DataMigrations` and a kernel P4b call (06 §6.6), in `core/module/` and
  `core/kernel/`, which the row does not list.
