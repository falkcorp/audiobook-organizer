### Fixed

#### Hard deletes: every "does this book own files" check counts retired rows, and a bulk hard delete hands the primary flag on

`DeleteBook` refuses a book that holds any `book_file` row, retired rows
included, but two clean-up paths asked with the live-only reader and so passed
a book whose only rows were retired. Version-group clean-up (`reconcile`) and
the iTunes regroup delete pass now ask `CheckBookOwnsNoFiles`, the question
`DeleteBook` asks; the regroup run store carries the retired-row reader for it
(the narrow store it wraps hides that capability).

Both also repeat the check under the merge lock, right before the listening
state is carried, so a file row that lands in between no longer moves a
reader's progress to another copy, and (for clean-up) no longer deletes the
duplicate's audio file first and finds out afterwards that the book cannot be
deleted: the row delete now comes before the file removal.

The bulk hard delete (`POST /audiobooks/batch-operations`) now shares the single
delete's steps through `merge.HardDeleteLiveBookLocked`: a deleted primary hands
its group's flag on (a bulk-deleted primary used to leave the group with none),
and `book.deleted` is published and the author and series caches are dropped
for each hard delete that went through.

`DeleteBook`'s own refusal now words a book that holds only retired rows the
same way as the check that predicts it ("cannot be moved") instead of telling
the user to move rows that cannot be moved.
