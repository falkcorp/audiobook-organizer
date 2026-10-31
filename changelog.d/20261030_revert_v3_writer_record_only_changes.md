### Fixed

- The operation revert now reverses the two change types the v3 `ops.Writer` records: `book_file_modify` (one book_file field, restored field by field while it still holds what the Writer wrote, through the store's `ModifyBookFile` compare-and-swap) and `book_authors_modify` (a book's whole credit list, restored exactly, order included, while the list is still the one the Writer wrote). A row whose target changed since is refused and counted in the result's Failed and ChangedSince; the operation's other rows still revert. The preflight predicts the same refusals.
