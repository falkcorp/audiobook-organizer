<!-- file: docs/executive-summaries/2026-07-13-merge-serialization-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 3b7f0c92-6a41-4d58-8e12-9c05a2f7e6d4 -->
<!-- last-edited: 2026-07-13 -->

# Executive Summary: Preventing Merged Books From Corrupting or Disappearing

## What changed

When the app finds two entries that are really the same audiobook, it can
"merge" them: it keeps the best copy and files the others away as alternate
versions. Several parts of the app can do this at the same time — the automatic
duplicate scan, the auto-resolve pass, and manual merges you trigger from the
web page — and they all share the same merge machinery.

We found that the merge step wasn't protected against two of these running at
the exact same moment on a book they both touch. When that happened, the two
merges could step on each other mid-write and leave the library in a bad state:
a book marked as both the "kept" copy and a "filed-away" copy at once, a group
of versions split in two, or — worst case — the copy meant to be *kept* getting
filed away instead, so a whole set of versions quietly dropped out of the
library view.

The fix makes each merge finish completely before the next one starts, so two
merges can never interleave on the same book. This covers every path that
merges books, including manual merges from the web page.

## Why it mattered

Automatic merging is on in production, so this could silently corrupt a version
group or make books appear to vanish from the library with no error shown. No
data was actually deleted from disk — the audio files are always left alone —
but the library's record of which copy is the "real" one could be scrambled.

## Also hardened

- **Undo safety for automatic merges:** an automatic merge now records its
  "how to undo this" entry *before* it makes the change, not after. Previously a
  crash at the wrong moment could leave a completed merge with no way to reverse
  it. If that safety record can't be written, the merge is skipped rather than
  done blindly.
- **No double-merging in a batch:** the (currently-off) AI-review auto-merge
  path now skips a book that was already merged away earlier in the same batch,
  and tidies up leftover duplicate suggestions afterward.

All three fixes ship together and are covered by new tests, including one that
deliberately runs many merges at once and confirms they no longer collide.
