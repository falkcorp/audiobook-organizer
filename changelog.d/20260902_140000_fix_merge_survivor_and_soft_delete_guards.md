### Fixed

#### Merges can no longer keep a file-less book, re-merge a deleted one, or hard-delete on a failed soft-delete

Three data-loss shapes found by the 2026-09-02 dedup bug hunt (F1/F2/F4), all
closed at `merge.Service.MergeBooks`, the chokepoint every merge path
(UI merge, dedup auto-merge, review verdicts, reconcile, iTunes heal,
diagnostics) funnels through:

- **File-aware survivor election.** A book with `book_file` rows now always
  beats one with none; only inside that tier does the existing `BookIsBetter`
  format/bitrate rule decide. Before, an m4b "ghost" row with no route to any
  audio could win over the mp3 that actually had the file, putting the only
  playable copy on the 30-day purge clock. Forcing a file-less book as the
  primary while another participant has files is refused with
  `FilelessPrimaryError` instead of being honored. Merging books that *all*
  lack file rows is still allowed (nothing to lose; refusing would strand the
  12,525-book ghost class).
- **Soft-deleted inputs refused.** `GetBookByID` returns soft-deleted rows and
  the single-valued `book:hash:` index never drops them, so after a manual
  "keep A" the next full scan handed the live winner its own deleted loser and
  merged them again *in the opposite direction* — a group whose only primary
  was a deleted row, both sides hard-deleted by the purge later. A soft-deleted
  book is now refused as primary always (`SoftDeletedInputError`), and as a
  loser unless it already belongs to the group the merge resolves to (a
  replayed verdict or retried op is a no-op, not an error). The version group
  is chosen from live participants only, so a stale pair cannot drag a live
  book into an unrelated group. `dedup.handleFileHashMatch` skips a
  soft-deleted or already-grouped index owner and no longer forces the index
  owner (the most recently created row) as primary.
- **No hard-delete fallback.** `merge.SoftDeleteBook` and the maintenance
  job's `ddSoftDeleteBook` used to answer a failed `UpdateBook` by calling
  `DeleteBook` on the same store — turning "a write failed" into "the row and
  its files are gone". Both now return the error. A loser that cannot be
  soft-deleted fails the merge with an error naming it, rather than a warning
  and a clean result while the loser stays live. The `bookSoftDeleter`
  interface drops `DeleteBook` so the fallback cannot come back by accident.

Tests: real-PebbleStore repros for every guard, engine-level stale-owner
tests, and 14/14 mutants killed (each guard removed or inverted in turn;
every mutant is caught by a named test).
