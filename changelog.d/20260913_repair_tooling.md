### Added

- `PATCH /api/v1/audiobooks/:id/files/:file_id` accepts `track_number` and `disc_number` (non-negative; negatives are 400). Each change is recorded in the book's metadata history as `book_file:<file id>:<track_number|disc_number>`, and metadata-history undo reverts it on the file itself (409 if the file's number changed since). Track 0 is stored but means "no number": the rename planner sorts it after the numbered files and names it by position. To put a file first, number it 1.
- `POST /api/v1/audiobooks/:id/split-to-books` takes `"as_one_book": true` (and an optional `"title"`) to move the selected files into ONE new standalone book instead of one book per file. Files stay where they are on disk; both books' duration, size and file count are recomputed; unknown file IDs and a selection of every file are refused with 400.

### Fixed

- `POST /api/v1/audiobooks/:id/write-back` no longer reports success when its rename fails. It now plans the rename read-only first and refuses with 409 `file_work_would_fail` before anything moves; a rename that still fails stops the request before tags are written (409 for a duplicate target, 500 otherwise) with the reason in the body. The `library.bulk-write-back` operation had the same silent failure and now counts such books as failed without writing their tags.
