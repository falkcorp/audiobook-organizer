<!-- file: docs/executive-summaries/2026-11-17-intro-clip-classification-executive-summary.md -->
<!-- version: 2.0.0 -->
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

## Re-reading is safe to repeat and to interrupt

**What it was.** A whole-library job that rewrites rows can be stopped
half-way, and can collide with another process updating the same file's row.

**Why it mattered.** A careless rewrite could undo a scan's fresh measurements
or leave the library half old and half new.

**The fix.** Each file is written on its own, and only the new record; if
another process changed the row in the meantime, the job reads it again and
decides again. Running the job twice changes nothing the second time, and an
interrupted run continues after the last file it finished, even if files were
added or removed meanwhile. A file that fails to save stops the saved position
from moving past it, so the next attempt tries it again.
