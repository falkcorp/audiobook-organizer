### Fixed

#### Hard delete (bulk and single) no longer moves listening progress off a book it then refuses to delete

`POST /api/v1/audiobooks/batch-operations` with `hard_delete` carried a book's
users' listening state (progress, finished status, positions, bookmarks) to the
version Audiobookshelf lists and only then called `DeleteBook`, which refuses a
book that still owns `book_file` rows (`database.ErrBookOwnsFiles`). The book
stayed, but its users' state had already been moved off it. The batch now checks
the book's `book_file` rows before the carry, the way the single-book delete
does, and skips a book that owns files with the same wording
(`hard delete <id>: book still owns book_file rows (N row(s)); soft-delete it
instead, or move its files to another book first`). The skip is a failed item in
the existing per-item `results`, never a success, and the rest of the batch
proceeds. The check is repeated under the merge lock the carry and delete run in
(`merge.HardDeleteKeepingUserState` takes a `precheck` for this), so a file
moved onto the book by a merge between the first check and the carry is caught
too. A fail-closed read error refuses the delete.

The single-book hard delete (`DELETE /api/v1/audiobooks/:id` with a hard delete,
`AudiobookService.DeleteAudiobook`) checked the book's files before the lock but
not under it, so a file moved onto the book in between could still be followed by
a carry and a refusal. It now passes the same owned-files `precheck`, so the
check is repeated under the merge lock before the carry, and a refusal there
moves nothing and blocks no hash.
