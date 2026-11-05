<!-- file: docs/agent-tasks/platform-core/F5-14-schedules-oracle-no-catchup-storm.md -->
<!-- version: 1.0.0 -->
<!-- guid: 80c882d4-5cd4-40d6-8c6b-e278487a2d3d -->
<!-- last-edited: 2026-11-05 -->

# F5-14 — Conversion group T7, schedules: the 38 `TaskDefinition`s become `ScheduleFromConfig` declarations in `internal/books`, the 15 never-run v2 crons dropped per Q-C, with the schedule oracle and the no-catch-up-storm test

| Field | Value |
|---|---|
| ID | F5-14 |
| Release / flip | R2, tasks flip F5 (dark conversion PR; the declarations are attached by F5-18) |
| Wave | W29 |
| Tier | **T3** (Opus / high): schedule parity over every definition; a wrong default or clock rule is a first-boot stampede of full scans and paid AI calls (14 E16) |
| Depends on | F2-04 (core scheduler and the schedule-parity harness); F5-01 … F5-13 (every converted kind and its schedule hook); OWNER-04 (the recorded Q-C answer) |
| Gate | Plan, then execute autonomously in a worktree with its own PR. **SPEC-GATED on one entry** (Step 0) |

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
conversions' schedule hook (`sched func(tasks.Kind) *tasks.Schedule`), and prove with the schedule oracle that core's
scheduler over these declarations behaves as HEAD's scheduler does (05-tasks §5T.3.8 "What replaces v2 cron strings";
§5T.8.2 T7 row: "the 38 `TaskDefinition`s → `ScheduleFromConfig` declarations on their kinds; the 15 v2 crons →
none (listed in the release note)"; gate `TestScheduleParity_AllDefinitions`). Nothing is registered; HEAD's
scheduler keeps running until F5-18 deletes it ("so no kind ever has two schedulers", §5T.3.8).

## Step 0 — SPEC-GATED entry (run first)

`taskV2DefIDs` maps both `library_scan` and `library_scan_full` to `library.scan`, so two `TaskDefinition`s (two
cadences, two keys — 05-tasks names `modules.books.scheduler.schedules.library_scan_full` itself — and two HEAD
clocks) fire one kind `books.library.scan`, which can carry one `Common.Schedule` and has one durable clock
(§5T.3.8 "One clock per scheduled kind"). The spec does not say how the full sweep is declared. Check whether
it has been resolved since this brief was written:

```bash
grep -n 'library_scan_full' docs/specs/platform-core/05-tasks.md | grep -v 'schedules.library_scan_full"\|full_sweep.go\|"modules.books.scheduler.schedules.library_scan_full"' | wc -l
grep -n 'full sweep\|full-sweep' docs/specs/platform-core/05-tasks.md | grep -i 'kind' | wc -l
```

If either finds a sentence that names the full sweep's kind (or a rule for two schedules on one kind), follow it.
Otherwise **declare the other 37 entries, leave `library_scan_full` undeclared, keep its parity case as an expected
`t.Skip` naming this gap, and STOP before merging**: report "SPEC-GATED: 05-tasks §5T.3.8/§5T.8.2 give no
declaration for `library_scan_full` (two TaskDefinitions → one kind)". The same gap stops F5-16's clock import
for that pair. Do not invent a kind and do not merge the two cadences.

## Spec to read first (in this order)

1. 05-tasks §5T.3.8 whole (the `Schedule` API, the key rule R2-7, "One mechanism, one key rule", "What replaces
   v2 cron strings", "Durable clock", "First boot after the cutover"), §5T.3.1 (`Common.Schedule`), §5T.3.7 mode
   contract item 4 (a scheduled writer runs in the mode its `Schedule` declares).
2. 05-tasks §5T.8.2 T7 row; the T4 table (`db_optimize` → `core.storage.compact` plus
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
grep -rln 'sched func(tasks.Kind) \*tasks.Schedule' internal/ | wc -l                           # >=10 (every conversion's list function, rule R9)
ls internal/books/ | head                                                                        # internal/books exists (F3)
grep -rn 'modules.books.scheduler.schedules\|scheduler.schedules' internal/books/ --include=*.go | grep -v _test | wc -l   # >=16 (books' declared keys from F3); record the list
test ! -e internal/books/schedules.go && echo absent                                             # absent
# HEAD's scheduler (f7ac5b11e)
grep -c 'ts.registerTask(TaskDefinition{' internal/scheduler/tasks.go                          # 38
grep -c '' internal/scheduler/testdata/cadence.golden                                          # 38 (name|interval|daily_at|run_on_start|in_window|window_index|enabled_default)
grep -n '^var taskV2DefIDs = map\[string\]string{' internal/scheduler/maintenance.go | wc -l   # 1 (36 entries; transcode and batch_poller absent)
grep -rnE '^\s+Schedule:\s+' internal/plugins internal/server internal/maintenance --include=*.go | grep -v _test | grep -v 'Schedule: *nil' | grep -v 'handlers/opsv3' | wc -l   # 15 (the never-run v2 crons)
```

The 15 v2 crons at `f7ac5b11e`: `deluge.protected-paths-sync` (`*/30 * * * *`) and the `Schedule:` literals in
`internal/plugins/maintenance/cleanup.go` (7), `author.go`, `db.go`, `nightly_compact_activity_log.go`,
`integrity_check.go`, `dedup_ops.go`, `orphan_book_files.go` and `metadata.go`. List each with its def ID and cron
string in the PR description; that list is the release-note input (05-tasks §5T.3.8; R2-01 assembles the note).

## Files

| Action | Path |
|---|---|
| create | `internal/books/schedules.go`: the declarations and `func ScheduleFor(k tasks.Kind) *tasks.Schedule` (name coined here), the hook F5-18 passes to every conversion's list function |
| create | `internal/books/schedule_parity_test.go`: `TestScheduleParity_AllDefinitions` and `TestCutover_NoCatchUpStorm` |
| create | one `changelog.d/` fragment |

Do not modify `internal/scheduler` (HEAD's scheduler stays the oracle until F5-18 deletes it), any conversion
package, `core/`, `main.go` or `internal/books/module.go`.

## Code contract

```go
// internal/books/schedules.go (names coined here, except the tasks API)

// ScheduleFor returns the schedule of a books kind, or nil. F5-18 passes it as every conversion's
// sched hook. It never returns a schedule for a core kind (core owners declare their own).
func ScheduleFor(k tasks.Kind) *tasks.Schedule

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
| `GetInterval()` default / `DailyAt` / cron | the key's default `ScheduleConfig`: `{enabled: <IsEnabled default>, every: <interval>}`, `daily_at: "HH:MM"`, or `cron`; values exactly as `cadence.golden` records them |
| `IsEnabled` | the key's `enabled` (a HEAD config flag that M2 renames into the key) or `.When(gate)` for a gate that is not a schedule setting |
| `RunOnStart` | `.OnStart()` (and the config flag it reads, if any, as the gate HEAD applies) |
| `RunInMaintenanceWindow` + `maintenanceOrder` index | `Window: "nightly", Order: <index>` in the key's default |
| `transcode` | deleted: fires nothing (05-tasks §5T.3.8) |
| `batch_poller` | nothing here: `core.ai.batch-poll`'s minimal owner registration declares `core.ai.schedules.batch_poll` |
| `db_optimize` | the books half `books.maintenance.aux-store-compact` on `modules.books.scheduler.schedules.db_optimize` (nightly member); the core half's key `core.storage.schedules.compact` is F5-03's |
| `purge_old_logs` | nothing here: `core.log.journal.prune` is scheduled by `core.log.schedules.journal_prune` (05-logging §5, F5-03) |
| a v2 `OperationDef.Schedule` cron (15) | **none**, unless the owner's recorded Q-C answer enables one: then a declaration on its kind with key `modules.books.scheduler.schedules.<name>` (name coined per def, `[a-z][a-z0-9_]*`), default **disabled** unless the answer says otherwise |

Key declaration: each `modules.books.scheduler.schedules.<task name>` must be declared by books with type
`tasks.ScheduleConfig` (05-tasks §5T.3.8; registration fails otherwise). The 16 `scheduled.<entry>` keys exist
from F3. For every other task, declare its key in books' config section with the default above, **in the same
section type F3 created** (find it with the base-state grep). If that requires editing a file outside this row's
two files, make the edit and list it under Planner notes in the PR; if the section cannot take more keys without
a config migration, STOP and report.

## TDD: tests to write first (spec names, copied exactly)

| Test (05-tasks §5T.9) | Proves |
|---|---|
| `TestScheduleParity_AllDefinitions` **(ABO, fake clock)** | for each of HEAD's 38 `TaskDefinition`s, config enabled and disabled: core's scheduler over the converted declarations and HEAD's scheduler produce the same enqueue, start-run, maintenance-window reachability and clock-advance sequence; `transcode` and `series_normalize` never start themselves |
| `TestCutover_NoCatchUpStorm` **(ABO, fake clock)** | first boot after M4: no kind fires before its next due instant from HEAD's clock; the window clock, imported with `TZ` west of UTC after a run at 23:30 local, is not due again that day |

Build both on F2-04's harness (`core/tasks/taskstest/schedule/`): HEAD's side drives `internal/scheduler.NewTaskScheduler`
with the fake clock and the reachability deps `cadence_golden_test.go` uses; core's side runs F2-04's scheduler over
`ScheduleFor` and the converted kinds (built from the conversions' list functions with `ScheduleFor` as the hook, in
`coretest`). `TestCutover_NoCatchUpStorm` imports clocks the way M4 does (F5-16's clock decoders, or a fixture of
`tasks.Clock` values equal to what M4 writes) and then runs the first poll. A parity case for `library_scan_full`
is `t.Skip("SPEC-GATED: …")` while Step 0's gap stands.

Add (names coined here): `TestScheduleFor_KeysDeclaredAndValid` (every key obeys R2-7 and is declared with type
`tasks.ScheduleConfig`; no key on two kinds; `ScheduleFor` returns nil for every `core.*` kind) and
`TestScheduleFor_DroppedV2CronsAbsent` (none of the 15 crons yields a schedule unless Q-C enabled it).

## Steps

1. Step 0. Then the base checks; STOP on a mismatch or a missing Q-C answer.
2. Write the two spec tests and the two coined tests; watch them fail.
3. `schedules.go`, entry by entry in `cadence.golden` order; run the parity test per entry.
4. PR description: the 38-row table (task → kind → key → default → window order), the 15 dropped crons (or the
   ones Q-C enabled), the SPEC-GATED entry, and a disposition count over the 38 tasks that sums to 38
   (declared on a books kind / deleted (`transcode`) / core-owned key (`batch_poller`, `purge_old_logs`, and
   `db_optimize`'s core half) / SPEC-GATED (`library_scan_full`)).

## Acceptance (commands and expected output)

```bash
go build ./... && go vet ./internal/books/                                       # exit 0
go test ./internal/books/ -run 'TestScheduleParity_AllDefinitions|TestCutover_NoCatchUpStorm|TestScheduleFor_' -count=1 -v | grep -c '^--- PASS'   # 4
go test ./internal/books/ -run 'TestScheduleParity_AllDefinitions' -count=1 -v | grep -c 'SKIP.*SPEC-GATED'   # 1 while Step 0's gap stands, else 0
go test ./internal/scheduler/ -count=1 -short                                    # ok (HEAD's scheduler untouched)
git diff --name-only origin/main | grep -c '^internal/scheduler/\|^core/\|^main.go'   # 0
make ci                                                                          # exit 0
```

## STOP conditions

- Step 0's gap (merge blocked until the spec names the full sweep's declaration).
- No recorded Q-C answer.
- A HEAD config input of a task has no rename-table target in 05-config (the key's default would not reach the
  operator's value after M2).
- A parity difference that the declaration API cannot express (report the task and the sequence diff).

## Out of scope

Attaching the declarations (F5-18 passes `ScheduleFor`); deleting `internal/scheduler` (F5-18); clock import (M4,
F5-16); core owners' keys (F5-03, F2's minimal owner registrations).

## Idempotency / rollback

If `internal/books/schedules.go` exists and the four tests pass, run the acceptance instead. Rollback = revert;
nothing reads `ScheduleFor` before F5-18.

## Repo rules

- Never edit `main` or the primary checkout; one worktree per task.
- File headers on every created file; `changelog.d/` fragment without a header.
- Conventional commit `feat(books): schedule declarations for the 38 scheduler tasks, with the schedule oracle (dark)`,
  body ending `Co-Authored-By: Claude <noreply@anthropic.com>`; 09 names no release or flip trailer.
- `gh pr merge <n> --rebase`, never squash, never `--admin` past a required check.
- Report with exact counts and the COMPLETED / REMAINING / BLOCKED lines.

## Planner notes

- **SPEC-GATED entry:** `library_scan` and `library_scan_full` both fire `library.scan`; the spec has one
  schedule and one clock per kind and names a `library_scan_full` key without a kind for it. Same gap in F5-16.
- **How the declarations reach the kinds.** `Common.Schedule` is set at construction and `tasks.Definition` is
  opaque, so a declaration "on its kind" made in `internal/books` needs a hook in every conversion. The conversion
  briefs (F5-01, F5-04, F5-05, F5-06 … F5-13) add `sched func(tasks.Kind) *tasks.Schedule` to their list functions
  (their rule R9, a plan rule) so this PR touches only `internal/books`.
- **Key declarations beyond the 16 `scheduled.<entry>` keys** may need an edit to books' config section file
  (created by F3), which this row does not list; the lead should add that file to the row or confirm the section
  can be extended from `schedules.go`.
- 05-download §10 says `deluge.protected-paths-sync` "gets a real schedule"; 05-tasks §5T.3.8 and 14 Q-C default to
  deleting the 15 crons. The owner's Q-C answer decides; the brief applies it.
- `purge_old_logs`' HEAD default is 168 h while `core.log.schedules.journal_prune` defaults to 24 h (05-logging §5):
  the parity test will show the difference unless M2's rename table carries HEAD's value into the core key; if it
  does not, report it as a spec gap rather than changing either default.
