Fixed the re-review findings on the retired-version readers. The merge planner's
virtual-file attach no longer drops a file silently while the in-memory index is
still warming up: with no retired version in the library it uses the ordinary
lookup (which is complete then), and with one it stops the combine with an error
instead of planning an empty attach; `fix-version-groups` and `fs-regroup-xml`
take the same shortcut instead of refusing every group during warmup. Every
writer of the single-owner path index (book-file create and delete, the path-key
claim, the segment and fingerprint wipes, the window cascade) now commits under
one mutex that retire, un-retire and re-home also hold across their verify and
commit, which closes the window the previous round only narrowed. A new
architecture test lists every non-test caller of the retired-filtering getters
and fails on a new one that nobody reviewed. `tag-backfill` writes bounded
chunks of whole books so one write no longer holds hundreds of file stripes, and
`rewrite-path-prefix` runs a holder's live and retired units on one worker so
two workers never write the same book at once. The batch book-file writers'
retry restore now deep-copies the caller's rows. A retired version can no
longer lose or change its `retired_from_book_id` or `retired_at` through
`UpdateBookVersion`, and a retire into a version without that provenance is
refused, so the probe that tells the warmup lookups no retired version exists
cannot go false while one does. The merge planner stops on a failed row read
instead of planning a second row over a live one, `CombineBooks` settles its
file lookups before it writes its undo journal so a refusal leaves no Pending
journal, and an applied multi-row book-file delete whose fsync fails still
clears the in-memory index.

Nothing retires a row yet, so the retired-version rules themselves cannot fire
on production. The write-path changes in this entry do run there as soon as it
merges. Every book-file create, update and delete now commits under one
process-wide mutex, so those writers no longer run in parallel with each other
at the commit itself. The book-file deletes and the segment and fingerprint
wipes apply without a sync and fsync the write-ahead log once afterwards, so a
failed fsync now reports durability unknown instead of a plain error.
`tag-backfill` writes 32-row chunks of whole books, about eight times as many
batch writes and log fsyncs as before (roughly 23,000 instead of 3,000 on a
750,000-row library). `rewrite-path-prefix` groups its work by book, and a
combine refuses with an error on a store that cannot say whether a retired
version exists, where it used to proceed.
