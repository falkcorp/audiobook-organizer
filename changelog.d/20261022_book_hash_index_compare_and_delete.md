`DeleteBook` and `UpdateBook` (when a book's hash changes) remove the `book:hash:`, `book:originalhash:` and
`book:organizedhash:` index rows only while they still name the deleted book
(compare-and-delete, the rule the path-owner index already followed). Two books
holding one file share a single-valued hash row, so deleting the one that does
not own the row used to erase the live owner's lookup. New operation
`maintenance.book-hash-index-repair` checks the three hash indexes against the
live books (missing, dangling, wrong holder, trashed owner, unreadable owner,
raced) and, with `dry_run=false`, repoints each broken row at the live holder
with the smallest id using compare-and-set; it is a preview by default and never
deletes a row or writes a book. When the book that owns a shared row changes
its hash while a sibling still holds the old one, the row is left missing for the
sibling; the repair operation reports it as missing and restores it.
