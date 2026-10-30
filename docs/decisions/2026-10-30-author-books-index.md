<!-- file: docs/decisions/2026-10-30-author-books-index.md -->
<!-- version: 1.1.0 -->
<!-- guid: 6051a674-d644-443e-8d16-387e208b9667 -->
<!-- last-edited: 2026-10-30 -->

# Persisted author to books index (TODO SQ-02)

Taken autonomously on 2026-10-30 under the standing "take the recommended
option" instruction. Each choice lists what was rejected so it can be reviewed.

## Problem

`GetBooksByAuthorIDCore`'s Pebble fallback (used until memdb warmup finishes,
about 130 s after every restart) read every `book_authors:` row to find one
author's credit links, then every `book:` row to find legacy `Book.AuthorID`
links: two full-library unmarshal passes per request.

## Decisions

### 1. Two key families, split by the source of the link

`author_books:<authorID>:<bookID>` for credit rows and
`author_books_legacy:<authorID>:<bookID>` for `Book.AuthorID`. The reader takes
the union.

- Why: the listing is "credit row OR legacy AuthorID". The writers hold different
  locks (credit writers hold the `book_authors` stripe and cannot take the book
  stripe because the order is book, owner, book_authors; `UpdateBook` holds the
  book stripe only). With one shared key, a credit writer that read the old
  `AuthorID` could delete a key an `UpdateBook` had just set for the same pair,
  and the book would vanish from the listing with no error. Giving each key one
  writer family makes that impossible without a new lock.
- Rejected: one key per pair (the brief's literal shape). Needs either a new
  lock held by both writer families across a read-and-commit, or accepting the
  lost-key race. A missing key is the one failure that is silent.
- Rejected: indexing credit rows only. The legacy scan of every `book:` row
  would remain, so only half of the cost goes away.
- Rejected: reusing `book:author:`. `keyfamilies.go` registers it as a dead
  legacy index that retention sweeps.

### 2. Point-verify on read, trust gated by a sentinel

- Reader scans both ranges on one snapshot, point-reads each candidate row,
  re-checks the link (`AuthorID` equals the author, or the credit row holds
  it), then applies the same soft-delete and non-primary filters as the scan,
  ordered by book id like the scan. An extra key is therefore harmless; only a
  missing key can be wrong, and writers set their keys unconditionally on every
  write so a lost key heals on the book's next write.
- Until `system:backfill:author_books_index_v1_done` exists the reader keeps the
  old two-scan path unchanged, so a half-built index is never read.
- Rejected: skip verification. It would turn every stale key (a backfill racing a
  writer, a crash) into a wrong answer.

### 3. Merges, deletes and relinks keep the authoritative scan

`booksByAuthorIDForMutation` (`GetBooksByAuthorIDWithRoleCore`,
`GetBooksByAuthorIDForRelinkCore`) runs exactly when memdb is incomplete, and a
link it misses is an author deleted with an orphaned credit. A derived index
that can be short is the wrong input for that, so those paths are unchanged.
The `DeleteAuthor` junction sweep also stays a full scan for the same reason.
Both could be narrowed to the index later; that is a separate decision.

### 4. Build on first boot, reconcile on every boot, trust bound to the build

`ReconcileAuthorBooksIndex` writes every absent key (a point read per pair, so a
steady-state boot writes nothing), then records the sentinel. It runs in the
background from `server_lifecycle.go` on EVERY boot, immediately, never after
warmup: the index exists to cover the warmup window, and a reconcile that waited
for warmup would extend the time a sentinel from another build is not believed.

The brief asked for a one-time backfill when the marker is absent. The extra
reconcile on later boots is deliberate: a binary from before this change writes
no keys, and this project auto-reverts, so a roll-back followed by a roll-forward
would otherwise leave the sentinel set over a short index and the fallback would
silently under-report.

The sentinel's value is the VCS revision (`+dirty` when modified) of the build
that last completed a reconcile. The reader trusts the index when

1. the sentinel exists, and
2. the recorded revision equals the running build's, OR this process has
   finished a clean reconcile (`PebbleStore.authorBooksVerifiedThisBoot`).

Same build (a normal restart): trusted at once, no regression: the index covers
the warmup window. Different build: the reader scans until this boot's reconcile
completes, which then sets the in-process flag and rewrites the sentinel with the
current revision. An empty running revision (`go test`, a build without VCS
stamping) counts as equal to anything; a recorded value that is not a revision
(`1`, written by an unstamped build) differs from a stamped running build.

Why only missing keys matter: the reader point-verifies every candidate (row
exists and decodes, still links the author), so an extra key a binary left behind
is harmless. A missing key is the only way to be wrong, and the reconcile writes
exactly the absent keys, so "reconcile finished" is the whole condition.

An unreadable credit row makes the reconcile DELETE the sentinel (decision 5),
not merely leave it unset, so a sentinel an earlier clean run set cannot keep
being believed after this run disproved it.

Residual, not closed: one intermediate binary that does NOT contain this code
cannot be detected. If build A (index) runs, then build B (no index code) writes
books and credits, then build A runs again, the sentinel still names A and the
running build is A, so the reader trusts it until that boot's reconcile fills the
keys B skipped. B leaves no mark because it does not know the sentinel exists.
That window is now only as long as the reconcile pass itself. The storage-format
stamp (`buildStorageFormat`) only refuses a store written by a NEWER build, so it
cannot signal a downgrade and was left alone (bumping it would also stop an old
build from opening the store at all, which a rollback needs). Cost: two
sequential scans per boot, decode and point reads spread over `runtime.NumCPU()`
workers.

- Rejected: strictly one-time, with the rollback hazard documented. It leaves a
  silent failure that needs an operator to know to delete the sentinel.
- Rejected: waiting for warmup before reconciling a trusted index. It lengthened
  the window the reconcile is meant to close.
- To force a full rebuild by hand: delete `system:backfill:author_books_index_v1_done`
  (readers return to the scan until the next boot finishes), or bump the version
  in the constant.

### 5. Undecodable credit rows delete the sentinel

The old scan failed closed on an undecodable `book_authors` row. The index cannot
list that row's authors, so the reconcile deletes the sentinel (a sentinel set by
an earlier clean run included), readers go back to the scan (which fails closed
naming the row), and the reconcile returns an error that is logged at ERROR. A
later clean reconcile sets it again. An undecodable book row does not hold the sentinel: neither
path can list it.

### 6. Deleted author's legacy links stay

`DeleteAuthor` removes the credit keys it sweeps but leaves
`author_books_legacy:<deleted>:*` for books whose `AuthorID` still names it,
matching what the scan returned. `DeleteAuthor` repoints those scalars first, so
in practice the legacy keys move with the books.

## Not done

- No dry-run or apply parameter. The change writes only derived index keys and
  the sentinel; it never writes a book, credit or file row, and is idempotent.
  The brief's "new write path means stop" rule was read as covering writes to
  primary data.
- No consistency checker op (the book-hash index has one). The exact-match
  invariant is covered by tests; a repair op would be a follow-up.
- `DeleteAuthor`'s sweep still scans every credit row.
