<!-- file: docs/executive-summaries/2026-10-18-undo-every-book-combine-executive-summary.md -->
<!-- version: 1.10.0 -->
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
finalized at the end, the combine is still undoable: the record already lists
every step it took, so the Combine button still offers the number and says
the record is pending, and reverting it replays those steps.

## Restoring version groups

**What it was.** A merge can carry the other editions of the merged book
into the survivor's version group. That move had its own undo record and its
own old undo address, and the Revert action did not know about it.

**Why it mattered.** Undoing only the combine would have left editions in
the wrong group, with the wrong book marked as the main edition.

**The fix.** The Revert action now understands this second kind of record
and puts each edition back in its old group with its old main-edition flag.
An edition that has since been moved to some other group is left where it
is, and the revert's result says so. If a later merge moved an edition into the same group again,
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

## Fixes from the second review

**What it was.** A second review of #4064 found four ways a revert could
still go wrong. A combine that crashed right after sending a book to the
trash, but before noting it, left a book the revert would not recognise. A
title or author override was written before it was noted, so a failure at
the end left it impossible to undo. The revert renumbered the tracks of
books the combine never touched, wiping out later renumbering. And undoing
an edition move could try to put a reader's listening progress back onto a
book that no longer exists.

**Why it mattered.** Each one either lost a change the user made later or
left a combine that could not be fully undone. The owner's rule is that
listening progress is never removed.

**The fix.** A combine now notes each change before it makes it, or, for
the trash date, notes the exact date it is about to write. If the note
cannot be saved the combine stops instead of carrying on.
An override's undo now puts back the title, the narrator, the author and
exactly the field locks that were there before. Track numbers are restored
only on the files the combine actually moved. Listening progress with no
book left to go back to stays where it is, and both the revert and the
check before it say so. The first two are tested by stopping a combine (with an
override and a reader's progress) and a split-book merge at every write
they make, once just before the write and once just after it lands, and
then reverting. A stop in the middle of moving a reader's progress can
leave a copy of it on the surviving book as well; the progress on the
original book always comes back. The track-number and listening-progress
fixes each have a test of the exact case that went wrong.

## Fixes from the third review

**What it was.** A third review of #4064 found that an undo cut short by a
crash could never be finished: the record still said "combined", so every
retry saw the books it had already put back and refused. It also found:

- A manual combine that stopped part-way told the user only "failed",
  with no way to find the undo.
- The Combine message did not offer the undo it described.
- The nightly trash purge could race an undo and delete a book the undo
  had just put a reader's progress back on.
- A few smaller gaps: a co-author added after a combine was dropped by
  the undo, an author row could be created for an override that was then
  skipped, and some messages named the wrong place for kept progress.

**Why it mattered.** Each one either left a combine that could not be
fully undone, or put a reader's progress or a user's later edit at risk.

**The fix.**

- An undo now notes "started" before it changes anything, so running it
  again finishes the job. A test crashes the undo at 44 different points
  (just before and just after each thing it writes) and runs it again;
  every time, everything comes back.
- A combine that stops part-way now returns its undo number and says so.
  The Library message shows that number too, so the undo can still be
  found after the message is gone.
- The Combine message has an Undo button, and a Retry button when an undo
  only partly works.
- The purge now checks for progress and deletes the book in one step that
  an undo cannot interrupt. In that same step it looks at the book again,
  so a book someone restored from the trash a moment earlier is left
  alone instead of deleted.
- The undo keeps a co-author added since the combine and says so.
- An override creates a new author only after its plan is recorded.
- The kept-progress messages now name the right place.
- A split-book merge now handles a trash write that reports an error the
  same safe way a combine does.
- The code this pull request added no longer depends on the full
  database interface. That had turned a code-size check red.

Each of these has a test.

## Fixes from the fourth review

**What it was.** A fourth review of #4064 found:

- The undo had started deleting an author the combine created, with
  checks that could miss a book linked at the wrong moment, no record of
  the delete, and no way to undo it.
- The undo of a combine's author change dropped that same person's other
  credits on the book, such as narrator.
- The nightly trash purge made the chapter-repair tool reload the whole
  library, about 460 MB, for nearly every book it fixed while both ran.
- An undo that finished but could not save its "done" note could be run
  again, and that second run removed a lock the user had set since,
  subtracted the reader's listening time a second time, and reported
  changes it had not made.
- When an undo left something as it was, the message said "tags changed
  since the operation were left as they are", which is not what happened
  in a combine, and did not say what was left.
- A combine that stopped part-way lost its undo number in some error
  messages.

**Why it mattered.** The first two and the fourth could quietly lose a
credit, a lock or listening time someone had added. The third made a
routine repair run far slower and heavier than it needed to be.

**The fix.**

- Undoing a combine never deletes an author. When no book credits an
  author the combine created, the undo says so, and the existing clean-up
  job for empty authors removes it with its own safety checks and its own
  undo. A combine that cancels its own author or title change now says so
  on screen instead of only in the log.
- The undo takes back only the one author credit the combine wrote, so
  the same person credited since as narrator or co-author stays, and the
  undo says so.
- The purge still blocks merges while it deletes a book, but it no longer
  counts as a merge for the chapter-repair tool, which already sees every
  book the purge deletes.
- Running such an undo again now changes nothing done since. It puts a
  lock back only while it is still the one the combine set, it recognises
  listening progress it already put back, and it reports only what it
  actually changed.
- An undo that leaves something as it was now says so in plain combine
  terms and lists each item, in the Library message and the Activity log.
- The error for a combine that stopped part-way now always carries its
  undo number.

Each of these has a test.
