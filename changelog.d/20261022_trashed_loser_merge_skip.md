A book merge no longer fails outright when one of its losers is already in the
trash. `dedup.book-merge` skips an already-soft-deleted loser, merges the rest,
and reports each skipped id and its reason in the operation result
(`GET /operations/:id/result`: `merged`, `skipped`, `no_op`) and in the activity
line. When every loser is trashed the operation succeeds as a no-op. A
soft-deleted survivor is still an error. `POST /audiobooks/link` now checks,
before queueing, what the operation would refuse afterwards (a soft-deleted keep
book, an iTunes-protected participant, or an iTunes-protected version-group
sibling that would be re-homed) and answers 409 with the reason instead of
queueing an operation that fails. Known gap: the sibling check does not run when
the keep book has no version group.
