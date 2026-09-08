### Fixed

- **The nightly AcoustID fingerprint backfill was running on one core.** It used
  the project's worker-pool helper but never asked for any workers, and the
  helper's default is one — so a whole-library job that spawns `fpcalc` per file
  processed books strictly one at a time. It now runs a real pool, sized by the
  same `FP_PARALLEL_WORKERS` setting the fingerprint rescan uses, so there is one
  dial for fpcalc pressure rather than two that disagree.

  Two things had to be fixed before the pool was safe to turn on. Its
  fingerprinted/skipped/failed counters were plain integers written by every
  worker and also read by the progress-label callback, which the helper invokes
  inside each worker — a lost update that silently undercounts. Measured with the
  pool enabled and the counters left unguarded: **627 of 720 file outcomes
  recorded, 93 lost.** They are atomic now, and the run reports exactly 720.

  And its resume point was "the last book that finished", which is not a resume
  point once workers finish out of order — the newest finished book can sit above
  books still running, so resuming after it would skip them permanently. The
  checkpoint now stores the contiguous-completion watermark (every book below it
  is provably done regardless of finishing order) together with the ID of the
  book at that position. On resume the ID is checked against the collection: the
  book list is ID-ordered, so a single import that sorts earlier shifts every
  later position down one, and a bare index would step over exactly one book that
  nothing would ever revisit. A mismatch restarts from the beginning, which costs
  time on an idempotent job and cannot lose work.

  Checkpoints written by the previous version are still honoured, so an upgrade
  resumes where it was instead of restarting a nearly-complete pass.

### Changed

- `CLAUDE.md`'s mandatory concurrency section no longer points new maintenance
  and backfill ops at that same sequential loop as the pattern to copy. It was a
  documentation error that manufactured code bugs: the helper it names is not
  parallel unless a concurrency value is passed, and the exemplar omitted it. The
  section now names an op that really is parallel, and states the two traps —
  the helper defaults to sequential, and its label callback runs inside each
  worker, so anything it reads needs the same guarding as the work itself.
