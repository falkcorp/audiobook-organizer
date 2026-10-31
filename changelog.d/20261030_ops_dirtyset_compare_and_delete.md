### Added

- `*database.PebbleStore` gains `CompareAndDeleteRaw(key, expect)` and the dirty-set marker `MarkDirty(prefix, subjectID)` (value: 8-byte big-endian epoch), both under the same per-entry lock. A v3 Batch run over an `ops.DirtySet` source now starts on the real store and clears only the entries it processed; a subject re-dirtied while its item ran, and a failed item, stay dirty. `MarkIdentDirty` and `ClearIdentDirty` now go through the same two functions.
