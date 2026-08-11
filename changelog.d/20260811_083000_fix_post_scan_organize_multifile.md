### Fixed

#### Auto-organize after a scan failed on every multi-file book

Organizing a book takes one of three routes, and picking the wrong one fails
outright rather than degrading: a book already under the library root is
re-organized in place, a book whose `file_path` is a *directory* goes through
the multi-file path, and anything else goes through the single-file path.

That three-way decision existed only inside the organize worker loop. The
post-scan auto-organize hook — the pass that runs automatically at the end of
a library scan — called the single-file organizer directly, for everything.
Every multi-file book it touched therefore failed with

> `cannot organize "…": file_path … is a directory but single-file organize was requested — use organizeDirectoryBook for multi-file books`

Production logged **588 failures of exactly that shape in one post-scan run**
on 2026-08-11. Multi-file books are most of the library, so in practice the
automatic pass after a scan was organizing almost nothing.

The decision now lives in one place, `Service.OrganizeOneBook`, and both call
sites use it. A third caller cannot reintroduce the same omission by copying
the wrong half.

#### "Auto-organize complete: 0 organized" now says why it was zero

The same hook reported a single number, so an operator seeing zero could not
tell whether nothing needed organizing, everything failed, or the books were
not in the database at all. It also collapsed a DB *lookup error* and a book
that genuinely has no row into one bare `continue`, hiding both.

The completion line now reports organized / failed / not-in-DB / lookup-error
counts against the number scanned, and the first ten lookup errors are logged
individually.

Covered by four tests in `internal/organizer/organize_one_book_test.go`, which
assert on which *path* was taken rather than on whether an artifact was
produced. Two of them fail with the verbatim production error string when the
directory branch is removed.
