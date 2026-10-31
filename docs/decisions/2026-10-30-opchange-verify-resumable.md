<!-- file: docs/decisions/2026-10-30-opchange-verify-resumable.md -->
<!-- version: 1.0.0 -->
<!-- guid: c2726287-669c-4366-bd24-957e0a05e5aa -->
<!-- last-edited: 2026-10-30 -->

# opchange_by_book ensure: a resumable, incremental verify (04-P7)

## Question

`maintenance.opchange-book-index-rebuild` with `{"ensure": true}` is enqueued on
every boot (60 minute timeout). Its backfill and rebuild are resumable (a cursor
committed with each chunk), but its verify re-walked the whole operation-change
journal on every boot and had to finish inside one run before the index was
trusted. The journal is never pruned by log retention (UNDO-WINDOW-PRUNE), so the
verify only grows. Past the timeout every boot ends `timed_out`, the index is
never trusted, and `GetBookChanges` stays on the full scan (correct, slow).
TODO options: a longer timeout, a resumable verify, or pruning.

## Decision log

- **Chosen: the verify is resumable and incremental; the timeout is unchanged.**
  Code: `internal/database/pebble_store_opchange_verify.go`,
  `EnsureOpChangeByBookIndexWith`, `internal/plugins/maintenance/opchange_book_index.go`.
- **Rejected: a longer timeout.** A run's size grows with the journal, so any cap
  is eventually too small; it only moves the cliff (same reasoning as
  `2026-10-30-boot-ops-resumable-checkpoint.md`).
- **Rejected: journal pruning as the fix.** UNDO-WINDOW-PRUNE is an owner
  decision about the undo window, not a performance knob, and a pruned journal
  would still need a verify that can survive its own timeout.
- **State.** Two keys under `system:backfill:`, separate as asked:
  `opchange_by_book_verify_v1_cursor` (JSON `{cursor, rows, started}`: every row
  at or before the primary key `cursor` was checked clean) and
  `opchange_by_book_verify_v1_done` (a pass has reached the end of the journal
  since the last reset). The in-memory trust is still per boot and is set only
  when THIS boot's pass reached the end of the journal. A cursor lets a boot skip
  rows an earlier boot checked; it never lets it trust rows nobody looked at.
- **The cursor lives in the store, not in the registry checkpoint blob.** The
  next boot's NEW operation must find it, and `Reporter.Checkpoint` state is
  merged back only into the same operation row. A key-ordered Pebble iterator
  also needs no sorted collection, which is the reason `walkresume` collects and
  sorts file paths first.
- **Yield.** After a durable cursor, when less than `max(5 min, 2 x the slowest
  gap between checkpoints)` of the deadline remains and this attempt has
  checkpointed at least once, the verify stops and the op returns
  `registry.ErrContinueInPlace`; the registry re-queues the SAME operation with a
  fresh timeout. Checkpoints are every `opChangeVerifyProgressRows` (10,000) rows,
  one Sync write each. The yield is the rule `backfill-file-hashes` and the 04-P6
  ops use (#4308), so the two PRs agree.
- **ResumePolicy changes from ResumeDrop to ResumeRestart.** The registry honours
  `ErrContinueInPlace` only for `ResumeRestart` (`continuesInPlace`). Every mode
  is idempotent and keeps its resume point in the store. Consequence: a forced
  rebuild (`dry_run=false`) interrupted by a restart now resumes, and a rebuild
  always starts from the first row, so it redoes its work; a boot ensure resumes
  the cursor it left. The activity reconcile is already `ResumeRestart`.
  **Merge note:** #4308 pins this def at `ResumeDrop` in
  `TestBootOpDefs_WaitForWarmupAndKeepTheirPolicies` and says "the opchange
  rebuild stays ResumeDrop" in the `bootOps` comment in `server_lifecycle.go`;
  both lines change here and conflict on rebase.
- **Determinate progress.** The registry counts progress toward a yield only when
  it has a total (`processedItems.observe` ignores `current > 0, total <= 0`),
  and the op used to report `(rows, 0)`. The verify now sizes its pass with a
  key-only count of the rows ahead of the cursor (no decode, no point read) and
  reports `(position, total)`, position absolute across runs. The count costs a
  small fraction of the verify and is repeated on each resumed attempt.
- **Is the journal append-only? Not in key order, so the tail verify is bounded,
  not exact.** Confirmed from the writers: this binary only adds a row, rewrites
  one in place with its entry, or removes one with its entry, so a row the cursor
  has passed stays checked. But a key is `opchange:<opID>:<changeID>`, and an
  opID is a ULID, or a ULID behind a fixed prefix
  (`opswriter.BookHistory.NewBatchID`). ULIDs sort by time only within a prefix,
  so a row written while the binary was down (the rollback the verify exists to
  catch) with a smaller prefix sorts BEFORE the cursor and the tail verify does
  not read it. Pinned by `TestOpchangeVerify_BelowCursorRowIsFoundByTheFullWalk`.
  Mitigation chosen: a pass whose from-the-first-row walk began more than 7 days
  ago (`opChangeVerifyFullEvery`) is dropped and verified from the first row
  again, so a stray row waits at most a week, at the cost of one full (resumable)
  verify a week during which trust waits for the pass to finish. Larger
  alternatives not taken, with their cost, for the owner:
  1. verify the weekly full pass AFTER trusting the tail, as a second lane with
     its own cursor (no trust delay on that boot; about 80 more lines and a
     second state record);
  2. per-opID-prefix cursors (the exposure becomes clock regress only; needs the
     prefix set persisted and a seek-based walk);
  3. a persisted journal row count checked by a key-only scan (exact, but every
     journal writer would have to maintain the counter in its batch).
- **Early stop on a dirty verify.** The first missing entry or unmarked
  undecodable row stops the walk at its next 10,000-row window (the rebuild
  re-indexes every row and resets the state, so finishing the walk only delays
  it). The ERROR line's counts are therefore a lower bound; `SampleMissing` holds
  what the window found.
- **A rebuild resets both keys when it starts, and a finished one records its
  own coverage.** It visited every row after clearing trust, which is what a
  verify pass establishes, so it writes the last row it read as the cursor and the
  verified sentinel in its closing batch (only if the store was not reset since).
  A cut rebuild leaves neither key; the next ensure resumes the backfill and
  verifies from the first row. Without this a rebuild would cost one extra full
  verify on the next boot.
- **A backfill that ran in an ensure drops any verify state first.** Verify
  state is only valid while the backfill sentinel is set. A `Reset` wipes the
  keyspace, but a verify racing it could still write a cursor into the new one;
  a cursor writer also refuses when the store generation changed, and the next
  ensure's backfill (the sentinel is gone) clears whatever slipped through.
- **Undecodable rows.** A tail verify reads only the rows after the cursor, so
  the report's `Undecodable` on a complete pass is counted from the
  `opchange_undecodable:` markers whose row is still present and still
  undecodable. A row that is repaired or removed stops counting, so the op does
  not stay red on it.
- **`walkresume` is not vendored.** The shared package from #4308 is shaped for
  sorted file-path collections run through `registry.RunItems`, with the cursor
  in the registry checkpoint. This verify is a single sequential key-ordered
  Pebble walk with its cursor in the store, so only the API idea is shared (a
  cursor that is a value, a yield after a durable checkpoint, `ErrContinueInPlace`).
  Once #4308 merges, the yield test (`opChangeVerifyYieldDue`) is the piece to
  move onto a shared helper if one is extracted.
- **Concurrency.** Left sequential, as the backfill is: the work is Pebble reads
  in one instance and a resumable cursor needs rows in key order. A verify that
  shards the key range would need one cursor per shard.

## Not done

- The backfill and rebuild do not yield (they were already resumable across
  runs; a timed-out one resumes at the next boot). Only the verify gained a
  yield.
- Nothing prunes the journal; UNDO-WINDOW-PRUNE stays as filed.
