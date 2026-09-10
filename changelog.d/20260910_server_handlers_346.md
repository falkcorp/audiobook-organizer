### Fixed

- **Series-normalize no longer deletes a series whose only remaining books are
  trashed.** `mergeSeriesGroupHelper`, the third of three series-merge code
  paths, had no guard against the same gap already closed on the other two:
  both series getters skip soft-deleted books, so a series whose members were
  all in the trash enumerated empty and was deleted unconditionally, leaving
  those trashed rows pointing at a series ID that no longer resolves. The
  merge now reads the unfiltered reference count once before merging and
  refuses to delete a series it still sees referenced, even by books the
  listing getters cannot see — books that were repointed stay repointed, only
  the row removal is held back, and the refusal is reported rather than
  silently dropped.
