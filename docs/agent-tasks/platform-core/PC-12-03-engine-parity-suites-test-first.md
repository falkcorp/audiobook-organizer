<!-- file: docs/agent-tasks/platform-core/PC-12-03-engine-parity-suites-test-first.md -->
<!-- version: 1.0.0 -->
<!-- guid: 69015bbd-8886-4275-ae05-7e7db0388ac6 -->
<!-- last-edited: 2026-11-04 -->

# PC-12-03 — Port HEAD's resume, cancel and shutdown suites to run against both engines (test first)

| Field | Value |
|---|---|
| ID | PC-12-03 |
| Tier | **T3 Opus/high** — the parity proof of the fresh executor; a case ported loosely proves nothing, and the port is a judgement over 68 HEAD test functions |
| Wave | W12 (S5, release `S5r1`, dark) |
| Depends on | PC-12-01 |
| Serialized | PC-12-01 → {**PC-12-03**, PC-12-04, PC-12-05, PC-12-07} → PC-12-02 |
| Trailer | `Stage-Release: S5 1` |
| Digest | `docs/plans/platform-core/digests/S5.md` §7 ("HEAD's resume, cancel and shutdown suites run against both engines") |
| Owning spec | 05-tasks §5T.8.2 T0+T1 "Gate to finish"; §5T.3.6 (resume, cancel, watchdog); §5T.4 steps 2 and 4; advisor review §G1 risk 7 (`docs/plans/platform-core/reviews/spec-advisor-review.md`) |

## Goal

Express HEAD's resume, cancel and shutdown behaviour as an **engine-neutral** scenario suite in core, and run it
green against the v2 engine (`internal/operations/registry`) through an ABO adapter. PC-12-02 then runs the same
suite against the core executor. This PR changes no production code: it is test-first oracle work (advisor §G1 risk 6).

## ⛔ START HERE

```bash
REPO=<your clone>
git -C "$REPO" fetch origin
git -C "$REPO" worktree add "$REPO/../audiobook-organizer-pc-12-03" -b test/pc-12-03-engine-parity origin/main
cd "$REPO/../audiobook-organizer-pc-12-03"
git rebase origin/main
make web-deps
git worktree list
```

## Exact files

Create:

- `core/tasks/taskstest/parity/` — package `parity` (path `github.com/falkcorp/mediacore/tasks/taskstest/parity`):
  `doc.go` (the mapping table, step 3), `engine.go` (the neutral `Engine` interface), `scenarios.go` (the cases),
  `run.go` (`func Run(t *testing.T, newEngine func(t *testing.T) Engine)`). All names in this package are coined here.
- `internal/operations/registry/engine_parity_test.go` — package `registry_test`; adapts the v2 engine to `parity.Engine`
  and calls `parity.Run`.

Modify:

- `pkg/ops/opstest/` — only to export a helper the adapter needs (for example a fake store constructor already used by
  `opstest`); no behaviour change to any existing helper. If the adapter needs nothing from `opstest`, leave it untouched
  and say so in the PR description.

## Re-verify greps (identical at `748fd787a` and the plan checkout)

```bash
cd internal/operations/registry
ls *resume*_test.go *cancel*_test.go *shutdown*_test.go | sort -u | wc -l          # expect 15 files
cat $(ls *resume*_test.go *cancel*_test.go *shutdown*_test.go | sort -u) | grep -c '^func Test'   # expect 68
grep -c '^func Test' resume_test.go cancel_row_cas_test.go dispatch_shutdown_race_test.go       # expect 11, 14, 7
cd -
ls pkg/ops/opstest/*.go | grep -v _test.go | wc -l                                  # expect 6
test -d core/tasks && test -d core/tasks/taskstest && echo ok                        # PC-12-01; expect ok
test ! -e core/tasks/taskstest/parity && echo ok                                      # expect ok
```

The 15 files are: `cancel_persisted_test.go`, `cancel_requeue_race_test.go`, `cancel_row_cas_test.go`,
`dispatch_shutdown_race_test.go`, `fixer_resume_real_writer_test.go`, `resume_shutdown_roundtrip_test.go`,
`resume_supersede_test.go`, `resume_test.go`, `run_items_resume_test.go`, `shutdown_cause_test.go`,
`shutdown_drain_test.go`, `shutdown_race_test.go`, `shutdown_stub_handle_test.go`,
`shutdown_terminal_status_test.go`, `sweeptick_shutdown_race_test.go`. A different list or count: STOP and report.

## Design constraints (invariants)

1. **Neutral vocabulary.** Scenarios name only `tasks` types (`tasks.RunState`, resume policies of 05-tasks §5T.3.6,
   `tasks.Parallelism`) and plain data (item counts, cancel points, checkpoint values). `parity` imports nothing from
   ABO and nothing from `core/internal/executor` (core may not import ABO; the core-executor adapter lands in PC-12-02).
2. **One `Engine` interface, minimal.** It needs, at most: register an abstract definition (kind, shape, resume
   policy, parallelism, a body built from the scenario's item script), start a run, cancel a run, read a run's view,
   wait for a state with a deadline, shut down with a budget, simulate a crash (stop without shutdown) and restart over
   the same store, and count executions of each item. Anything engine-specific stays in the adapter.
3. **State comparison through `RunState`.** The v2 adapter maps HEAD's stored state to `tasks.RunState` with 05-tasks
   §5T.6.2's "Run states" table (for example HEAD `dropped` → `interrupted_dropped`; `interrupted` with
   `InterruptedWhy` `quiesced` → `interrupted_quiesced`), never by string equality.
4. **Deterministic.** Use `testing/synctest` or the engines' fake clocks; no `time.Sleep`. Every case passes
   `go test -race -count=3`.
5. **No assertion weakening.** A case asserts what HEAD's test asserts (final state, executions per item, checkpoint
   resumed from, fence refusal, drain bound). A HEAD assertion about engine internals (a private field, a v2 row's raw
   bytes) is either restated as an observable outcome or listed as not portable (step 3).

## Steps

1. Read the 15 files and 05-tasks §5T.3.6, §5T.4 steps 2 and 4, §5T.5 rows on resume, shutdown and cancel.
2. Write `engine.go` and `run.go` per the constraints.
3. In `doc.go`, a table written as Go comment lines that each start with `// | ` (one line per HEAD test function,
   68 lines, plus one header line), with columns `HEAD file`, `HEAD test`, `parity case` (or
   `not portable`), `reason` (for `not portable`: which engine-internal fact it asserts and which other case covers its
   observable behaviour). A `not portable` row without a covering case is not allowed.
4. Write `scenarios.go`: at least one parity case per behaviour group — resume policy matrix (each policy × shutdown,
   crash, timeout, cancel), supersede on resume, resume of a `RunItems` run from its watermark/checkpoint, cancel
   persisted across restart, cancel racing re-queue, cancel CAS on the run row (exactly one winner), shutdown drain
   bounded and remaining runs stamped interrupted, shutdown cause recorded, dispatch racing shutdown (no run starts
   after shutdown begins), schedule tick racing shutdown, terminal status after shutdown, Fixer resume skipping rows
   whose marks committed.
5. Write the v2 adapter in `engine_parity_test.go` and make every parity case pass on the v2 engine **without
   changing production code**. If a case fails on the v2 engine, the case is wrong (HEAD is the oracle): fix the case.
6. Do not delete or edit the 68 HEAD tests; they keep running.
7. Acceptance, headers, `changelog.d/` fragment (no header).

## Code contract

No spec-fixed names in this package; everything is coined here and marked `// name coined in PC-12-03`. Public entry
point: `func Run(t *testing.T, newEngine func(t *testing.T) Engine)`.

## Tests to write first

This PR is the tests. Done-check (row): HEAD's resume, cancel and shutdown suites green on the v2 engine through the
engine-neutral harness; PC-12-02 makes them green on the core executor.

## Acceptance

```bash
go test -race ./internal/operations/registry/ -run 'Parity' -count=3          # expect: ok ... (no FAIL, no DATA RACE)
go test ./internal/operations/registry/ -run 'Parity' -count=1 -v | grep -c '^    --- PASS'   # expect >= number of parity cases in scenarios.go
grep -c '^// | ' core/tasks/taskstest/parity/doc.go                             # expect >= 68 table rows (plus header)
grep -c 'not portable' core/tasks/taskstest/parity/doc.go                       # report the number in the PR description
grep -rn 'audiobook-organizer\|internal/executor' core/tasks/taskstest/parity --include=*.go | wc -l   # expect 0
go test ./internal/operations/registry/... -count=1                             # expect ok: the 68 HEAD tests still pass
(cd core && go vet ./tasks/taskstest/...)                                       # exit 0
make -C core ci            # exit 0
make ci                    # exit 0
git diff --stat origin/main -- internal/operations/registry | grep -v _test.go | tail -1   # expect only test files changed
```

## Out of scope

- The core-executor adapter and making the suite green on it (PC-12-02).
- Any production code change in either engine; any change of a HEAD test.

## STOP conditions

- More than 10 of the 68 HEAD tests would be `not portable`: STOP and report the list before writing the adapter.
- A behaviour you cannot express without importing ABO or `core/internal/executor` from `parity`.

## Rollback

Revert the PR; it adds tests only.

## Repo rules

- File headers on every new file; `changelog.d/` fragment without a header.
- Commits end with `Stage-Release: S5 1` then `Co-Authored-By: Claude <noreply@anthropic.com>`; `test(tasks): ...`;
  `gh pr merge <n> --rebase` after the required checks are green and OWNER-12's row is current; never squash.
- CLAUDE.md concurrency: tests may not leak goroutines (`goleak` if the package already uses it).
