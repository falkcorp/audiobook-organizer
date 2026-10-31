<!-- file: docs/decisions/2026-10-30-book-edit-join-stripe.md -->
<!-- version: 1.0.0 -->
<!-- guid: 8e2c5a17-4d90-4b36-a1f7-6c0b93d2e854 -->
<!-- last-edited: 2026-10-30 -->

# Book edit writes its credit lists under the book's write stripe (EDIT-JOIN-WRITE-STRIPE)

## Decision log

### Reuse `ModifyBookCredits` instead of adding `ModifyBookWithJoins`

The brief recommended a new `ModifyBookWithJoins` on `database.Store` and
PebbleStore. The code already has the exact primitive:
`PebbleStore.ModifyBookCredits` (credits_store.go, the `BookCreditsModifier`
capability) takes the book stripe, then the book_authors and book_narrators
stripes (the documented lock order), runs a callback over the full row and both
lists, and commits row and lists in one batch. A second method would be a
duplicate code path. So no `Store` method was added: nothing widens, no mock
regeneration is needed, and there is no MemStore counterpart to write (MemStore
does not implement ModifyBook either).

The audiobooks package resolves the capability at use
(`database.AsCapability[database.BookCreditsModifier]`), the way revert.go
already does, and fails the edit with an error when the store lacks it. Prod
passes the concrete PebbleStore. There is no fallback to `SetBookAuthors` /
`SetBookNarrators` (owner rule: cut over, no second path). Rejected: adding the
method to the `bookWriter` interface, because stub stores and the mockery
`MockStore` (generated from `Store`) would then need it everywhere.

### Changes to `ModifyBookCredits`

- It stages and mirrors only a list the callback changed. It used to rewrite
  both on every call; for the edit endpoint that would re-normalise legacy rows
  with tied positions on a title-only save (changing which author the
  organizer files the book under) and announce credit changes that did not
  happen.
- It bumps the library generation after the memdb mirror (`writeThroughThenBump`),
  as `SetBookAuthors` does, so a list cached between the row's bump and the
  mirror is not stale under the new generation.

### Narrator cast: resolved before the lock, decided inside it

`CreateNarrator` takes the narrator name-index and ID-counter locks, so it
cannot run under the book stripe. `planEditedNarrators` resolves the cast
(cleaning, self-read, junk, clear) before the commit, using the edit's pending
author rows for the author names (the same input the old post-commit code saw,
since the join used to be written first). Inside the callback `rowsFor` decides
from the freshly read row whether to apply it: an unchanged credit, or a credit
that is not the one the cast was resolved for (a concurrent writer's), leaves
the junction alone. Consequence: a narrator record created for an edit that
then fails to commit stays behind unreferenced (purge-empty-narrators removes
it).

### Behaviour changes

- The store's own `syncNarratorJunctionAfterWrite` no longer runs for edits
  (ModifyBookCredits does not run it); the edit's explicit cast covers every
  case it covered.
- A join that cannot be written now fails the edit; it used to come back as a
  post-commit warning on a half-saved edit.

### Left undone

None of the todo scope. The `-race` test shape in the brief (edit vs
SetBookNarrators) passes on the old code too, because each setter is atomic, so
a second test was added that races the edit against a narrator column write and
asserts the column and the junction agree.
