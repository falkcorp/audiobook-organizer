### Fixed

#### A series prune that refused to delete anything no longer reports success

The series merge was taught to stop and refuse when it could not move every book
out of a series before deleting it. That refusal ends with the words *"Re-run
after resolving the errors above."*

It was delivered to a run that reported itself green. `executeSeriesPrune`
returned "no error" no matter what happened inside it, so the operation was
marked **success** and the activity feed said *"Series prune completed."* Every
recorded failure — a book that could not be loaded, a refused delete, a skipped
orphan sweep — reached the operator only as a warning line truncated to ten
entries.

So the protection worked and nobody found out. The duplicate series stay,
membership split across both rows, and no re-run ever happens because nothing
asks for one.

The run now finishes its work and then reports the failure, with counts and the
first ten reasons.

This is the same mistake as the cache bug fixed a day earlier, in a different
place. Repoint-some-books-then-refuse is an outcome that did not exist when these
conditions were written, and it has to be taught to **every** condition that reads
the same facts — not just the first one somebody notices.

#### Books that vanish mid-run are no longer organized silently into nothing

After a series is renamed, the normalize operation moves each affected book's
files to match. If a book could not be loaded at that moment, it was skipped with
no log, no counter and no error — while still being counted in *"organizing the N
books it collected."*

The book keeps its old file path and stale tags. A re-run cannot fix it: the
series name is already clean, so a second run finds nothing to do and never looks
at that book again. The silence was permanent.

These are now counted, named individually in the log, and folded into the
operation's outcome. Sixty lines away in the same feature, the identical
situation already blocked a delete; here it counted for nothing.

#### A transient error can no longer decide which duplicate series gets deleted

When several series look like duplicates of one another, the one with the most
books is kept and the rest are merged into it and removed. A series whose book
count failed to load counted as **zero** books — so it lost, and losing means
being deleted.

One momentary read error could hand a 400-book series to a 2-book typo of itself.
The books survive, but the surviving row is the wrong one, and every external
reference to the original is gone. The run reported success.

A count that cannot be read now disqualifies that whole group of duplicates. The
merge is skipped and reported, and can be retried; the wrong outcome could not be
undone from the summary.

#### The series list no longer goes stale when a prune exits early

The cached series list was dropped at the end of the prune. The function has six
ways out and five of them are earlier — a cancellation, a failed listing, a failed
reference count. Any of those can happen *after* books have already been moved,
which is exactly when the cached list is most wrong.

It is now dropped on every exit path.

#### Two failures that reported "0 errors"

A failed refresh of the series list skipped the **entire** orphan sweep while the
summary still read *"0 orphans deleted, 0 errors"* — a clean bill of health from a
phase that never ran.

Separately, if the list of series could not be read at all, the normalize
operation reported *"complete, 0 affected books"* with status success, and the
dry-run preview showed an empty, clean-looking action list. Nothing had been
examined in either case. An empty result now means "nothing needs doing"; a
failure says so.

### Fixed (tests)

Two tests were checking less than they claimed.

The cache fix above shipped with no test that could detect its removal — the
existing pair covered "rows removed" and "nothing happened", never the
books-moved-but-nothing-removed state the fix exists for. Reverting it left the
suite green.

And the test written to prove the refusal survives the sweep that follows it used
a fixture whose book-membership lookups returned fixed answers, ignoring the
repoints performed during the test. That kept one specific regression invisible:
reverting the sweep to the filtered count — the defect that produced 6,893 phantom
series references in production — still passed. The fixture now answers from live
state, so it fails.

A fixture that cannot reach a code path cannot host a defect on it, and a passing
mutation score only ever covers the defects the fixture makes reachable.
