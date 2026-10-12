<!-- file: docs/executive-summaries/2026-10-11-window-print-matching-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: e334db9f-27c6-4c39-9fb1-7654a5745fd8 -->
<!-- last-edited: 2026-10-11 -->

# The extra audio fingerprints are now put to use

Branch `task/02-pr8` (holistic plan 02, PR 8).

## Executive Summary

- **What was missing.** The Macs have been taking short audio fingerprints
  from the middle of every file (at 10, 50 and 90 percent of the way
  through), not just from the start, where most audiobooks share the same
  publisher intro. Nothing ever compared those fingerprints with each other,
  so they could not help find duplicates or match stray chapter files to the
  book they came from.
- **What changed.** The server now keeps a lookup table of those
  fingerprints, so it can find files that contain the same audio without
  comparing every file against every other one. It works even when the same
  audio starts a few seconds earlier or later in one file than the other. In
  tests on made-up fingerprints it found 99 of every 100 true copies and no
  unrelated ones.
- **Duplicates.** A match between two books' fingerprints now adds a small,
  fixed number of points (6) to the duplicate score. It can only back up
  other evidence; it can never by itself make two books look like
  duplicates. On the Duplicates page it shows as "Window AcoustID" in its own
  colour, and it is never listed as the reason a pair is on screen.
- **Chapter fragments.** On the Repairs page, a row that links a stray
  chapter to its parent book now also says how much of the chapter's audio
  was found in the parent ("window containment 0.92 ..."). This is
  information for you only. It never makes a row ready to apply. You still
  approve every row yourself, exactly as before.
- **Nothing happens until you run it.** The lookup table has to be built
  once with the new "Build window-print index" operation. Until it has
  finished a full pass, the new duplicate evidence stays switched off. The
  operation only previews unless you tell it to run for real, and it can be
  stopped and resumed.

## Details

- **Disk space.** For the 742,000 files the plan counts, the table needs
  about 1.6 GB (scaled up from a 4,000-file test database) and at most
  1.75 GB by the planned estimate, less if some files are short.
- **No audio is read on the server.** Everything uses fingerprints the Macs
  already stored.
- **Duplicate scores are now labelled with a new formula version.** Existing
  duplicate scores keep their old label and values until you run a rescore,
  which should be done at a time when no library scan is running. Nothing
  rescans on its own.
- **Switching it off.** Setting the new signal's points to 0 in the dedup
  settings turns its effect off; the chapter line is information only.
