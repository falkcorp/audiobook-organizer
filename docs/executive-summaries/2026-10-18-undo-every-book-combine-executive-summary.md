<!-- file: docs/executive-summaries/2026-10-18-undo-every-book-combine-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: c8d8966a-51e0-4b39-875b-779568682968 -->
<!-- last-edited: 2026-10-18 -->

# Every book combine can be undone from the Operations page

**Pull request:** not yet opened (branch feat/undo-combine-coverage). It
unblocks #4060 (task 01-P81h), which retires the two old merge-undo
addresses.

## Executive Summary

- **One undo button for every combine.** Books get combined in about a dozen
  ways: by hand, by review approvals, by duplicate cleanup jobs, by the
  iTunes repair, by the chapter-merge tool. Each combine keeps a private undo
  record, but the general "Revert" action on the Operations page could find
  that record for only one of those ways. Now every way leaves a link the
  Revert action follows.
- **The link is written before anything changes.** Each combine writes the
  link right after its undo record and before it touches a single book. If
  the link cannot be written, the combine is refused. A combine can no
  longer happen with no way back.
- **Jobs undo as a whole.** When a background job combines many books,
  reverting that job undoes all of its combines together. A combine nobody
  started as a job gets an undo number of its own. Combines started from the
  app return that number, and the surviving book's change history lists it.
- **Groups of versions are restored too.** Merging two books can also move
  the other versions of a book (for example the abridged and unabridged
  editions) into a new group. Reverting now puts those versions back in
  their old group. The merged books themselves stay in the trash, which is
  what the old undo did as well.
- **Older combines are covered by a one-time job.** A new maintenance job
  gives every combine made before this change its own undo number. It
  previews by default, writes nothing until told to, and records which undo
  number belongs to which combine.
- **Verified end to end.** Every combine path has a test that combines real
  books, reverts the operation, and checks that every book, every file, and
  every outside ID (such as the iTunes ID) is back where it was.

## Undo links at the point of every combine

**What it was.** The Revert action on the Operations page reads a list of
recorded changes for an operation. Combines kept their undo record somewhere
else, and only one tool (the split-books repair) added a link from that list
to the record. Every other combine could be undone only through two older,
separate addresses.

**Why it mattered.** Task 01-P81h retires those two addresses. Without
links, every combine except the split-books repair would have become
permanent the moment that change shipped. The owner treats undo as a hard
requirement.

**The fix.** The link is now written at the single spot each kind of combine
passes through, rather than by each tool. A tool added later gets the link
without having to remember it. A combine whose link cannot be saved is
refused before it changes anything.

## Grouping undo by job

**What it was.** Some tools combine many books in one run, for example the
bulk split-book merge, the same-folder duplicate merge, the chapter-group
merge, the duplicate book merge, and the iTunes repair.

**Why it mattered.** If each combine got its own unrelated undo number, a
bad run could only be undone one combine at a time, and the numbers would be
hard to find.

**The fix.** These tools pass their own job number down, so every combine in
the run is linked under it and one Revert undoes the whole run. Combines that
run outside any job, such as the manual Combine button, a review approval or
a split-book candidate merge, get a fresh number. The app's response returns
it where there is one, and the surviving book's change history always lists
it.

## Restoring version groups

**What it was.** A merge can carry the other editions of the merged book
into the survivor's version group. That move had its own undo record and its
own old undo address, and the Revert action did not know about it.

**Why it mattered.** Undoing only the combine would have left editions in
the wrong group, with the wrong book marked as the main edition.

**The fix.** The Revert action now understands this second kind of record
and puts each edition back in its old group with its old main-edition flag.
If an edition has been moved again since, the undo refuses and says why
rather than overwriting the newer move.

## Covering combines made before this change

**What it was.** Combines already in the library were made before the links
existed, so their undo records have no link.

**Why it mattered.** Once the old addresses are retired, those combines
could not be undone at all.

**The fix.** A new maintenance job finds every undo record with no link and
gives each one a link under a new number of its own, never the job's own
number. That way, reverting the backfill job itself can never undo every
past combine at once. It skips records that no undo would accept: already
undone, abandoned, or from a combine that was refused before it changed
anything. It works on one record at a time while holding the same lock that
combines use, so it cannot race a combine that is running. It previews by
default. The owner decides when to run it for real.
