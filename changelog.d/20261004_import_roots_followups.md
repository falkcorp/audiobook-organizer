### Fixed

#### Import roots: review follow-ups to #3718 (manual-only guard, bounded waits, one read per apply call, memo bound to its store)

- **The owner-manual-only guard checks the transcribed stand-ins itself.** When the resolver skips a row it returns no query. That happens to a part row, and to a row whose import-root read failed so the root was listed as siblings. The bulk guard then had nothing to match. A blank-titled Big Finish file directly in an import root, whose intro transcription ("Doctor Who: The Chimes of Midnight") was the only signal, reached the gate unmatched and was refused only as `identity_stale`, which a review-page bulk pin overrides. `applygate.BulkManualOnlyGuard` now checks the book's and each file's transcribed title, whatever the resolver decided. The folder name needs no extra check, because it is already part of every path the guard reads.
- **No lock is held across the store read.** A new `metabatch.ImportRootsCache` makes one read at a time, outside its mutex. A caller with no list yet waits at most 2 s for that read. A caller that already has a list keeps using it while a refresh runs.
- **The TTL window starts only on a successful read.** A failed read is retried by the next caller. A read that panics releases its waiters and consumes nothing.
- **Each apply call reads the import paths once.** Before, each book read them up to three times. The batch apply, the bulk-apply preview and the apply-when-scanned op each put one cache in front of the store for the length of the call (`withCachedImportPaths`).
- **A `FolderMemo` is bound to one store.** `NewFolderMemo(store)` takes the store, and every listing and the import-root set come from it, so one pass cannot mix two stores' rows or roots.
