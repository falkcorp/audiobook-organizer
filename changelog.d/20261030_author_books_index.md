### Added

#### Persisted author to books index

The author book list (`GET /authors/:id/books`) no longer reads the whole library during the roughly two minutes after a restart while the in-memory copy warms up. A new index (`author_books:<author>:<book>` for credit rows, `author_books_legacy:<author>:<book>` for the book's primary author) is kept in step on every book create, update, delete, credit change, author delete and merge, built once on the first boot after this ships, and reconciled on later boots. Merges and deletes still use the full scan.
