### Fixed

#### Hard delete (bulk, single and trash purge) no longer moves listening progress off a book it then refuses to delete

`POST /api/v1/audiobooks/batch-operations` with `hard_delete` carried a book's
users' listening state (progress, finished status, positions, bookmarks) to the
version Audiobookshelf lists and only then called `DeleteBook`, which refuses a
book that still owns `book_file` rows (`database.ErrBookOwnsFiles`). The book
stayed, but its users' state had already been moved off it.

All three hard-delete paths now ask the question `DeleteBook` asks, through one
shared helper, `database.CheckBookOwnsNoFiles`: before the carry, and again
under the merge lock the carry and delete run in (`merge.HardDeleteKeepingUserState`
takes a `precheck` for this). The helper counts retired `book_file` rows too,
as `DeleteBook` does; a check made with `GetBookFiles` passed a book that held
only retired rows and `DeleteBook` then refused after the state had moved. A
book with only retired rows is now refused up front with wording that says those
rows cannot be moved (it no longer advises moving files that cannot be moved).
The helper fails closed: a store that cannot list retired rows, or a failed
read, refuses the delete. The batch path reads the rows through the
`RetiredBookFileReader` capability, so `batchBookStore` is not widened.

- Batch: a book that owns files is a failed item in the per-item `results`,
  never a success, and the rest of the batch proceeds.
- Single delete (`DELETE /api/v1/audiobooks/:id`): passes the same `precheck`,
  so a refusal under the lock moves nothing and blocks no hash. The no-state
  branch (nothing to carry) runs the check under the lock too, and no longer
  blocks a hash for a book that gained a file before the lock.
- Trash purge (`purgeNow`, the nightly purge, discard-progress-and-purge): the
  state-carrying purge now makes the same under-lock check, as the discard path
  already did.
- A refusal after the state moved (a non-merge writer such as the scanner added
  a file after the lock check) is reported as its own outcome:
  `merge.StateMovedBookKeptError` / `STATE_MOVED_BOOK_KEPT` (HTTP 409), saying the
  state moved to the sibling and the book was kept. It is no longer reported as
  `OWNS_FILES` "nothing deleted". A refusal by the precheck is a
  `merge.PrecheckRefusedError`; callers keep matching
  `merge.ErrCarryPrecheckRefused`, and `merge.PrecheckCause` returns the cause.

`POST /api/v1/audiobooks/batch-operations` now also requires `library.delete`
when any operation is a hard delete (`action: "delete"`, `hard_delete: true`),
as `DELETE /api/v1/audiobooks/:id` does. It previously needed only
`library.edit_metadata`, so a role with edit but not delete could permanently
delete books through the batch route. Update, soft delete and restore are
unchanged.
