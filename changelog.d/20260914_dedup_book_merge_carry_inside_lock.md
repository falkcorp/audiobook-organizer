### Fixed

- The `dedup.book-merge` op no longer copies the losers' iTunes stats onto the
  keep book outside the merge lock (A1#10). `applyBookMergeReroute` used to read
  the keep book, copy the six iTunes fields first-win, and write the whole row
  back with `UpdateBook` before calling `merge.Service.MergeBooks`. A failed
  write was only logged, so the merge still soft-deleted the losers and their
  stats were left on rows headed for the purge clock. The full-row write also
  reverted any edit made to the keep book between that read and the write. And a
  loser that another merge had already consumed still gave its stats to a second
  keeper, whose own merge was then refused.

  The copy now runs inside `merge.Service.MergeBooksWithOptions`
  (`MergeOptions{CarryITunesFields: true}`): under `mergeSerializeMu`, after the
  soft-deleted-input guard, and in the same `ModifyBook` that marks the survivor
  primary. If that write fails, the merge fails with every loser still live.
  The survivor's version-group write now uses `ModifyBook` rather than a
  full-row `UpdateBook` of the row read at the top of the merge.
  `TransferITunesMetadataFirstWin` moved to `internal/merge/itunes_transfer.go`,
  and `dedup.TransferITunesMetadataFirstWin` now forwards to it.
  `applyBookMergeReroute` no longer takes a store, and the `bookRerouteStore`
  interface is gone.
