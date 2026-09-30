### Fixed

- Review workspace: "Refetch stale" now refetches every stale book the rail counts (it read "3,511 stale" but refetched the 10 in the reviewable list). `POST /metadata/batch-fetch-candidates` accepts `{"stale": true}`, which the server resolves to the summary's own stale set through one shared predicate and forces, so known-empty rows are re-asked. The confirm dialog shows the server count, the toast reports the server's `book_count`/`skipped`, and owner-marked "no match" books are no longer counted stale, because the fetch never searches them.
