- [ ] **`dedup.MergeBooks` hard-delete path has no audio-route guard** — surfaced by the
  #3053 review (gate count L1). `merge.Service.MergeBooks` now refuses to keep a
  primary with no audio route while a loser has one (`FilelessPrimaryError`), and
  elects the survivor with `HasAudioRoute`. The legacy `internal/dedup/book_dedup.go`
  `MergeBooks` (kept solely for `internal/reconcile/itunes_heal.go:314`) still takes
  `keepID` as given, hard-deletes every `mergeIDs` row via `store.DeleteBook`, and
  never asks whether `keepID` has files: a heal that picks a row with no `book_file`
  rows and an empty `FilePath` as the keeper deletes the only rows that reached the
  audio. It also still carries the ext-ID / ITL gap its own doc comment records.
  Fix shape: either route `itunes_heal` through `merge.Service.MergeBooks` (soft
  delete + version group + `FollowMerge`) and delete the legacy function, or — if the
  hard-collapse semantics are genuinely required — call `merge.HasAudioRoute` on the
  keeper and each loser before any delete and return a typed refusal. Add a test that
  seeds a file-less keeper with a file-bearing loser and asserts nothing is deleted.
