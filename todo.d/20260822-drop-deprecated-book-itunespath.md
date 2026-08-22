### 🧹 DEP-1e — drop the deprecated `Book.ITunesPath` field

- [ ] **Remove `ITunesPath *string` from `database.Book`** and the `BookCore` round-trip that
      carries it. Spun out of the 2026-05-01 re-audit close-out (item 42), where it was the one
      sub-item that is genuinely still open.

  **Correcting the record.** The prior close-out note called DEP-1e "moot (post-SQLite removal)".
  It is not moot — the field is still declared and still copied at HEAD `629d5fa79`. Nothing
  re-checked the claim after the SQLite store was deleted, so a stale justification outlived its
  reason.

  **Exact extent (measured with `gopls findReferences` on the field, not a name grep).**
  `Book.ITunesPath` has **6 references, total**:

  - `internal/database/store.go:220` — the declaration itself
  - `internal/database/bookcore.go:207` — read in `func (b *Book) Core() BookCore`
  - `internal/database/bookcore.go:321` — written in `func (c *BookCore) ToBook() Book`
  - `internal/itunes/service/importer_mock_test.go:127, 152, 177` — test-only writes

  So it is a **pure carrier**: written by tests, round-tripped through `BookCore`, and read by no
  production logic on any path. Removal is mechanical — delete the field, delete
  `BookCore.ITunesPath` (`bookcore.go:62`) and both copy lines, then fix the three test literals.

  **Why a name grep is the wrong instrument here.** `grep 'book\.ITunesPath'` returns **0 hits**
  and looks like proof the field is already dead. It is not: the two real call sites use receivers
  named `b` and `c`. Meanwhile `grep '\bITunesPath\b'` returns **75** non-test hits, nearly all of
  which are the *authoritative* `BookFile.ITunesPath` (a plain `string`) and are unrelated to this
  task. Neither count answers the question; only symbol resolution does. Do not re-scope this task
  from either number.

  **Do not confuse the two fields.** `BookFile.ITunesPath` (`store.go:810`, a `string`) is live and
  load-bearing — iTunes import, write-back, path repair and reconcile all use it. Only the
  `Book`-struct `*string` is being removed.

  **Before removing, re-run the reference check** — if a new production reader has appeared since
  2026-08-22, keep the field and re-scope. Gate: `go build ./...` + `make test`.
