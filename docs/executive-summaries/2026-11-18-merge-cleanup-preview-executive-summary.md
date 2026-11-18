<!-- file: docs/executive-summaries/2026-11-18-merge-cleanup-preview-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: fea71315-0fbe-4b70-a41a-0174fad61b55 -->
<!-- last-edited: 2026-11-18 -->

# A new review list shows which merges look mistaken, and changes nothing

**Date:** 18 November 2026. This is the first step of a planned series. It adds
a list to look at. It does not fix, merge, split or delete anything, and it has
no button that does.

## Executive Summary

- When two books are merged, the losing book keeps a pointer to the one it was
  merged into. Some of those merges were wrong: two different books were joined,
  or a short fragment of a book was kept while the full recording was set aside.
  There was no single place to see which merges look wrong.
- The Repairs tab now has a "Fix mistaken merges" list that goes through every
  book that was merged into another and sorts each into one of three kinds of
  mistake, or says plainly that it cannot tell.
- The three kinds are: two different works that should be separated again, a
  fragment kept in place of the full recording (the two should swap roles), and
  a pointer that leads to a book that has itself been set aside (it should lead
  to the live book instead).
- A row only proposes a fix when the evidence agrees: different titles, different
  authors or a different place in a series, and matching or clearly different
  running times. A row with one weak signal, or with a running time that is
  missing or could be two parts of one book, is listed with its reasons and no
  proposal.
- Rows the owner already refused to join, rows that belong to the version-group
  repair, books holding retired file versions, and merges that Audiobookshelf
  clients still follow are all listed as held, with the reason, so none of them
  is proposed by accident.
- The list reports counts: how many rows of each kind, how many were held and for
  which reasons, and how many looked like the earlier automatic merge pass
  (it leaves a recognisable mark) versus some other cause. Those counts are what
  the later steps of the plan are sized from.
- This change was checked against test libraries only. It has not been run against
  the real library, so the real counts are not known yet.

## The list of mistaken merges

**What it was.** The system could say a book had been merged away, but not
whether the merge made sense. Finding the wrong ones meant opening books one at a
time.

**Why it mattered.** A wrong merge hides a real book behind another one, or keeps
a fragment as the "main" copy. Fixing them by hand is slow, and fixing them in
bulk without evidence risks making things worse.

**The fix.** A read-only list that works out each book's evidence from its title,
authors, series position, running time, file sizes and file fingerprints, shows
that evidence next to each row, and holds back anything it cannot be sure of. It
reads the whole library using several cores and writes nothing; a test checks that
a full run leaves every book and file record exactly as it was.
