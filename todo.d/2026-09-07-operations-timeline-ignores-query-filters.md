## `GET /operations/timeline` silently ignores its query filters (2026-09-07)

**This one caused real prod damage, so it is filed on its own rather than as a
footnote.**

`GET /api/v1/operations/timeline?status=canceled` returned the **identical** rows
as the same call with no filter at all — 3 rows either way. The `status`
parameter is neither honored nor rejected: it is silently dropped, and the
endpoint answers as if the caller had asked for everything. The response is also
a **truncated recent-activity view, not a census**: it reported 3 rows at
`limit=80` while the store actually held at least 70 canceled operations.

Both properties together make it an actively misleading instrument — it returns
a plausible, confidently-wrong answer rather than an error.

**What it cost:** `DELETE /api/v1/operations/history?status=canceled` was sized
against that view as "blast radius: 1 row" (the single stale
`maintenance.transcribe-book-intros` zombie). It deleted **70** rows of
operation history on production. Only canceled-op audit history was lost — no
running or queued ops, no book or file data — but the sizing was wrong by 70x
and nothing in the API surfaced that.

- [ ] **Honor `status` (and every other documented query filter) on
  `/operations/timeline`, or reject unsupported filters with a 400.** A filter
  that is ignored rather than rejected fails silently in the same direction as
  the caller's assumption. Rejecting is acceptable; silently ignoring is not.
- [ ] **Audit the other query params on this endpoint** (`limit`, and any
  def_id/plugin/date filters) for the same "accepted then dropped" behavior —
  `limit=80` returning a small truncated set suggests limit is not doing what a
  caller would expect either.
- [ ] **Make the truncation explicit.** If the endpoint is deliberately a recent
  view, say so in the response (a `truncated: true` / `total` field) so it cannot
  be mistaken for a complete listing. There is no `GET /operations/v2` list
  endpoint (only `/operations/v2/:id`), so timeline is the obvious listing and
  callers will keep reaching for it.
- [ ] **Add a dry-run / count mode to `DELETE /operations/history`**, and consider
  a delete-by-id endpoint. Today it deletes **by status only**
  (`DeleteOperationsByStatus`,
  `internal/server/handlers/operations/handler.go:215`) and `DELETE
  /operations/v2/:id` only cancels (a no-op on an already-terminal op), so there
  is no supported way to remove exactly one stale row and no way to preview a
  deletion's true scope.

**Regression test to add:** assert that a filtered request returns a *different*
result set than the unfiltered one for a fixture with mixed statuses. A filter is
unproven until the response actually changes when the filter changes.
