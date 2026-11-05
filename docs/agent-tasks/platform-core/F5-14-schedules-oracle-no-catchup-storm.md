<!-- file: docs/agent-tasks/platform-core/F5-14-schedules-oracle-no-catchup-storm.md -->
<!-- version: 1.1.3 -->
<!-- guid: 80c882d4-5cd4-40d6-8c6b-e278487a2d3d -->
<!-- last-edited: 2026-11-05 -->

# F5-14 — Conversion group T7, schedules: the 38 `TaskDefinition`s become `ScheduleFromConfig` declarations in `internal/books`, the 15 never-run v2 crons dropped per Q-C, with the schedule oracle and the no-catch-up-storm test

| Field | Value |
|---|---|
| ID | F5-14 |
| Release / flip | R2, tasks flip F5 (dark conversion PR; the declarations are attached by F5-18) |
| Wave | W33 |
| Tier | **T3** (Opus / high): schedule parity over every definition; a wrong default or clock rule is a first-boot stampede of full scans and paid AI calls (14 E16) |
| Depends on | F2-04 (core scheduler and the schedule-parity harness); F5-01 … F5-13 (every converted kind and its schedule hook); OWNER-04 (the recorded Q-C answer), F5-01, F5-02, F5-03, F5-04, F5-05, F5-06, F5-07, F5-08, F5-09, F5-10, F5-11, F5-12, F5-13, OWNER-04 |
| Gate | Plan, then execute autonomously in a worktree with its own PR |

## START HERE

```bash
REPO=<your clone>                       # adjust
SLUG=platform-core-f5-14-schedules
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/.worktrees/$SLUG" -b "agent/$SLUG" origin/main
cd "$REPO/.worktrees/$SLUG"
git rebase origin/main
npm ci --prefix web                     # or: make web-deps, once R1-05 has merged
```

Do **not** run `go work init` (CLAUDE.md). `core/` is the nested module `github.com/falkcorp/mediacore`.

## Goal

Replace HEAD's 38 scheduler `TaskDefinition`s (`internal/scheduler/tasks.go`) with **one schedule declaration per
scheduled kind**, books code in `internal/books/schedules.go`, that the tasks flip F5-18 attaches through the
conversions' schedule hook (`sched func(tasks.Kind) *tasks.Schedule`, 05-tasks §5T.8.2 conversion rule 4), and prove with the schedule oracle that core's
scheduler over these declarations behaves as HEAD's scheduler does (05-tasks §5T.3.8 "What replaces v2 cron strings";
§5T.8.2 T7 row: "the 38 `TaskDefinition`s → `ScheduleFromConfig` declarations on their kinds; the 15 v2 crons →
none (listed in the release note)"; gate `TestScheduleParity_AllDefinitions`). Nothing is registered; HEAD's
scheduler keeps running until F5-18 deletes it ("so no kind ever has two schedulers", §5T.3.8).

## The full sweep has its own kind

`taskV2DefIDs` maps both `library_scan` and `library_scan_full` to `library.scan`. 05-tasks §5T.8.2 (the paragraph
after the kind-rename map's rule 6) resolves the pair: "`library_scan_full` … maps to `books.library.scan-full`, a
new books kind with no HEAD definition of its own (T7), so its clock (`library_scan_full_last_run`) and its
`scheduled.library_scan_full` entry reach that kind and not `books.library.scan`. **One schedule per kind
stands:** `books.library.scan` carries `library_scan`'s schedule and `books.library.scan-full` carries the full
sweep's (`modules.books.scheduler.schedules.library_scan_full`)". F5-13 defines `books.library.scan-full`; this PR
declares its schedule like any other entry. No entry is gated.

## Spec to read first (in this order)

1. 05-tasks §5T.3.8 whole (the `Schedule` API, the key rule R2-7, "One mechanism, one key rule", "What replaces
   v2 cron strings", "Durable clock", "First boot after the cutover"), §5T.3.1 (`Common.Schedule`), §5T.3.7 mode
   contract item 4 (a scheduled writer runs in the mode its `Schedule` declares).
2. 05-tasks §5T.8.2 T7 row; conversion rule 4 (the schedule-lookup hook); the scheduler-name rule after the
   kind-rename map (`library_scan_full` → `books.library.scan-full`); the T4 table (`db_optimize` → `core.storage.compact` plus
   `books.maintenance.aux-store-compact`; `purge_old_logs` → `core.log.journal.prune`); "Three core kinds whose
   owners flip after F5" (`batch_poller` → `core.ai.batch-poll`, key `core.ai.schedules.batch_poll`, declared by
   its minimal owner registration).
3. 05-tasks §5T.9 rows `TestScheduleParity_AllDefinitions` and `TestCutover_NoCatchUpStorm`; §5T.10 (the window
   keys `core.tasks.windows.nightly.*`, `core.tasks.timezone`, `core.tasks.schedule_poll`).
4. 05-config's rename table rows `scheduled` and `scheduled.<entry>` (16 entries → `modules.books.scheduler.schedules.<entry>`,
   each "a `tasks.ScheduleConfig` by HEAD's own reading of the entry"), `scheduled.db_optimize` (two targets), and
   every row for the HEAD config values the 38 tasks read (intervals, enable flags, run-on-start flags).
5. 04 §2.4 (schedule declarations go into `internal/books`); 14 E16 and Q-C.

## Base state: re-verify before editing

```bash
# Owner input: the recorded Q-C answer (OWNER-04). Missing = STOP.
#   (where the lead records it: the tracking issue / docs/plans/platform-core/STATE.md; quote it in the PR)
# F2-04 and the conversions (0 hits on any = STOP)
grep -rn 'func ScheduleFromConfig(key string) \*Schedule' core/tasks/*.go | wc -l              # 1
grep -rn 'func (s \*Schedule) OnStart() \*Schedule\|func (s \*Schedule) When(' core/tasks/*.go | wc -l   # 2
ls core/tasks/taskstest/schedule/ | grep -v _test | wc -l                                       # >=1 (F2-04's parity harness)
grep -rln 'sched func(tasks.Kind) \*tasks.Schedule' internal/ | wc -l                           # >=10 (every conversion's list function, conversion rule 4)
grep -rn '"books.library.scan-full"' internal/server/*.go | grep -v _test | wc -l               # >=1 (F5-13)
ls internal/books/ | head                                                                        # internal/books exists (F3)
grep -rn 'modules.books.scheduler.schedules\|scheduler.schedules' internal/books/ --include=*.go | grep -v _test | wc -l   # >=16 (books' declared keys from F3); record the list
test ! -e internal/books/schedules.go && echo absent                                             # absent
test -f internal/books/config.go && echo present                                                 # present (books' config section, F3); record its section type
# HEAD's scheduler (ac4368bdb)
grep -c 'ts.registerTask(TaskDefinition{' internal/scheduler/tasks.go                          # 38
grep -c '' internal/scheduler/testdata/cadence.golden                                          # 38 (name|interval|daily_at|run_on_start|in_window|window_index|enabled_default)
grep -n '^var taskV2DefIDs = map\[string\]string{' internal/scheduler/maintenance.go | wc -l   # 1 (36 entries; transcode and batch_poller absent)
grep -rnE '^\s+Schedule:\s+' internal/plugins internal/server internal/maintenance --include=*.go | grep -v _test | grep -v 'Schedule: *nil' | grep -v 'handlers/opsv3' | wc -l   # 15 (the never-run v2 crons)
```

The 15 v2 crons at `ac4368bdb`: `deluge.protected-paths-sync` (`*/30 * * * *`) and the `Schedule:` literals in
`internal/plugins/maintenance/cleanup.go` (7), `author.go`, `db.go`, `nightly_compact_activity_log.go`,
`integrity_check.go`, `dedup_ops.go`, `orphan_book_files.go` and `metadata.go`. List each with its def ID and cron
string in the PR description; that list is the release-note input (05-tasks §5T.3.8; R2-01 assembles the note).

## Files

| Action | Path |
|---|---|
| create | `internal/books/schedules.go`: the declarations, `func ScheduleFor(k tasks.Kind) *tasks.Schedule` (name coined here), the hook F5-18 passes to every conversion's list function, and `func ScheduledKinds() []tasks.Kind` (name coined here): the sorted list of every books kind `ScheduleFor` returns a schedule for |
| create | `internal/books/schedule_parity_test.go`: `TestScheduleParity_AllDefinitions` and `TestCutover_NoCatchUpStorm` |
| modify | `internal/books/config.go`: books' config section gains the `modules.books.scheduler.schedules.<task>` keys beyond the 16 `scheduled.<entry>` keys F3 declared, each typed `tasks.ScheduleConfig` |
| create | one `changelog.d/` fragment |

Do not modify `internal/scheduler` (HEAD's scheduler stays the oracle until F5-18 deletes it), any conversion
package, `core/`, `main.go`, `internal/books/module.go` or any other `internal/books` file.

## Code contract

```go
// internal/books/schedules.go (names coined here, except the tasks API)

// ScheduleFor returns the schedule of a books kind, or nil. F5-18 passes it as every conversion's
// sched hook. It never returns a schedule for a core kind (core owners declare their own).
func ScheduleFor(k tasks.Kind) *tasks.Schedule

// ScheduledKinds lists, sorted, every books kind ScheduleFor returns a non-nil schedule for.
// The parity test builds its stub kinds from it; F5-18 re-checks it against the real list functions.
func ScheduledKinds() []tasks.Kind

// one entry per HEAD TaskDefinition that fires a books kind:
//   tasks.ScheduleFromConfig("modules.books.scheduler.schedules.<task name>")   // key rule R2-7: no hyphens
//       [.OnStart()]                // HEAD RunOnStart (05-tasks §5T.3.8)
//       [.When(gate)]               // a HEAD IsEnabled gate that is not a schedule setting
//       .Live() | .Preview()        // required for a writer (v3 rule D8; 05-tasks §5T.3.1 kernel rejects)
//       [.Params(p)]                // what HEAD's TriggerFn passes
//       [.Missed(tasks.Coalesce)]   // HEAD's countDue behaviour is Coalesce (the zero value)
```

Mapping rules (05-tasks §5T.3.8):

| HEAD `TaskDefinition` | Declaration |
|---|---|
| fires a def through `taskV2DefIDs` | on the kind that def converts to (05-tasks §5T.8.2 rule 1 or 4) |
| `library_scan_full` | on `books.library.scan-full` (F5-13's kind), key `modules.books.scheduler.schedules.library_scan_full` (05-tasks §5T.8.2 scheduler-name rule); `library_scan` stays on `books.library.scan` |
| `GetInterval()` default / `DailyAt` / cron | the key's default `ScheduleConfig`: `{enabled: <IsEnabled default>, every: <interval>}`, `daily_at: "HH:MM"`, or `cron`; values exactly as `cadence.golden` records them |
| `IsEnabled` | the key's `enabled` (a HEAD config flag that M2 renames into the key) or `.When(gate)` for a gate that is not a schedule setting |
| `RunOnStart` | `.OnStart()` (and the config flag it reads, if any, as the gate HEAD applies) |
| `RunInMaintenanceWindow` + `maintenanceOrder` index | `Window: "nightly", Order: <index>` in the key's default |
| `transcode` | deleted: fires nothing (05-tasks §5T.3.8) |
| `batch_poller` | nothing here: `core.ai.batch-poll`'s minimal owner registration declares `core.ai.schedules.batch_poll` |
| `db_optimize` | the books half `books.maintenance.aux-store-compact` on `modules.books.scheduler.schedules.db_optimize` (nightly member); the core half `core.storage.compact` (F5-03's kind) uses `core.storage.schedules.compact`, declared at F2 by the kernel's core-section list |
| `purge_old_logs` | nothing here: `core.log.journal.prune` is scheduled by `core.log.schedules.journal_prune` (05-logging §5; the kind is F5-03's, the key is declared from F2) |
| a v2 `OperationDef.Schedule` cron (15) | **none**, unless the owner's recorded Q-C answer enables one: then a declaration on its kind with key `modules.books.scheduler.schedules.<name>` (name coined per def, `[a-z][a-z0-9_]*`), default **disabled** unless the answer says otherwise |

Key declaration: each `modules.books.scheduler.schedules.<task name>` must be declared by books with type
`tasks.ScheduleConfig` (05-tasks §5T.3.8; registration fails otherwise). The 16 `scheduled.<entry>` keys exist
from F3. For every other task, declare its key in books' config section in `internal/books/config.go` (the section
type F3 created there) with the default above. If the section cannot take more keys without a config migration,
STOP and report.

## TDD: tests to write first (spec names, copied exactly)

| Test (05-tasks §5T.9) | Proves |
|---|---|
| `TestScheduleParity_AllDefinitions` **(ABO, fake clock)** | for each of HEAD's 38 `TaskDefinition`s, config enabled and disabled: core's scheduler over the converted declarations and HEAD's scheduler produce the same enqueue, start-run, maintenance-window reachability and clock-advance sequence; `transcode` and `series_normalize` never start themselves |
| `TestCutover_NoCatchUpStorm` **(ABO, fake clock)** | first boot after M4: no kind fires before its next due instant from HEAD's clock; the window clock, imported with `TZ` west of UTC after a run at 23:30 local, is not due again that day |

Build both on F2-04's harness (`core/tasks/taskstest/schedule/`): HEAD's side drives `internal/scheduler.NewTaskScheduler`
with the fake clock and the reachability deps `cadence_golden_test.go` uses; core's side runs F2-04's scheduler over
**one stub `tasks.Task` per kind in `ScheduledKinds()`**, each with `Common.Schedule: ScheduleFor(k)` and a `Run` that
only records the start (built in the test, not from the conversions: the conversions' list functions `t6g<k>Kinds` and
`t7Kinds` are unexported methods with unexported deps, and this row edits no conversion package). The schedule is the
only field the parity compares, so a stub carries everything the scheduler reads. `TestCutover_NoCatchUpStorm` imports clocks the way M4 does (F5-16's clock decoders, or a fixture of
`tasks.Clock` values equal to what M4 writes, `library_scan_full_last_run` as `LastDue` of `books.library.scan-full`)
and then runs the first poll. `library_scan` and `library_scan_full` are two ordinary parity cases on two kinds.

Add (names coined here): `TestScheduleFor_KeysDeclaredAndValid` (every key obeys R2-7 and is declared with type
`tasks.ScheduleConfig`; no key on two kinds; `ScheduleFor` returns nil for every `core.*` kind),
`TestScheduleFor_DroppedV2CronsAbsent` (none of the 15 crons yields a schedule unless Q-C enabled it) and
`TestScheduleFor_KindListMatchesConversions`: the stub list is the list the conversions register, by name.
`ScheduledKinds()` equals a golden literal in `schedule_parity_test.go` (one kind per HEAD `TaskDefinition` that fires a
books kind: `"books." + taskV2DefIDs[name]` by conversion rule R3, `library_scan_full` → `books.library.scan-full`,
`db_optimize` → `books.maintenance.aux-store-compact`; `transcode`, `batch_poller`, `purge_old_logs` and core halves
absent), and every golden name `books.<id>` is declared by a conversion: exactly one non-test file under
`internal/plugins/` or `internal/server/` holds either the literal `"books.<id>"` or both a `tasks.Kind("books." +`
expression and the HEAD ID literal `"<id>"` (a source scan; conversion rule R3 declares each kind as
`tasks.Kind("books." + <HEAD def ID>)` beside HEAD's ID constant). A conversion that renames or drops a scheduled kind
fails this test. F5-18, which builds the real kinds from the
list functions, re-checks that `ScheduledKinds()` is a subset of the kinds it registers and that each registered kind's
`Common.Schedule` is `ScheduleFor(k)`.

## Steps

1. The base checks; STOP on a mismatch or a missing Q-C answer.
2. Write the two spec tests and the three coined tests; watch them fail.
3. `schedules.go` and the extra keys in `config.go`, entry by entry in `cadence.golden` order; run the parity
   test per entry.
4. PR description: the 38-row table (task → kind → key → default → window order), the 15 dropped crons (or the
   ones Q-C enabled), and a disposition count over the 38 tasks that sums to 38 (declared on a books kind,
   `library_scan_full` on `books.library.scan-full` included / deleted (`transcode`) / core-owned key
   (`batch_poller`, `purge_old_logs`, and `db_optimize`'s core half)).

## Acceptance (commands and expected output)

```bash
go build ./... && go vet ./internal/books/                                       # exit 0
go test ./internal/books/ -run 'TestScheduleParity_AllDefinitions|TestCutover_NoCatchUpStorm|TestScheduleFor_' -count=1 -v | grep -c '^--- PASS'   # 5
go test ./internal/books/ -run 'TestScheduleParity_AllDefinitions' -count=1 -v | grep -c 'SKIP'   # 0
grep -c 'schedules.library_scan_full' internal/books/schedules.go internal/books/config.go | awk -F: '{s+=$2} END {print (s>=1)}'   # 1
go test ./internal/scheduler/ -count=1 -short                                    # ok (HEAD's scheduler untouched)
git diff --name-only origin/main | grep -c '^internal/scheduler/\|^core/\|^main.go'   # 0
make ci                                                                          # exit 0
```

## STOP conditions

- No recorded Q-C answer.
- A HEAD config input of a task has no rename-table target in 05-config (the key's default would not reach the
  operator's value after M2).
- A parity difference that the declaration API cannot express (report the task and the sequence diff).

## Out of scope

Attaching the declarations (F5-18 passes `ScheduleFor`); deleting `internal/scheduler` (F5-18); clock import (M4,
F5-16); core owners' keys (declared at F2; the core kinds are F5-03's and F2's minimal owner registrations).

## Idempotency / rollback

If `internal/books/schedules.go` exists and the five tests pass, run the acceptance instead. Rollback = revert;
nothing reads `ScheduleFor` before F5-18.

## Repo rules

- Never edit `main` or the primary checkout; one worktree per task.
- File headers on every created or modified file (version bumped on `config.go`); `changelog.d/` fragment without a header.
- Conventional commit `feat(books): schedule declarations for the 38 scheduler tasks, with the schedule oracle (dark)`,
  body ending `Co-Authored-By: Claude <noreply@anthropic.com>`; 09 names no release or flip trailer.
- `gh pr merge <n> --rebase`, never squash, never `--admin` past a required check.
- Report with exact counts and the COMPLETED / REMAINING / BLOCKED lines.

## Planner notes

- 05-download §10 still says `deluge.protected-paths-sync` "gets a real schedule"; 05-tasks §5T.3.8 and 14 Q-C
  default to deleting the 15 crons. The owner's Q-C answer decides; the brief applies it.
- `purge_old_logs`' HEAD default is 168 h while `core.log.schedules.journal_prune` defaults to 24 h (05-logging §5):
  the parity test will show the difference unless M2's rename table carries HEAD's value into the core key; if it
  does not, report it as a spec gap rather than changing either default.
