## Offset-pagination walkers: 5 remaining + the CI flake is NOT the cross-page swap

C815 (#PR) collapsed the 4 `internal/reconcile` whole-library offset loops to
single-snapshot reads. Two follow-ups:

- [ ] **5 more walkers share the exposure** (whole-library offset loops over
      `GetAllBooksCore(pageSize, offset)`, memdb-dispatched → cross-page
      snapshot-swap window): `internal/quarantine/service.go:232`,
      `internal/plugins/maintenance/repair_junk_titles.go:76`,
      `internal/plugins/maintenance/title_backfill.go:86`,
      `internal/plugins/maintenance/title_repair.go:199`,
      `internal/plugins/maintenance/duration_backfill.go:97`, plus the two
      internal pagers in `pebble_store.go` (:1414 folder-dup, :1551
      metadata-dup). Same collapse applies; verify each loop is pure
      accumulate (the reconcile four were) before editing.
- [ ] **The original CI flake (run 30702594886, 39/40 books) CANNOT be the
      cross-page swap**: 40 books with pageSize 5000 is a SINGLE page. The
      book was missing from the snapshot served by that one call — which
      points at a warmup/publish race (a book created while the memdb rebuild
      iterator was past its key, published without it, write-through buffer
      hole?). That is a STORE-layer bug and survives any enumeration pattern.
      Needs its own repro: create books concurrently with a forced warmup
      rebuild and diff the published snapshot against Pebble.
