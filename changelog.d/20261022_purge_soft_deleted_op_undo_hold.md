"Purge soft-deleted" is now an operation (`library.purge-soft-deleted`) instead of
a synchronous request that ran for over a minute and showed nothing. `DELETE
/audiobooks/purge-soft-deleted` now answers 202 with the operation id (it
answered 200 with the result before: a breaking change for API clients); the
purge runs on a worker pool with progress, logs one summary line, emits an
activity entry, and stores the full breakdown as the operation result
(`purged`, `skipped_owns_files`, `kept_undo_possible`, `kept_has_progress`,
`carried_to_sibling`, `carry_failed`, `failed`, `errors`; id lists are capped at
200 and `truncated` says so, the counts are exact). `POST
/audiobooks/purge-soft-deleted/preview` runs the same operation as a dry run
that classifies every trashed book exactly as the purge would and changes
nothing; the operation itself is a preview unless `dry_run` is false.
`GET /audiobooks/soft-deleted` carries `purge_breakdown` (purgeable versus
blocked, by reason) from the last pass.

A soft-deleted book that is the retired shell of a combine whose undo can still
run (an applied, undo_failed or fully journaled pending combine journal that
still stamps the shell) is no longer purged by any path: the bulk purge and the
nightly purge keep it and count it as `kept_undo_possible`, single "Purge now"
and "Discard progress and purge" answer 409 `UNDO_POSSIBLE`. Purging such a
shell used to make the combine un-revertable. Combine journals are never
expired, so a held shell stays in the trash until its combine is undone. Other
undo records that need a trashed book (operation-ledger soft-deletes and
auto-merge losers) hold it too; see the undo-hold coverage entry.
