<!-- file: docs/agent-tasks/platform-core/F2-05-taskhttp-hello-count-coretest.md -->
<!-- version: 1.1.2 -->
<!-- guid: 47990117-cfa7-4261-af4a-d76cf77ed7da -->
<!-- last-edited: 2026-11-05 -->

# F2-05 — `internal/taskhttp` (native `/api/core/v1/tasks` routes, `RedactRunView`) and `hello.count`'s TaskSpec on `tasks.NativeStores`

| Field | Value |
|---|---|
| ID | F2-05 |
| Release / flip | R2, foundation PRs (F2), **dark** (nothing mounts the routes or registers the kind in this PR) |
| Wave | W29 |
| Tier | **T2** (Sonnet / high): routes and a sample task over contracts that F2-01…F2-04 already fixed |
| Depends on | F2-03, F2-04, R1-37 |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f2-05-taskhttp
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
```

No `go work init` (CLAUDE.md). Core tests run from inside `core/`.

## Goal

1. `github.com/falkcorp/mediacore/internal/taskhttp` (`core/internal/taskhttp/`): the native routes of 05-tasks
   §5T.3.10's table under `/api/core/v1/tasks`, written fresh (a re-implementation of HEAD's
   `internal/server/handlers/opsv3`, 04 §8.6), every route with its permission (R6), every `RunView` through
   `tasks.RedactRunView`. The package exposes a constructor that takes a `tasks.Client` (and the config writer
   `PATCH /schedules/{kind}` needs) and returns the route group; nothing calls it in this PR.
2. `core/examples/hello/count.go`: the `hello.count` definition exactly as 06 §6.12.3 (`countTask(s *service)
   tasks.Definition`), on `tasks.NativeStores`. It is **not registered** here.

**What moved out of this row** (05-tasks §5T.4 step 1; 06 §6.4.10 F2 row): mounting `taskhttp`, the kernel's start
rule (the executor, scheduler and `taskhttp` start when any kind is registered or `c/tasks/` holds runs), hello's
`Registrar.Tasks` call and `TaskLedger`, `core/coretest/tasks.go` (`RunTask`, `WithRuns`, `RunResult`), the
regeneration of `core/openapi/core.json` and the client, and `TestDisabledModuleWithRunsBoots` are all F2-16's, the
F2 wiring row. The route tests here run over a **fake executor** (a `tasks.Client` fake in the package's tests), so
this PR needs no kernel.

Dark: no `taskhttp` route is mounted anywhere in this PR, and ABO imports nothing of it (05-tasks §5T.8.2 Core group).

## Spec to read

05-tasks §5T.3.7 (permission table, mode contract rules 1–2), §5T.3.10 (route table, `Client`, actor stamping,
`TimelineQuery`, `RedactRunView`), §5T.3.11 (held → 409), §5T.5; 05-http-api §3.1–§3.4 (routes, typed input,
problem+json, JSON naming), §4.4 (pagination), §4.5 (idempotency is the task start's only), §5 (lifecycle),
§7 (OpenAPI; the regeneration of `core/openapi/core.json` is F2-16's, with the mount); 06 §6.12.3 (`hello.count`),
§6.4.10 F2 row (what F2 lands and who starts it).

## Base state

```bash
grep -rn 'type Executor interface' core/tasks/                 # 1 hit, else STOP
test -d core/internal/executor && test -d core/internal/scheduler && echo ok   # "ok", else STOP
grep -rn 'func RedactRunView' core/tasks/                      # 1 hit, else STOP
test -d core/examples/hello && test -d core/coretest && echo ok # "ok" (R1-13, R1-09), else STOP
test ! -e core/internal/taskhttp && echo absent                # "absent"; if present see Idempotency
test ! -e core/examples/hello/count.go && echo absent          # "absent"
grep -rn 'Registrar' core/module/registrar.go | grep -c 'Tasks('   # 0 (Registrar.Tasks is F2-16's), else STOP and report
ls internal/server/handlers/opsv3/*.go | grep -vc _test         # 9 non-test files at f7ac5b11e (read-only reference)
```

## Files

Create only `core/internal/taskhttp/` (with its tests and a `tasks.Client` fake local to the tests) and
`core/examples/hello/count.go` (+ `core/examples/hello/count_test.go`). Do **not** modify
`core/examples/hello/module.go`, `core/coretest/`, `core/kernel/`, `core/module/`, `core/openapi/core.json` or the
generated client: they are F2-16's in W28.

## Routes (05-tasks §5T.3.10; copy paths and permissions exactly)

`GET /definitions`, `GET /definitions/{kind}`; `POST /definitions/{kind}/runs` (copies `Idempotency-Key` into
`StartRequest.IdempotencyKey`; a replay answers `200` with `Idempotent-Replayed: true`; a `dry_run`/`dryRun` key at the
top level or inside `params` answers `400 tasks.mode_field_unsupported`; unknown kind `404 tasks.unknown_kind`);
`GET /runs`, `GET /runs/{id}`, `/runs/{id}/rows`, `/runs/{id}/items`, `/runs/{id}/log`; `POST /runs/{id}/cancel`;
`DELETE /runs/{id}` (`409` while active); `POST /runs/{id}/approve`; `POST /runs/{id}/decide`; `GET /counts`,
`GET /timeline`; `GET /schedules`, `PATCH /schedules/{kind}` (writes the kind's `ScheduleConfig` key through
config; `409` when operator-locked); `GET /pause`, `POST /pause`, `POST /unpause`. Held-run mutations answer
`409 tasks.held`.

## Tests (names from 05-tasks §5T.9; coined names marked)

Route tests run over the fake `tasks.Client` and a fake principal resolver (the route group with a request
context carrying a principal and its permissions; no core authentication exists before F4):

- `TestStart_DryRunFieldRefused400`, `TestStart_IdempotencyKeyHeaderReplays` (the replay header and status; the
  executor half is F2-03's), `TestTaskStart_IdempotencyKeyReplaysRunView` (05-http-api §12: a repeated
  `Idempotency-Key` on task start answers `200` with the existing `RunView` and `Idempotent-Replayed: true`),
  `TestStart_ConcurrentSameIdempotencyKeyOneRun` (over the fake: one `Start` reaches it),
  `TestTimelineLogDiscard` (core half; the (ABO) parity case is F5's), `TestHeld_MutationsRefused409` (the HTTP
  half: the fake returns the held sentinel and the route answers `409 tasks.held`).
- `TestRunView_RedactedOnRESTRoutes` (name coined here): `GET /runs` and `GET /runs/{id}` pass every `RunView`
  through `tasks.RedactRunView`. The spec's `TestRunView_CurrentRedactedWithoutUsersView` covers REST and the stream
  through `RunViewRedactor` and is F6-02's; do not use its name here.
- `TestApprove_RequiresVerifiedMethodAndPermission` and the per-route permission cells: write the table now, with
  the session and key cells `t.Skip`ped "needs core auth (F4-05)"; F4-05 finishes them.
- `TestHelloCount_Conformance` (name coined here, in `core/examples/hello/count_test.go`):
  `taskstest.Conformance(t, countTask(<a hello service over an in-memory keyspace>))` passes (package `hello`, so the
  unexported `countTask` and `service` are reachable). Running the task through `h.RunTask` on a `coretest` App, and
  `TestStart_ViewOnlyDefRefusedAtRegister` (a registration-time refusal), are F2-16's: they need the registration
  and `coretest/tasks.go`.

## Acceptance

```bash
cd core
go build ./... && go vet ./internal/taskhttp/... ./examples/hello/...                 # exit 0
go test ./internal/taskhttp/... ./examples/hello/... -race -count=1                    # all ok
go test ./internal/taskhttp/ -run 'TestStart_DryRunFieldRefused400|TestStart_IdempotencyKeyHeaderReplays|TestTaskStart_IdempotencyKeyReplaysRunView|TestStart_ConcurrentSameIdempotencyKeyOneRun|TestTimelineLogDiscard|TestHeld_MutationsRefused409|TestRunView_RedactedOnRESTRoutes' -race -count=1 -v | grep -c '^--- PASS'   # 7
grep -rn 'taskhttp' kernel/ app/ | wc -l                                               # 0 (nothing mounts it yet)
grep -rn 'countTask' examples/hello/ | grep -v _test | grep -vc 'count.go'             # 0 (not registered yet)
git diff --stat origin/main -- openapi/core.json | wc -l                               # 0 (no regeneration here)
cd ..
git grep -n 'mediacore/internal/taskhttp' -- ':!core/' | wc -l                        # 0 (ABO mounts no taskhttp route)
make -C core ci                                                                        # exit 0
make ci                                                                                # exit 0
```

## Out of scope

Mounting `taskhttp`, registering `hello.count`, the kernel's start rule, `coretest`'s task cells, regenerating
`core/openapi/core.json` and the client (F2-16); mounting any task route in ABO and deleting HEAD's routes (F5-18);
`RunViewRedactor` and the event stream (F6-02); core authentication and the permission cells' live half (F4-05).

## Rollback

If `core/internal/taskhttp/` and `core/examples/hello/count.go` exist, run the acceptance instead. Rollback = revert
the PR; nothing mounts `taskhttp` or registers `countTask` yet.

## Repo rules

File headers; conventional commit with `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`;
`changelog.d/` fragment.
