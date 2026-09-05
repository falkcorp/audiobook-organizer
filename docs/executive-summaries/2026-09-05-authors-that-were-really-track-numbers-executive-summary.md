<!-- file: docs/executive-summaries/2026-09-05-authors-that-were-really-track-numbers-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5b8e2c4f-7a19-4d63-b0e5-2f9c1a7d3e84 -->
<!-- last-edited: 2026-09-05 -->

# Authors that were really track numbers

**Pull requests:** https://github.com/falkcorp/audiobook-organizer/pull/3062 (stop
creating them), https://github.com/falkcorp/audiobook-organizer/pull/3063 (the repair
tool), https://github.com/falkcorp/audiobook-organizer/pull/3068 (the last bucket, and a
safety fix found on the way). The repair itself ran against the production library on
5 September 2026.

## Executive Summary

- The library's list of authors held 19,978 names. About one in eight was not a
  person. They were things like "01 - Chapter One", "Track 03", "64kbps", the title of
  a chapter, or the title of the book itself — usually with a number stuck on the
  front. They came from imports where the file-naming scheme puts a track number where
  the app expected an author's name.
- Every one of them was a real entry in the app: it appeared in the author list, in
  search, in the phone app's author browser, and books were filed under it. A reader
  browsing by author would scroll past hundreds of "authors" that were chapter titles.
- The cleanup sorted the numbered names into three kinds and treated each correctly.
  79 were numbered copies of a real author ("03 - Brandon Sanderson") and were merged
  into the real one, with their books moved across. 812 became a chapter or book
  title once the number was removed, so there was nothing to merge into; they were
  deleted. 1,610 were pure shrapnel — numbers, bitrates, "Track 12" — and were
  deleted.
- The author list went from 19,978 entries to 17,477. 4,012 books were touched. Both
  runs finished with zero failures.
- About 3,370 books now have no author on record at all, because the junk entry was
  the only "author" they ever had. They are still in the library, still playable and
  still findable by title. Filling in their real authors is the next job: the bulk
  metadata fetch, which asks outside sources for each book's details.
- Nothing was deleted blind. Every deletion was preceded by a dry run that listed
  every row it would remove, and the 812-row list was read through by hand before
  the real run. A separate safety fix means the delete path now refuses to remove an
  author if it could not first move the author's books somewhere safe.

## What changed

- **New entries are stopped at the door.** The import path that produced these names
  now recognises a track number in the author position and refuses to create an
  author from it, so the list does not refill as new books arrive.
- **A repair tool that knows the difference between the three cases.** Merging a
  numbered copy into the real author is the right fix for one kind of row and the
  wrong fix for the other two; deleting is right for those and would lose books for
  the first. The tool classifies first and acts second, reports its counts before
  anything is applied, and treats the 812 "title, not person" rows as a separate,
  opt-in step so they could be reviewed on their own.
- **An author is never deleted with its books still pointing at it.** Each book is
  first moved to its surviving authors, and if the book has none left, it is
  recorded as such rather than left pointing at an entry that no longer exists. If
  that hand-over fails for any reason, the author is left alone and the failure is
  counted. The earlier version of this code would have deleted the author anyway;
  that is the class of bug that left about 212 books pointing at nobody earlier in
  the summer.

## What this does not do

- It does not invent authors for the 3,370 books left without one. That needs
  outside information, which is what the metadata fetch is for.
- It does not touch a further 662 entries that fail the "is this a person" test for a
  reason other than numbering — publisher names, copyright lines, "translator"
  suffixes. Some of those name real people. They are a different defect and were
  deliberately left for their own pass.
- It is slow. Deleting one author still means reading the whole author-to-book index,
  so the 1,610-row run took about half an hour. Reading that index from memory
  instead would make each delete near-instant; that is a small, known change that
  has not been made yet.
