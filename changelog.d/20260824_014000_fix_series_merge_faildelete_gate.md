### Fixed

#### A failed repoint no longer deletes the series anyway

Reading the complete-set getter fixed *which* books a series merge tries to move.
It did nothing about what happens when moving one **fails** — and both merge loops
in `internal/server/duplicates_helpers.go` recorded the failure and then deleted
the series regardless, leaving the failed row pointing at nothing.

That is the same end state as the stranding bug, reached through the error path
instead of the getter, with one difference that makes it worse: `executeSeriesPrune`
returns `nil` either way, so the operation is marked successful and the activity
feed reports a completed prune.

Three branches reached the delete — a hydrate error, an update error, and a book
the membership getter lists but a later point-get cannot resolve. The last of
those returned `(nil, nil)`, was neither counted nor logged, and so could strand a
row leaving **no trace whatsoever**. It is reachable rather than theoretical: the
Pebble store returns `(nil, nil)` on `ErrNotFound`, and the getter may serve a row
from the memdb that the point-get can no longer hydrate.

Both paths are now fail-closed — the series row is removed only after every book
has been repointed — and a refusal names the series, how many of its books were
affected, and what to do next.

Also fixed: the canonical-series vote silently treated a series whose book count
failed to load as empty, so a transient read error could decide which of two
duplicate series got **deleted**, leaving no record of having done so.

#### A refused merge no longer leaves the series list stale

Introducing that refusal broke a nearby assumption, caught in review before it
shipped. The cached series list is dropped only when a run "cleaned" something,
on the stated reasoning that *"a run that cleaned nothing changed nothing."*

That was true when a merge either completed or errored. A merge that repoints
books and then refuses the delete changes every one of those books' series while
removing no rows — so the count stayed at zero, the cache was kept, and
`/api/v1/series` went on serving the pre-merge membership under its 24-hour TTL.
That is the same stale-list symptom measured in production on 2026-08-14,
reached from the opposite direction. Repointed books are now counted in their own
right, and the invalidation reports both numbers.

#### A partial series-normalize no longer discards the work that succeeded

Recording the affected-book collection error (above) turned a swallowed failure
into one that aborted the operation — which skipped organize and tag write-back
for **every** book in the run, not just the series that failed.

Because the renames and merges have already committed by then, a re-run finds no
contaminated names, computes no actions, and never organizes those files: the
failure was permanent rather than retryable. The operation now organizes and
retags the books it did collect, then reports the failure. The status is still
`failed` — deferring it buys file consistency, not silence.

### Changed

#### The series-normalize affected-book list stays on the filtered getter

A previous entry widened `executeSeriesNormalizeCore`'s affected-book list to the
complete set, on the reasoning that a row the merge repoints should also have its
file moved. **That reasoning was wrong and the change has been reverted.**

`affectedBookIDs` is not a record of what was repointed — it is the worklist for
`ReOrganizeInPlace` and the tag write-back, so it decides which *files* are
touched. The organizer deliberately never organizes a non-primary version while a
primary exists in its version group, and `duplicates_ops.go` calls
`ReOrganizeInPlace` directly, bypassing that filter. Widening the list therefore
did not keep row and file in sync; it overrode organize policy from the outside.

It would also have collided. The default folder and file naming patterns carry no
codec, quality or edition variable, so a primary and its alternate rip compute the
**same** destination path — one would claim it and the other would be refused, with
the winner decided by emission order rather than by which copy is primary.

Repointing an alternate rip and moving its file are separate questions with
different answers. Only the first belongs in the stranding fix.

### Known residuals

Recorded rather than fixed, because each changes what a run does to a real library
rather than correcting a defect. Tracked in `todo.d`:

- **Every guard here counts against what the membership getter returned, and that
  getter has no completeness guard of its own.** It reads the in-memory index
  unconditionally when warm. Two populations therefore sit outside the guard: books
  in the trash (excluded by design — latent, it bites on restore), and books the
  in-memory index has **lost** while their on-disk row survives, which is a live,
  primary, untrashed book stranded immediately with no error raised. The second is
  structurally the same defect this work removed from the series-renumbering job:
  a guard whose sample space is the filtered getter's own output, so the rows the
  bug lives on can never trip it.

  The fix already exists and is already used *in the same function* —
  `executeSeriesPrune`'s phase 2 fails closed on the unfiltered reference count,
  with a comment calling the filtered fallback "the failure family this repo keeps
  rediscovering" — while phase 1, sixty lines above, has none. Adding it makes the
  prune refuse merges it currently completes, which is why it is a decision rather
  than a fix.
- **A repointed non-primary version keeps stale series tags,** because nothing adds
  it to any write-back list. The fix is to split one list into an organize list
  (filtered) and a write-back list (complete), which would begin writing tags to
  files this operation has never touched.
- **`MergeSeries`, the store-level primitive, has no reference-count guard at all.**
  Every guard described here lives in a caller, so a new caller inherits none of
  them.
