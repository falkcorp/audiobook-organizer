### Changed

- `readstatus.RecomputeUserBookState` and `readstatus.SetManualStatus` declared an
  **anonymous** composite interface — `interface{ database.BookFileStore;
  database.UserPositionStore }`, roughly 86 methods — inline in their signatures,
  for the four methods they actually call. That interface is now named
  `readstatus.Store` and narrowed to those four.
- Two callers that only carried those wide surfaces to satisfy it are narrowed as
  a result: `positionSyncStore` (86 transitive methods → `readstatus.Store` plus 6
  direct calls) and `handlers.ReadingStore` (→ `readstatus.Store` plus 3). Both now
  state the forwarding relationship by embedding the interface they forward to.
  No behaviour change; no function bodies were edited.
