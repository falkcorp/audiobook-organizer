A book merge no longer fails outright when one of its losers is already in the
trash. `dedup.book-merge` skips an already-soft-deleted loser, merges the rest,
and reports each skipped id and its reason in the operation result
(`GET /operations/:id/result`: `merged`, `skipped`, `no_op`) and in the activity
line. When every loser is trashed the operation succeeds as a no-op. A soft-deleted survivor is still an error.
`POST /audiobooks/link` checks, before queueing, what the operation would refuse
afterwards (a soft-deleted keep book, an iTunes-protected selected book, or a
soft-deleted loser that already sits in a live participant's version group,
which the merge admits and iTunes-guards) and answers 409 with the reason
instead of queueing an operation that fails. A non-selected iTunes version-group
sibling is not refused (it gets database-only changes, see the iTunes entry),
so the preflight no longer depends on whether the keep book has a version group.
