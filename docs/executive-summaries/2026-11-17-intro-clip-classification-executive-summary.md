<!-- file: docs/executive-summaries/2026-11-17-intro-clip-classification-executive-summary.md -->
<!-- version: 2.2.0 -->
<!-- guid: 2ea20baf-f7c7-4c6f-99bf-ec253571136b -->
<!-- last-edited: 2026-11-17 -->

# The stored audio intros now say what they are, and nothing anyone relies on changed

**Date:** 17 November 2026. Covers one pull request. The new re-read job is
off until someone starts it, and it previews by default.

## Executive Summary

- For every audiobook file, the library keeps the words the speech-to-text
  worker heard in the first minute or so of audio. Until now those words were
  only mined for a title, author and narrator. Each stored intro can now also
  record what kind of clip it is: a title announcement, credits, a chapter
  marker, a part marker, front or back matter such as a prologue, plain
  narration, music or silence, or noise. Chapter and part numbers and chapter
  titles are recorded too.
- This is stored alongside, not instead. The title, author, narrator and the
  "read" or "could not read a title" mark on every file are exactly what they
  were, and so is the way new intros are read for them. The checks that decide
  which books a bulk change may touch, and the search that finds a book's
  metadata, read those fields, so none of them sees a different value.
- Filling in the new record for files already processed needs no audio work: a
  new job re-reads the stored words. It writes only the new record.
- The job also measures, without changing anything, how its reading compares
  with what is already stored: how many files agree, how many would have an
  empty field filled, how many disagree and in what way (a publisher's name in
  the title, a cut-off title, a different title, a series name that would be
  lost), and how many stored titles are visibly broken. That is the evidence a
  later decision about repairing stored titles would need.
- The job previews first. Starting it for real is a separate, explicit request,
  and it refuses to run while a library scan is in progress.

## The stored text was only used for three fields

**What it was.** The intro words were stored, but only the title, author and
narrator were kept from them. A clip that was really a chapter heading or a
stretch of narration looked the same as a clip that failed to produce a title.

**Why it mattered.** Nobody could tell how many files held a real title
announcement and how many held nothing useful.

**The fix.** Each intro can now store its kind and the details found in it, next
to the existing fields, without changing what those fields mean.

## An earlier version of this change rewrote stored titles, and was cut back

**What it was.** The first version of the job also corrected stored titles,
authors and narrators, and promoted files from "could not read a title" to
"read", using a newer reading of the same words.

**Why it mattered.** Review found that this could quietly change what other
parts of the library see: a book's series name could disappear from its stored
title, and a book marked as hands-off for bulk changes could lose that mark. The
newer reading is also free to be more aggressive than the one every other part
of the library was built around.

**The fix.** The two readings are now separate. The old one is untouched, and a
saved comparison over the whole existing corpus and every rule in its tests
shows zero differences. The new one only fills the new record. Repairing stored
titles, if it is ever done, is a separate decision to make from the job's
measured counts.

## Re-reading is safe to repeat and to interrupt, and a live run is not cheap

**What it was.** A whole-library job that rewrites rows can be stopped
half-way, and can collide with another process updating the same file's row.

**Why it mattered.** A careless rewrite could undo a scan's fresh measurements
or leave the library half old and half new.

**The fix.** Each file is written on its own, and only the new record; if
another process changed the row in the meantime, the job reads it again and
decides again. Running the job twice changes nothing the second time. An
interrupted run does not remember where it stopped: started again, it goes back
to the first file, reads every file it already finished, sees that each is
current and leaves it alone. That costs one extra read-only pass, and it is
deliberate. An earlier version remembered a position and skipped everything
before it, which would have skipped any file that appeared or moved in the
library between the two attempts while still reporting success. The job
works in passes over the library as it stands at the start of each pass, and
finishes successfully when a whole pass writes nothing and finds nothing that
moved. Files that appear after that last pass began are outside the run, and a
file that was moved to another book between a listing and its read is read again
under its new book. If passes keep producing writes, the job stops after five
and fails saying so; running it again picks up from there. A file that fails to save fails the run,
and the next attempt tries it again.

**The cost the job reports.** Saving the new record rewrites the whole stored
entry for the file, not just the few hundred bytes being added, and that entry
carries the file's audio fingerprint. A live run therefore rewrites the full
entry of every previously processed file. The preview says how many entries
that is and how many bytes in total (within ten bytes per entry, because the
saved entry carries a time stamp whose width varies), so the size is known
before anyone starts it; a live run reports the exact size of what it wrote. A
write that was applied but that the database did not confirm as durable is
counted on its own line, apart from errors. Because nothing reads the new record yet, a live run is not
recommended until something does; the preview is the useful part today. Storing
the record under its own small key, so that a re-read writes about two hundred
bytes per file, is filed as a follow-up for the owner to decide.

**One known gap.** A file that holds a transcript and none of the other stored
intro marks can only be found when it is the single file of a book that also
carries the transcript. Such a file in a book with several files, or in a book
with no transcript of its own, is not reached; the job counts it with the files
it did not select, and the gap is listed as a follow-up.
