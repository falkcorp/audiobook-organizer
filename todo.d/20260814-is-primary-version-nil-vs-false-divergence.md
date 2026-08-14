## `is_primary_version` means different things on the library path and the author path

Found 2026-08-14 while trying to verify an author merge. Both paths accept the
same parameter and answer confidently; they disagree about what a NIL flag
means, so the same book is primary on one path and non-primary on the other.

### Measured on production 2026-08-14

Library-wide the filter partitions the library exactly, and the rows it returns
carry an explicit `false`:

    is_primary_version=false  -> total=22552   rows have is_primary_version: false
    is_primary_version=true   -> total=41317
    (no filter)               -> total=63869   = 22552 + 41317 ✓

On the author path it returns rows whose flag is **null**, not false:

    author_id=38542&is_primary_version=false -> 1 row, is_primary_version: null
    author_id=38543&is_primary_version=false -> 1 row, is_primary_version: null

And it cannot return a book whose flag is explicitly `false`. Book
`01KNDB8NWHXV2DKRQESBA9SDRA` records `author_id: 42623`, `is_primary_version:
false`. Yet:

    author_id=42623                          -> 1 row, and it is NOT that book
    author_id=42623&is_primary_version=false -> 0 rows
    author_id=42623&is_primary_version=true  -> 1 row (a different book)

So a book that exists, names that author, and is explicitly non-primary is
unreachable through its own author's listing under every value of the filter.

### Likely cause

`memdb_schema.go` builds the index with `effectiveBoolFieldIndex{Default:
true}` — a nil `IsPrimaryVersion` indexes as **true**. A post-filter comparing
the raw `*bool` instead sees nil as "not true" and treats it as **false**. Same
nil, opposite readings, depending on which layer answers.

That also explains the shape of the disagreement: the author path is
primary-only by design (`GetBooksByAuthorIDCore` is the LISTING view — see
#2410), so an explicitly-`false` book is dropped before the parameter is ever
consulted, while a nil-flag book survives the index (nil→true) and is then
handed to a post-filter that calls it false.

- [ ] Decide the single meaning of a nil `IsPrimaryVersion` and apply it in both
      places. `Default: true` is already the storage answer, so the post-filter
      is the side that should change — but confirm before flipping, because
      22,552 books currently answer to `false` library-wide and some of those
      may be nil-flagged.
- [ ] Add a conformance test in the shape used by #2406/#2410/#2411: one
      fixture containing a nil-flag book, an explicit-true book and an
      explicit-false book; assert the library path and the author path classify
      all three identically. A fixture without a nil-flag row cannot catch this.
- [ ] Decide whether the author listing SHOULD expose non-primary books at all.
      Today it cannot, which is defensible for a listing, but it means the UI
      has no way to show a book on the author page it is genuinely attached to.

⚠️ **This is why the 46627 merge could not be verified** — see the handoff. Every
available instrument for "which books does author X have" is either
primary-only or disagrees about nil, so `0 non-primary books for 43791` is not
evidence the merge failed. Do not read it as such.
