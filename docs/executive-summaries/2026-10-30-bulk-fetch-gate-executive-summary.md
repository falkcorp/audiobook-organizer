<!-- file: docs/executive-summaries/2026-10-30-bulk-fetch-gate-executive-summary.md -->
<!-- version: 1.0.0 -->
<!-- guid: 87658ecd-404c-4c8d-95a8-ce8fbc10b40a -->
<!-- last-edited: 2026-10-30 -->

# An unchecked way to overwrite book details is closed

## Executive Summary

One old button-less shortcut in the server could take the best-looking match
from an online catalogue and write it straight into a book's details, skipping
the checks every other path makes (is the match good enough, does it describe
the same book, is it the right volume in a series). A wrong match could
silently replace correct titles, authors and descriptions.

Nothing in the app used that shortcut any more, so it has been shut off rather
than patched. Anything that still calls it gets a clear "removed" reply that
points to the checked route: fetch matches first, then apply them, with the
checks run on every one. No book data was changed by this work.
