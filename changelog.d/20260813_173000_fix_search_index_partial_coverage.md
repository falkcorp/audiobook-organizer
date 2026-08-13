### Fixed

#### Library search could not see books a cancelled index build never reached

Searching the web Library page for a book returned unrelated results while the
same search in the AudiobookShelf app returned the right ones. Measured on
production 2026-08-13: books created in April were 97% searchable (38 found, 1
missing in a sample), books created in August were 2% (1 found, 50 missing).

Root cause: `buildSearchIndexIfEmpty` is the only bulk build of the Bleve index
and it returns early unless the index has **zero** documents — encoding
"non-empty means complete". The build honours the shutdown context, so a
restart part-way through leaves a populated-but-incomplete index that the next
boot then declines to touch, permanently. Because it walks books in ULID order
and ULIDs are time-ordered, the rows lost are always the newest ones.

The dirty-set reconciler added earlier does not cover this: books are marked
dirty only when a queue-full drop discards an index event, so a book the
backfill never reached was never enqueued, is never dirty, and is never
repaired.

On boot the server now compares indexed documents against book count and, when
the index is short, marks the books dirty so the existing reconciler re-indexes
them. Seeding the durable dirty set rather than re-running the build means a
cancellation mid-sweep costs nothing — the marks survive the restart.

#### Search no longer degrades to substring matching without saying so

Three paths silently fell back from the full-text index to
`store.SearchBooks`, a whole-query substring match over title/author/narrator
only: a nil search index, a query-parser error, and a translation error. None
logged anything, so a multi-word query could quietly stop matching and nothing
in the logs would indicate which matcher had answered. All three now log a
warning naming the query and the reason.
