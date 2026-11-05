<!-- file: docs/agent-tasks/platform-core/F2-04-scheduler-schedule-parity-harness.md -->
<!-- version: 1.0.0 -->
<!-- guid: 150263db-02f3-457c-9fd5-39a0970861c8 -->
<!-- last-edited: 2026-11-05 -->

# F2-04 — `core/internal/scheduler` written fresh, schedule keys, `core.window.nightly`, and the schedule-parity harness

| Field | Value |
|---|---|
| ID | F2-04 |
| Release / flip | R2, foundation PRs (F2), **dark** |
| Wave | W24 |
| Tier | **T3** (Opus / high): a fresh scheduler whose durable clocks must never stampede at first boot nor lose or duplicate a fire; time-zone and DST correctness |
| Depends on | F2-02 |
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
`TestScheduleParity_AllDefinitions` and `TestCutover_NoCatchUpStorm` drive. Dark: nothing constructs the scheduler
in ABO before F5 (05-tasks §5T.8.2 Core group).

## Spec to read first

1. 05-tasks §5T.3.8 (whole section: the key rule R2-7, `ScheduleConfig`, `Every` only for the relay, "Durable
   clock", "First boot after the cutover"), §5T.3.11 (a held kind's clock is not polled), §5T.4 steps 2 and 4
   (start and stop order), §5T.5 schedule rows, §5T.7 ("One scheduler goroutine"), §5T.10 (the `core.tasks.*`
   keys this package reads).
2. 05-tasks §5T.8.2 T4 table row `maintenance.window` → `core.window.nightly`, and §5T.9 schedule rows.
3. 05-config §1.3.1 (`config.Duration`), §1.12.1 "Every core section exists from F2" (the `core.tasks` section is
   declared by F2-09 in the kernel's core-section list; this PR **reads** its values, it does not declare them).

## Base state: re-verify before editing

```bash
grep -rn 'type ClockStore interface' core/tasks/        # 1 hit, else STOP
grep -rn 'func NativeStores' core/tasks/                # 1 hit (F2-02), else STOP
grep -rn 'func ScheduleFromConfig' core/tasks/          # 1 hit (F2-01), else STOP
grep -rn 'type Duration ' core/config/                  # 1 hit (F2-09); if 0, STOP (ScheduleConfig needs it)
grep -n '"core.tasks"' core/kernel/core_sections.go    # >=1 (F2-09 declared the section), else STOP
test ! -e core/internal/scheduler && echo absent        # "absent"
# HEAD scheduler being re-implemented (read only; counts at f7ac5b11e)
grep -n 'const intervalLastTickPrefix = "scheduled_task_last_tick."' internal/scheduler/interval_clock.go   # 1 hit
grep -n 'const intervalPollInterval = time.Minute' internal/scheduler/interval_clock.go                   # 1 hit
grep -n '^func ParseCron' internal/scheduler/cron.go                                                       # 1 hit
grep -n '^func nextDailyAt' internal/scheduler/daily_at.go                                                 # 1 hit
```

`internal/scheduler/cron.go` imports `pkg/ops`; read its grammar (`*`, `a`, `a-b`, `a,b,c`, `*/n`, `a-b/n`; no
names, no `?`/`L`/`W`/`#`) and re-implement it, copying no ABO import.

If F2-01 used the `time.Duration` fallback for `ScheduleConfig.Every`, switch it to `config.Duration` in this PR
(allowed edit in `core/tasks/schedule.go`).

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
- Tests beside them.

Allowed small edit: `core/tasks/schedule.go` (the `config.Duration` switch only). Nothing else.

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
go test ./internal/scheduler/... -run 'TestSchedule_EnqueueAndClockOneBatch|TestSchedule_NoBootStampede|TestSchedule_CoalesceMissed|TestSchedule_DSTSpringForwardFallBack|TestScheduleFromConfig_UndeclaredKeyRefused|TestScheduleFromConfig_ForeignNamespaceRefused|TestScheduleFromConfig_OwnerKeyAccepted|TestScheduleFromConfig_HotReload|TestEvery_RefusedExceptRelay' -race -count=1 -v | grep -c '^--- PASS'   # 9
grep -rn 'audiobook-organizer\|internal/scheduler"' internal/scheduler/ | wc -l       # 0 (no ABO import)
cd ..
git grep -n 'mediacore/internal/scheduler' -- ':!core/' | wc -l                      # 0 (dark)
make -C core ci                                                                     # exit 0
make ci                                                                             # exit 0
```

## Out of scope

Declaring the `core.tasks` section or any schedule key (F2-09 and each owner); converting HEAD's 38
`TaskDefinition`s (F5-14); kernel construction of the scheduler; deleting HEAD's scheduler (F5-18).

## Idempotency / rollback

If `core/internal/scheduler/scheduler.go` exists, run the acceptance instead. Rollback = revert; nothing imports it.

## Repo rules

File headers; conventional commit (`feat(core/scheduler): fresh scheduler and schedule-parity harness (dark)`) with
`Co-Authored-By: Claude <noreply@anthropic.com>`; `gh pr merge <n> --rebase`; `changelog.d/` fragment.

## Planner notes

- F2-04 reads the `core.tasks.*` keys and the `core.tasks.windows.*` keys that F2-09 declares, and needs
  `config.Duration` from F2-09's `core/config`. F2-09 is in W22, so the wave order already satisfies it, but the
  dependency edge F2-04 → F2-09 is missing from the row.
- F2-03 and F2-04 are in the same wave (W24) and both may switch a fallback type in `core/tasks/` left by F2-01
  (`ReadySignal` in F2-03, `ScheduleConfig.Every` in F2-04). The edits are in different files
  (`definition.go` and `schedule.go` in F2-01's suggested layout), but if F2-01 put both in one file they collide;
  check the file before starting and rebase after the sibling merges.
