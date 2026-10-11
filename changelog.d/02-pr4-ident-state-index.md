### Added

#### Every book has an identification state, counted in O(1) and filterable with `ident:`

Each book is now in exactly one identification state: `unasked` (no
metadata_cache row), `asked_empty` (a row with no candidate), `has_candidates`
(candidates nobody has applied; reason `stale` when the row is marked Stale),
`applied` (review status `matched` or `audio_confirmed`) or `held` (ruled
`no_match`). The state is stored as an index (`ident:state:`, `ident:by:`,
`ident:n:` and `ident:meta:` key families) and kept current by write-through:
every writer of an input commits the state change in the same batch as the
change itself (PutMetadataCache, DeleteMetadataCache, CreateBook, UpdateBook,
DeleteBook, the raw writers under `metadata_cache:`, Reset), so each state's
count is a counter read. A wipe of the book or cache keyspace rebuilds it.

The counters have two scopes: every book, and the Library's default view
(primary versions, a nil flag counting as primary; trash and quarantined books
excluded). The default-view `missing` count (unasked + asked_empty) is the
metadata goal number.

- The Library search bar accepts `ident:` with `unasked`, `asked_empty`,
  `has_candidates`, `applied`, `held` or `missing`, negatable (`-ident:applied`).
  An unknown value or a pattern is a 400 that lists the valid values. Over the
  default view with no other filter the count is the counter (0.2 µs on 40,000
  books); anything else walks only the books in the wanted states (first page
  of `ident:missing` over 40,000 books in 3.8 ms).
- The review summary carries `ident_counts`
  (`unasked`, `asked_empty`, `has_candidates`, `applied`, `held`, `missing`,
  `ready`) for the default view.
- The index is built after memdb warmup on boot when its marker is absent or
  old; until it finishes, `ident:` queries answer 503 with `Retry-After` and
  `ident_counts.ready` is false. With a current marker the boot reconciles it in
  place, which repairs rows written by a build that predates the index.
- A durable dirty set (`ident:dirty:`) with `MarkIdentDirty`,
  `IdentDirtyPage` and a compare-and-delete `ClearIdentDirty` is in place for
  the identification driver; nothing marks books dirty yet.

#### `asin:` Library filter

`asin:` filters on the book's ASIN with the usual grammar (substring, quoted
literal, glob, regex), on the memdb-resident row.
