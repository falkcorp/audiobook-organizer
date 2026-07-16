<!-- file: docs/executive-summaries/2026-07-16-split-book-merge-orphan-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 9d2c4b71-3a68-4e05-bf19-6c7a08e4d3f2 -->
<!-- last-edited: 2026-07-16 -->

# Executive Summary: Stopping a Split-Book Merge From Orphaning Audio Files

## What changed

Sometimes a single audiobook gets imported as several separate entries — one per
chapter or disc. The app can stitch those back together with a "split-book merge":
it picks one entry to keep, moves every other entry's audio files onto the kept
entry, and then files the now-empty leftover entries away.

We found that if moving an entry's files failed partway through — a storage hiccup,
a locked file — the app still went ahead and deleted that leftover entry anyway. But
its files had **not** moved, so they were left pointing at an entry that no longer
exists: the audio effectively disappeared from the library even though the file was
still on disk. Worse, the web page reported the whole merge as a success, so there
was no sign anything had gone wrong.

The fix makes the app delete a leftover entry **only after** confirming its files
actually moved (or that it had no files to begin with). If a move fails, that entry
is now left exactly as it was — nothing is deleted — so its audio stays visible and
the operator can simply retry the merge.

## Why it mattered

This is a quiet data-loss path: no file is erased from disk, but the library's link
to that audio is broken, so books can silently drop out of view with a "success"
message shown. It only triggers when a file move fails mid-merge, so it was rare —
but when it hit, there was no error and no easy way to notice.

The fix ships with tests that deliberately simulate a failed file move and confirm
the affected entry is neither deleted nor stripped of its files, while a clean merge
still behaves exactly as before.
