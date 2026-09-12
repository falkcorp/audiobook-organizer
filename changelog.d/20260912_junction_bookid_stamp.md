### Fixed

#### Narrator/author credits stored without a book ID froze the book in memdb

`SetBookNarrators` and `SetBookAuthors` wrote caller rows to Pebble verbatim.
The narrator split in `POST /operations/optimize-database` built rows with no
`book_id`, and `PUT /audiobooks/:id/narrators` passes client JSON straight
through, so a client omitting `book_id` did the same. memdb's `{BookID,
NarratorID}` / `{BookID, AuthorID}` primary index rejects such a row, and since
`UpsertBookToMemDB` reloads the junction from Pebble, every later update of the
book aborted its whole memdb transaction. The book's memdb copy (title, credits,
files) stopped following its updates, and warmup dropped the row after a restart,
flagging the table incomplete. The earlier author-side fix had backfilled
`BookID` in memdb only, so the Pebble author rows had the same defect.

The store now owns the invariant: the key's book ID is stamped onto every row on
write, on read (`GetBookAuthors` / `GetBookNarrators`), in the memdb replace
helpers, and at warmup. A caller-supplied `book_id` naming a different book is
overridden and logged. Startup migration 63 rewrites rows already on disk,
lossless because the correct value is in the key. It replays the repaired sets
into memdb only when memdb is live: at startup it runs during the async warmup,
whose write buffer is capped at 50,000 ops (overflow switches memdb off for the
process), and warmup's own key stamping already loads the right rows. `TestSetBookAuthors_ExplicitBookIDIsUnchanged` asserted the old
divergent behaviour and is replaced by `TestSetBookAuthors_MismatchedBookIDFollowsTheKey`.
