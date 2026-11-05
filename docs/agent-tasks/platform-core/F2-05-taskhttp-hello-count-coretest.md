<!-- file: docs/agent-tasks/platform-core/F2-05-taskhttp-hello-count-coretest.md -->
<!-- version: 1.0.0 -->
<!-- guid: 47990117-cfa7-4261-af4a-d76cf77ed7da -->
<!-- last-edited: 2026-11-05 -->

# F2-05 — `internal/taskhttp` (native `/api/core/v1/tasks` routes, `RedactRunView`), `hello.count` on `tasks.NativeStores`, and `coretest` on tasks

> **SPEC-GATED.** Step 0 below checks for spec text that does not exist at the plan commit. If Step 0 fails, STOP
> and report; do not invent the missing design. Everything after Step 0 is the brief to execute once the spec says
> what Step 0 looks for.

| Field | Value |
|---|---|
| ID | F2-05 |
| Release / flip | R2, foundation PRs (F2), **dark in ABO** |
| Wave | W25 |
| Tier | **T2** (Sonnet / high): routes and a sample task over contracts that F2-01…F2-04 already fixed |
| Depends on | F2-03, F2-04 |
| Gate | Plan, then execute autonomously in a worktree with its own PR (after Step 0 passes) |

## START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f2-05-taskhttp
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
```

## Step 0 — spec gate (run first; STOP if either check fails)

The spec disagrees with itself on when `hello.count` and the kernel's task wiring land, and no spec text says how the
kernel runs the executor for `hello`/`coretest` while not running it for ABO:

- 04 §8.1 F2 row, 05-tasks §5T.6.1 ("`hello.count` runs on it from the PR that lands it") and §5T.8.2 Core group
  ("`hello` and `coretest` run on them") land `hello.count` and `coretest` on tasks at **F2**.
- 06 §6.4.10 lands `module.Registrar.Tasks` (the only way a module registers a kind) at the **tasks flip F5**, and its
  hello paragraph says "`hello.count` with the tasks flip".
- 05-tasks §5T.8.2 and 05-metrics §13 item 1 say ABO's kernel starts no executor or scheduler and mounts no
  `taskhttp` route before F5, but no spec text states the kernel rule that starts them for `hello`/`coretest` at F2
  (a kernel option, "only when a kind is registered", or another rule), and no row owns the kernel's task wiring
  (building `NativeStores` at P4c, validating and registering definitions at P6–P7, `Executor.Run` at the 06
  §6.5.2 executor step, mounting `taskhttp`).

```bash
cd "$REPO/.worktrees/$SLUG"
# (a) 06 §6.4.10 must land Registrar.Tasks or hello.count at F2 (today: 0 and 1)
sed -n '/^| R2, foundation (F2, dark)/p' docs/specs/platform-core/06-app-sdk.md | grep -c 'Tasks\|hello.count'   # need >=1
grep -c 'hello.count. with the tasks flip' docs/specs/platform-core/06-app-sdk.md                            # need 0
# (b) a stated kernel rule for running the executor/scheduler/taskhttp for hello and coretest before F5
grep -nE 'kernel (constructs|starts|runs) (the|its) (executor|task executor)' docs/specs/platform-core/06-app-sdk.md docs/specs/platform-core/05-tasks.md   # need >=1 hit that names F2
```

If (a) or (b) fails: STOP. Report: "F2-05 is spec-gated: 06 §6.4.10 lands Registrar.Tasks/hello.count at F5 while 04
§8.1, 05-tasks §5T.6.1 and §5T.8.2 land them at F2, and no spec text gives the kernel rule (or the owning row) for
starting the executor, scheduler and taskhttp for hello/coretest but not ABO before F5."

## Goal (once Step 0 passes)

1. `github.com/falkcorp/mediacore/internal/taskhttp` (`core/internal/taskhttp/`): the native routes of 05-tasks
   §5T.3.10's table under `/api/core/v1/tasks`, written fresh (a re-implementation of HEAD's
   `internal/server/handlers/opsv3`, 04 §8.6), every route with its permission (R6), every `RunView` through
   `tasks.RedactRunView`.
2. `core/examples/hello/count.go`: the `hello.count` task exactly as 06 §6.12.3, on `tasks.NativeStores`.
3. `core/coretest/tasks.go`: `Harness.RunTask`, `WithRuns`, `RunResult` as 06 §6.4.8.
4. Kernel wiring exactly as the Step-0 spec text states (file named there).

Dark in ABO: no `taskhttp` route is mounted in ABO's App before F5 (05-tasks §5T.8.2; 06 §6.4.10 F2 row).

## Spec to read

05-tasks §5T.3.7 (permission table, mode contract rules 1–2), §5T.3.10 (route table, `Client`, actor stamping,
`TimelineQuery`, `RedactRunView`), §5T.3.11 (held → 409), §5T.5; 05-http-api §3.1–§3.4 (routes, typed input,
problem+json, JSON naming), §4.4 (pagination), §4.5 (idempotency is the task start's only), §5 (lifecycle),
§7 (OpenAPI: `core/openapi/core.json` regenerates); 06 §6.4.8 (`coretest`), §6.12.3 (`hello.count`),
§6.12.6 (hello's test).

## Base state

```bash
grep -rn 'type Executor interface' core/tasks/                 # 1 hit, else STOP
test -d core/internal/executor && test -d core/internal/scheduler && echo ok   # "ok", else STOP
grep -rn 'func RedactRunView' core/tasks/                      # 1 hit, else STOP
test -d core/examples/hello && test -d core/coretest && echo ok # "ok" (R1-13, R1-09), else STOP
test -f core/openapi/core.json && echo ok                       # "ok" (R1-13)
ls internal/server/handlers/opsv3/*.go | grep -vc _test         # 9 non-test files at f7ac5b11e (read-only reference)
```

## Files

Create `core/internal/taskhttp/`, `core/examples/hello/count.go`, `core/coretest/tasks.go`; modify
`core/examples/hello/` registration (hello's `Register` adds `countTask`) and `core/openapi/core.json`
(regenerated). Plus the kernel file the Step-0 text names.

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

`TestStart_DryRunFieldRefused400`, `TestStart_IdempotencyKeyHeaderReplays`, `TestStart_ConcurrentSameIdempotencyKeyOneRun`,
`TestRunView_CurrentRedactedWithoutUsersView` (the `GET /runs` half; the stream half lands with F6),
`TestTimelineLogDiscard` (core half; the (ABO) parity case is F5's), `TestHeld_MutationsRefused409` (HTTP half),
`TestStart_ViewOnlyDefRefusedAtRegister`, `TestNativeStores_RoundTrip`'s `hello.count` half ("`hello.count` on a
zero-module `coretest`"), and hello's `TestHello_CountTask` (name coined here) from 06 §6.12.6
(`h.RunTask(t, "hello.count", hello.CountParams{})` succeeds). `TestApprove_RequiresVerifiedMethodAndPermission`
needs core authentication, which lands at F4: write its table now with the session/key cells `t.Skip`ped with
"needs core auth (F4)", and list it for F4-05.

## Acceptance

```bash
cd core
go build ./... && go vet ./internal/taskhttp/... ./examples/hello/... ./coretest/...   # exit 0
go test ./internal/taskhttp/... ./examples/hello/... ./coretest/... -race -count=1     # all ok
go run ./examples/hello openapi > /tmp/core.json && diff -q /tmp/core.json openapi/core.json   # no output (committed file current); use the subcommand name R1-13 wired
cd ..
git grep -n 'mediacore/internal/taskhttp' -- ':!core/' | wc -l                        # 0 (ABO mounts no taskhttp route)
make -C core ci                                                                        # exit 0
make ci                                                                                # exit 0
```

## Out of scope

Mounting any task route in ABO; deleting HEAD's routes (F5-18); the event stream (F6); core authentication (F4).

## Rollback

Revert the PR; ABO does not import `taskhttp`, and `hello`/`coretest` return to their R1 shape.

## Repo rules

File headers; conventional commit with `Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`;
`changelog.d/` fragment.

## Planner notes

- SPEC-GATED (Step 0): 06 §6.4.10 (Registrar.Tasks and `hello.count` at F5) contradicts 04 §8.1, 05-tasks §5T.6.1
  and §5T.8.2 (both at F2), and no spec text gives the kernel rule for running the executor, scheduler and
  `taskhttp` for `hello`/`coretest` but not ABO before F5. No inventory row owns the kernel's task wiring
  (`core/kernel/tasks.go` or similar); add it to this row once the spec decides.
- `TestApprove_RequiresVerifiedMethodAndPermission` and the permission cells of every route need core
  authentication (F4); F4-05 should own finishing them.
