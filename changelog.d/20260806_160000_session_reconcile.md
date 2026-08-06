<!-- file: changelog.d/20260806_160000_session_reconcile.md -->
<!-- version: 1.0.0 -->
<!-- guid: b3f70d21-8c46-4e59-a1d7-2065fe83b94c -->
<!-- last-edited: 2026-08-06 -->

### Changed

- **`TODO.md` reconciled against what shipped on 2026-08-06**, and an executive
  summary added for the review-queue work.

  Three entries are now closed: owner items 1+2 (recommendations + override,
  #2163), the `dedupe-book-file-rows` performance item (#2161), and the
  directory-shaped-books residue, which now records the measured dry-run result
  (434 of 1,019 linkable).

  The `dedupe-book-file-rows` entry keeps its original analysis but leads with a
  correction, because nearly every premise in it was wrong: the op was dying on
  the 5-minute progress watchdog rather than its 2-hour timeout, the real total
  was ~1.3 h rather than 2.4 h, and the cost is per-deleted-row rather than
  per-book. Leaving the original text without that correction would have taught
  the next reader three false things.

  Six new findings were also recorded as `todo.d/` fragments — the residual
  react-router advisory and why it is accepted, two frontend navigation sinks
  that are safe only by accident, the broken e2e suite, two memdb write-path
  follow-ups, the three multidisc holds that turned out to be duplicates, and a
  survey of how far behind the frontend framework versions are.
