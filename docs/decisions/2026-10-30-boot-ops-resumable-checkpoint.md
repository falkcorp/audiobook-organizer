<!-- file: docs/decisions/2026-10-30-boot-ops-resumable-checkpoint.md -->
<!-- version: 1.0.0 -->
<!-- guid: b4e792d3-9623-43ed-9986-fccdf51d80aa -->
<!-- last-edited: 2026-10-30 -->

# Boot-enqueued library walks: resumable checkpoint, not longer timeouts (04-P6)

## Question

The boot-enqueued ops `maintenance.external-id-backfill` (30 min),
`movement-atom-cleanup` (60 min), `malformed-m4b-remux` (120 min) and
`malformed-m4b-transcode` (6 h) are full-library walks. On a fresh install, or
any library whose done flags are unset, a large first run can exceed its timeout.
A timeout is terminal (`timed_out`): the done flag stays unset, the retry and
the next boot's enqueue get a new operation id with no saved state, and the walk
starts again from zero. A library too large for the timeout never finishes.
Options in the TODO entry: longer timeouts, a resumable checkpoint
(`ResumeRestart` over a cursor), or a lower per-plugin concurrency cap.

## Decision log

- **Chosen: resumable checkpoint, timeouts unchanged.** Each walk keeps a
  cursor and, for the three `ResumeRestart` ops, also yields before its
  deadline.
- **Rejected: longer timeouts.** A timeout bounds a run that is stuck, and a
  run's size grows with the library, so any fixed cap is eventually too small;
  raising it only moves the cliff and lets a stuck run hold a registry worker
  longer. The timeouts stay at 30 min / 60 min / 120 min / 6 h and
  `TestBootOpDefs_WaitForWarmupAndKeepTheirPolicies` now pins them.
- **Rejected: a lower per-plugin concurrency cap.** It does not shorten a run, it
  only lets it hold fewer workers; the walk still times out.
- **A checkpoint alone is not enough, so the ops also yield.**
  `reporter.Checkpoint` state is keyed by operation id and the registry merges
  it back only when the same row is re-queued (`resumeRestart` at boot, an
  operator's Retry, or `ErrContinueInPlace`). A run that hits its Timeout ends
  `timed_out` and nothing re-queues it, which is exactly the case the TODO is
  about. So the three `ResumeRestart` ops follow the `backfill-file-hashes`
  pattern (`yieldDue`): after a durable checkpoint, when less than
  `max(5 min, 2 x the longest gap between checkpoints)` of the deadline remains,
  the walk stops and the plugin returns `registry.ErrContinueInPlace`. The
  registry re-queues the SAME operation with a fresh Timeout and the checkpoint
  merged into its params; it honours the yield only if the attempt reported
  progress past where it started, which the walks do (absolute position).
- **Checkpoint format: the registry's own.** No second format. The checkpoint is
  the op's params with `resume` (a `walkresume.State`) updated, written with
  `Reporter.Checkpoint`; the mode flags (`if_needed`, `quarantine_after`) are in
  it because the registry's merge overlays the checkpoint on params. The fan-out
  is `registry.RunItems` with `Concurrency` set and `CheckpointStateFn`, whose
  contiguous-completion watermark is what makes a checkpoint safe under
  out-of-order completion. The walks live in layer-3 packages that may not import
  the layer-4 registry, so the new leaf package `internal/walkresume` is the
  seam: the walk is written against `State`/`Run`/`Executor`, and the plugin
  supplies a `Run` whose executor is `RunItems`.
- **Cursor = a value, never a position.** The file walks (movement, remux,
  transcode) collect their candidates first and sort them, then resume at the
  first path after the cursor (`sort.SearchStrings`), so files added or removed
  between attempts cannot shift the resume point. Collecting first replaces the
  old pre-count walk, so it costs no extra pass. A cursor over a live
  `filepath.WalkDir` was rejected: its order is not the order of the path strings
  (`a/b` is visited before `a-c`, but `"a-c" < "a/b"`). The tests use a
  `Batman` / `Batman-Begins` layout for this.
- **External-id: two phases.** The book pass is sorted by book ID and checkpoints
  the last ID of each 10,000-book chunk after its write; the iTunes XML pass is a
  stream with no value to key on, so it checkpoints a track index, taken only at
  an album boundary with the batch flushed (so a resume starts on a clean album).
  The index is a stream position, so an XML re-exported between attempts can
  shift it; every write is ignore-existing, so the worst case is a few albums not
  registered by this one-time catch-up. A resume in the XML phase skips the book
  pass. Both stay sequential: per-book work is a map lookup and the only store
  calls are the bulk writes.
- **Counters that gate the done flag are in the checkpoint.** The remux and
  transcode passes withhold the flag when a protected file was skipped. A skip
  below the cursor is not seen again after a resume, so `skipped_protected` is
  carried; without it the resumed attempt would mark the pass done and the file
  would never be considered. Pinned by
  `TestMalformedPasses_ProtectedSkipBeforeCursorStillWithholdsDoneFlag`.
- **Concurrency.** The per-file passes did sequential taglib reads and ffmpeg
  runs. They now run on a fixed small pool: 4 for movement and remux, 2 for the
  transcode (a full AAC re-encode; ffmpeg is itself multi-threaded and these run
  at PriorityLow beside user work), not `NumCPU`. `RunItems` runs in
  `ErrModeCollect`: on a yield every not-yet-started item returns at once, and in
  `ErrModeFail` the first of those would cancel the items in flight (an ffmpeg
  killed mid-file). Items past the watermark when a run stops (up to the
  checkpoint interval plus the pool size) are redone on resume; every pass is
  idempotent (a rewritten file reads clean the second time).
- **Transcode stays `ResumeAsk`; this departs from the brief.** The brief asked
  for all four to be `ResumeRestart`. The transcode re-encodes audio in place and
  is `ResumeAsk` by the code's own "Hard rule" comment, by the 2026-10-15 owner
  decisions that `enqueueBootOps` encodes (`skipWhileAsk`, `skipWhenAllSet`), and by
  a test. Changing a destructive op's policy is the owner's call. It checkpoints
  too: an operator's Retry of an interrupted run goes through the same
  `requeueInPlace` and continues from the cursor. It does not yield (the registry
  honours a yield only for `ResumeRestart`), so a transcode that hits its 6 h
  Timeout still ends `timed_out` and the next boot starts it from zero. Moving it
  to `ResumeRestart` would make it finish on any library, at the cost of
  unattended re-encoding after a restart; that is the open question for the
  owner.
- **Watchdog.** `MinCheckpointInterval` is left unset on all four, so the
  "uncheckpointed" strike (a `ResumeRestart` op silent for 5 min) is not armed.
  The checkpoint cadence is every 10 completed items, which on a library of clean
  files is seconds, but a run of large ffmpeg jobs can exceed 5 min between
  checkpoints, and that would be a spurious strike.

## Not done

- `maintenance.book-atpath-index-backfill` (part 2 of the TODO entry) is a
  ResumeDrop book scan with a sentinel and was not in this task's list of four.
  It has the same shape and the same fix (sorted book-ID cursor plus a yield).
