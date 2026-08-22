### Fixed

- **Every duplicate-detection action reported failure for work that actually
  succeeded.** Book and author duplicate scans, series duplicate refresh, series
  deduplicate, series prune, series merge, series normalize and book merge all
  handed the progress bar the id of a legacy operation record. When the older
  operation lookups were retired on 2026-08-16, the page started resolving those
  ids against the newer operations system, where they do not exist — so the
  lookup failed and surfaced as an error, while the scan or merge itself had
  already completed. All eight now return an id the page can resolve.
- **Merging duplicate books failed a second, separate way.** The merge call read
  the response envelope instead of the operation inside it, so it had no id to
  follow at all. Merging reported failure even once the id above was correct.

### Changed

- **The duplicate-detection actions no longer create legacy operation records.**
  All eight are now native v2 operations. The undo/provenance ledger that series
  merge and series prune write is unaffected: it is keyed by the operation id
  that writes it and read back per book, and both sides moved together.
