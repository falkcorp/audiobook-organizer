- [ ] **AUTHOR-SNAPSHOT** The metadata apply never refreshes the `Book.Author`
      snapshot stored in the book row. `internal/metafetch/service_apply.go`
      (~line 306) writes `AuthorID` and the `book_authors` join but leaves the
      snapshot alone. `UpdateBook` keeps the old snapshot when a write leaves
      it nil, and its comment at `internal/database/pebble_store.go:2672` says
      the snapshot is "recomputed on read"; that is false, since `GetBookByID`
      does not recompute it. Result: the snapshot is nil or stale on most
      books. The apply gate stopped reading it in PR #3578, but organize and
      rename paths, quarantine, one cache-identity check, the fetch inputs and
      the API `author_name` still prefer it (full list in #3578). Done means
      one of two things: every author writer refreshes the snapshot, or the
      snapshot is dropped from the row and hydrated from `AuthorID` on read.
