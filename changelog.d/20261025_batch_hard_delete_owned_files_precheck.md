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
  already did. Purge's first check (before anything is touched) and both
  discard-progress-and-purge checks (early and under the lock) use the shared
  helper too, so a trashed book that holds only retired rows is refused before any
  tombstone, progress carry or erase, with or without listening state. A book is
  counted once in the nightly purge report: skipped when refused before the carry,
  an error when something failed after the state moved.
- A refusal after the state moved (a non-merge writer such as the scanner added
  a file after the lock check) is reported as its own outcome:
  `merge.StateMovedBookKeptError` / `STATE_MOVED_BOOK_KEPT`, saying the state
  moved to the sibling and the book was kept. It is HTTP 409 only when the cause
  is owned files and 500 for any other failure after the carry (a tombstone write
  during a purge, for example); the purge reports every failure after the carry
  this way. It is no longer reported as `OWNS_FILES` "nothing deleted". A refusal
  by the precheck is a
  `merge.PrecheckRefusedError`; callers keep matching
  `merge.ErrCarryPrecheckRefused`, and `merge.PrecheckCause` returns the cause.

Both bulk routes now ask for the permissions the single-book routes ask for, on
top of the `library.edit_metadata` they are mounted on. What a request does to
books' place in the library is read from the request the handler has bound and is
about to run (not by a second parse of the body), so a body with trailing bytes
or a duplicate key cannot slip past:

- Hard delete (`POST /api/v1/audiobooks/batch-operations`, `action: "delete"`,
  `hard_delete: true`) needs `library.delete`, as `DELETE /api/v1/audiobooks/:id`
  does.
- Soft delete needs `library.delete` too: the `delete` action without
  `hard_delete`, and an update setting `marked_for_deletion` to true or
  `library_state` to `deleted`, on `/audiobooks/batch-operations` and on
  `POST /api/v1/audiobooks/batch`.
- Restore needs `library.organize`, as the single-book restore does: the
  `restore` action, and an update setting `marked_for_deletion` to false, on both
  routes.
- A list that trashes and restores needs both. Plain metadata edits still need
  only `library.edit_metadata`.
- The request's shape is not the whole story: `library_state` set to `organized`
  (or any live value) on a book that is in the trash takes it out of the trash
  and looks like any metadata edit. So the handler also hands the service what
  the caller holds (`batch.TrashGrants`, never read from the body) and the service
  judges each row after loading it, under the row's write lock, with
  `database.IsInTrash`: a book that leaves the trash, or has its `library_state`
  set to a live value while it is in the trash, needs `library.organize`; a book
  that enters the trash needs `library.delete`. A row the caller may not change
  fails with a message naming the permission, nothing is written for it, and the
  rest of the request proceeds. `marked_for_deletion` and `library_state` are the
  only payload fields that touch the trash. In-process callers (the queued
  metadata apply) pass no grants and are not judged.

The routes previously needed only `library.edit_metadata`, so a role with edit but
not delete could trash, restore or permanently delete books through them. The
library page now disables "Delete Selected" without `library.delete` and
"Restore Selected" without `library.organize` (with the reason as a tooltip), and
shows the server's permission message when a bulk action is refused.
