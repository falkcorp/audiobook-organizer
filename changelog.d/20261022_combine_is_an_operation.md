"Merge into one book" no longer runs inside the HTTP request. `POST
/audiobooks/combine` now answers 202 with the id of the new `dedup.book-combine`
operation (it answered 200 with the finished result; a breaking change for API
clients) and the combine runs in the background, sharing `dedup.book-merge`'s
concurrency key so a combine and a merge on the same books cannot interleave.
The request is checked first, so a request that cannot succeed fails the call:
400 for a repeated id or empty `merge_ids`, 404 for a missing book, 409 for a
soft-deleted or iTunes-protected book (`merge.PreflightCombine`). The combine's
outcome is the operation result (`GET /operations/:id/result`): `primary_id`,
`files_moved`, `books_deleted`, `journal_id`, `operation_id` (always set on success; the operation's
own id: `POST /operations/:id/revert` undoes the combine), `journal_pending`,
`warnings`, `message`, and on failure `failed`, `error` and, for a combine that
stopped part-way, `incomplete`. The candidate clean-up after a combine moved
into the operation. There is no synchronous fallback. In the web UI, merge,
combine and purge close their dialog at once, show a "running in the background"
notice with a link to the operation, and report the real outcome when the
operation ends; a failed merge or combine is never reported as done, and books
with an operation in flight cannot be submitted again.

A failed `dedup.book-merge` now stores a result too (`failed`, `partial`,
`error`) so the page can tell a merge that changed nothing from one that stopped
part-way, and its candidate clean-up covers only the books actually merged.
