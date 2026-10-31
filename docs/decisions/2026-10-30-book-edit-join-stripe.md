<!-- file: docs/decisions/2026-10-30-book-edit-join-stripe.md -->
<!-- version: 1.1.0 -->
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

### An edit that changes no credit list writes the row alone

`ModifyBookCredits` reads and JSON-decodes both lists to hand them to its
callback, so one corrupt `book_authors:` or `book_narrators:` value would block
every edit of that book, title-only included. When the edit carries neither a
pending author join nor a narrator plan, `updateAudiobook` therefore uses plain
`ModifyBook` (row only). The alternative, making `ModifyBookCredits` decode a
list lazily, was rejected: the callback decides what it changes, so the store
cannot know in advance, and the other callers (revert.go, authormerge.go,
credits_backfill.go) want the eager read. An edit that does change a list still
reads both, so a corrupt list blocks that edit (it could not be merged
correctly anyway).

The capability check (`BookCreditsModifier`) runs at the top of
`updateAudiobook`, before any author or narrator record is created, so a store
without it fails the edit and leaves nothing behind.

### Narrator resolution failures fail the edit

`planEditedNarrators` returns an error, not a warning, when the book's authors
cannot be read or a narrator name cannot be created. The edit writes the
Narrator column and the junction in one commit; a cast resolved only in part
would save a column the junction does not match, which is the disagreement this
change exists to prevent. No partial save: the edit is refused with the error.

### Behaviour changes

- For an edit that changes a credit list, the store's own
  `syncNarratorJunctionAfterWrite` does not run (ModifyBookCredits does not run
  it); the edit's explicit cast covers every case it covered. A row-only edit
  goes through ModifyBook, which runs it as before; with no narrator change it
  does nothing.
- A join that cannot be written now fails the edit; it used to come back as a
  post-commit warning on a half-saved edit. The same holds for a narrator that
  cannot be resolved or created.
- Only the row and the joins this edit writes are atomic together. Other
  writers are not made atomic by this change (see the follow-up below).

### Follow-ups

- Other writers of a book still write the row and its credit lists as
  separate steps, so a concurrent edit can interleave with them:
  `internal/metafetch/service_apply.go` (around lines 460 and 472) writes the
  book row and then calls `SetBookAuthors` / `SetBookNarrators`. Moving them
  onto `ModifyBookCredits` is a separate change and is not part of this one.

### Left undone

None of the todo scope. The `-race` test shape in the brief (edit vs
SetBookNarrators) passes on the old code too, because each setter is atomic, so
a second test was added that races the edit against a narrator column write and
asserts the column and the junction agree.
