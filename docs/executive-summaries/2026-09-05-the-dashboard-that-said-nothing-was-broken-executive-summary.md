<!-- file: docs/executive-summaries/2026-09-05-the-dashboard-that-said-nothing-was-broken-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7e1c9a34-2f68-4d05-b9a7-3c6e05f1d824 -->
<!-- last-edited: 2026-09-05 -->

# The dashboard that said nothing was broken

**Pull request:** _pending — this branch._

## Executive Summary

- **What you saw:** the dashboard's "Broken Files" tile read **0**, even while books were
  failing to download because their files weren't where the app expected. The tile was
  telling you the library was healthy at the exact moment it wasn't. (You called it
  "obnoxious" — it was.)
- **Why it lied:** the tile was counting from an internal list that nothing keeps up to
  date any more. That list has had no writer for a long time, so it was always empty, so
  the count was always zero — regardless of how many books were actually broken. It wasn't
  measuring the library; it was measuring a bookkeeping leftover.
- **The fix, two parts:**
  1. **The tile now counts the real thing.** Each audio-file record already carries a
     "this file is missing" flag. The counter now tallies the distinct books that have at
     least one file flagged missing, instead of reading the dead list. So the number on the
     tile is now derived from the same truth everything else uses.
  2. **A new maintenance step keeps that flag honest.** Checking every one of the
     library's hundreds of thousands of files against the disk on every dashboard refresh
     would be far too slow, so the tile trusts the stored flag. The new step —
     "mark missing files" — is what makes the flag trustworthy: it checks each file against
     the disk and sets the flag where the bytes are gone **and clears it where they've come
     back** (for instance after a repair put the file back). Without the "clear it back"
     half, the count could only ever climb and would stay wrong forever after a repair.
- **Data safety:** this step never moves or deletes anything — it only sets a true/false
  flag. It defaults to a preview that reports exactly what it would change before it
  changes anything, and it re-checks each file one more time immediately before writing, so
  a file whose situation changed in the meantime is left alone rather than mislabeled.
- **One subtlety worth stating plainly:** the tile counts your *primary* copy of each book,
  not every redundant version of it. The new step's preview reports its "how many books are
  broken" number using that same primary-only rule, so the number it predicts and the
  number the tile shows are the same number — an operator comparing the two won't see a
  phantom mismatch.
- **How it was checked:** the fix was verified by breaking a file on a real test library,
  running the flag-writer, and confirming the counter moves from 0 to 1 — on both of the
  two internal paths that compute it — so the flag genuinely reaches the tile, not just the
  database.

## Why this belongs with the download story

This is the reporting half of the same problem covered in
[Books that would not download](2026-09-05-books-that-would-not-download-executive-summary.md).
That work fixed the cause of files going missing and built the tools to repoint the ones
already broken. But the dashboard was still reporting **0 broken**, which hid the scale of
the damage and made it look like nothing was wrong. A repair effort you can't see the
results of is hard to trust. This makes the counter tell the truth, and gives it a
maintenance step that keeps it true as the repairs land.

## The mechanism, briefly

The counter used to have a fast path that read a "files with errors, by book" index. That
index is written by an error-recording call that nothing live invokes any more, so it was
permanently empty and the count was permanently zero. The counter now derives the number
inline, the same way on both the fast in-memory path and the slower fallback path (the two
are held together by a shared conformance test so they can't drift): count the distinct
primary books that own at least one file whose stored "missing" flag is set.

The stored flag is only as honest as its last reconciliation, so the flag needs a writer.
The `mark-missing-files` maintenance step is that writer. It reconciles in both directions,
previews by default, writes only the flag, and re-checks each file at the moment of writing
so it never records a stale answer.
