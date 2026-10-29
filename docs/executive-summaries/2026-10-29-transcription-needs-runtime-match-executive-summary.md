<!-- file: docs/executive-summaries/2026-10-29-transcription-needs-runtime-match-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8e2b4d71-6c0a-4f93-b5d8-1a7c3e9f2046 -->
<!-- last-edited: 2026-10-29 -->

# A short piece of a long book can no longer pass as the whole book

## Executive Summary

When the app listens to the first minute of a book, it reads out the title and
author, and that is a strong clue about which book it is. The app used to trust
that clue so much that it nearly settled the question by itself. The trouble is
that every disc or part of a long book starts with the same introduction. So one
disc of a long book, sitting in a folder by itself, scored as if it were the whole
book, and the app was willing to write the whole book's details onto it. This
change makes the app ask a second question before it trusts the clue: does the
running time of the match fit the running time of the files? If it does not, or
the app cannot tell, the clue is set aside and the book goes to a person.

## What changed

- **The clue now has to fit the running time.** The "heard in the intro" boost
  only counts when the match's length is within 10% of the length of the book's
  files. A book whose length cannot be worked out yet (some files unmeasured,
  sizes missing) does not get the boost either. Before, it did.
- **The reason is shown, not hidden.** A match the app set aside this way carries
  a warning in the review screen saying why: it looks like a piece of a longer
  book, the lengths disagree, or the length could not be compared. The score
  breakdown shows the step with a plain explanation, and a new switch lists only
  these books. The app only calls something a piece of a longer book when the
  files themselves prove it, never from a guess about the audio quality.
- **Every route that writes is covered.** The bulk apply, the version upgrade, the
  nightly match of heard titles and the automatic fetch all check the running time
  at the moment they would write, against the files as they are then. A match
  saved before this change, with no warning on it, is checked the same way. The
  owner can still apply a match by hand from the review screen after looking at it.
- **No pointless searching.** A match refused for its running time does not send
  the book off to try other sources, since another source cannot change the length
  of the files.

## A real example

A book whose files were named like "CD 01- 001" held two files with no sizes
recorded, and only one of them had been measured, at 7 hours 46 minutes. The
intro matched a long book, the score was doubled, and the match came out at 3.09,
well above the bar. With one file unmeasured and no sizes recorded, nothing proved
the files were the whole book. The score now stays where it was before the boost, the warning shows
"the book's total runtime is unknown", and the book waits for review.

## What to know

- Books that were already applied are not touched.
- Some correct matches will now wait for review until their files are measured.
  That is intended: a match the app cannot check is a match it will not write.
- Running the length measurement over a book's files, and recording file sizes,
  is what lets such a match go through on its own again.
