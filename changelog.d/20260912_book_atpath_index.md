### Added

#### Every book at a path is now findable, not just the last one written

The store kept a single "which book is at this path" key, and the last writer
won. With two books at one folder the key named whichever wrote last, and when
either one moved away the key was deleted while the other still sat there. Code
that needs to know whether a path is free had to scan every book row instead
(about 134 ms per question in production).

A new index (`book_atpath:`) records every book at every path, and
`LiveBookIDsAtPath` now answers from it. It is maintained by `CreateBook`,
`UpdateBook` and `DeleteBook` in the same atomic write as the book row, and
`UpdateBook` rewrites its own entry on every save so a racing stale write cannot
strip it. Every answer is checked against the book row, so a leftover entry is
dropped rather than trusted, and a read error fails the call rather than
returning a partial list.

The index is built once in the background after the in-memory warmup finishes
on the first start of this version, spread across every CPU core (one reader
handing rows to a pool of workers). Until it is built, `LiveBookIDsAtPath`
keeps using the full scan, so the answer is never taken from a half-built index.
`LiveBookIDsAtPath` is now part of the main store interface, so it works through
the search-index wrapper that production runs behind.

Two maintenance operations come with it:

- **Book path-set index verify** (read-only): compares the index to the book rows
  and fails if any live book is missing from the index at its path.
- **Book path-set index rebuild**: rewrites the index for every book. Run it,
  then the verify, after any rollback to a build older than this one, because an
  older build moves books without updating the index.

Review follow-ups on the same change:

- The signature-sidecar migration now writes the book's path-set entry in the
  same atomic write as the row. Without it, a book moved by a concurrent save
  just as the migration committed could end up at its old folder with no entry
  there, so the folder would read as empty.
- A factory reset now also forgets that the index was built, so lookups fall
  back to the full scan until the index is rebuilt.
- One unreadable book row no longer stops the index from ever being built. Such
  rows are skipped, counted and logged as errors with sample ids. The rebuild
  maintenance operation reports them as a failure, as the verify operation does.
- The lookup interface was split in two (`BookNaturalKeyReader` and
  `BookPathSetReader`) to stay within the eight-method interface limit. The
  main store's method set is unchanged.
