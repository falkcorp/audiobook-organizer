### Fixed

#### Three more series-merge paths no longer strand non-primary versions

A series merge repoints every book it is handed to the surviving series and then
deletes the merged-away one. Three paths read `GetBooksBySeriesIDCore` — the
**listing** getter, which hides non-primary versions — so an alternate rip of a
book was never repointed, and the series it still pointed at was deleted out from
under it. TASK-029 fixed this shape inside `internal/dedup/series_dedup.go`; these
are the paths outside it.

- `internal/server/duplicates_helpers.go` — two sites. `mergeSeriesGroupHelper`
  had **no guard at all**: its `DeleteSeries` is unconditional, so every hidden
  row was stranded. The `executeSeriesPrune` merge loop had the same shape.
- `internal/plugins/maintenance/series_denumber_op.go` — this one *looked*
  defended and was not. It refuses to delete unless `movedAll` is true, but
  `movedAll` starts `true` and is only ever set `false` inside the loop over the
  rows the filtered getter returned. A row that getter excluded is never
  iterated, so it can never flip the flag and the delete proceeds anyway. The
  guard's sample space was the filtered set the bug lives outside of.

#### A failed repoint no longer deletes the series anyway

Swapping the getter fixes *which* books a merge tries to move. It does nothing
about what happens when moving one **fails** — and both `duplicates_helpers.go`
merge loops recorded the failure and then deleted the series regardless, leaving
the failed row pointing at nothing and reporting the prune as successful. That is
the same end state as the bug above, reached through the error path instead of
the getter.

Both are now fail-closed: the series row is removed only after every book has
been repointed. A refusal names the series, how many books were affected, and
what to do about it.

The `(nil, nil)` branch — a book the membership getter lists but a subsequent
point-get cannot hydrate, which the Pebble store returns on `ErrNotFound` — was
the worst of these. It was recorded nowhere at all, so a merge could strand a row
and leave no trace whatsoever. It is now a first-class failure on both paths.

Also fixed: the canonical-series vote silently treated a series whose book count
failed to load as empty. The error decided which series got **deleted** and left
no record of having done so.

### Changed

#### The affected-book list stays on the filtered getter, deliberately

An earlier revision of this change also widened `executeSeriesNormalizeCore`'s
affected-book list to the complete set, reasoning that a row the merge repoints
should also have its file moved. **That reasoning was wrong and has been
reverted.** `affectedBookIDs` is the worklist for `ReOrganizeInPlace` and the tag
write-back — it decides which *files* are touched, not which rows were repointed.

The organizer deliberately never organizes a non-primary version while a primary
exists in its version group, and `duplicates_ops.go` calls `ReOrganizeInPlace`
directly, bypassing that filter. Widening the list therefore would not have kept
row and file in sync; it would have overridden organize policy from the outside.
It would also have collided: the default naming patterns carry no codec, quality
or edition variable, so a primary and its alternate rip compute the **same**
destination path, and the one emitted first by the stable series ordering would
have claimed it while the other was refused.

Repointing an alternate rip and moving its file are separate questions with
different answers. Only the first belongs in this change.

### Known residuals

Recorded rather than fixed, because each is a production-data judgement call
rather than a bug fix:

- **Trashed rows are still invisible to all three paths.** Both series getters
  exclude soft-deleted books by design, so a series holding a live book and a
  trashed one is still deleted with the trashed row left pointing at it. Closing
  this means gating the merges on the unfiltered `SeriesRefCounts` — the guard
  `executeSeriesPrune`'s own phase 2 already uses sixty lines further down, and
  which fails closed there with a comment calling the filtered fallback "the
  failure family this repo keeps rediscovering." Adding it to phase 1 makes the
  prune refuse merges it currently completes.
- **A repointed non-primary version keeps stale series tags,** because nothing
  adds it to any write-back list. The fix is to split the one list into an
  organize list (filtered) and a write-back list (complete), which would start
  writing tags to files this operation has never touched.
- **`MergeSeries`, the store-level primitive, has no ref-count guard at all.**
  Every guard discussed here lives in a caller, so a new caller inherits no
  protection.

`internal/maintenance/jobs/cleanup_series.go` was audited and deliberately **not**
changed here. It does not strand: its guard is driven by an independently-sourced
*unfiltered* count, so it refuses the delete instead. Its problem is the mirror
image — the refusal is permanent — and it is fixed separately.
