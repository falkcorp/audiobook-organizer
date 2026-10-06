<!-- file: docs/executive-summaries/2026-10-05-fetched-matches-no-longer-vanish-executive-summary.md -->
<!-- version: 1.2.0 -->
<!-- guid: 66826c76-f279-4c56-85d5-cf3dab35eb52 -->
<!-- last-edited: 2026-10-05 -->

# Fetched matches no longer vanish

## Executive Summary

- **Matches were being thrown away.** The library looks books up with online
  providers such as Audible. It keeps the matches it finds so the owner can
  review them. A background job then fills in a book's Audible number (ASIN)
  every six hours, and that number is often taken from the very match waiting
  for review. Filling it in deleted the waiting match.
- **How many books.** On production, 1,078 books ended up with an Audible
  number, no review decision and nothing left to review. Another 421 lost
  their matches when their author or title was tidied up on 4 October.
- **The rule now.** A match is thrown away only when the book's title, or its
  author's name, changes. Those are the details the search was made from.
  Filling in an Audible number or an ISBN never throws a match away. If an
  Audible number is replaced with a different one, the match is kept but
  cannot be applied while the two numbers disagree.
- **Applying a match no longer deletes it.** Applying a match used to delete
  the very match that had just been applied.
- **Getting the lost matches back.** A new repair, "Refetch lost metadata
  candidates", is on the Repairs tab of the review page. It lists every book
  that had a match and lost it. For the books the owner selects, it asks the
  providers again and stores what they find for review. It never changes a
  book and never applies anything. It can be tried as a dry run first.
- **Still happens on purpose.** A title or author-name change still clears a
  book's matches, because they were searched for under the old name. When a
  repair tidies a junk title or relinks an author, the book loses its matches
  and nothing looks it up again automatically. The new repair finds those
  books too, but someone has to run it. Keeping such matches instead would
  need the database to re-check them against the new name. That is a bigger
  change and is left for later.
- **One automatic path now checks Audible numbers.** The overnight job that
  fills an empty title or author from a match heard in the book's opening
  narration now refuses a match whose Audible number disagrees with the
  book's. Matches can now outlive a number change, so without this check it
  could have written another book's title onto this one.
- **Applied books are not applied again (follow-up, same day).** Applied
  books now keep their matches, so the bulk "apply cached matches" action
  counted every already-applied book as something it would apply. It would
  have redone each book's file work, tag writing and iTunes update, and in
  "replace" mode could have overwritten the owner's later edits. It now skips
  them and reports them as "already applied". It still re-applies when the
  owner clicks Apply on that one book in the review lane.
- **A replaced Audible number now flags every kept match.** Some providers
  (Open Library, Google Books) give no Audible number, so the
  number-disagreement check could not catch their matches. Each kept match
  now records the Audible number it was found under. When the book's number
  is replaced or removed, a match without the new number cannot be applied
  automatically, and the background lookup searches that book again.
- **The single-book search window shows the disagreement.** A match whose
  Audible number disagrees with the book's is marked "ASIN conflict", and
  applying it asks for confirmation first. A match found under an older number
  is marked "Fetched for another ASIN".
- **The recovery repair no longer pauses library scans.** It only re-asks the
  providers and writes no book data, so library scans keep running while it
  works.
- **Undo keeps matches.** Undoing a change deleted the book's matches every
  time. Now the same rule applies as for any other edit: only a title or
  author-name change clears them.
- **Not yet done.** The repair has not been run on production. Running it, and
  then reviewing what comes back, is the owner's call.

## Matches deleted by number fills

**What it was.** The database treated any change to a book's Audible number
or ISBN as "this is a different book now" and deleted its stored matches.

**Why it mattered.** Filling in a missing number is the most common such
change, and it says nothing against the match. Often it confirms it.

**The fix.** Only a title or author-name change deletes matches. The apply
safety check refuses a match whose Audible number disagrees with the book's.
Since the follow-up it also refuses a match with no number that was found
under an Audible number the book no longer carries. Keeping the match is
therefore safe.

## Matches deleted by applying them

**What it was.** After applying a match, three apply paths deleted all stored
matches for the book, including the one just applied.

**Why it mattered.** When an automatic apply did not mark the book as done, the
book was left with neither a decision nor anything to review.

**The fix.** Those deletes are gone. If the apply changes the title or author,
the database still clears the old matches on its own.

## Recovering what was lost

**What it is.** A repair that finds books whose last lookup found a match but
which now have none, and that the owner has not ruled on.

**How it behaves.** It plans first and shows each book. Only the books picked
are looked up again, at the providers' normal speed limits. A book that has
been reviewed or looked up again since the plan is not looked up again.
