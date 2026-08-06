<!-- file: todo.d/20260805_214100_chapters_backfill_from_duplicates.md -->
<!-- version: 1.0.0 -->
<!-- guid: 5c9e13ab-70d2-4f86-b451-2a86e0f37d94 -->
<!-- last-edited: 2026-08-05 -->

- [ ] **Backfill chapters into files that lack them, using a duplicate as the
  source of timings** — owner request 2026-08-05. Turn a chapterless M4B into a
  properly chaptered one by borrowing structure from another copy of the same
  book that already encodes it.

  Sources of chapter timings, in preference order:
  1. **Audible/provider chapter data** — check whether the metadata providers we
     already query expose chapter titles WITH start offsets. If they do this is
     by far the cleanest path and needs no duplicate at all.
  2. **A per-chapter duplicate.** A chapterless `Book.m4b` alongside a duplicate
     stored as N mp3s, one per chapter: each file's duration gives a chapter
     length, and the cumulative sum gives the offsets. Filenames often give the
     titles.
  3. **A playlist with timings** (see [[playlists-full-support]]) — cue sheets
     and some playlist formats carry explicit offsets.

  🔴 **GATE ON NEAR-EXACT ACOUSTIC MATCH.** Owner was explicit. Chapter offsets
  borrowed from a *different edition* — different narrator, abridged vs
  unabridged, a remaster with different silence padding — are worse than no
  chapters at all: they read as correct and silently mis-seek. Require an
  AcoustID fingerprint match well above the ordinary dedup threshold, and reject
  on ANY duration mismatch beyond a small tolerance. Absent fingerprint must mean
  "cannot apply", never "assume it matches" — same rule as
  [[version-group-acoustic-audit]].

  Also verify the summed chapter durations reconcile to the target file's total
  runtime before writing; a shortfall means the duplicate is incomplete (the
  Successors debris covered 12 of 13 tracks, which would have silently truncated).

  Write path: chapters go into the M4B container. Treat it as a tag write with
  the usual safety — this repo's dominant incident class is write-back wipes, and
  `books/itunes/**` remains hands-off regardless.

  Depends on [[chapters-served-to-clients]] to know which books lack chapters.
