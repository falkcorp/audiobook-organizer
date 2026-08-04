<!-- file: changelog.d/20260804_080000_purge_millisecond_durations.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7ad86e89-caff-4b83-8cdb-ec0403de1d98 -->
<!-- last-edited: 2026-08-04 -->

### Fixed

- **`UpdateBookFile` now normalises `Duration` to the stored standard (seconds).** It
  was the last write path that did not. `CreateBookFile`, `UpsertBookFile` and
  `BatchUpsertBookFiles` all called `normalizeBookFileDuration`; an *update* did not —
  so an update could reintroduce exactly the millisecond corruption those three exist
  to prevent. The unit invariant now holds at the store, not at each caller's
  discretion.

  Applying it unconditionally is safe because `DurationLooksLikeMillis` only fires when
  reading the value as seconds implies an impossible sub-4 kbps file **and** dividing by
  1000 lands back inside a plausible audio band. A correct row is never touched, and a
  corrected row is never divided twice.

  Four tests cover it — conversion on update, inertness on good data, idempotence, and
  fingerprint preservation (this path writes the whole struct, and a fingerprint-wipe
  bug has shipped here before). Verified all three failure cases fail on *behaviour*
  with the guard removed: `stored duration = 1048000, want 1048`.

### Added

- **`maintenance.purge-millisecond-durations`** — a one-shot backfill for rows written
  before the guard existed. Closing the write path stops new corruption but rewrites
  nothing, and production holds roughly 6,000 millisecond rows (measured: 1.9% of a
  2,733-row sample).

  The symptom is stark. A book reading **9,906 hours** is 34 rows of milliseconds;
  9,906 h ÷ 1000 ≈ 9.9 h, an ordinary audiobook. Every row in it reads 48–53 kbps
  interpreted as milliseconds versus ~0.1 kbps as seconds — only one interpretation
  describes a real audio file.

  Design mirrors `dedupe-book-file-rows`, which is now well proven:

  - **Two passes.** Discovery reads the cheap memdb `Core` projection (which already
    carries `Duration` and `FileSize`, all the predicate needs); the repair re-reads
    each book Pebble-direct, because the memdb projection strips `AcoustIDFingerprint`
    and this path writes the whole struct.
  - **Re-tests every row against the full-fidelity copy** rather than trusting
    discovery. The memdb snapshot can be stale — it was, twice today — and a row
    already corrected must not be divided again.
  - **Dry-run by default**, reporting old → new per row.
  - **Parallel by book** via `registry.RunItems`, safe for the same disjoint-partition
    reason: a `book_file` row belongs to exactly one book.
  - Recomputes each book's aggregates only where rows actually changed, and notes that
    corrected totals stay invisible until memdb refreshes.
