<!-- file: changelog.d/20260804_040000_dedupe_progress_heartbeat.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6bcb2b54-a9b1-4b78-b7f5-8d0c47704606 -->
<!-- last-edited: 2026-08-04 -->

### Fixed

- `maintenance.dedupe-book-file-rows` is no longer killed mid-run by the registry's
  stuck-op watchdog. The first full production run was cancelled at **book 19 of 194**:

  ```
  registry: strike recorded  kind=stuck  message="no progress for 5m12s (timeout=5m0s)"
  registry: canceling stuck op
  ```

  The op reported progress exactly once per book, at the bottom of the loop. Per-book
  cost averages ~45s but varies widely — a book with 47 duplicate rows does far more
  work than one with 2 — so a single heavy book could exceed the watchdog's 5-minute
  window on its own. From the outside a healthy op was indistinguishable from a hung
  one, and the watchdog is right to be aggressive: it exists because of the
  "silent for hours" incident class.

  Two changes, one primary and one defensive:

  - The per-book loop now emits a **heartbeat after each duplicate group**, deliberately
    re-reporting the *current* index rather than advancing it — the book is not
    finished, so the bar must not move. The only purpose is to stamp `lastProgressAt`.
  - `ProgressTimeout` is declared at 30 minutes, matching the precedent
    `malformed-m4b-transcode` set for a slow-but-healthy per-item op.

  No data was at risk — books commit independently and the op is idempotent, so the 19
  completed books stayed correct and a re-run resumes the remainder. But at ~19 books
  per cancelled run the op could not finish its own workload, which made a
  194-book cleanup a ten-invocation chore.

  Separately, the code comment claiming this op had destroyed a duration on
  `The Trapped Mind Project` is corrected in place. That finding was retracted: the
  book's entire audio is a 13.5-second file, and a full-library dry run reported
  "would salvage fields on 0 keepers" across all 194 books. The keeper field-merge
  guard remains as defence against a real-but-unobserved hazard.
