<!-- file: docs/executive-summaries/2026-10-29-swap-stuck-recovery-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 693e173d-2d71-4264-a893-4b9413529bb2 -->
<!-- last-edited: 2026-10-29 -->

# A book whose version switch stopped half way can now be found and repaired

## Executive Summary

A book can keep several versions of itself, and moving the playing version to the
trash first switches the book to another version. If that switch stopped half way
(the audio files had moved but the app could not write down that they had), the
book was left with no playing version, nothing listed it, and the only repair was
by hand. This change adds the missing pieces: a list of the books in that state,
a Resume button that repairs one, and a refusal to trash anything of such a book
until it is repaired.

## What changed

- **Such books are listed.** Version Management has a third tab, "Unfinished
  swaps". Each book is shown with its folder, each version's status and the
  audio files each version owns, and either a Resume button or the reason a
  resume would be refused.
- **Resume reads the disk before it acts.** For every file it checks whether the
  file sits in the book's folder or in its version's own storage folder, and it
  only trusts what it finds there. If every file is already where the switch
  wanted it, the switch is finished: only the records are brought up to date and
  no file is moved. If the files are not where it wanted them, they are moved back
  where they were and the old version plays again. It refuses, and changes
  nothing, when the disk and the records disagree in a way it would have to guess
  at (a file in both places, a file in neither, a shortcut instead of a file, a
  file another record claims, a second playing version).
- **A half-finished switch blocks trashing.** Deleting any version of such a book
  is refused with a message pointing to Resume. This matters most for the rarer
  case where only the last status write failed: the old version already reads as
  an ordinary alternate, and a second delete would have trashed it while the book
  still played its files.
- **The nightly clean-up reports them.** The clean-up lists these books in its
  result and log, and the operation page links to the new tab. It never resumes
  one by itself: resuming can move files, and that belongs to a person.
- **Resume asks for the same permission as the switch it repairs** (organize the
  library), and it waits for any running merge or combine like the switch did.

## What to know

- Resume is run from the page, one book at a time, after a confirmation that says
  whether files will be moved.
- A book the page cannot repair is left exactly as it was, and the reason is shown
  next to it.
- Deluge is told about the finished switch when both versions are known; when only
  the entering version is known it is not told.
