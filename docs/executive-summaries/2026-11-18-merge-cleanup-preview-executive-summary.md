<!-- file: docs/executive-summaries/2026-11-18-merge-cleanup-preview-executive-summary.md -->
<!-- version: 1.4.0 -->
<!-- guid: fea71315-0fbe-4b70-a41a-0174fad61b55 -->
<!-- last-edited: 2026-11-19 -->

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
- A row only proposes that two books be separated when at least two signs agree
  that they are different works: titles that share nothing, authors that share no
  name, or different places in the same series. Even then it holds back if the
  titles match (or one book is also known by the other's title), if the two files
  are the same recording, or if the running times and file sizes are identical.
  When a fact is missing or unreliable (a placeholder such as "Unknown" for a title
  or author, an author record that no longer exists, a file with no measured
  length or no recorded size, or chapters that are listed but no longer on disk
  and would change the answer) the row is listed with its reasons and no proposal.
  A pair where one book's author is the other book's title (a book whose title and
  author were swapped, stored either way round) is sent to the repair that exists
  for that, not proposed here.
- Rows that belong to the version-group repair, books holding retired file
  versions, and merges that Audiobookshelf clients still follow are listed as
  held, with the reason, so none of them is proposed by accident. A pair the
  owner already refused to join is held whenever the proposal would say the two
  are one work (a swap, or moving a pointer onto the other book); a proposal to
  separate them agrees with the refusal, so it is shown, marked with the owner's
  refusal.
- When a pointer leads to a book that has been set aside, the list follows the
  trail further, to the live book the content went to. It looks at every book on
  the trail that was itself merged away, whether or not it was set aside, and at
  every other book merged into the same missing book (set aside or not, with or
  without a file), together with the books on their trails. For each of them it
  looks at the books holding its old file versions and at the other books in its
  version group, including group members that were set aside but still point on.
  It proposes pointing at a live book only if that book really looks like the
  same work. A live book that was also merged into a missing book is counted as
  another live copy of the work, not as nothing. A book is said to stand on its
  own only when none of this leads to a live book, no other live copy is left
  behind the same missing book, and the version group has no live book left. If
  the group still has a live book but no main version to point at, if the trail
  loops or runs on without an end, or if several live copies lead to the same
  missing book, the rows are held and nothing is proposed. A separation of that
  kind keeps the book in its version group, so a book that would be left as its
  group's only live member with no main version is held for a main-version
  hand-off.
- The list reports counts: how many rows of each kind, how many were held and for
  which reasons, and how many looked like the earlier automatic merge pass
  (it leaves a recognisable mark) versus some other cause or an unknown one. Every
  row is in those counts, including the ones that are held or could not be read.
  Those counts are what the later steps of the plan are sized from. If the
  library's in-memory index is found to be missing books, the list refuses to run
  and says by how many, rather than judge a library that is not whole.
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
