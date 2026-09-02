## Dedup search resolves book IDs with a full `GetAllBooksCore` read

`resolveBookIDsMatching` (`internal/server/handlers/dedup/search.go`) turns a search needle
into a set of book IDs by reading every book via `GetAllBooksCore(0, 0)`. That routes to
memdb and does no per-book I/O, but it materializes a full `Book` per row before narrowing
to `BookCore`, so a search over a ~44K-book library allocates the whole library transiently.

The alternative already on the interface is worse, not better: `GetAllBooksFullFrom`'s memdb
path lists IDs from memdb and then does a Pebble point read PER BOOK.

What would actually beat both is a store-level projection that matches during the memdb walk
and returns only the matching IDs — the same argument `ListBookIDs` already makes for itself
("saves ~50x memory vs GetAllBooksCore(0,0)").

- [ ] Measure the real cost of a dedup search against the production library first
- [ ] If it warrants the change: add the projection to `BookBulkReader`, implement on
      `MemStore` + `PebbleStore`, regenerate mocks
- [ ] Keep the author-name join — the projection has to see author names, not just books
