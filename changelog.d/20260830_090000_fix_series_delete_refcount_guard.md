### Fixed

#### Two series-merge paths deleted a series without asking whether anything still pointed at it

`dedup.MergeSeries` (the interactive "merge these series" operation) and phase 1
of the series auto-prune both reassigned the books they could see and then
deleted the merged-away series row **unconditionally** — including after a
reassignment they had just recorded as failed, and including when the
enumeration came back empty.

Empty is not the same as unreferenced. Both series getters skip soft-deleted
books, so a series whose books are all in the trash enumerates as empty,
reassigns nothing, and used to be deleted anyway — leaving every one of those
rows holding a series ID that no longer resolves. That is the shape that had
already produced 6,893 phantom series IDs held by 13,322 live books (measured on
production 2026-08-14, `internal/database/series_bookref.go`).

Both paths now read `database.SeriesRefCounts` — the UNFILTERED count, which
sees trashed and non-primary rows — once before their loop, and refuse to delete
any series whose reference count exceeds what they actually reassigned. This is
the pattern `csMergeSeriesGroup` and `DedupSeries` already used. A refusal is
reported through the channel the caller already returns (`result.Errors` for
`MergeSeries`, the prune's recorded-errors summary for phase 1) and does not
count as a completed merge; books that were successfully reassigned stay
reassigned. Both fail CLOSED: a store that cannot answer the unfiltered question
aborts before deleting anything rather than falling back to the filtered count.

`MergeSeries` also had one fully silent path — a book the series getter listed
but `GetBookByID` could not hydrate was skipped with no error and no counter,
and the delete went ahead. It is now reported and excluded from the reassigned
count, so the guard catches it.

Phase 2 of the prune deliberately keeps its own second, fresh reference scan:
phase 1 repoints books ONTO the series it keeps, so reusing the pre-phase-1
counts there would delete the merge target out from under everything phase 1
just moved into it.

This PREVENTS future stranding. It does not repair the ~6,893 series IDs already
phantom; that repair is tracked separately.
