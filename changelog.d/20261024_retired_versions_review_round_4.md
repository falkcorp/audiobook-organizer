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
retry restore now deep-copies the caller's rows. Nothing retires a row yet, so
running behavior does not change.
