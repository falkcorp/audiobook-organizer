<!-- file: docs/executive-summaries/2026-10-18-undo-every-book-combine-executive-summary.md -->
<!-- version: 1.1.1 -->
<!-- guid: c8d8966a-51e0-4b39-875b-779568682968 -->
<!-- last-edited: 2026-10-18 -->

# Every book combine can be undone by reverting its operation

**Pull request:** #4064 (branch feat/undo-combine-coverage). It unblocks
#4060 (task 01-P81h), which retires the two old merge-undo addresses.

## Executive Summary

- **One undo button for every combine.** Books get combined in about a dozen
  ways: by hand, by review approvals, by duplicate cleanup jobs, by the
  iTunes repair, by the chapter-merge tool. Each combine keeps a private undo
  record, but the general "Revert" action could find that record for only
  one of those ways. Now every way leaves a link the Revert action follows.
- **The link is written before anything changes.** Each combine writes the
  link right after its undo record and before it touches a single book. If
  the link cannot be written, the combine is refused. A combine can no
  longer happen with no way back.
- **Most jobs undo as a whole.** When a background job such as the
  chapter merge or the same-file duplicate merge combines many books,
  reverting that job on the Operations page undoes all of its combines
  together. The duplicate engine's merges (auto-resolve, the cluster merge,
  the AI and file-hash auto merges) are the exception: each merge there gets
  its own undo number, by the owner's choice.
- **Combines outside a job get their own number.** The manual Combine
  button and the split-book candidate merge return it. Review approvals
  write it to the log. For every path, the surviving book's change history
  lists it. These numbers are not jobs, so the Operations page does not list
  them.
- **A revert never overwrites newer work.** Undoing a combine now restores
  only the books that combine itself sent to the trash. A book it left alone,
  or never reached because it stopped part way, keeps any edits made since.
  File records the combine created are moved back to the book they belong
  to instead of being deleted.
- **A combine that stopped part way can be undone.** A combine now writes
  down each step before it takes it. If it stops on an error or a crash,
  reverting it undoes exactly the steps that ran.
- **Groups of versions are restored too.** Merging two books can also move
  the other versions of a book (for example the abridged and unabridged
  editions) into a new group. Reverting now puts those versions back in
  their old group. The merged books themselves stay in the trash, which is
  what the old undo did as well.
- **Older combines are covered by a one-time job.** A new maintenance job
  gives every combine made before this change its own undo number. It
  previews by default, writes nothing until told to, and records which undo
  number belongs to which combine.
- **Tested through the revert.** Each combine path has a test that combines
  books in a real database, reverts the operation, and checks that the books,
  their files and their version groups are back. The tests for the chapter
  merge, the same-file duplicate merge, the duplicate book merge and the bulk
  split-book merge run the job itself, so one that stopped passing its own
  number down would fail. The iTunes repair's test calls its duplicate
  collapse step directly, with the number given.

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
the run is linked under it and one Revert undoes the whole run. The
duplicate engine's merges each get a number of their own instead, since
grouping them would mean changing how every engine caller starts a merge;
the owner chose to keep one number per merge. Combines that run outside any
job, such as the manual Combine button, a review approval or a split-book
candidate merge, also get a fresh number. The manual Combine and split-book
merge responses return it, a review approval logs it, and the surviving
book's change history always lists it. If a combine's undo record cannot be
saved at the end, the Combine button says plainly that this one cannot be
undone and offers no number.

## Restoring version groups

**What it was.** A merge can carry the other editions of the merged book
into the survivor's version group. That move had its own undo record and its
own old undo address, and the Revert action did not know about it.

**Why it mattered.** Undoing only the combine would have left editions in
the wrong group, with the wrong book marked as the main edition.

**The fix.** The Revert action now understands this second kind of record
and puts each edition back in its old group with its old main-edition flag.
An edition that has since been moved to some other group is left where it
is. If a later merge moved an edition into the same group again,
the undo refuses as a whole and says why, rather than undoing that later
merge. The undo works even if the merge's surviving book has been purged
since.

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
anything. It reads the existing links once at the start without blocking
combines, then checks and writes each record while holding the same lock
combines use, so it cannot race a combine that is running. It records what
it decided for every record, including the ones it skipped and why. It
previews by default. The owner decides when to run it for real.

## Fixes from review

**What it was.** Review of #4064 found that reverting a combine could
rewrite a book the combine never retired: one it left alone, or one it never
reached because it stopped part way. Any edits made to that book since would
have been lost. It also found that the undo deleted file records the combine
had created, and that a combine that stopped part way could not be undone
at all.

**Why it mattered.** An undo that overwrites newer work is worse than no
undo. The owner's rule is never to delete file records.

**The fix.** The undo now checks that each book was sent to the trash by
this exact combine before restoring it, and leaves every other book alone.
Created file records are moved to the book whose file they are; one with no
such book left stays on the surviving book, and the revert's result names it.
A combine
now records each step before taking it, so a stopped combine undoes exactly
what it did. Each of these has a test that failed before the fix.
