<!-- file: docs/executive-summaries/2026-09-11-the-rows-a-deleted-book-left-behind-executive-summary.md -->
<!-- version: 1.1.0 -->
<!-- guid: 5d2778ad-2a2a-48b1-a8a1-232c12d38ae8 -->
<!-- last-edited: 2026-09-11 -->

# The rows a deleted book left behind

**Pull request:** https://github.com/falkcorp/audiobook-organizer/pull/3228

## Executive Summary

When a book is permanently deleted — because it was a duplicate that got
merged away, because it was purged, or because an archive sweep removed it —
the app is supposed to remove everything it ever recorded about that book. It
removed most of it: the book itself, its file lookups, its fingerprint, its
chapters, and the "is this a duplicate?" suggestions that pointed at it.

It did not remove the notes that say **who wrote and who narrated** the book.
Those two records sat there after every delete. Nothing ever cleaned them up,
and the part of the app that answers "which books does this author have?"
reads those records directly. So an author could keep a credit for a book that
no longer existed, and the author counts drifted a little further from the
truth on every delete.

While fixing that, the same check was run against every other kind of note
the app keeps on a book. Five more were being left behind in exactly the same
way: the tags on the book (and the reverse list that answers "which books have
this tag?", so a deleted book could still show up under a tag and inflate the
tag counts), the user's free-form labels, the book's alternative titles, the
record of metadata matches a person had rejected, and the cached list of
candidate matches from the online catalogs.

All seven are now removed in the same single step as the book itself, so a
delete either removes everything or nothing. A test creates a book with every
one of those notes, deletes it, and checks the raw storage to confirm nothing
for that book is left — and that a second, unrelated book's notes are untouched.

Three kinds of record are deliberately **not** removed, because they are meant
to outlive the book: the stable identity that Audiobookshelf-style listening
apps store their progress against (a phone that still remembers the old book
must be redirected, not told "not found"), the safety copy written just before
a delete so it can be undone, and the per-file records, which have their own
cleanup.

Rows that earlier deletes already left behind are still there. Cleaning those
up is filed as a follow-up job that will first report how many it finds before
it removes anything.
