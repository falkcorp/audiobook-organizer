### Fixed

- **Library-scan progress no longer shows a "total" that counts up instead of a
  fixed denominator.** Two mechanisms were behind the symptom, both verified
  against a live prod scan:
  - The discovery ("Discovering folders: N found") and tag-reading ("Reading
    tags: N files") phases have no bounded denominator while they run, but they
    reported `total == current` (both climbing). The UI rendered that as a
    determinate bar pinned at ~100% while a number raced upward. They now report
    an **indeterminate** total (0), which the UI already renders as an animated
    bar plus the count.
  - The per-book "Processed" phase seeded its denominator from a **stale,
    file-unit** estimate (`len(scanCache)` for incremental scans, a whole-tree
    file pre-count for full scans) and then patched it upward with `max()`. It
    now uses a **book-unit** denominator accumulated from the real `len(books)`
    discovered per folder, so the numerator (books) and denominator (books) are
    commensurate and `current <= total` by construction — no `max()` patch. The
    full-tree file pre-pass (a second walk of the whole library) is removed.
- Corrected scan log lines that labelled grouped **books** as "files" ("scan
  started: N books to process", "N/N books processed").
