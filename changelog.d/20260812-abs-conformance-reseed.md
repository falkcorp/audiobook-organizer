### Changed

- **The ABS conformance fixtures are now seeded from the oracle, so they cannot drift
  from it.** The fake library's two books were built from constants invented in the test
  file: six identical 1662-second tracks of 2049–2054 bytes against a real *Odyssey*
  capture with six distinct durations and 11–21 MB files, and a single-file book of 4096
  bytes against a real 120 MB m4b. That drift is why twelve assertions could not compare
  values at all. Per-track durations, sizes, filenames, tags and chapter boundaries now
  come out of the fixture itself.

  File sizes conform via sparse files: `mapper.go:225` stats every track and lets the
  on-disk size override the recorded one, so `metadata.size` can only match if the file
  really is that long — about 200 MB per run written as holes, costing no disk. Safe for
  the Range assertions because they compare a suffix range against the response's own
  tail rather than against a byte pattern.

- **Allowances are now bounded, and an allowance that never fires fails the test.** A
  blanket exemption at `media.duration` accepts any duration there forever; the day we
  report half a book's length, a suite full of such exemptions says nothing. A bounded
  allowance states the widest gap its stated cause can produce — 0.5 s for a rounded
  track, 3.0 s for a six-track aggregate — and anything wider still fails as a different
  defect.

  This is not theoretical. The bound immediately caught `sixChapters()` returning flat
  1662.5-second spans where the oracle's first chapter ends at 1386.057: a **276-second**
  gap that a blanket "chapter bounds may differ" would have absorbed as the known
  sub-second truncation. Those chapters are now seeded from the oracle and match exactly,
  so they need no allowance at all.

### Fixed

- **`mediaItemId`, `bookId`, `date` and `dayOfWeek` are treated as volatile in
  conformance comparisons.** The first two are the same minted sync ids as
  `libraryItemId`, reached under a different name from the progress and playback bodies;
  the last two are when a captured listening session happened. Four permanent
  "mismatches" that no run could ever satisfy.

### Added

- **Five production findings surfaced by comparing values**, filed rather than fixed
  alongside test work: `BookFile.Duration` is an `int` (2.431 s lost across six tracks,
  `startOffset` drifting 2.200 s by track 6, ~0.4 s per track boundary);
  `/api/me/sessions` reports `itemsPerPage` as the item count rather than the page size
  where its siblings in `stats.go` get it right; `deviceInfo.deviceType` is never derived
  from the User-Agent; `BookFile.BitrateKbps` is int-kbps so 96208 bps round-trips as
  96000; and `publishedYear` renders `Book.PrintYear` as an int, so a raw `"800BC"` tag
  comes back `"800"`. See `todo.d/20260812-bookfile-duration-integer-seconds.md`.
