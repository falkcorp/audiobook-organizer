<!-- file: docs/executive-summaries/2026-07-13-author-series-data-loss-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 7d2a9f31-6c48-4b0e-9a15-3e8b7c0f4d62 -->
<!-- last-edited: 2026-07-13 -->

# Executive Summary: Stopping Silent Author and Series Loss on Book Edits

**Shipped:** follow-on to the July 11 organizer fix
([#1887](https://github.com/falkcorp/audiobook-organizer/pull/1887) confirmed it,
commit 98c2a218 patched the first spot). This change closes the underlying gap
and the remaining places it could bite.

## Executive Summary

Some routine background operations could quietly erase a book's **Author** and
**Series** from the saved record. Nothing was shown to be wrong at the time —
the information just disappeared from storage on the next save.

We found the single root cause behind it, fixed it in one central place so it
can't recur across the whole class of operations, and then fixed the two
remaining operations that were still tripping over it. We also fixed one more
spot that wasn't in use yet but would have caused the same loss the moment it
was turned on.

## What was wrong, in plain terms

When the app saves a book, it saves the full record. But several operations save
a **lightweight copy** of a book that deliberately leaves out heavy fields to
run fast. The save routine already knew to protect most of those heavy fields —
if the lightweight copy left them blank, it kept the previously-stored values
instead of wiping them.

Author and Series were missing from that protected list. So any operation that
saved a lightweight copy — for example, the maintenance job that splits a
combined "Author A & Author B" entry into two separate authors — would blank out
the stored Author and Series on that book.

The author-splitting job had an extra wrinkle: it was also *changing* the
author, so simply keeping the old value would have left the book labeled with the
now-wrong combined author.

## What we changed

1. **Fixed the root cause once, centrally.** The save routine now protects Author
   and Series the same way it already protects the other heavy fields: if a save
   comes in without them, it keeps what was already stored rather than erasing it.
   This is a backstop for every operation of this kind, not just the ones we knew
   about. Author and Series are always re-derived from the book's author and
   series IDs, so keeping them is always the right call — no operation ever
   legitimately clears them.

2. **Fixed the two author-splitting jobs properly.** Instead of saving a
   lightweight copy, they now load the full stored record, set both the new author
   *and* a correct, fresh author label, and save that — so the book ends up
   correctly labeled, not just un-erased. If loading the full record ever fails,
   they still complete the author change rather than skipping it.

3. **Fixed a not-yet-used file-move step** that would have wiped the whole record
   the moment it was wired up.

## Why it matters

Author and Series are core to how a library is organized and browsed. Losing them
silently means books drift out of their series and away from their authors with
no error and no obvious cause — the kind of corruption that is hard to notice and
harder to trace back. This change stops it at the source and repairs every path
that could trigger it, with regression tests that reproduce the loss and prove it
no longer happens against the real production-style database.

Every existing author and series decision is preserved; the fix only ever keeps
or correctly refreshes this data, and it can be rolled back as a single change.
