### Fixed

#### Version groups no longer lose members (two "primary" copies of the same book)

Grouping two copies of a book as versions of each other could leave the library
showing **both** tiles as the primary edition, and pressing "set primary" on
either one would not fix it. Found in production on *The Successors*: two books
carried the same version-group ID, but asking for that group's members returned
only one of them.

Because the lookup returned a short list, everything built on top of it was
quietly wrong. `set-primary` demotes "every other member of the group" — with a
member missing from the list, it never demoted the stray, so two primaries
survived. The same lookup backs `ApplyVersionGroup`, the safety net that keeps
one primary per group when a regrouping hold is approved, so approving a hold
could leave two primaries behind as well.

Root cause, in two halves:

**The read path treated "found something" as "found everything."**
`GetBooksByVersionGroup` reads a `book:versiongroup:<gid>:<id>` index and falls
back to a full scan only when the index yields *zero* rows:

    if len(books) > 0 { sortVersions(books); return books, nil }

A *partially* populated index returns its partial set and never reaches the
fallback. The paradox this creates is worth stating plainly: deleting the
**entire** index returns the **correct** answer, while deleting **one row**
returns a wrong one. More data loss produced a better result, which is why the
defect survived so long without ever surfacing an error.

**The write path could not heal.** `UpdateBook` wrote a book's index row only
when its `VersionGroupID` actually *changed*. A row missing for any reason — a
book that acquired its group through a path that did not trip that comparison,
or one written before the index existed — could never come back, because every
later edit left the group unchanged and so skipped the write. Re-submitting the
grouping did not repair it either: the group ID was already correct, so nothing
changed and nothing was written.

The fix is on the write side, where the incompleteness originates:

- `UpdateBook` now writes the current group's index row unconditionally, so any
  book touched by any write path repairs its own entry. Deleting the *old* row
  is still gated on the group actually changing.
- The one-time index backfill's sentinel moved from `v1` to `v2`, so every
  existing deployment rebuilds the index once on next start. Installations
  repair themselves — there is no manual step and no maintenance op to run.
- All three writers of this index now store the book ID as the row value. The
  index is a pointer index — the reader takes the ID from the key and looks up
  the authoritative book row — so the full copy of the book that `CreateBook`
  and `UpdateBook` used to store was never read, and rewriting it on every
  update would have been pure write amplification.

The zero-result fallback is deliberately **kept**. Gating it on the backfill
sentinel was considered and rejected: a genuinely missing row would then return
an empty group rather than the full scan's correct answer, trading a silent
under-report for a silent zero on a path that also feeds version listings and
metadata writeback.

Regression tests damage exactly **one** index row rather than all of them, since
wiping the whole index hits the fallback and passes even with the bug present.
