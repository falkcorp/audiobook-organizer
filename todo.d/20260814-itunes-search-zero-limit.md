## PERF-4 is still live: iTunes search calls `SearchBooks(search, 0, 0)`

Re-verified 2026-08-14 while adding the status column to the 2026-06-22
security sweep: `internal/server/handlers/itunes.go:709` still calls
`h.store.SearchBooks(search, 0, 0)` — the exact call the audit flagged as
returning no rows (its cited mechanism: the store treats limit 0 as "nothing",
`pebble_store.go` search path).

- [ ] First MEASURE, don't assume: confirm what `SearchBooks(q, 0, 0)` returns
      today (the store may have changed limit-0 semantics since June). A
      bogus-value + known-good-value probe against a seeded store settles it in
      one test.
- [ ] If it returns nothing: the iTunes search surface has been silently empty
      — fix with a bounded call (or route through Bleve IDs + iTunes filter,
      as the audit suggested), and add the value-asserting test that would
      have caught a filter answering nothing.
- [ ] If it returns everything: that is the opposite failure (unbounded
      materialization on a handler path) and wants a limit anyway.
