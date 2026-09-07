## Terminal ops never get `completed_at`, so they linger as zombies (2026-09-07)

A `maintenance.transcribe-book-intros` op queued **2026-06-26** was still sitting
in the operations timeline on 2026-09-07 reading `canceled` at `199/200`
("99%"), with `resume_count: 4` and **`completed_at: None`**. It looked like a
live, recently-failed job for over two months and prompted a false report that
the transcribe drain fix (#3014, merged 2026-09-01) had not worked — the fix was
fine; the row simply predated it by 67 days and nothing ever buried it.

- [ ] **Set `completed_at` when an op reaches a terminal status.** `canceled` is
  terminal (`internal/operations/registry/registry.go:916`) and deliberately
  non-resumable (`worker.go:97`), yet the row carried no completion timestamp.
  Check every terminal path (completed, failed, canceled, timeout) — an op the
  UI shows as finished but with a null completion time is indistinguishable from
  a stuck one.
- [ ] **Age out / visually distinguish terminal ops in the timeline** so a
  months-old corpse cannot be mistaken for current activity.

**Related instrument bug found in the same session — fix or document:**

- [ ] **`GET /api/v1/operations/timeline` silently IGNORES its `status=` filter.**
  It returned the identical 3 rows with and without `?status=canceled`, and is a
  truncated recent-activity view, not a census. There is no `GET /operations/v2`
  list endpoint (only `/operations/v2/:id`), so timeline is the obvious listing
  and it misleads. Either honor the filter or reject unsupported filters — a
  filter that is ignored rather than rejected returns a plausible wrong answer.
- [ ] **There is no delete-one-op endpoint.** `DELETE /operations/history` deletes
  **by status only** (`DeleteOperationsByStatus`,
  `internal/server/handlers/operations/handler.go:215`); `DELETE
  /operations/v2/:id` cancels and is a no-op on an already-terminal op. Sizing a
  cleanup from the timeline as "1 row" and firing `?status=canceled` deleted
  **70** rows on prod. Consider a by-id delete and/or a dry-run count.
