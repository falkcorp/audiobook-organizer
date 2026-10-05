<!-- file: docs/executive-summaries/2026-10-05-linking-versions-brings-the-whole-family-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 0b6f6f3e-7a52-4c1b-9d0e-5e2c8a41d7b9 -->
<!-- last-edited: 2026-10-05 -->

# Linking versions brings the whole family

PR: [#3758](https://github.com/falkcorp/audiobook-organizer/pull/3758) (follow-up to #3756)

## Executive Summary

- **A merged book's other versions now come with it.** When two books are
  linked as versions of each other, the book being folded in often already
  had its own family of versions (an organized copy, an earlier merge). Those
  used to stay behind in a group of their own. Now they join the kept book's
  family, as extra versions, and nothing about them is deleted.
- **Only the books you name are retired.** The book folded in is retired as
  before; the versions that come along with it stay in the library.
- **It can always be undone.** Every merge that brings versions along first
  writes down which versions moved and where they came from. If it cannot
  write that down, it does not merge. Undo sends those versions back to their
  old family with their old "main version" setting, unless someone has moved
  them somewhere else since, and makes sure the old family has one main
  version again.
- **iTunes books and half-scanned books are protected.** If a version that
  would come along is an iTunes library book, or one whose files have not
  been fully scanned yet, the merge is refused and nothing changes. This
  matters because an iTunes original usually sits in the same family as its
  organized copy, and that copy is a common book to merge away.
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
