<!-- file: docs/executive-summaries/2026-11-17-intro-clip-classification-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2ea20baf-f7c7-4c6f-99bf-ec253571136b -->
<!-- last-edited: 2026-11-17 -->

# The stored audio intros now say what they are, and can be re-read without listening again

**Date:** 17 November 2026. Covers one pull request. The new re-read job is
off until someone starts it, and it previews by default.

## Executive Summary

- For every audiobook file, the library keeps the words the speech-to-text
  worker heard in the first minute or so of audio. Until now those words were
  only mined for a title, author and narrator. Each stored intro now also
  records what kind of clip it is: a title announcement, credits, a chapter
  marker, a part marker, front or back matter such as a prologue, plain
  narration, music or silence, or noise. Chapter and part numbers and chapter
  titles are recorded too.
- Fixing the reading rules used to mean listening to the audio again, which
  takes hours of worker time across the library. A new job re-reads the stored
  words instead, with no audio work at all, so a rule fix reaches every file
  already processed.
- The job cannot make the library worse. A stored title, author or narrator is
  never emptied, and a stored value is replaced only when it is visibly broken
  (for example, it contains the publisher's name or runs on into the next
  sentence) and the new value is clean. A file marked "could not read a title"
  becomes "read" only when a title is actually found.
- Several reading mistakes were fixed along the way: Roman numerals and spelled
  out numbers in chapter headings, "Publisher presents, Title" announcements
  that left the publisher's name in the title, and the word "continued" in
  ordinary narration being taken for a chapter.
- The job previews first and reports how many files fall in each kind before
  and after, and which files cannot be re-read at all (no stored words).
  Starting it for real is a separate, explicit request, and it refuses to run
  while a library scan is in progress.

## The stored text was only used for three fields

**What it was.** The intro words were stored, but only the title, author and
narrator were kept from them. A clip that was really a chapter heading or a
stretch of narration looked the same as a clip that failed to produce a title.

**Why it mattered.** Nobody could tell how many files held a real title
announcement and how many held nothing useful, and any improvement to the
rules needed another full audio pass to take effect.

**The fix.** Each intro now stores its kind and the details found in it, next
to the existing fields, without changing what those fields mean.

## Re-reading is safe to repeat and to interrupt

**What it was.** A whole-library job that rewrites rows can be stopped
half-way, and can collide with another process updating the same file's row.

**Why it mattered.** A careless rewrite could undo a scan's fresh measurements
or leave the library half old and half new.

**The fix.** Each file is rewritten on its own, and only the fields the job
names; if another process changed the row in the meantime, the job reads it
again and decides again. Every change is idempotent, so running the job twice
changes nothing the second time, and an interrupted run continues from its last
saved position. A file that fails to save stops the saved position from moving
past it, so the next attempt tries it again.
