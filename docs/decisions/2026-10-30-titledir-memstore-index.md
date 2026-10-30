<!-- file: docs/decisions/2026-10-30-titledir-memstore-index.md -->
<!-- version: 1.0.0 -->
<!-- guid: b10433e3-4dfd-4c08-958b-65a0efabbc01 -->
<!-- last-edited: 2026-10-30 -->

# TODO-TITLEDIR: a memdb index for `GetBooksByTitleInDir`

## Decision log

Context: `GetBooksByTitleInDir` walked and JSON-decoded the whole book keyspace
once per newly imported book, inside the scanner's folder+title version-link
stripe. The TODO entry offered two fixes: a MemStore `(normalizedTitle,
parentDir)` index, or a dir-prefixed key range in Pebble.

Chosen: the MemStore index, delegating from `PebbleStore` when memdb is
published, exactly the shape `GetBooksBySeriesIDCore` and
`GetFolderDuplicatesCore` use.

- **Index maintenance is go-memdb's, not hand-written.** Unlike the sync code
  those two getters sit beside, a declared secondary index is rebuilt by
  memdb on every `Insert` and `Delete` of a books row. The write-through
  (`UpsertBookToMemDB`, `DeleteBookFromMemDB`) needed no change; a retitle or a
  move re-keys the row because the upsert replaces it. Tests cover retitle,
  move, soft delete and hard delete against both backends.
- **Always indexed, no skip arm.** The old scan never excluded an empty title or
  an empty `FilePath`, so the indexer emits a key for every book. Skipping
  them would have made the memdb answer differ from the scan.
- **Key = `normalizedTitle 0x00 dir 0x00`.** Normalization is
  `util.NormalizeTitle` (lowercase + trim), applied on both the indexed row and
  the lookup argument. The dir is compared byte for byte, as before. The
  trailing NUL makes a lookup exact (`/lib/Batman` does not match
  `/lib/Batman Beyond`). Every candidate is re-checked against the exact
  predicate, so a NUL inside a title cannot return a wrong row.
- **Always-on index, not opt-in like the sort indexes.** One key per book, about
  100-150 bytes: the same order as the existing `file_path` index, versus the
  19-22 MB per sort index the opt-in ones cost. The scanner needs it on every
  import, so there is no configuration in which it is unwanted.
- **Pebble fallback kept, in two cases only.** (1) memdb not yet published
  (warmup, `UseMemDB` off); (2) memdb refusing with `ErrMemdbIncomplete`,
  because a short sibling list is a missed version link. Other memdb errors are
  returned. The fallback now hoists the title normalization out of the per-row
  loop, and logs (Warn, with the book ID) an undecodable row instead of dropping
  it silently. It still skips rather than errors on such a row, so a single
  corrupt row cannot disable version linking for the whole library during
  warmup.
- **Result fidelity.** memdb rows are the stripped projection
  (`stripBookForMemdb`), so `Description`, `VersionNotes`, `BookSig*`, `Author`
  and `Series` are nil on the memdb path. The only caller reads identity,
  format, duration, size, path, version-group, primary and deletion fields, and
  re-fetches by ID before writing. Documented on both methods.

Rejected:

- **Dir-prefixed Pebble key range.** It would need a new on-disk index (or a
  path-ordered key family) with a migration and a backfill, and it would still
  decode every row under a large directory such as a flat library root. It
  would also keep two index implementations to maintain for one lookup.
- **Hashing the key to a fixed width.** Saves a few MB at library scale but
  makes the index opaque to debugging; not worth it at this size.
- **Caching the Pebble scan result per import run.** Stale the moment a sibling
  is created, which is the very event the lookup exists to observe.
- **Making the fallback return an error on an undecodable row.** Stricter than
  steady state; see above.

Measured (`BenchmarkGetBooksByTitleInDir`, in-memory Pebble, 2,000 books): memdb
about 1.6 microseconds per call, Pebble scan about 2.9 ms per call, and the scan
grows linearly with the library.
