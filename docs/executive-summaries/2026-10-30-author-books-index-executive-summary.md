<!-- file: docs/executive-summaries/2026-10-30-author-books-index-executive-summary.md -->
<!-- version: 1.1.1 -->
<!-- guid: e3cb2b0d-5ff6-4154-895e-5ac5a42f8d24 -->
<!-- last-edited: 2026-10-30 -->

# An author's book list no longer reads the whole library right after a restart

## Executive Summary

For about two minutes after every restart the app is still loading its fast copy
of the library, and during that time it answers "which books does this author
have" by reading every book and every credit in the library, twice, for each
request. Opening an author page in that window was slow, and several people
doing it at once made it slower. The app now keeps a small, permanent list of
which books belong to which author and reads only that list. Nothing about what
the author page shows has changed.

## What changed

- **A permanent author-to-books list.** It is kept up to date whenever a book is
  added, changed, removed, re-credited, or when an author is deleted or merged
  into another. It is stored together with the change it describes, so the two
  cannot disagree after a crash.
- **It is checked every time it is read.** Each book the list names is looked at
  again before it is shown, so a stale entry can never put a wrong book on an
  author's page. A missing entry is the only way to be wrong, which is why every
  way of changing a book or its credits writes its entry.
- **It is built once, then kept in step with the data.** On the first start after this ships
  the list is built from the existing library in the background. Until the build
  finishes the app keeps using the old method, so a half-built list is never
  used. Every later start checks the list at once and adds anything missing, which
  also repairs it after a roll-back to an older version that did not keep it.
  After an update or roll-back the app ignores the saved list until that check
  finishes, so it can never show a short list.
- **Merging and deleting authors still use the full check.** Those must see every
  link, so they do not depend on the list.

## What to expect

The first start after the update does extra background work while the list is
built; author pages are as slow as before until it finishes. After that, author
pages open at normal speed straight after a restart. The first start after
a different version has run (an update or a roll-back) uses the old method until
the check finishes, a short while, then switches to the list.
