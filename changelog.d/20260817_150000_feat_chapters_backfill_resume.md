### Changed

- **`maintenance.chapters-backfill` now resumes where it stopped.** It carries the
  longest timeout in the codebase (24 hours) over a whole-library enumeration, and
  ran under `ResumePolicy=Drop` — so a restart mid-run threw away up to a day of
  ffprobe work and began again at book zero.

  It converts because of two properties it already had: each item probes and
  persists entirely within itself, accumulating nothing a later phase consumes,
  and it is explicitly idempotent — a book that already has stored chapters
  short-circuits on a single key lookup. `maintenance.duration-backfill` has
  neither (its first phase builds an in-memory fix list for its second phase to
  apply), so that op deliberately stays on `Drop`.

  Three details that are easy to get wrong, each pinned by a test that was
  verified to fail without it:

  - **The checkpoint carries every parameter, not just the position.** Checkpoint
    state is *merged* into the resumed run's parameters, so any omitted field
    returns as its zero value — dropping `apply` would silently downgrade a live
    run to a dry run, and dropping `bookIds` would widen a bounded cohort run to
    the whole library. Both failures look exactly like success.
  - **`limit` now caps the operation, not the attempt.** A twice-restarted run
    with `limit=100` could otherwise write 300 books while every individual
    attempt honoured its cap.
  - **Book IDs are sorted before the run.** A watermark counts positions, so it
    means nothing without a stable order. Both stores enumerate in ID order
    today, but resuming must not depend on two implementations continuing to
    agree — that class of silent divergence is what #2399 was.
