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

**The same filter explains the series symptom too.** The user also reported
*"no matter what with some of them you get the same series but only two books."*
Series `152828` holds **8** of the 9 books but only **2** of them are `organized`
(the two seq-5 rows), so ABS renders that series with two books. Series `207866`
holds the single organized seq-3 book, so it renders with one. 2 + 1 = the three
books on the author page. One filter accounts for every number the user saw —
there is no separate series-visibility bug.

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

## Why the books are `imported` is NOT yet established

An earlier draft of this note asserted they are `imported` because
organize/write-back had not moved them yet, and that fixing the rename collisions
would flip them to `organized`. **That claim is not supported by the evidence and
is retracted.** Measured file paths for all 9:

```
imported   seq=1  /mnt/bigdata/books/audiobook-organizer/Nameless Author/Nameless Sovereign/...
imported   seq=2  /mnt/bigdata/books/audiobook-organizer/Nameless Author/Nameless Sovereign 2 - Unknown Author/...
organized  seq=3  /mnt/bigdata/books/audiobook-organizer/Nameless Author/Nameless Sovereign 3 - A Cultivation Prog...
imported   seq=6  /mnt/bigdata/books/audiobook-organizer/Nameless Author/Nameless Sovereign/Nameless Sovereign, Bo...
```

**Every one of the 9 — organized and imported alike — is already physically
inside the organized tree**, and both states appear under both flat
(`Author/Title`) and nested (`Author/Series/Title`) shapes. So `library_state`
does not track physical location, and these books are not sitting outside
awaiting a move that collisions are blocking.

- [ ] **Determine what `library_state` actually means and why these 6 are
  `imported`.** Candidates: the book was scanned in place and never *claimed* by
  an organize run (so `imported` means "not placed by us", regardless of where it
  sits), or the state is simply stale and was never updated after a successful
  placement. These imply completely different fixes — one is a state-repair
  backfill, the other is a bug in organize's state write.
- [ ] **Re-measure after the collision resolver lands** before changing the ABS
  filter. Not because the causal link is established — it is not — but because
  the population of `imported` books may shift and the filter change should be
  decided against current data.

Side observation from the same paths: one book sits under a directory named
`Nameless Sovereign 2 - Unknown Author` — an "Unknown Author" string baked into a
path for a book whose author is known. Separate junk-metadata artifact, not the
cause here.
