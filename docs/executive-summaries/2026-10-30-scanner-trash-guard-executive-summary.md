<!-- file: docs/executive-summaries/2026-10-30-scanner-trash-guard-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 76c36b08-b7a9-47dd-8192-301de0294df7 -->
<!-- last-edited: 2026-10-30 -->

# A library scan can no longer pull a deleted book out of the trash

**Date:** 30 October 2026

## What was wrong

When you delete books in bulk, some are put in the trash by a label alone. If one
of those books had a very small file (smaller than the minimum size the library
treats as a real audiobook), the next scan of that file marked the book
"suspicious". That label change quietly took the book out of the trash and back
into the library. Nobody restored it, no permission was checked, and nothing
recorded that it had happened.

Books deleted the other way (with a deletion flag) stayed hidden, but the scan
still overwrote the note of what state they had before, which a later restore
relies on.

## What changed

The scan now leaves the state of any trashed book alone, however it was trashed.
It still keeps the book's file details (path, size, checksum) up to date, so a
later restore starts from correct facts. Each scan's summary now says how many
trashed books it left alone, so the effect is visible.

## Why it mattered

This was a quiet way for deleted books to come back, so a deletion could not be
trusted to stay a deletion whenever a scan found a tiny or truncated file.

## What to expect

Nothing visible changes day to day. A trashed book stays in the trash until a
person restores it. A book that is not in the trash and has a tiny file is still
marked "suspicious", as before.
