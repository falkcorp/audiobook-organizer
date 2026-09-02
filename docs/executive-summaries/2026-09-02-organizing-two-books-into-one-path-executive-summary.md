<!-- file: docs/executive-summaries/2026-09-02-organizing-two-books-into-one-path-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 6d2f8b41-3a9e-4c57-b8e1-9f0c2d7a5e63 -->
<!-- last-edited: 2026-09-02 -->

# Organizing two books into one path

**Pull request:** [#3046](https://github.com/falkcorp/audiobook-organizer/pull/3046)

## Executive Summary

- When the organizer moves files into their tidy `Author/Title/` folders, two different
  books can work out to the *same* destination file — the classic shape of a duplicate
  that has not been merged yet. The organizer runs many books at once, so two of them
  reaching the same spot at the same moment is not rare.
- Until now that collision could quietly corrupt a file. Both copies wrote into one
  shared scratch file and the last one to finish "won", leaving a file that was a mix of
  the two. A deliberate test of this reproduced the corruption 30 times out of 30.
- Separately, the book that lost the race simply assumed the file already there was its
  own and recorded it — so the library could point one book at another book's audio, with
  nothing in any log to say so.
- Both are fixed. Each copy now writes to its own private scratch file that no other copy
  can open, and the final step refuses to overwrite anything already at the destination.
  A book adopts an existing file only after proving it is the same file or the same
  content; otherwise it leaves its record alone and writes a warning naming both paths.
- This closes the door; it does not look for files already damaged by it. Whether any
  existing file was corrupted this way is not known — a mixed file has the right name and
  size, so only a content check could tell, and none has been run. The organizer's
  cleanup step still removes the new scratch files.
- Every safety check added here was tested by removing it and confirming the test fails
  (8 of 8 caught; 2 checks are belt-and-braces duplicates of another and were documented
  as such).

## What this does not cover

Two books that legitimately share one destination are still two books; this change stops
them damaging each other's files, it does not merge them. That is the dedup lane's job.
