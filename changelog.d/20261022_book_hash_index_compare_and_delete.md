`DeleteBook` removes the `book:hash:`, `book:originalhash:` and
`book:organizedhash:` index rows only while they still name the deleted book
(compare-and-delete, the rule the path-owner index already followed). Two books
holding one file share a single-valued hash row, so deleting the one that does
not own the row used to erase the live owner's lookup. New operation
`maintenance.book-hash-index-repair` checks the three hash indexes against the
live books (missing, dangling, wrong holder, trashed owner, unreadable owner,
raced) and, with `dry_run=false`, repoints each broken row at the live holder
with the smallest id using compare-and-set; it is a preview by default and never
deletes a row or writes a book. Known gap: `UpdateBook`'s hash-index update
still deletes the old rows unconditionally (see the TODO).
