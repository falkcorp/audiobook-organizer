<!-- file: changelog.d/20260804_090000_ms_purge_complete.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6a535402-a856-4b04-9aed-fdc81fef7f1e -->
<!-- last-edited: 2026-08-04 -->

### Fixed

- **Millisecond durations are gone library-wide.** `maintenance.purge-millisecond-durations`
  applied against production:

  ```
  apply : 314,153 rows scanned, 214 books affected, 1,384 ms rows,
          converted 1,384, recomputed 214 books,
          skipped 9,352 (already seconds), failed 0
  verify: 314,153 rows scanned, 0 millisecond durations found — nothing to do
  ```

  The two books that survived the row-dedupe are now correct:
  `01KNDB9V04D7MBTFVDKYWX286E` 19,294.11h → 9,906.11h → **9.90h**, and
  `01KNDB9ZHJSMBY7D98Y82PQTK0` 15,556.96h → 8,049.06h → **8.05h**. All ten books
  tracked through this work now read 8–17h.

  The 9,352 skipped rows are the reassuring detail: they sit *inside* the same 214
  affected books and were correctly left untouched, so the predicate discriminates per
  row rather than condemning a whole book.

  Together with the `UpdateBookFile` normalisation, the unit corruption is both
  repaired and closed off — every write path now agrees that `Duration` is seconds.

### Corrected

- The earlier estimate of "~6,000 millisecond rows (1.9%)" was **wrong by roughly 4×**.
  The true count is **1,384 rows (0.44%)**. That figure was extrapolated from a
  2,733-row sample which turned out to be a targeted dump rather than a random one, so
  its rate did not generalise. Recorded because the mistake is reusable: prefer a full
  scan over an extrapolated sample for any number that drives a decision.
