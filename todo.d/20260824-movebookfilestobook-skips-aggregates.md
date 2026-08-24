## 🔴 `MoveBookFilesToBook` updates neither book's aggregates

Found 2026-08-24 while auditing BookFile mutators for PR #2861. Not fixed there — that PR
scoped to `BatchUpsertBookFiles`, and this method has a different, larger caller set.

`internal/database/pebble_store_bookfiles.go` — `MoveBookFilesToBook(fileIDs, sourceBookID,
targetBookID)` deletes each row under `book_file:<source>:<id>`, sets
`f.BookID = targetBookID`, rewrites it under the target, and ends at
`return batch.Commit(pebble.Sync)`. No `notifyBookFileChange` for either book, and no memdb
refresh.

**Both books are left wrong, in opposite directions.** Duration and FileSize move *out* of
the source and *into* the target, so afterwards the source still counts runtime it no
longer owns and the target does not count what it now does. Every other mutator that
changes which files a book owns recomputes; this one does not.

Distinguish this from the mutators that correctly skip the recompute:
`UpdateBookFileHashes`, `SetBookFileHash`, `ClearAllAcoustIDFingerprints` and
`SweepBookFileSegDrop` also omit `notifyBookFileChange`, but none of them touches Duration
or FileSize, so omitting it is right. `MoveBookFilesToBook` is the one that is actually
wrong.

### Callers to check (8 non-test)

- `internal/merge/service.go:535`, `:652`
- `internal/plugins/maintenance/itunes_regroup.go:272`
- `internal/plugins/maintenance/fs_regroup_xml.go:252`
- `internal/server/handlers/versions.go:303`, `:432`, `:532`
- `internal/dedup/split_book_merge.go:87`

Some already recompute the survivor themselves (`merge/service.go:574` does), so the fix is
**not** simply "add two notifies" — that would double-recompute the callers that
compensate. Audit which callers already heal which book before changing the method.

### Fix

Recompute **both** the source and the target once, at the end of `MoveBookFilesToBook`,
then remove the now-redundant compensating recomputes in the callers that have them. Add
the memdb refresh the method is also missing, matching `DeleteBookFilesByIDs`.

- [ ] Audit all 8 callers for existing compensating recomputes
- [ ] Recompute source + target in the method; drop caller-side duplicates
- [ ] Add the missing memdb refresh
- [ ] Regression test: move files A→B, assert BOTH books' aggregates are correct
