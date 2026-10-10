### Added

#### `bookfiles.FirstAudioFile` / `FirstAudioFiles` read the first audio file from `book_file` rows

`Book.FilePath` is stale for many books, so callers that only want "a path for
this book" now have one tested helper that reads the `book_file` rows and never
`Book.FilePath`. It picks the first present audio file by (disc, track, path),
skips Missing rows, empty paths and non-audio extensions (a configurable
`audioext.Set`), and reports `ok=false` for a book with no usable row instead
of falling back. `FirstAudioFiles` is the batch form for list pages: one store
call for N books. No `Store` method was added and no caller was migrated.
