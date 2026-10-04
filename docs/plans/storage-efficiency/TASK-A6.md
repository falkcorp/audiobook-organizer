<!-- file: docs/plans/storage-efficiency/TASK-A6.md -->
<!-- version: 1.1.0 -->
<!-- guid: e6e114c8-759a-4c70-ba14-a4a5628796af -->
<!-- last-edited: 2026-10-03 -->

# TASK-A6: Progress-log throttle

Wave W1, in parallel with A1, A2, A4 and A5. Model: sonnet. Reviewer:
code-reviewer.

## 1. Goal and why

**Goal.** Stop writing one operation log row per progress update. Emit a
progress log line only when one of these holds:

- the message's shape changes (digits normalised);
- 30 seconds have passed since the last progress line;
- the operation reaches its end.

`UpdateOpProgressV2`, the bus event and the Prometheus progress gauges keep
firing on every update, exactly as today.

**Why.** `dbReporter.UpdateProgress` logs one line per distinct message
(`reporter_db.go:388-397`). Messages carry counters, such as
`Books 3/76994 (scanned 4, ...)` and `Stale series objects 6/15942`, so
every update is distinct and the duplicate filter never fires. Measured
(eval R6):

| Op | Log rows |
|---|---|
| one `library.scan` | about 300,935 |
| one full `metafetch.asin-backfill` | about 76,996 |
| one 8-second `repairs.plan` | about 15,894 |

That is about 0.6M `opv2:log:` rows a day, each written with `pebble.Sync`,
and kept forever (F3). The watchdog reads progress touches, not log rows
(`reporter_db.go:355-360`), so liveness is unaffected.

## 2. ⛔ START HERE (run this first)

```bash
cd /Users/jdfalk/repos/github.com/jdfalk/audiobook-organizer
git fetch origin main
git worktree add ../aorg-storage-a6-progress-log-throttle -b perf/storage-a6-progress-log-throttle origin/main
cd ../aorg-storage-a6-progress-log-throttle
npm ci --prefix web
```

- Do NOT run `go work init`.
- Do NOT spawn subagents.
- Never edit the primary checkout.
- Commit work in progress every 15 minutes, and push it to your own branch.
- Next, run the "already done if" check in `## Idempotency / Rollback` below.
  If it says the work exists, stop and report instead of redoing it.

## Idempotency / Rollback

Already done if this holds (worktree root):

```bash
grep -c progressShape internal/operations/registry/reporter_db.go   # prints 1 or more when done
```

On `origin/main` at `373ba19d2` it prints 0, so the task is not done. If it
prints 1 or more, stop and report "already done" (or, if the tests in section 7
are missing, finish those).

Rollback: before the first push, remove the worktree and branch, or `git reset
--hard origin/main` inside it. After the PR merges, `git revert` it. The task
is code only: it changes no stored data, and reverting restores one log row per
distinct progress message.

## 3. Read before editing

Line numbers in this section come from `origin/main` at `373ba19d2`. Section 4
re-greps each one; where a number differs, use the grep result.

- `internal/operations/registry/reporter_db.go`:
  - `:44-96`: the `dbReporter` struct, especially `progressMu`,
    `lastProgressMessage`, `runCtx`;
  - `:141-170`: the test constructors;
  - `:225-307`: `flushLoop`. Its `case <-ctx.Done():` branch at `:276` is
    the terminal flush on normal completion;
  - `:352-400`: `UpdateProgress`;
  - `:476-494`: `markTerminal`. It runs only when a run is ABANDONED, after
    which `Log()` drops everything. It is not the normal completion path.
- `internal/operations/registry/worker.go:258-270`. The deferred join calls
  `awaitFlush` (called at `:267`, defined at `reporter_db.go:242`), which waits
  for the flush loop after the run, so lines staged in the terminal branch are
  persisted before the store closes.
- `internal/operations/registry/reporter_db_test.go:1-66` and `:291-320`.
  The test harness: `newTestReporter`, `fakeStore.logsFor` (defined in
  `teststore_test.go:561`), and the cancel-then-poll pattern.
- `internal/operations/registry/export_test.go`. The package-internal test
  hooks. You add one.

## 4. Re-verify anchors

1. `grep -n 'if message != "" && message != last' internal/operations/registry/reporter_db.go`
   → `391:`. This is the filter the design cites (`reporter_db.go:391`).
   Holds.
2. `grep -n 'func (r \*dbReporter) UpdateProgress\|func (r \*dbReporter) flushLoop\|func (r \*dbReporter) markTerminal' internal/operations/registry/reporter_db.go`
   → `354`, `260`, `480`.
3. `grep -n 'case <-ctx.Done():' internal/operations/registry/reporter_db.go` → `276:`
4. `grep -n 'lastProgressMessage string' internal/operations/registry/reporter_db.go` → `82:`
5. `grep -n 'func NewDBReporterForTest(' internal/operations/registry/reporter_db.go` → `141:`
6. `grep -n 'func (f \*fakeStore) logsFor\|func newFakeStore' internal/operations/registry/*_test.go`
   → `teststore_test.go:82`, `teststore_test.go:561`.
7. `grep -n 'func newTestReporter\|func TestReporterDB_UpdateProgressWritesColumns\|func TestReporterDB_LogFlushOnContextCancel' internal/operations/registry/reporter_db_test.go`
   → `23`, `50`, `291` (section 3 and section 7 cite them; `newTestReporter`
   builds the reporter through `NewDBReporterForTest`, which starts the flush
   loop in synchronous mode).
8. `grep -n 'func (f \*fakeStore) progressOf' internal/operations/registry/teststore_test.go`
   → `587:`. This is the exact getter the progress-column test uses:
   `cur, total, msg := store.progressOf(opID)`.
9. `grep -n 'func (r \*dbReporter) flushProgressLazy\|func (h \*fanoutHandler) Handle\|func (r \*dbReporter) awaitFlush\|func (r \*dbReporter) Log(' internal/operations/registry/reporter_db.go`
   → `292`, `110`, `242`, `409`. Step 3 says `flushProgressLazy` reads
   `lastProgressMessage` (line `300`); step 4 reads `Log` and
   `fanoutHandler.Handle`.
10. `grep -n 'awaitFlush' internal/operations/registry/worker.go` → `267:`
11. `grep -n 'terminated' internal/operations/registry/reporter_db.go`
    → `73` (comment), `77` (the field), `413` (the only read, in `Log`),
    `481` (the only write, in `markTerminal`). Step 4's rule depends on this.
12. `grep -c SetReporterClockForTest internal/operations/registry/export_test.go`
    → `0` (the helper does not exist yet; step 5 adds it; `1` means step 5 is
    already done).

## 5. Steps

1. **New fields on `dbReporter`**, guarded by `progressMu`:
   - `lastLoggedShape string`;
   - `lastLoggedMessage string`;
   - `lastLoggedAt time.Time`;
   - `pendingLog *pendingProgressLine`, where
     `type pendingProgressLine struct { message string; current, total int }`;
   - `nowFn func() time.Time`. `newDBReporter` sets it to `time.Now`.
2. **`func progressShape(msg string) string`.** Replace every maximal run of
   ASCII digits `0-9` with one `#`. For example `"Books 3/76994 (scanned 4, skipped 12)"`
   → `"Books #/# (scanned #, skipped #)"`. Digits only: no sign, no decimal
   point (`"1.5 GB"` → `"#.# GB"`).
3. **`UpdateProgress`.** Replace the block at `:388-397`. Under `progressMu`,
   compute the decision, then log outside the lock:
   - `message == ""`: no line, no pending.
   - `message == r.lastLoggedMessage`: no line, clear `pendingLog`. This
     keeps today's rule that an identical message is never logged twice in a
     row.
   - Otherwise, emit when ANY of these holds, then set `lastLoggedShape`,
     `lastLoggedMessage` and `lastLoggedAt = now`, and clear `pendingLog`:
     - `r.lastLoggedAt.IsZero()` (nothing logged yet);
     - `progressShape(message) != r.lastLoggedShape`;
     - `now.Sub(r.lastLoggedAt) >= progressLogInterval`.
   - Otherwise set `pendingLog = &pendingProgressLine{message, current, total}`.

   Add `const progressLogInterval = 30 * time.Second` with a doc comment
   citing the R6 numbers.

   The emitted line is the same `r.logger.LogAttrs(...)` call as today, with
   the same message, level and attrs (`phase=progress`, `progress_current`,
   `progress_total`).

   Leave untouched: the `touchProgressFn` call, the `progressCurrent`,
   `progressTotal` and `lastProgressMessage` updates, `progressGen`,
   `metrics.SetOpProgress`, the synchronous `UpdateOpProgressV2`, and the bus
   publish. Keep `lastProgressMessage`: `flushProgressLazy` (`:292`; it reads the
   field at `:300`, anchor 9) uses it for the DB progress column.
4. **Terminal line.** Add `func (r *dbReporter) emitPendingProgressLine()`.
   Under `progressMu`, take `pendingLog` and nil it. Outside the lock, if it
   was non-nil, log it with the same `LogAttrs` call. In `flushLoop`'s
   `case <-ctx.Done():` branch, call it FIRST, before `r.flushLogs()`, so the
   line is in the buffer that branch flushes. Do not call it from
   `markTerminal`: after abandonment `Log()` drops lines on purpose (R-3,
   `:410-415`).

   **The `terminated` rule (concrete check).** `runCtx` is already cancelled
   when the terminal branch runs, so the emitted line must not be dropped on
   that account. Run anchor 11. The line is persisted only if both of these
   hold: (a) `terminated` is read in exactly one place, the early return at
   `reporter_db.go:413` in `Log`, and written in exactly one place,
   `markTerminal` (`:481`), which the normal completion path (the
   `case <-ctx.Done():` branch) never calls; (b) neither `Log` nor
   `fanoutHandler.Handle` (`:110-130`) tests `runCtx.Err()` or `ctx.Err()`
   (`grep -n 'ctx.Err()' internal/operations/registry/reporter_db.go` must show
   no hit inside those two functions). If either fails, stop and report
   it: do not work around it.
5. **Test hook** in `export_test.go`:
   ```go
   // SetReporterClockForTest replaces a DB reporter's clock. Call before the first UpdateProgress.
   func SetReporterClockForTest(rep Reporter, now func() time.Time) { rep.(*dbReporter).progressMu.Lock(); rep.(*dbReporter).nowFn = now; rep.(*dbReporter).progressMu.Unlock() }
   ```
   Read `nowFn` under `progressMu` in `UpdateProgress`.
6. Update the comment above the old block (`:388-390`). It currently says
   "Skipping duplicates keeps a 50K-row scan from producing 50K log lines",
   which was not true. State the new rule and the R6 figures.

## 6. Do not touch

- `UpdateOpProgressV2`, `flushProgressLazy`, `metrics.SetOpProgress`, the
  bus publish, `touchProgressFn`, the watchdog.
- `Log()` and `flushLogs()`. Ordinary `Log` calls are never throttled.
- `internal/database/` (A1, A2, A4, A5), `internal/server/` (A1, A2, A5),
  `internal/metrics/` (A1).
- `database.Store`, `iface_*.go`, `mocks/`.

## 7. Tests

New file `internal/operations/registry/reporter_progress_throttle_test.go`
(package `registry_test`). Count only rows whose `Attrs` JSON contains
`"phase":"progress"`. Wait for flushes with the poll pattern in
`TestReporterDB_LogFlushOnContextCancel` (`reporter_db_test.go:291`): poll
`store.logsFor(opID)` until a deadline. Use a 2 s deadline, not a sleep.

- `TestProgressThrottle_CounterStreamOneLinePlusTerminal`. Fix the clock at
  t0. Make 100,000 calls `UpdateProgress(i, 76994, fmt.Sprintf("Books %d/76994", i))`
  for i = 1..100,000. Then cancel the context. Assert exactly 2 progress
  rows: the first is `"Books 1/76994"` and the second (terminal) is
  `"Books 100000/76994"`.
- `TestProgressThrottle_SlowStreamOneLinePer30s`. The clock is
  t0 + (i−1) s for call i. Make 95 calls `"Books i/95"`, i = 1..95, then
  cancel. Assert exactly 5 rows, in order: `"Books 1/95"` (t = 0 s),
  `"Books 31/95"` (30 s), `"Books 61/95"` (60 s), `"Books 91/95"` (90 s), and
  the terminal `"Books 95/95"`.
- `TestProgressThrottle_ShapeChangeLogsImmediately`. Fixed clock. Send
  `"Books 1/10"`, `"Books 2/10"`, `"Authors 1/5"`, `"Authors 2/5"`,
  `"Done"`. Assert rows `"Books 1/10"`, `"Authors 1/5"` and `"Done"`, and no
  terminal extra, because nothing is pending after `"Done"`.
- `TestProgressThrottle_IdenticalMessageNeverRepeated`. The clock advances
  31 s between 3 identical `"Scanning"` calls. Assert 1 row.
- `TestProgressThrottle_ProgressColumnsStillEveryUpdate`. After 1,000
  throttled updates, `cur, _, _ := store.progressOf(opID)` (anchor 8; the same
  getter `TestReporterDB_UpdateProgressWritesColumns` uses) returns
  `cur == 1000`.
- `TestProgressShape`, a table test of `progressShape`. It lives in an
  internal test file `reporter_progress_shape_internal_test.go` (package
  `registry`), because the function is unexported. Cases: `""`, `"abc"`,
  `"12"`, `"Books 3/76994"`, `"1.5 GB"`, `"x9y99z"`.

- Anti-over-suppression: the throttle must still let the lines that matter
  through. Three named tests pin that:
  `TestProgressThrottle_ShapeChangeLogsImmediately` (a new phase logs at once),
  `TestProgressThrottle_CounterStreamOneLinePlusTerminal` (the terminal line is
  always written, so the final state of a throttled stream is never lost) and
  `TestProgressThrottle_ProgressColumnsStillEveryUpdate` (the DB progress
  columns still update on every call). Each fails if the throttle
  swallows its case.

Run the existing reporter tests too. `TestReporterDB_*` must still pass
unchanged. If one asserted one log line per distinct message, report it by
name, and fix it only if its assertion encodes the old flood behaviour.

## 8. Verify

```bash
go build ./...
go vet ./internal/operations/registry/...
go test -race -count=1 ./internal/operations/registry/...
go test -race -count=1 ./internal/server/ -run 'OpLog|Operation'
make lint-errcheck-ratchet
make lint-width
```

No `Store` interface changes, so `scripts/check-interface-width.sh` is not
required.

## 9. Deliverables

- Bump the version and `last-edited` on `reporter_db.go` and
  `export_test.go`. New test files get fresh Go headers
  (`uuidgen | tr A-Z a-z`).
- Fragment `changelog.d/<YYYYMMDD>_storage_a6_progress_log_throttle.md`, no
  header, `### Changed`, one `####` entry with the R6 numbers and the new
  rule.
- Check that `git diff origin/main | grep -nE "ab""k_[A-Za-z0-9]{16,}|172\.16\.[0-9]{1,3}\.[0-9]{1,3}"` prints
  nothing.
- Commit with exactly
  `perf(registry): throttle progress log lines by shape change, 30 s, or terminal`
  (`<type>(<scope>): ...` form), ending with:

  ```
  Co-Authored-By: Claude Sonnet 5.5 <noreply@anthropic.com>
  Claude-Session: https://claude.ai/code/session_017MtQ5LP2n3t3bs7AhptkKJ
  ```
- `sha=$(git rev-parse HEAD); git push origin "${sha}:refs/heads/perf/storage-a6-progress-log-throttle"`
- `gh pr create --base main --head perf/storage-a6-progress-log-throttle`.
  Do NOT merge.

## 10. Exit criteria and report

- [ ] The 100,000-update test yields exactly 2 progress rows; the slow-stream
      test yields exactly 5.
- [ ] Progress columns, bus and metrics behaviour unchanged: the existing
      tests pass, and `TestProgressThrottle_ProgressColumnsStillEveryUpdate`
      passes.
- [ ] `go test -race ./internal/operations/registry/...` passes.
- [ ] PR open, not merged.

Report:

```
TASK-A6 report
head sha: <sha>
PR: <url>
files changed: <list>
tests: <name> PASS (<time>) ...; registry package ok (<time>)
not done / deviations: <list or "none">
```
