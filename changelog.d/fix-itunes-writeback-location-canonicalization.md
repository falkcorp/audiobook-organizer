### Fixed

#### iTunes ITL writeback now produces valid, safety-contract-clean track locations

The `rebuild` / `rebuild-full` / export writeback wrote each track's location as
the raw stored `ITunesPath` — a mix of `file://…%20` URLs and Linux `/mnt/…`
paths — straight into the ITL `0x0D` Location field, which `SafeWriteITL`'s
`ITLSafetyContract` rejected wholesale (the class of bad write that corrupted the
generated library on 2026-07-05). Two fixes:

- Every track location is now derived from the book's **current** `FilePath`,
  reverse-mapped via the configured iTunes path mappings and canonicalized to a
  native Windows path (`W:\…`), so the ITL points iTunes at wherever the file
  currently lives (the `audiobook-organizer` copy for organized books) rather than
  the frozen original path. Unmappable locations are skipped-with-warn + metric,
  never written raw. Track **adds** now also write the required `0x0B` LocalURL
  sibling (the metadata-update path already did; adds did not).
- `startCacheWarmers` fire-and-forget goroutines gained a `recover()` guard so a
  warmup fault (e.g. a store read tripping a dependency bug, or a torn DB on a
  clone) degrades to a cold cache instead of crashing the server at startup.

Validated end-to-end on a full-fidelity ZFS-clone replica of production: a blank
library populated cleanly (11,819 tracks added, contract accepted) where the old
code was rejected on every track.
