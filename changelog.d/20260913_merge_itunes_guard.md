### Fixed

- Merges can no longer touch books in the active iTunes library. A new guard,
  `merge.GuardITunesProtected` (`internal/merge/itunes_guard.go`), runs at APPLY
  time inside the merge lock and before any write. It refuses a merge when any
  participating book, survivor or loser, has its own file path or a book_file
  path under the folder holding `itunes.library_read_path`, under
  `itunes.media_root`, or under a `books/itunes/` path segment. Matching is on a
  folder boundary after cleaning the path, so a sibling folder such as
  `.../Audiobooks2` is not matched and a relative path is refused because it
  cannot be checked. The guard fails closed: if a book or its files cannot be
  read, the merge is refused. With iTunes sync on and both paths empty, every
  merge is refused. Before this change, the only exclusion was where regroup
  proposals were generated, so a review approval, dedup candidate, bulk op or
  HTTP merge could still merge an iTunes book.

  The guard runs in `merge.Service.MergeBooks`, `merge.Service.CombineBooks`,
  `dedup.MergeBooks` (the iTunes-heal hard-delete path),
  `dedup.MergeSplitBookCluster`, and early in `MergeBooksJournaled` so that a
  refusal writes no provisional undo-journal entry. Refusals come back as the
  typed `merge.ErrITunesProtected`, which `merge.IsRefusal` recognises. The HTTP
  merge endpoints return 409. Review approve returns 409
  `REVIEW_ITUNES_PROTECTED` and leaves the item pending, and bulk approve skips
  the item instead of stopping. Dedup auto-resolve (`refused_itunes`),
  merge-same-path-dupes (`refused-itunes` bucket), the LLM auto-merge and the
  split-book bulk merge count these refusals separately from failures, and
  none of them marks the candidate resolved.
