### Fixed

#### Split-book merge no longer reverts the keep row, strands PIDs, or goes unjournaled (A1#5)

- `dedup.MergeSplitBookCluster` wrote the keep book back whole from a read taken
  before the file moves, reverting `FileSize` (recomputed by the move) and any
  column another writer changed. It now writes only `Duration` and `Title`
  through `ModifyBook`.
- A src's external-ID mappings (iTunes PIDs) are now reassigned to the keep before
  the src is soft-deleted. If the reassignment fails the src is left live and a
  re-run finishes it (fail closed, as `MergeBooks` does).
- Each run now writes a combine undo journal (origin `split_book_merge`) before its
  first write, so `UndoCombine` can reverse it: srcs restored, files and PIDs moved
  back, the suggested title rolled back. A journal that cannot be written refuses
  the merge.

#### A failed combine undo can be retried (A1#8)

- An undo that failed part way left the journal `undo_failed`, which
  `UndoCombine` then refused. The rows the attempt had already restored also
  failed every "still combined" precondition. A retry is now accepted. Every
  precondition takes either the combined state or the recorded pre-combine state,
  and every undo step skips what is already restored, so the retry ends where a
  clean undo does.
