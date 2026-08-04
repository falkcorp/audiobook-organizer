<!-- file: changelog.d/20260804_030000_canary_finding_retraction.md -->
<!-- version: 1.0.0 -->
<!-- guid: 53903694-6576-4606-8206-cd3fdf1a51be -->
<!-- last-edited: 2026-08-04 -->

### Fixed

- Corrects the record on the `maintenance.dedupe-book-file-rows` canary. It was written
  up as having destroyed the duration on `The Trapped Mind Project`, leaving the book at
  0.00h. **That was wrong, and the op behaved correctly on all 10 canary books, not 8.**

  The book's entire audio content is a 13.5-second, 91,958-byte MP3 — a stub, not a real
  audiobook — and the surviving row matches the file exactly:

  ```
  iTunes copy        91958 bytes   duration=13.485s   bit_rate=54554
  surviving DB row   file_size=91958                  duration=13
  ```

  130 rows × 13s ≈ 1,690s ≈ 0.47h before the run; one row of 13s after. `0.00h` is
  simply what 13 seconds renders as. The mistake was treating a rounded display value
  as evidence of loss without probing the underlying file.

  The keeper field-merge shipped for this remains correct and stays — ranking selects a
  whole row, so a keeper genuinely can lack a field one of its twins holds — but it is
  **hardening against a latent hazard, not a repair of an observed loss**.

  Two real defects on that book did surface while checking, and are now tracked: its
  book-level `file_size` reads 532,805,172 (532 MB) for a 91 KB file, and the API
  reports `file_exists: true` for a `file_path` that is absent from disk.
