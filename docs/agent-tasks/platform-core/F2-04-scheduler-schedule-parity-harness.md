<!-- file: docs/agent-tasks/platform-core/F2-04-scheduler-schedule-parity-harness.md -->
<!-- version: 1.1.4 -->
<!-- guid: 150263db-02f3-457c-9fd5-39a0970861c8 -->
<!-- last-edited: 2026-11-05 -->

# F2-04 — `core/internal/scheduler` written fresh, `core.window.nightly` declared in `core/tasks`, every F2 schedule key and the `core.tasks` config keys, and the schedule-parity harness

| Field | Value |
|---|---|
| ID | F2-04 |
| Release / flip | R2, foundation PRs (F2), **dark** |
| Wave | W28 |
| Tier | **T3** (Opus / high): a fresh scheduler whose durable clocks must never stampede at first boot nor lose or duplicate a fire; time-zone and DST correctness |
| Depends on | F2-02, F2-09 (`core/config`, `config.Duration` and the kernel's core-section list in `core/kernel/core_sections.go`) |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>
SLUG=platform-core-f2-04-scheduler
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
```

No `go work init` (CLAUDE.md).

## Goal

Write `github.com/falkcorp/mediacore/internal/scheduler` (`core/internal/scheduler/`), a **re-implementation** of
HEAD's `internal/scheduler` (05-tasks §5T.8.1 scheduler row; 04 §8.6): one scheduler goroutine polling every
`core.tasks.schedule_poll`, one durable clock per scheduled kind through `tasks.ClockStore`, the due fire as **one
storage batch** (`ClockStore.Advance` + `RunStore.Insert`, inside `WriteRuns`), interval (`Every` in
`ScheduleConfig`), `DailyAt`, 5-field `Cron`, maintenance windows (`core.tasks.windows.<name>`, run as
`core.window.<name>`, members in `Order`), `OnStart`, `When` gates, `Missed` policies, the first-sight no-stampede
rule and DST. Add the schedule-parity harness `core/tasks/taskstest/schedule/` that F5-14's
`TestScheduleParity_AllDefinitions` and `TestCutover_NoCatchUpStorm` drive.

This row is also the **one owner of `core.window.nightly`** (05-tasks §5T.8.2 T4 table row `maintenance.window`:
"`core/tasks`, the one owner: declared at F2 with the scheduler"), declared in `core/tasks/window.go`, and of **every
schedule key typed `tasks.ScheduleConfig` that exists at F2**, plus the `core.tasks.windows.*` keys, all added to the
kernel's core-section list in `core/kernel/core_sections.go` (F2-09 created that file and declared every section and
every other key; `core/config` cannot name `tasks.ScheduleConfig` before `core/tasks` exists, and `storage` at L1
cannot hold one, 05-storage §5S.4). Storage and every other owner only **schedule into** the window through
`ScheduleConfig.Window`; none declares it.

Dark: nothing constructs the scheduler in ABO before F5 (05-tasks §5T.8.2 Core group); the kernel's start rule that
constructs it for `hello` is F2-16's. Declaring keys changes no behaviour (05-config §1.12.1).

## Spec to read first

1. 05-tasks §5T.3.8 (whole section: the key rule R2-7, `ScheduleConfig`, `Every` only for the relay, "Durable
   clock", "First boot after the cutover"), §5T.3.11 (a held kind's clock is not polled), §5T.4 steps 2 and 4
   (start and stop order), §5T.5 schedule rows, §5T.7 ("One scheduler goroutine"), §5T.10 (the `core.tasks.*`
   keys this package reads).
2. 05-tasks §5T.8.2 T4 table row `maintenance.window` → `core.window.nightly`, and §5T.9 schedule rows.
3. 05-config §1.3.1 (`config.Duration`, `Define`), §1.3.6 (key grammar, map keys are data), §1.12.1 "Every core
   section exists from F2" (F2-09 declared the `core.tasks` section and its non-schedule keys; this PR adds the
   schedule-typed keys and the windows keys to the same list).
4. 05-storage §5S.4 "Task kinds" (`core.storage.schedules.compact` is declared at F2 by the kernel's core-section
   list; `storagetasks` only passes it to `ScheduleFromConfig`, from F5).
5. Each owner's schedule-key row (the table below).

## Base state: re-verify before editing

```bash
grep -rn 'type ClockStore interface' core/tasks/        # 1 hit, else STOP
grep -rn 'func NativeStores' core/tasks/                # 1 hit (F2-02), else STOP
grep -rn 'func ScheduleFromConfig' core/tasks/          # 1 hit (F2-01), else STOP
grep -rn 'type Duration ' core/config/                  # 1 hit (F2-09); if 0, STOP (ScheduleConfig needs it)
grep -n '"core.tasks"' core/kernel/core_sections.go    # >=1 (F2-09 declared the section), else STOP
grep -n 'schedules\.' core/kernel/core_sections.go | wc -l   # 0 (F2-09 left every schedule key to this row), else STOP
grep -rn 'type ScheduleConfig struct' core/tasks/       # 1 hit (F2-01), else STOP
test ! -e core/internal/scheduler && echo absent        # "absent"
test ! -e core/tasks/window.go && echo absent           # "absent"
# HEAD scheduler being re-implemented (read only; counts at ac4368bdb)
grep -n 'const intervalLastTickPrefix = "scheduled_task_last_tick."' internal/scheduler/interval_clock.go   # 1 hit
grep -n 'const intervalPollInterval = time.Minute' internal/scheduler/interval_clock.go                   # 1 hit
grep -n '^func ParseCron' internal/scheduler/cron.go                                                       # 1 hit
grep -n '^func nextDailyAt' internal/scheduler/daily_at.go                                                 # 1 hit
```

`internal/scheduler/cron.go` imports `pkg/ops`; read its grammar (`*`, `a`, `a-b`, `a,b,c`, `*/n`, `a-b/n`; no
names, no `?`/`L`/`W`/`#`) and re-implement it, copying no ABO import.

`ScheduleConfig.Every` is already `config.Duration` (F2-01 depends on F2-09); no switch is needed.

**The schedule keys this row declares** (regenerate at your base; a different count means the spec moved: declare what
your base says and list the difference in the PR body):

```bash
grep -nE '^\| `core\.[a-z_.<>]*schedules\.[a-z_]*`( / `core\.[a-z_.]*`)? \| `tasks\.ScheduleConfig`' \
  docs/specs/platform-core/05-*.md | grep -v '05-indexer' | wc -l        # 12 table rows (13 keys) at the plan's base
```

| Key | Owner row (default) |
|---|---|
| `core.ai.schedules.batch_poll` | 05-ai §7 (`{enabled: true, every: 5m}`) |
| `core.audit.schedules.prune` | 05-rbac §10 (`{enabled: true, every: 24h}`) |
| `core.auth.schedules.session_prune`, `core.auth.schedules.onetime_prune` | 05-authn §8 (`{enabled: true, every: 1h}`) |
| `core.backup.schedules.create`, `core.backup.schedules.verify` | 05-backup §5B.7 (`every: 24h`, `every: 168h`) |
| `core.diagnostics.schedules.prune` | 05-health §5H.7 (`{enabled: true, every: 1h}`) |
| `core.integrations.schedules.health` | 05-metadata M1.7 (`{enabled: true, every: 5m}`) |
| `core.library.schedules.quarantine_prune` | 05-library §5L.7 (`{enabled: true, every: 24h}`) |
| `core.library.schedules.transfer_recover` | 05-library §5L.7 (`{enabled: true, every: 24h}`); F7-06 registers `core.library.transfer-recover` on it with `ScheduleFromConfig("core.library.schedules.transfer_recover").OnStart()`, so it also runs once at start (05-library §5L.3.3) |
| `core.log.schedules.journal_prune` | 05-logging §8 (`{"enabled":true,"every":"24h"}`). Declared here in the kernel's `core.log.schedules` section struct (F2-09 declared the section); F5-15 later swaps the declaration to `logjournal.Schedules` (05-logging §5) in the same file, so one type is registered |
| `core.tasks.schedules.prune` | 05-tasks §5T.10 (`{"enabled": true, "every": "24h"}`) |
| `core.updater.schedules.check` | 05-updater §5U.8 (`{enabled: true, every: 1h}`) |
| `core.storage.schedules.compact` | 05-storage §5S.4 "Task kinds" (prose, not a table row: a nightly-window member, DB-persisted, seeded once by M2 from HEAD's `scheduled.db_optimize`). The spec states no default: declare `{enabled: true, window: "nightly"}` (a window member, as §5S.4 says) and record it in the PR body |

Not declared here: `core.download.schedules.poll` (phase 2), 05-indexer's keys (non-normative sketch), and
`core.updater.schedules.apply` (05-updater §5U.8: registered by the release that builds it). The windows keys
`core.tasks.windows.<name>.{start,end,enabled}` (05-tasks §5T.10: hours and a bool, `nightly` defaults 1 / 4 / true)
are declared here too, as the one map field `windows` of the `core.tasks` section struct (05-config §1.3.6).

## Files

Create:

- `core/internal/scheduler/` — `scheduler.go` (`New(opts Options)`, `Run(ctx)`, `Stop(ctx)`; names and `Options`
  coined here: the `tasks.Stores`, the `tasks.Executor`'s `WriteRuns` and `StartIn`, a config reader for
  `ScheduleConfig` keys and `core.tasks.*`, a clock source, the held-kind set), `clock.go`, `cron.go`, `daily.go`,
  `window.go` (`core.window.<name>` kinds), `validate.go` (registration checks of §5T.3.1 for schedules, as a pure
  function the kernel will call at P6/P7: `func ValidateSchedules(defs []tasks.Definition, declared
  func(key string) (isScheduleConfig, owner string, ok bool)) error`, name coined here).
- `core/tasks/taskstest/schedule/` — the harness: a fake clock and zone, a recorder of enqueues, start-runs,
  window reachability and clock advances, and `Run(t, defs, config, steps)` (name coined here) returning the
  observed sequence, so F5-14 can compare core's sequence with HEAD's.
- `core/tasks/window.go` — the declaration of the window kind `core.window.nightly` (05-tasks §5T.8.2 T4 table;
  §5T.3.8 `Window`): the kind name as a constant and the window definition the scheduler runs, with `core/tasks` as
  its one owner. Exported names coined here (e.g. `const WindowNightly Kind = "core.window.nightly"`); keep them in
  this one file.
- `core/kernel/core_sections_schedule_test.go` — `TestCoreSections_ScheduleKeysDeclared` (name coined here).
- Tests beside them.

Modify:

- `core/kernel/core_sections.go` — add the 14 schedule keys of the table above, each a field of type
  `tasks.ScheduleConfig` in its owner's section struct with the owner's default and `reload` from its Hot-reload
  column (all "yes"), and the `windows` map field of the `core.tasks` section. Touch no other key F2-09 declared.

Nothing else; in particular not `core/kernel/core_sections_test.go` (F2-09's) or any boot file (F2-16 wires).

## Code contract and invariants

- The key rule (R2-7): `<owner>.<subsystem>.schedules.<name>`, owner `core` (core kinds) or `modules.<id>` (the
  registering module), segments `[a-z][a-z0-9_]{0,47}`, declared by that owner with type `tasks.ScheduleConfig`,
  one kind per key. `ValidateSchedules` refuses everything else, and refuses `Every` on any kind but
  `core.outbox.relay`, and a scheduled writer with neither `Live()` nor `Preview()`.
- Exactly one of `Every`, `DailyAt`, `Cron`, `Window` when `Enabled`; `Every >= core.tasks.schedule_poll`.
- **One batch per fire:** `ClockStore.Advance` (checked against the clock the poll read) and `RunStore.Insert` staged in
  one `WriteRuns`; `ErrClockConflict` = another poll won, no fire.
- **No stampede:** first sight seeds `LastDue = now` and does not fire. An empty `ScheduleHash` (every clock the
  cutover imports) derives the next instant from `LastDue` and the current config (HEAD's interval rule,
  `interval_clock.go`), so an imported clock continues.
- The clock advances on every due check whether or not the `When` gate let it enqueue.
- A scheduled start folds into an active run of the same mode (HEAD's `EnqueueOp` rule for scheduled defs).
- `Coalesce` fires once for any number of missed instants and counts them; `SkipMissed` waits for the next.
- DST: `DailyAt`/`Cron` in `core.tasks.timezone`; a skipped wall time fires at the first instant after; a repeated
  one fires once.
- `core.window.nightly`: hours from `core.tasks.windows.nightly.start`/`.end` (defaults 1 and 4), wraps midnight,
  runs members in `Order` once per opening.
- A clock read error skips the poll (metric `schedule_errors`), never "due now".
- One goroutine for all schedules; no package-level state (R5); stops before the executor drains (§5T.4 step 4).
- The scheduler's instruments (`schedule_lag`, `schedule_missed`, `schedule_errors`) follow the executor's switch:
  created only when the owner of the App wants them (ABO creates none before F5; 05-tasks §5T.11).

## TDD: tests to write first (names copied from 05-tasks §5T.9)

- `TestCoreSections_ScheduleKeysDeclared` (name coined here, in `core/kernel/core_sections_schedule_test.go`): the
  set of leaf paths of type `tasks.ScheduleConfig` reflected from the core-section list equals the table's 14 keys
  exactly (`core.library.schedules.transfer_recover` among them), and `core.tasks.windows.nightly.{start,end,enabled}`
  resolve with defaults 1, 4, true.
- `TestWindowNightly_DeclaredOnceInTasks` (name coined here): `core.window.nightly` is declared only in
  `core/tasks/window.go` (an AST or `go list` grep over `core/...` finds no second declaration).

- `TestSchedule_EnqueueAndClockOneBatch` (crash injection between clock advance and enqueue: neither or both)
- `TestSchedule_NoBootStampede`
- `TestSchedule_CoalesceMissed`
- `TestSchedule_DSTSpringForwardFallBack` (`synctest`, fake zone)
- `TestScheduleFromConfig_UndeclaredKeyRefused`
- `TestScheduleFromConfig_ForeignNamespaceRefused` (a core kind on `modules.x.…`, a module kind on another module's
  or a `core.` key, a key without `.schedules.`, a hyphenated segment, a `duration`-typed key, one key on two kinds)
- `TestScheduleFromConfig_OwnerKeyAccepted` (`core.backup.schedules.create`,
  `modules.books.scheduler.schedules.library_scan_full`)
- `TestScheduleFromConfig_HotReload`
- `TestEvery_RefusedExceptRelay`
- `TestTransitionVsCancelOneWins`'s clock half is F2-02's suite; do not duplicate.
- Harness self-test `TestScheduleHarness_RecordsSequence` (name coined here): two kinds, interval and window, a
  fake day, the recorded sequence matches a hand-written golden.

`TestScheduleParity_AllDefinitions` and `TestCutover_NoCatchUpStorm` are (ABO) rows owned by F5-14; this PR only
gives them the harness.

## Steps

1. Base-state greps; plan in the PR body.
2. Tests first; then cron/daily/window math; then the clock and poll loop; then `ValidateSchedules`; then the
   harness.
3. `-race` green; headers; changelog fragment; acceptance.

## Acceptance

```bash
cd core
go build ./... && go vet ./internal/scheduler/... ./tasks/...                        # exit 0
go test ./internal/scheduler/... ./tasks/taskstest/schedule/... -race -count=1       # all ok
go test ./kernel/ -run 'TestCoreSections_' -count=1                                 # ok (F2-09's tests still pass beside the new one)
grep -v '//' kernel/core_sections.go | grep -c 'tasks.ScheduleConfig'              # 14 (F2-09's `// schedule keys (tasks.ScheduleConfig): F2-04` comments excluded)
grep -rn '"core.window.nightly"' --include='*.go' . | grep -v _test | wc -l         # 1 (tasks/window.go)
go test ./internal/scheduler/... -run 'TestSchedule_EnqueueAndClockOneBatch|TestSchedule_NoBootStampede|TestSchedule_CoalesceMissed|TestSchedule_DSTSpringForwardFallBack|TestScheduleFromConfig_UndeclaredKeyRefused|TestScheduleFromConfig_ForeignNamespaceRefused|TestScheduleFromConfig_OwnerKeyAccepted|TestScheduleFromConfig_HotReload|TestEvery_RefusedExceptRelay' -race -count=1 -v | grep -c '^--- PASS'   # 9
grep -rn 'audiobook-organizer\|internal/scheduler"' internal/scheduler/ | wc -l       # 0 (no ABO import)
cd ..
git grep -n 'mediacore/internal/scheduler' -- ':!core/' | wc -l                      # 0 (dark)
make -C core ci                                                                     # exit 0
make ci                                                                             # exit 0
```

## Out of scope

Declaring any section or any non-schedule key (F2-09); the two `secrets.Ref` keys (F3-03); `FailClosed` (F3-02);
registering any kind on a schedule key (each owner's flip; `storagetasks` from F5); converting HEAD's 38
`TaskDefinition`s (F5-14); kernel construction and start of the scheduler (F2-16); deleting HEAD's scheduler (F5-18).

## Idempotency / rollback

If `core/internal/scheduler/scheduler.go` exists, run the acceptance instead. Rollback = revert; nothing imports it.

## Repo rules

File headers; conventional commit (`feat(core/scheduler): fresh scheduler and schedule-parity harness (dark)`) with
`Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; `changelog.d/` fragment.

## Planner notes

- The inventory groups `core.tasks.windows.*` with the schedule keys, but 05-tasks §5T.10 types them as hours and a
  bool, not `tasks.ScheduleConfig`. This brief keeps them with this row as the inventory says (the window's one owner)
  and declares them as the `windows` map field; F2-09's brief leaves them out.
- `core.storage.schedules.compact` has no owner-table row with a default (05-storage §5S.4 is prose); the brief asks
  the agent to record the default chosen. 05-storage should state it.
