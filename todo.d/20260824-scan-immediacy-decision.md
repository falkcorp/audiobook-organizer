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

- [x] **~~Add a per-book last-scanned timestamp before building the 6-day age gate.~~**
      **RESOLVED 2026-08-24 by decision, not by code — no new field is needed.**
      This task assumed COOLDOWN semantics ("don't re-read a file we scanned in
      the last 6 days"), which is the only one of the three readings that needs a
      per-book *scanned-at* timestamp. The user chose **HYBRID** instead: a new
      or unknown file is scanned immediately, and a file the library already
      knows about is re-read only once its **mtime** is more than 6 days old.
      `LastScanMtime` already carries exactly that, so the gate shipped against
      existing fields.

      Two claims in the original text did NOT survive the decision, and are
      recorded here so they are not re-derived:

      - *"a new field makes every existing row read 'never scanned', so the first
        tick after deploy re-reads the whole library"* — moot, there is no new
        field.
      - *"The gate belongs on the cache-miss path, not as an OR arm on the
        unchanged path"* — this is the QUIESCENCE reading, which the user
        explicitly rejected because it would delay discovery of a newly added
        book by six days. The gate deliberately sits on the **changed** branch:
        cache-miss is read immediately, `NeedsRescan` is checked first so a
        forced rescan bypasses it, and `force_update` passes a nil cache so a
        full sweep never consults it.

      Shipped in `classifySkipFile` / `rescanFreshCutoff`
      (`internal/scanner/scanner.go`) behind `min_rescan_age_hours`, default 144,
      `-1` disables.

- [ ] **Watch `too-fresh` in the scan summary on the first real run after deploy.**
      The gate is new and its skip reason is reported separately from
      `unchanged` precisely because it means *deferred* work rather than work
      correctly avoided. A run where `too-fresh` is a large fraction means
      something is churning the library — that is a finding, not a success. If
      it is near zero, the gate is inert on this library and the 144h default is
      the wrong number.
