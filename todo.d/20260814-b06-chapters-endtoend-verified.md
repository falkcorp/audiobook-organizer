## B06 chapters end-to-end: VERIFIED on prod — E02 backfill only needs the residue

Measured against the LIVE ABS surface on production 2026-08-14 (ABS API is
enabled and serving at root: `/ping`, `/api/libraries`, `/api/items/:id`;
library `b5e3a5b2…`, 34,513 items):

- **Multi-file synthesis works.** 28-file book (Mutineer): 28 synthesized
  chapters, offsets contiguous, `last end == media.duration` exactly
  (103,747s), titles from embedded track titles.
- **Stored chapters ARE being served — and they are widespread.** 21 of 29
  sampled single-file items return >1 chapter (37, 85, 105 …), which for a
  single file can only come from the chapters store. Timeline sanity-checked
  on one (37 chapters, contiguous, last end 28,885.1 vs duration 28,885).
  The "backfill has NOT run library-wide so most books have no stored
  chapters" premise is stale — scan-time extraction has already covered
  ~72% of the sampled single-file population.
- **Graceful absence works.** Single-file item with no stored chapters
  serves exactly one whole-book chapter (0 → duration).

**E02 implication:** the chapters-backfill run is a RESIDUE job (~28% of
single-file books in the sample), not a whole-library one. Run it dry-run
first to size the real target set; the serving path needs no changes.
