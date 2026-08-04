<!-- file: changelog.d/20260804_050000_delete_bookfile_index_seek.md -->
<!-- version: 1.0.0 -->
<!-- guid: 288ae8bd-b678-4dfa-af06-8f893fab217e -->
<!-- last-edited: 2026-08-04 -->

### Changed

- `PebbleStore.DeleteBookFile` resolves its row through the `book_file_id` secondary
  index instead of iterating every `book_file:` key. The primary key is
  `book_file:<bookID>:<fileID>`, so an ID-only caller cannot seek it directly — which
  is why the original implementation walked the whole keyspace looking for a suffix
  match, once **per deleted row**. The index it now uses
  (`book_file_id:<fileID>` → `book_file:<bookID>:<fileID>`) already existed for
  precisely this purpose and is what `UpdateBookFileHashes` and `SetBookFileHash` use.

  **Measured scope, so nobody over-credits this.** At 8,000 rows the two paths are
  indistinguishable — 9.196 ms vs 9.144 ms per delete. Pebble walks that many keys
  essentially for free, and per-delete cost is dominated by fixed overhead (the `Sync`
  commit and the change notification), not by the lookup. Extrapolating the walk
  linearly to the production 316,453 rows puts the scan at roughly 0.36 s per delete,
  so for `dedupe-book-file-rows` (~15 deletes per book) it accounts for perhaps 5
  seconds of a book that takes ~90.

  This was written while investigating that op's runtime, and the initial hypothesis —
  that the scan explained it — **did not survive measurement**. The change is kept
  because removing an O(N)-per-delete is right on its own terms, but the op's cost is
  still unattributed and wants a pprof run against production (`make deploy-debug`)
  rather than another guess.

  The old scan survives as a fallback for rows written before the index existed, so
  deleting a pre-index row still works — just slowly. Four tests cover index
  resolution, index-entry cleanup, the pre-index fallback, and sibling rows being left
  untouched.
