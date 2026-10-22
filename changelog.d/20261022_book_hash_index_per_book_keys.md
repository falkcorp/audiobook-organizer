The three file-hash indexes (`GetBookByFileHash`, `GetBookByOriginalHash`,
`GetBookByOrganizedHash`) are now multi-valued: one key per hash and book
(`book_hash:`, `book_originalhash:`, `book_organizedhash:`, registered in
`keyfamilies.go`) instead of one shared `book:hash:<h>` row per hash. A writer
only ever touches its own keys, so no delete or update can erase another book's
lookup, which is what the 2026-10-21 purge of combine shells may have done. This
replaces the compare-and-delete guard the earlier commits of this change added to
`DeleteBook` and `UpdateBook`,
which could not fix a row that can only name one of its holders. Readers
verify every candidate against its own row and choose deterministically: the
live holder with the smallest id, else the trashed holder with the smallest id.
`CreateBook`, `UpdateBook` and `DeleteBook` maintain the keys in the same batch
as the row; `UpdateBook` re-sets the current keys every time, so a missing key
heals on the next write. New `PebbleStore.BooksByFileHash` lists every holder;
the delete-time block-hash guard uses it instead of the single answer, which
would have named the book being deleted and missed the other holder.

`maintenance.book-hash-index-repair` now checks and backfills the new keys:
preview by default, `dry_run=false` writes every missing key and, when every
book row decoded, sets the completion sentinel and deletes the retired
single-valued keys, in that order. Boot enqueues it with `ensure=true` on every
start (rollback-safe, like the opchange ensure). Until the sentinel is set,
lookups return the union of the new keys and the legacy key, so nothing is
missed during the cut-over. A book row that cannot be decoded keeps the sentinel
unset and fails the op. After a rollback to a pre-change build and a roll
forward, the next boot's ensure writes the keys that build did not.
