<!-- file: docs/executive-summaries/2026-10-05-linking-versions-brings-the-whole-family-executive-summary.md -->
<!-- version: 1.3.0 -->
<!-- guid: 0b6f6f3e-7a52-4c1b-9d0e-5e2c8a41d7b9 -->
<!-- last-edited: 2026-10-05 -->

# Linking versions brings the whole family

PRs: [#3758](https://github.com/falkcorp/audiobook-organizer/pull/3758) (follow-up to #3756), and a follow-up that makes undo safe

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
- **Which copy is kept has not changed.** Sound quality still decides which
  copy a merge keeps. A better-sounding copy is never thrown away just
  because a worse one has already been filed into the library.
- **The family's main version stays visible.** If the kept book is not yet
  filed into the library but another version in the family is, that version
  becomes the main version, so the title stays listed in Audiobookshelf. The
  owner chose this on 5 October 2026. Choosing the main version by hand
  still wins.
- **iTunes comes first.** If the kept book is in iTunes, it stays the main
  version so its iTunes track is not removed. If it is not filed into the
  library yet, Audiobookshelf will not show the title. The merge records
  each such case so they can be listed.
- **Listening progress follows the visible version.** Progress, read status
  and the Audiobookshelf link from the merged-away book go to the version
  Audiobookshelf shows. An undo reverses this too.
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
