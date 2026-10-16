New op `identification.advance` (spec 02 PR 14), off by default behind
`identification_advance_enabled`. It drains the identification dirty set
(`ident:dirty:`) a page of 256 books at a time and runs each marked book once
through the existing candidate fetch, with the worker count candidate-fetch
uses (sum of enabled provider budgets) set explicitly on `RunItems`. A mark is
cleared by compare-and-delete on its epoch, so a book re-marked while it runs
stays dirty and is processed again. Writers now mark books: a scan that
creates a primary book or changes a book's title or author, a successful
merge (the survivor), the store's retitle chokepoint, and a completed catalog
harvest that filed new entries or staled old ones (the author's unasked and
asked-empty primary books). A candidate-cache write never marks. With the flag
on, a 5-minute `identification_advance` task enqueues the op while the set is
non-empty, and the 6-hour `candidate_fetch` task only marks its selection and
logs `safety_net_marked=N` instead of fetching. With the flag off,
`candidate_fetch` sends exactly the params it did before. The driver records
the reasons the state index cannot derive (`no_usable_title`, `fragment`,
`manual_only`, `quota_deferred`) in a new `ident:reason:` family keyed to the
book's question fingerprint. The op body lives in `internal/identification`
in the ops-v3 Batch shape and does not import the operations registry; the v2
adapter is `internal/server/identification_advance_op.go`. `ScanState` gains
the per-stage `StageStatus` type and four file-stage fields, unused so far.
