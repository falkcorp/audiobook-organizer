### Added

- `internal/books/store`: the durable topic `books.book.relocated.v1` (payload `book_id`, `from_path`, `to_path`; keyed by book; 16 partitions; observable with `books:view`) and its staging in the store's one book-update path, in the same batch that writes a new `file_path`. A move and its message commit together or not at all: a failed commit stages nothing, and a refused stage refuses the move. No message is staged when the path is unchanged or the new path is empty. Dark: nothing installs the outbox (`SetRelocationOutbox`) or declares the topic until the events flip (F6-03), so no behaviour changes.
- `internal/plugins/deluge`: `RelocationConsumer`, the outbox consumer `books.deluge.book-relocated` that starts one Live `books.deluge.path-update` run per relocation, with the book ID only (the run reads the book's current path). Not registered until the events flip, which also deletes the in-process Deluge moves it replaces.

### Changed

- `books.deluge.path-update` (the native kind, not yet live) folds a start only into a queued run of the same book. It no longer folds into a running run with the same params, which may already have read the book's old path.
