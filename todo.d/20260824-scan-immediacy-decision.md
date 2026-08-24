- [ ] **Decide how a forced per-book rescan gets picked up immediately.**
      `POST /audiobooks/:id/force-rescan` (#2856) sets `NeedsRescan`, which is
      precise — one book, not the 1,458 files in `newbooks/audiobooks` — but it
      defers to the next scan tick, up to 6 hours away. The obvious fix, giving
      folder-scoped scans their own `ConcurrencyKey`, is **unsafe**: dispatcher
      Gate 3b records the 2026-08-07 incident where two ops doing whole-row
      read-modify-write on the same rows silently lost fields, and a full scan
      walks every import path so the scopes overlap in the normal case. Gate 3b
      cannot be narrowed either — `Writes []Resource` is a static field on the
      OperationDef, so no invocation can declare "I touch only book X".
      Three options, written up in
      `docs/superpowers/specs/2026-08-24-staged-library-scan-design.md`:
      accept the delay; build a bounded single-book re-read path outside
      `library.scan` (needs a new serialization mechanism); or make the full
      scan short enough that queueing behind it is fine — the staged pipeline,
      which is the root fix.

- [ ] **Add a per-book last-scanned timestamp before building the 6-day age gate.**
      There is none today: `ScanCacheEntry` carries only mtime/size/`NeedsRescan`,
      the book row carries `LastScanMtime`/`LastScanSize`/`NeedsRescan`, and
      `LastScan *time.Time` belongs to `ImportPath`. Two consequences to decide
      deliberately: a new field makes every existing row read "never scanned", so
      the first tick after deploy re-reads the whole library; and the timestamp
      must be written **unconditionally**, not inside the existing
      `GetBookByFilePath(...) != nil` branch at `internal/scanner/scanner.go`,
      because the books the gate exists to help are exactly the ones whose cache
      entry is missing. The gate belongs on the cache-miss path, not as an OR arm
      on the unchanged path — OR'ing it naively would start skipping genuinely
      changed files. Measure with the skip-rate summary added in #2858 first.
