### Added

#### `GET /api/v1/authors/ref-audit` — classify author references before repairing them

`book.AuthorID` is a denormalized pointer that `DeleteAuthor` does not sweep, so
deleting an author can leave books pointing at an id that no longer exists. The
new read-only endpoint takes a comma-separated `ids` list (up to 5000 per
request, deduplicated) and sorts each id into one of three buckets:

- **live** — the author row still exists; the reference is fine.
- **tombstoned** — the row is gone but a tombstone redirects it to a canonical
  author, so reads still resolve. Untidy, not broken, and not repair scope.
- **dangling** — no row and no tombstone. Genuinely broken.

The distinction matters because `GetAuthorByID` follows tombstone redirects: an
audit that only asks "is this id in the live author list?" counts merged-away
authors as damage and overstates the problem. The response also names the
redirect target for each tombstone, so a tombstone pointing at a second deleted
author is visible rather than silently counted as self-healing.

The endpoint writes nothing — it exists to scope a repair, not to perform one.
