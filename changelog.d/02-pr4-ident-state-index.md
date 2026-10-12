### Added

#### Every book has an identification state, counted in O(1) and filterable with `ident:`

Each book is now in exactly one identification state: `unasked` (no
metadata_cache row), `asked_empty` (a row with no candidate), `has_candidates`
(candidates nobody has applied; reason `stale` when the row is marked Stale),
`applied` (review status `matched` or `audio_confirmed`) or `held` (ruled
`no_match`). The state is stored as an index (`ident:state:`, `ident:by:` and
`ident:meta:` key families) and kept current by write-through: every writer of
an input commits the book's state row and membership key in the same batch as
the change itself (PutMetadataCache, DeleteMetadataCache, CreateBook,
UpdateBook, DeleteBook, the raw writers under `metadata_cache:`, Reset). A wipe
of the book or cache keyspace rebuilds it.

The per-state counts are held in memory, not stored: a writer adds its move
after its commit succeeds, and every open (and every rebuild) recounts them
from the `ident:by:` keys (a keys-only scan: 2.9 ms at 40,000 books, 8.1 ms at
120,000). A
read-write lock lets writers of different books commit and fsync concurrently;
only a rebuild's final recount, Reset and the drift check exclude them. On a
disk store, five interleaved rounds against the pre-index build put the median
throughput ratio of CreateBook and of a state-changing PutMetadataCache at
0.88-1.11 of it with 1, 8 and 32 concurrent writers (a first draft that held
one exclusive lock across every synced commit was 2.5-4x slower at 8 writers
and about 10x at 32).

The counts have two scopes: every book, and the Library's default view
(primary versions, a nil flag counting as primary; trash and quarantined books
excluded). `missing` is unasked + asked_empty over the default view: books
with no fetched candidate and nothing applied. It is the metadata goal's count
before the goal's exclusion of the excluded franchise (about 281 books), which
nothing can tell apart yet (a later `manual_only` reason will), so for now it
exceeds the goal number by those franchise books that are unasked or
asked_empty.

- The Library search bar accepts `ident:` with `unasked`, `asked_empty`,
  `has_candidates`, `applied`, `held` or `missing`, negatable (`-ident:applied`).
  An unknown value or a pattern is a 400 that lists the valid values. Over the
  default view with no other filter the count is the counter (about 0.1 µs on
  40,000 books); anything else walks only the books in the wanted states (first
  page of `ident:missing` over 40,000 books in 3.8-5.5 ms on a loaded machine).
- The review summary (index and page views) carries `ident_counts`
  (`unasked`, `asked_empty`, `has_candidates`, `applied`, `held`, `missing`,
  `ready`) for the default view, and the review rail shows each count as a link
  that opens the Library with that `ident:` search; the Library's
  "Select all N items" for it is the same number.
- The Review Title box knows `ident:` and `asin:` as field names, as the
  Library's parser does, so it refuses them ("only title: filters apply here")
  instead of searching titles for the text `ident:missing`.
- The index is built after memdb warmup on boot when its marker is absent or
  old; until it finishes, `ident:` queries answer 503 with `Retry-After` and
  `ident_counts.ready` is false. With a current marker the boot reconciles it in
  place, which repairs rows written by a build that predates the index. A
  failed build is retried with backoff (six retries over about two minutes), as
  is a failed rebuild after a wipe; after that an ERROR names the new
  `maintenance.ident-index-rebuild` op, which previews what a rebuild would fix
  by default and rebuilds with `dry_run=false`.
- A book row that will not decode is in no state, and no longer fails a
  metadata_cache write for that book.
- A durable dirty set (`ident:dirty:`) with `MarkIdentDirty`,
  `IdentDirtyPage` and a compare-and-delete `ClearIdentDirty` is in place for
  the identification driver; nothing marks books dirty yet.

#### `asin:` Library filter

`asin:` filters on the book's ASIN with the usual grammar (substring, quoted
literal, glob, regex), on the memdb-resident row. `asin:*` lists books that
have one, `-asin:*` books that do not.
