<!-- file: docs/executive-summaries/2026-10-29-purge-kept-files-sweep-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2b6d9f3e-8a41-4c70-9d5e-0f7a3c1b8e64 -->
<!-- last-edited: 2026-10-29 -->

# Audio files that a clean-up skipped are now removed once nothing needs them

## Executive Summary

When an old version of a book is purged, the app deliberately leaves behind any
audio file that something else still points at: another record, or a book sitting
in the trash that could still be restored. That is the safe choice. But nothing
ever came back to those files. If the other book was later deleted for good, its
file stayed on disk with nobody owning it, taking up space and invisible to the
owner. The nightly clean-up now comes back to each skipped file, asks the same
questions the original purge asked, and removes the file only if nothing needs it
any more.

## What it was

A purge writes down every file it skipped and why, on the purged version. That
list was only ever shown once, in the night's report. After that, no job read it.

## Why it mattered

Every skipped file is a file the owner would have to find and delete by hand, with
no list telling them where it is. Over time these add up as disk space lost to
files the library no longer knows about.

## The fix

The nightly clean-up ends with a pass over those lists. For each skipped file it
repeats the purge's own checks, in the same code, so the two can never disagree:
a book that still plays the file, any other record or book (trashed ones too) that
names it under any spelling of its path, the protected places (import folders, the
iTunes library, failed-import folders, anything outside the library), and whether
the disk can really say the file is gone. A file nothing needs is removed and
crossed off its list. A file that is still needed stays, with its note. Any check
that cannot be answered, such as a failed read, an unmounted library or a busy
merge, leaves the file in place and tries again the next night. The iTunes library
is never touched.

The night's report now says how many files were removed, how many notes were
dropped because the file was already gone, how many files are still needed, and how
many were left because a check could not be answered.

## Verification

Tests cover a file whose owner is gone (removed), a file still owned (kept), a
failed read (kept), an iTunes file (kept), a file a live book plays (kept), a
library that is not ready (kept), a failed removal (kept) and a second run (does
nothing), and run a pool of workers over many purged versions under the race
detector. Each key check was confirmed to fail when the code under test was broken
on purpose.
