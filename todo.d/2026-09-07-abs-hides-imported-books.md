## ABS layer hides every `imported` book — author pages undercount (2026-09-07)

**Confirmed on production, not inferred.** A user reported an author page showing
"only three books" when the library holds nine by that author.

Measured for author `Nameless Author` (id 40260):

```
native  GET /api/v1/authors/40260/books   -> count: 9   (all 9 correctly linked)
ABS     GET /api/authors/40260            -> numBooks: 3, libraryItems: []
```

Per-book state of those 9:

```
library_state='organized'  -> 3 books  (seq 3, 5, 5)   <- exactly the 3 ABS shows
library_state='imported'   -> 6 books  (seq 1, 2, 2, 4, 4, 6)  <- silently dropped
all 9: is_primary_version=true, quarantined_at=null
```

**Root cause:** `absItemFilterBase()`
(`internal/server/handlers/abs/browse.go:186-194`) gates the shared contributor
index on a 3-way AND — `IsPrimaryVersion && LibraryState == "organized" &&
!QuarantinedAt`. `numBooks` is then just `len(idx.authorBooks[id])`
(`browse.go:1344`). Books that are imported but not yet organized fail the
`== "organized"` test and vanish from author pages, series, counts and browse.

**Why this is a bug and not deliberate gating:** the book at seq 6
("Nameless Sovereign, Book 6") is `imported` and was **actively playing in the
user's client** while absent from its own author's page. The server streams it
but will not list it. Whatever the intent, "playable but unlistable" is not it.

- [ ] **Decide the intended semantics and make the filter match them.** Should the
  ABS layer expose `imported` books? If yes, relax the `LibraryState` clause. If
  no, it must at least be consistent — a book that cannot be listed should not be
  streamable/resumable either. **Blast radius is wide**: this filter feeds the
  shared contributor index, so authors, series, counts and browse all change
  together. Not a drive-by fix.
**`libraryItems: []` is NOT a bug — ruled out by measurement.** An earlier draft
of this note claimed it was a second defect (suspected memdb/Pebble ID drift in
`GetBooksByIDs`). That is **wrong** and was disproved directly:

```
GET /api/authors/40260               -> numBooks 3, libraryItems 0
GET /api/authors/40260?include=items -> numBooks 3, libraryItems 3
```

and all three organized book IDs resolve individually with **HTTP 200**. Omitting
items unless `?include=items` is asked for is correct Audiobookshelf behaviour and
is implemented deliberately (`internal/server/handlers/abs/browse.go:1432,1449,1478-1482`).
There is exactly **one** bug here — the `library_state` filter — not two. Do not
go chasing `GetBooksByIDs`.
- [ ] **Add a test that a non-`organized` book is reachable wherever it is
  playable**, so the two surfaces cannot drift apart again.

**Causal link worth recording:** books remain `imported` because the
organize/write-back step has not moved them, and that step is currently failing
on rename collisions (see the apply-path collision resolver work). Fixing the
collisions should convert `imported` → `organized` and make these books appear
without touching the ABS filter at all — so **fix collisions first and re-measure
before changing this filter**, or the filter change may be solving a symptom that
is about to disappear on its own.
