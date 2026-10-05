<!-- file: docs/executive-summaries/2026-10-05-linking-versions-brings-the-whole-family-executive-summary.md -->
<!-- version: 1.7.0 -->
<!-- guid: 0b6f6f3e-7a52-4c1b-9d0e-5e2c8a41d7b9 -->
<!-- last-edited: 2026-10-05 -->

# Linking versions brings the whole family

PRs: [#3758](https://github.com/falkcorp/audiobook-organizer/pull/3758) (follow-up to #3756), a follow-up that makes undo safe, [#3762](https://github.com/falkcorp/audiobook-organizer/pull/3762), its review follow-up, a second review follow-up, and a third

## Executive Summary

- **A merged book's other versions now come with it.** When two books are
  linked as versions of each other, the book being folded in often already
  had its own family of versions (an organized copy, an earlier merge). Those
  used to stay behind in a group of their own. Now they join the kept book's
  family, as extra versions, and nothing about them is deleted.
- **Only the books you name are retired.** The book folded in is retired as
  before; the versions that come along with it stay in the library.
- **It can be undone.** Every merge that brings versions along first writes
  down which versions moved and where they came from. If it cannot write that
  down, it does not merge.
- **What an undo reverses: only the versions that came along.** Undo sends
  those versions back to their old family with their old "main version"
  setting, and makes sure both families have one main version afterwards. It
  does not bring back the book that was folded in; that is restored from the
  trash, or by the separate undo for automatic merges.
- **When an undo is refused.** An undo that has already been done is refused,
  and so is the undo of a merge that never happened (refused before it changed
  anything). It is also refused when a later merge moved the same version into
  the same family again: undoing the old merge would quietly reverse the newer
  one. A version someone has moved to a third family since is left there.
- **iTunes books and half-scanned books are protected.** If a version that
  would come along is an iTunes library book, or one whose files have not
  been fully scanned yet, the merge is refused and nothing changes. This
  matters because an iTunes original usually sits in the same family as its
  organized copy, and that copy is a common book to merge away.
- **Sound quality alone decides which copy is kept.** A better-sounding copy
  is never thrown away because a worse one is already filed into the library.
  Since the owner's decision at 13:00 on 5 October 2026, it is also never
  thrown away because someone has listened to the worse one. Their progress
  moves to the version Audiobookshelf shows.
- **The family's main version stays visible.** If the kept book is not yet
  filed into the library but another version in the family is, that version
  becomes the main version, so the title stays listed in Audiobookshelf. The
  owner chose this on 5 October 2026. Choosing the main version by hand
  still wins.
- **iTunes comes first.** If the kept book is in iTunes, it stays the main
  version so its iTunes track is not removed. If it is not filed into the
  library yet, Audiobookshelf will not show the title.
- **Every merge says whether Audiobookshelf will show the title.** Each merge
  reports which version is the main one and which holds the listening
  progress. When Audiobookshelf will not list the title, it also gives the
  reason: an iTunes book kept, no filed copy, a main version picked by hand
  that is not filed, or a quarantined copy. Every merge button in the app now
  reports this, including the bulk ones and the diagnostics suggestions.
  Background repairs that merge books still record only what they merged.
- **Listening progress follows the visible version.** Progress, read status
  and the Audiobookshelf link from the merged-away book go to the version
  Audiobookshelf shows. Every undo reverses this, including the undo of an
  automatic merge. Undoing a whole family move and then undoing the automatic
  merge also gives the listener their progress back.
- **Progress is never deleted with a retired book.** If the progress could not
  be moved at merge time, the book stays in the trash and is not
  permanently deleted. Before this, one rare failure moved nothing and left
  no record, and the nightly purge then deleted the progress for good.
- **Deleted versions come back to the right family.** A version that was in
  the trash when its family was merged away now comes back into the merged
  family when it is restored, not into the old family that no longer has
  anyone in it.
- **The iTunes clean-up uses the same merge.** It used to merge duplicates the
  old way and left their other versions behind. It now uses the same merge,
  with the same safety checks it had before.
- **Two safety checks are now tested.** A check that stops a merge from pulling
  an already-deleted book into the wrong family, and the rule that picks a
  family when two are equally large, now have tests that fail if either is
  removed.

## Versions left behind by a merge

**What it was.** Linking book B into book A moved B into A's family of
versions, but B's own family members stayed where they were.

**Why it mattered.** One title ended up split across two families: the kept
book in one, the folded-in book's other copies in another. Those copies looked
like a separate book that had never been linked.

**The fix.** A merge now moves the whole of the folded-in book's family into
the kept book's family. The extra copies stay live and are never the main
version; the old family is left empty. The owner chose this behaviour on
5 October 2026.

## Undo

**What it was.** Undo restored the two merged books from their saved
before-merge records. The copies that came along had no such record.

**Why it mattered.** Without a record, undo would have left those copies in
the kept book's family while the folded-in book went back to its old one.

**The fix.** Every merge that moves versions now keeps its own record of
them, whichever button or repair started it, and an undo action reads that
record to put them back.

## Undo that could reverse a later merge

**What it was.** An undo could be run twice. If the same versions had been
merged again in between, the second undo moved them back out of the newer
merge's family.

**Why it mattered.** One click could quietly unpick a merge nobody asked to
undo.

**The fix.** Each undo record now remembers which versions it has already sent
back. A finished undo is refused, and so is one where a newer merge holds the
same version. The undo of an automatic merge is refused the second time as
well.

## Listening progress lost at the purge

**What it was.** A merge moves a listener's progress to the version
Audiobookshelf shows, writing it down first so an undo can reverse it. If that
note could not be saved, the move stopped before anything moved and before the
usual safety record was written.

**Why it mattered.** The progress stayed on the retired book. Weeks later the
nightly purge permanently deleted that book, and the progress with it.

**The fix.** If the note cannot be saved, the progress is now moved anyway, with
its own safety record. Separately, the purge now refuses any retired book that
still holds someone's progress and lists it instead. Two clean-ups that
permanently delete duplicate books (the duplicate-version clean-up and the
iTunes regroup) first move that progress to the copy they keep, then delete
the duplicate. Undoing an iTunes library copy moves its listeners' progress
back to the original the same way. Each of these moves is all or nothing:
progress, bookmarks and the Audiobookshelf link must all arrive, or every
listener's progress is put back where it was and the book is kept. Before,
one listener's progress could move while another's did not. The undo also
refuses straight away when the original is in the trash. A user account the
safety check cannot read now stops the delete instead of being skipped.
Ordinary merged-away books, whose progress has already moved, still purge.

A third review found four more ways progress could end up rewound or out of
sight, now closed. If a listener kept playing a duplicate while its progress
was being moved, and the move was then called off, their newer position was
wound back; it is now kept, along with everything they had before. Progress
is only moved onto a kept copy that Audiobookshelf shows; if the kept copy is
hidden, the duplicate stays. When the iTunes regroup can only move progress to
a book Audiobookshelf does not show, it still moves it and now says so,
naming the books. Undoing an iTunes library copy puts the original back
first, and if any later step fails the progress goes to whichever copy
Audiobookshelf shows; running the undo again finishes it. Every one of these
deletes now checks one last time for new progress right before it deletes.

A fourth review tightened how called-off moves put progress back. If the
listener marked a book unfinished after finishing it, that later choice now
stands instead of the book snapping back to finished. When the two copies of
the progress cannot be put in time order, the one that is further ahead wins
and listened time keeps the larger value (owner decision). A reset is kept
cleanly, with the old positions cleared rather than left half in place. If
the listener played the kept book while the move was being called off, only
what the move brought there is taken back, so their listened time is no
longer counted on both books. The iTunes regroup no longer moves progress off
a book Audiobookshelf shows onto one it does not; it keeps the book and says
so. The duplicate clean-up checks that the kept copy is still shown at the
moment it moves the progress, not only a moment before.

## Undo that left progress on the wrong book

**What it was.** Some undo paths did not have the information they needed to
return a listener's progress. Undoing an automatic merge could not find its
record when the version holding the progress was already in the family, or
came in with a different book. Undoing a whole family move put progress back on
the kept book without writing that down. And a failed undo could not be retried.

**Why it mattered.** The restored book came back with no progress, and the
listener's Audiobookshelf link still pointed at another version.

**The fix.** Every such move is now recorded, every undo can find its record,
and an undo that fails part way finishes when it is run again.

A second case was found in review. If the kept book had since been merged into
the visible version, undoing the family move wrote over its own record. The
later automatic-merge undo then brought the book back with no progress. The
undo now keeps a separate record, and the progress comes back.

## A repair that finished into the wrong book

**What it was.** The repair that rebuilds books from loose chapter files
follows a merged-away book to the book it went into. It did this using the
Audiobookshelf link, which now leads to the visible version rather than the
kept one.

**Why it mattered.** The repair would have moved chapters onto a version the
owner did not choose to keep.

**The fix.** The repair now reads the merge's own record to find the kept book.
If the book was restored from the trash and merged again since, the newer
merge decides.

The merge report could also say a title was visible in Audiobookshelf when
it was not, if the visible version was in the trash or had stopped being the
main version. It now gives a reason in both cases.

