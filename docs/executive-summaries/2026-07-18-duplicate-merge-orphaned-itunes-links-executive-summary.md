<!-- file: docs/executive-summaries/2026-07-18-duplicate-merge-orphaned-itunes-links-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 2f8a6c34-9b71-4d02-8e15-3a7c0d5e9b46 -->
<!-- last-edited: 2026-07-18 -->

# Executive Summary: Fixing How Merging Duplicate Books Handled iTunes Links

## What changed

When the library finds two entries that are really the same audiobook, you can
merge them: keep one, drop the other. The older "duplicate merge" button did the
drop by **permanently deleting** the extra entry outright.

The problem was what that deletion left behind. Each book can carry links to
iTunes — the identifiers iTunes uses to recognize a track. Permanently deleting
the extra entry did **not** clean up those links, so:

- the iTunes identifiers kept pointing at an entry that no longer existed, and
- the extra entry's tracks were never removed from iTunes, so iTunes kept
  showing the duplicate forever.

A later iTunes import could then follow one of those stale links straight to a
deleted entry.

The fix routes the duplicate-merge button through the app's newer, safer merge
path — the same one the rest of the app already uses. That path moves the iTunes
links over to the kept entry, tells iTunes to remove the duplicate's tracks, and
**retires the extra entry instead of erasing it**, so it can still be recovered.
It also carries the extra entry's iTunes listening stats (rating, play count,
etc.) onto the kept entry so nothing visible is lost in the switch, and it now
correctly refuses to delete the very entry you asked to keep even if it was
listed on both sides of the merge.

## Why it mattered

This is a quiet data-integrity path: no audio file was erased, but the library's
iTunes links were left dangling and duplicate tracks were stranded in iTunes,
with no error shown. Because the old button deleted entries outright, a merge
that hit this bug could not be undone.

The fix ships with tests that merge books on a real database and confirm the
dropped entry is retired (not erased), its iTunes link now resolves to the kept
entry, its listening stats moved across, and the kept entry can never be
accidentally deleted or demoted when it appears on both sides of a merge.
