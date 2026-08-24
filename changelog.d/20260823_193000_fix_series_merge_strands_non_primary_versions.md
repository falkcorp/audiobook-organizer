### Fixed

#### Series merges no longer strand alternate versions of a book

Merging two duplicate series moved the books out of the one being deleted and
then deleted it. But it asked for those books with `GetBooksBySeriesIDCore`,
which is a *listing* getter: it deliberately hides non-primary versions,
because an alternate rip of a book is a duplicate of something already shown.

Hiding a row from a list and leaving it behind in a merge are different things,
and the merge loop treated them as the same. Every alternate version of a book
in the merged-away series was left pointing at a series ID that no longer
existed. `internal/database/series_bookref.go` records the production scale of
this shape: 6,893 phantom series IDs held by 13,322 live books.

Added `GetBooksBySeriesIDAllVersions`, the complete-set counterpart, mirroring
the `GetBooksByAuthorIDWithRoleCore` fix made to the author getters on
2026-08-14. Three call sites in `internal/dedup/series_dedup.go` now use it:
the two reassignment loops that repoint books before deleting a series, and the
pass that links the merged authors onto the kept series' books. That third one
was not stranding rows — it was silently *under-applying a write*: a
non-primary version skipped there never received the merged author credits, and
nothing revisits it afterwards, so the omission was permanent.

The dedup scan's series preview card deliberately still uses the listing
getter. It is display-only and cannot strand anything, and switching it would
re-introduce a separate bug fixed earlier this month, where a series listing
showed every alternate rip alongside the book it duplicates.

Two limits worth stating plainly:

- `AllVersions` does **not** mean unfiltered. Soft-deleted (trashed) books are
  still excluded from both getters, because a trashed row cannot be repointed.
- This therefore closes only half the hazard, and the remaining half is not
  covered everywhere. The scheduled dedup pass (`DedupSeries`) has an
  unfiltered reference-count guard that refuses to delete a series a trashed
  row still points at; that guard was left in place and is still load-bearing.
  The manually-invoked merge (`MergeSeries`) has **no such guard** and deletes
  the series unconditionally, so a trashed row there is still stranded. That
  gap predates this change and is not introduced by it, but it is worth
  stating plainly rather than implying the guard covers both paths.

A code comment and the operator-facing refusal message both claimed the
skipped rows were "trashed or non-primary". The non-primary half became false
with this change — that getter now returns them — so both were corrected to
name the causes that actually remain: trashed rows, and rows whose
reassignment failed.

The regression is pinned by a conformance test keyed on the **getter pair**
rather than on a list of call sites — it asserts `AllVersions` returns a
superset of `Core`, differing by exactly the non-primary version, on both the
in-memory and on-disk backends. The task brief that prompted this work
enumerated three call sites to convert; there were four. A per-site checklist
cannot catch a site nobody wrote down, or one added later. A structural
assertion about the getters themselves fails on both.
