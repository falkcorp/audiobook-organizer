<!-- file: docs/executive-summaries/2026-09-07-the-books-that-quietly-stopped-getting-organized-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 0c4a6d31-8f5b-4e2a-9d17-3b6e5a0c4712 -->
<!-- last-edited: 2026-09-07 -->

# The books that quietly stopped getting organized

**Pull requests:** #3098, #3100 (merged), #3096, #3099.

## Executive Summary

- **What happened:** four separate faults were all doing the same thing to your library —
  quietly dropping books out of the organizing pipeline and reporting success anyway. Each
  one on its own looked like a small oddity. Together they meant a book could be approved,
  fetched, matched and then simply never end up where it belonged, with nothing anywhere
  saying so.

- **What you would have noticed:** books you had approved sitting unchanged in the library.
  Books that had been filed correctly showing up as "not organized" again after a routine
  rescan. A bulk apply that ran, finished, and left half the batch untouched. In every case
  the app said the work had completed.

- **Was any data lost? No — but work was.** Nothing was deleted or overwritten wrongly. What
  was lost was the *instruction*: the app forgot it still owed you the change. That is
  cheaper than losing files, and much harder to spot, because a library that never got
  organized looks exactly like a library that was never asked to be.

### The four faults, in plain terms

- **A rescan un-organized your books (#3098).** Scanning the library re-reads what is on
  disk. It was also resetting each book's status back to "just imported," even for books that
  had already been filed correctly. From then on those books were treated as unfinished work,
  and they dropped off the organized view. Scanning now leaves an already-organized book's
  status alone.

- **Books that were already in the right place never got marked as such (#3100).** When the
  organizer looked at a book, found it already exactly where it belonged, and had nothing to
  move, it would sometimes skip recording "yes, this is done" — but only when the run had not
  been started through the normal jobs list. So the same books were re-examined every time,
  forever, and never counted as finished. They are now stamped as organized whether or not a
  formal job wrapped the run.

- **A file already sitting in the target spot failed the book permanently (#3096).** If the
  place a book was supposed to move to was already occupied, the move failed — and it failed
  the same way every single time it was retried, so that book could never be organized again.
  It now works out what the occupant actually is. If it is the same audio content already in
  your library, the duplicate is cleared away and the records are tidied up. If it is a copy
  of a file from outside the library, the library's copy is linked to the original rather
  than duplicated on disk, and its tags are rewritten in full. Only genuinely unclear cases
  are set aside for a person to look at, and even those no longer block the rest of the run.

- **Restarting the server threw away a bulk apply in progress (#3099).** Applying saved
  metadata to a few hundred books takes a while. Restarting the app halfway — an ordinary
  deploy — abandoned the run: the books already done kept their metadata, and everything
  still queued was silently forgotten. On one 699-book batch the remaining 350 simply never
  happened. The run now records its progress as it goes and carries on after a restart with
  the books it still owes, counting against the batch you actually started rather than
  restarting the numbers. A second batch queued behind the first no longer cancels it. A
  single book that cannot be applied is passed over and reported instead of failing the whole
  run — and a book skipped only because the system was momentarily busy is retried at the end
  of the run rather than dropped.

### Why these are grouped together

Each fix on its own reads as a minor correctness bug. The pattern is what matters: in all
four cases the app treated "I could not do this" as "there was nothing to do," and reported
completion. That is the failure mode that costs you a library slowly enough that nobody
notices. The specific fixes are above; the general change is that each of these paths now
either finishes the work or says out loud that it did not.

### What this does not cover

These changes are on the code branch, not yet on the running server — deploying them is a
separate, deliberate step. Until then the behaviors above are unchanged in day-to-day use.
